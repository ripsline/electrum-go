package indexer

import (
    "bytes"
    "context"
    "encoding/binary"
    "fmt"
    "log"
    "sync"
    "syscall"
    "time"

    "github.com/btcsuite/btcd/wire"
    zmq "github.com/pebbe/zmq4"
)

type ZMQSubscriber struct {
    blockAddr string
    txAddr    string

    blockChan chan *wire.MsgBlock
    txChan    chan *wire.MsgTx
    gapChan   chan ZMQGapEvent

    ctx    context.Context
    cancel context.CancelFunc

    wg sync.WaitGroup

    blocksReceived int64
    txsReceived    int64
    errors         int64

    lastBlockSeq uint32
    lastTxSeq    uint32
    seqGapsBlock int64
    seqGapsTx    int64

    mu sync.RWMutex
}

type ZMQGapEvent struct {
    Stream   string
    Expected uint32
    Got      uint32
}

type ZMQConfig struct {
    BlockAddr     string
    TxAddr        string
    BlockChanSize int
    TxChanSize    int
}

func DefaultZMQConfig() ZMQConfig {
    return ZMQConfig{
        BlockAddr:     "tcp://127.0.0.1:28332",
        TxAddr:        "tcp://127.0.0.1:28333",
        BlockChanSize: 10,
        TxChanSize:    1000,
    }
}

func NewZMQSubscriber(config ZMQConfig) (*ZMQSubscriber, error) {
    ctx, cancel := context.WithCancel(context.Background())

    z := &ZMQSubscriber{
        blockAddr: config.BlockAddr,
        txAddr:    config.TxAddr,
        blockChan: make(chan *wire.MsgBlock, config.BlockChanSize),
        txChan:    make(chan *wire.MsgTx, config.TxChanSize),
        gapChan:   make(chan ZMQGapEvent, 10),
        ctx:       ctx,
        cancel:    cancel,
    }

    if err := z.testConnections(); err != nil {
        cancel()
        return nil, err
    }

    return z, nil
}

func (z *ZMQSubscriber) testConnections() error {
    blockSock, err := zmq.NewSocket(zmq.SUB)
    if err != nil {
        return fmt.Errorf("failed to create ZMQ socket: %w", err)
    }
    defer blockSock.Close()

    blockSock.SetConnectTimeout(5 * time.Second)

    if err := blockSock.Connect(z.blockAddr); err != nil {
        return fmt.Errorf("failed to connect to block ZMQ at %s: %w\n"+
            "Ensure Bitcoin Core is configured with: zmqpubrawblock=%s",
            z.blockAddr, err, z.blockAddr)
    }

    txSock, err := zmq.NewSocket(zmq.SUB)
    if err != nil {
        return fmt.Errorf("failed to create ZMQ socket: %w", err)
    }
    defer txSock.Close()

    txSock.SetConnectTimeout(5 * time.Second)

    if err := txSock.Connect(z.txAddr); err != nil {
        return fmt.Errorf("failed to connect to tx ZMQ at %s: %w\n"+
            "Ensure Bitcoin Core is configured with: zmqpubrawtx=%s",
            z.txAddr, err, z.txAddr)
    }

    log.Printf("✅ ZMQ connections verified")
    log.Printf("   Blocks: %s", z.blockAddr)
    log.Printf("   Txs:    %s", z.txAddr)

    return nil
}

func (z *ZMQSubscriber) Start() error {
    z.wg.Add(1)
    go z.subscribeBlocks()

    z.wg.Add(1)
    go z.subscribeTxs()

    log.Println("📡 ZMQ subscribers started")

    return nil
}

func (z *ZMQSubscriber) newSubSocket(addr, topic string) (*zmq.Socket, error) {
    socket, err := zmq.NewSocket(zmq.SUB)
    if err != nil {
        return nil, err
    }

    socket.SetLinger(0)
    socket.SetRcvtimeo(1 * time.Second)

    if err := socket.Connect(addr); err != nil {
        socket.Close()
        return nil, err
    }

    if err := socket.SetSubscribe(topic); err != nil {
        socket.Close()
        return nil, err
    }

    return socket, nil
}

func (z *ZMQSubscriber) subscribeBlocks() {
    defer z.wg.Done()

    backoff := time.Second
    maxBackoff := 30 * time.Second

    for {
        if z.ctx.Err() != nil {
            return
        }

        socket, err := z.newSubSocket(z.blockAddr, "rawblock")
        if err != nil {
            log.Printf("⚠️  Block ZMQ connect failed: %v", err)
            if !z.waitBackoff(backoff) {
                return
            }
            backoff = minDuration(backoff*2, maxBackoff)
            continue
        }

        backoff = time.Second
        log.Println("📡 Listening for blocks on ZMQ...")

        if err := z.recvBlocks(socket); err != nil {
            z.mu.Lock()
            z.errors++
            z.mu.Unlock()

            log.Printf("⚠️  Block ZMQ recv error: %v", err)
            socket.Close()
            if !z.waitBackoff(backoff) {
                return
            }
            backoff = minDuration(backoff*2, maxBackoff)
            continue
        }

        socket.Close()
    }
}

func (z *ZMQSubscriber) recvBlocks(socket *zmq.Socket) error {
    for {
        select {
        case <-z.ctx.Done():
            log.Println("📡 Block subscriber shutting down")
            return nil
        default:
        }

        msg, err := socket.RecvMessageBytes(0)
        if err != nil {
            if isZMQTimeout(err) {
                continue
            }
            return err
        }

        if len(msg) < 2 {
            log.Printf("⚠️  Invalid block ZMQ message: %d parts", len(msg))
            continue
        }

        if len(msg) >= 3 {
            z.handleSequence("block", msg[2])
        }

        blockData := msg[1]

        var block wire.MsgBlock
        if err := block.Deserialize(bytes.NewReader(blockData)); err != nil {
            z.mu.Lock()
            z.errors++
            z.mu.Unlock()

            log.Printf("⚠️  Failed to deserialize block: %v", err)
            continue
        }

        z.mu.Lock()
        z.blocksReceived++
        z.mu.Unlock()

        // Never drop blocks. Backpressure is allowed here.
        select {
        case z.blockChan <- &block:
        case <-z.ctx.Done():
            return nil
        }
    }
}

func (z *ZMQSubscriber) subscribeTxs() {
    defer z.wg.Done()

    backoff := time.Second
    maxBackoff := 30 * time.Second

    for {
        if z.ctx.Err() != nil {
            return
        }

        socket, err := z.newSubSocket(z.txAddr, "rawtx")
        if err != nil {
            log.Printf("⚠️  Tx ZMQ connect failed: %v", err)
            if !z.waitBackoff(backoff) {
                return
            }
            backoff = minDuration(backoff*2, maxBackoff)
            continue
        }

        backoff = time.Second
        log.Println("📡 Listening for mempool txs on ZMQ...")

        if err := z.recvTxs(socket); err != nil {
            z.mu.Lock()
            z.errors++
            z.mu.Unlock()

            log.Printf("⚠️  Tx ZMQ recv error: %v", err)
            socket.Close()
            if !z.waitBackoff(backoff) {
                return
            }
            backoff = minDuration(backoff*2, maxBackoff)
            continue
        }

        socket.Close()
    }
}

func (z *ZMQSubscriber) recvTxs(socket *zmq.Socket) error {
    for {
        select {
        case <-z.ctx.Done():
            log.Println("📡 Tx subscriber shutting down")
            return nil
        default:
        }

        msg, err := socket.RecvMessageBytes(0)
        if err != nil {
            if isZMQTimeout(err) {
                continue
            }
            return err
        }

        if len(msg) < 2 {
            continue
        }

        if len(msg) >= 3 {
            z.handleSequence("tx", msg[2])
        }

        txData := msg[1]

        var tx wire.MsgTx
        if err := tx.Deserialize(bytes.NewReader(txData)); err != nil {
            z.mu.Lock()
            z.errors++
            z.mu.Unlock()
            continue
        }

        z.mu.Lock()
        z.txsReceived++
        z.mu.Unlock()

        // Never drop mempool txs. Backpressure is allowed here.
        select {
        case z.txChan <- &tx:
        case <-z.ctx.Done():
            return nil
        }
    }
}

func (z *ZMQSubscriber) handleSequence(stream string, seqBytes []byte) {
    if len(seqBytes) < 4 {
        return
    }
    seq := binary.LittleEndian.Uint32(seqBytes[:4])

    z.mu.Lock()
    defer z.mu.Unlock()

    if stream == "block" {
        if z.lastBlockSeq != 0 && seq != z.lastBlockSeq+1 {
            z.seqGapsBlock++
            expected := z.lastBlockSeq + 1
            select {
            case z.gapChan <- ZMQGapEvent{Stream: "block", Expected: expected, Got: seq}:
            default:
            }
        }
        z.lastBlockSeq = seq
    } else if stream == "tx" {
        if z.lastTxSeq != 0 && seq != z.lastTxSeq+1 {
            z.seqGapsTx++
            expected := z.lastTxSeq + 1
            select {
            case z.gapChan <- ZMQGapEvent{Stream: "tx", Expected: expected, Got: seq}:
            default:
            }
        }
        z.lastTxSeq = seq
    }
}

func (z *ZMQSubscriber) waitBackoff(d time.Duration) bool {
    t := time.NewTimer(d)
    defer t.Stop()

    select {
    case <-z.ctx.Done():
        return false
    case <-t.C:
        return true
    }
}

func isZMQTimeout(err error) bool {
    return zmq.AsErrno(err) == zmq.Errno(syscall.EAGAIN)
}

func minDuration(a, b time.Duration) time.Duration {
    if a < b {
        return a
    }
    return b
}

func (z *ZMQSubscriber) BlockChan() <-chan *wire.MsgBlock {
    return z.blockChan
}

func (z *ZMQSubscriber) TxChan() <-chan *wire.MsgTx {
    return z.txChan
}

func (z *ZMQSubscriber) GapChan() <-chan ZMQGapEvent {
    return z.gapChan
}

func (z *ZMQSubscriber) Stop() {
    log.Println("📡 Stopping ZMQ subscriber...")

    z.cancel()
    z.wg.Wait()

    close(z.blockChan)
    close(z.txChan)
    close(z.gapChan)

    z.mu.RLock()
    log.Printf("📡 ZMQ subscriber stopped (received %d blocks, %d txs, %d errors, seq gaps: blocks=%d txs=%d)",
        z.blocksReceived, z.txsReceived, z.errors, z.seqGapsBlock, z.seqGapsTx)
    z.mu.RUnlock()
}

func (z *ZMQSubscriber) Stats() ZMQStats {
    z.mu.RLock()
    defer z.mu.RUnlock()

    return ZMQStats{
        BlocksReceived: z.blocksReceived,
        TxsReceived:    z.txsReceived,
        Errors:         z.errors,
        BlockQueueLen:  len(z.blockChan),
        TxQueueLen:     len(z.txChan),
        SeqGapsBlock:   z.seqGapsBlock,
        SeqGapsTx:      z.seqGapsTx,
    }
}

type ZMQStats struct {
    BlocksReceived int64
    TxsReceived    int64
    Errors         int64
    BlockQueueLen  int
    TxQueueLen     int
    SeqGapsBlock   int64
    SeqGapsTx      int64
}

func (s ZMQStats) String() string {
    return fmt.Sprintf("ZMQ: %d blocks, %d txs received, %d errors, gaps: blocks=%d txs=%d, queues: blocks=%d txs=%d",
        s.BlocksReceived, s.TxsReceived, s.Errors, s.SeqGapsBlock, s.SeqGapsTx, s.BlockQueueLen, s.TxQueueLen)
}