package main

import (
    "bytes"
    "context"
    "encoding/hex"
    "errors"
    "log"
    "strings"
    "time"

    "github.com/btcsuite/btcd/rpcclient"

    "github.com/ripsline/electrum-go/internal/config"
    "github.com/ripsline/electrum-go/internal/electrum"
    "github.com/ripsline/electrum-go/internal/indexer"
    "github.com/ripsline/electrum-go/internal/metrics"
)

func runMainLoop(
    ctx context.Context,
    cfg *config.Config,
    writer *indexer.Writer,
    zmqSub *indexer.ZMQSubscriber,
    server *electrum.Server,
    chainManager *indexer.ChainManager,
    rpcClient *rpcclient.Client,
    mempool *indexer.MempoolOverlay,
) error {
    pruneTicker := time.NewTicker(time.Duration(cfg.Indexer.UndoPruneInterval) *
        time.Minute)
    defer pruneTicker.Stop()

    statsTicker := time.NewTicker(1 * time.Minute)
    defer statsTicker.Stop()

    reorgCheckTicker := time.NewTicker(30 * time.Second)
    defer reorgCheckTicker.Stop()

    mempoolReconTicker := time.NewTicker(5 * time.Minute)
    defer mempoolReconTicker.Stop()

    blocksProcessed := 0
    txsIndexed := 0

    log.Println("✅ Live processing active")

    for {
        select {
        case <-ctx.Done():
            log.Println("📍 Main loop exiting...")
            return nil

        case block, ok := <-zmqSub.BlockChan():
            if !ok {
                return nil
            }
            height, affected, err := writer.IndexBlock(block)
            if err != nil {
                log.Printf("⚠️  Block processing error: %v", err)

                if isReorgError(err) {
                    log.Println("🔄 Reorg detected, catching up to tip...")
                    catchUpErr := chainManager.CatchUpToTip()
                    if errors.Is(catchUpErr, indexer.ErrPrunedGap) {
                        return catchUpErr
                    }
                    if catchUpErr != nil {
                        log.Printf("⚠️  Catch-up after reorg failed: %v", catchUpErr)
                    } else {
                        log.Println("✅ Catch-up after reorg complete")
                    }
                }
                continue
            }

            blocksProcessed++

            var headerBuf bytes.Buffer
            if err := block.Header.Serialize(&headerBuf); err != nil {
                log.Printf("⚠️  Failed to serialize header: %v", err)
                continue
            }
            headerHex := hex.EncodeToString(headerBuf.Bytes())

            server.NotifyNewBlock(height, headerHex)

            if len(affected) > 0 {
                server.MarkScripthashDirtyMany(affected)
                for _, shHex := range affected {
                    server.NotifyScripthashStatusHex(shHex)
                }
            }

            if blocksProcessed%cfg.Indexer.UndoPruneInterval == 0 {
                writer.TriggerPrune()
            }

        case tx, ok := <-zmqSub.TxChan():
            if !ok {
                return nil
            }
            affected, err := writer.IndexMempoolTx(tx)
            if err == nil {
                txsIndexed++
                if len(affected) > 0 {
                    server.MarkScripthashDirtyMany(affected)
                    for _, shHex := range affected {
                        server.NotifyScripthashStatusHex(shHex)
                    }
                }
            }

        case gap, ok := <-zmqSub.GapChan():
            if !ok {
                return nil
            }
            if gap.Stream == "block" {
                log.Printf("⚠️  ZMQ block sequence gap detected: expected %d, got %d",
                    gap.Expected, gap.Got)
                err := chainManager.CatchUpToTip()
                if errors.Is(err, indexer.ErrPrunedGap) {
                    return err
                }
                if err != nil {
                    log.Printf("⚠️  Catch-up after ZMQ gap failed: %v", err)
                }
            } else if gap.Stream == "tx" {
                log.Printf("⚠️  ZMQ tx sequence gap detected: expected %d, got %d",
                    gap.Expected, gap.Got)
                if _, err := reconcileMempool(rpcClient, mempool, writer); err != nil {
                    log.Printf("⚠️  Mempool reconcile after ZMQ gap failed: %v", err)
                }
            }

        case <-pruneTicker.C:
            writer.TriggerPrune()

        case <-reorgCheckTicker.C:
            rpcStart := time.Now()
            blockCount, err := rpcClient.GetBlockCount()
            metrics.ObserveBitcoinRPC("getblockcount", rpcStart, err)
            if err != nil {
                log.Printf("⚠️  Periodic tip check RPC failed: %v", err)
                continue
            }

            currentHeight := chainManager.GetCurrentHeight()
            coreTip := int32(blockCount)
            metrics.BitcoinCoreHeight.Set(float64(coreTip))

            if coreTip > currentHeight {
                missedBlocks := coreTip - currentHeight
                log.Printf("🔄 ZMQ safety check: missed %d block(s), "+
                    "catching up (%d -> %d)",
                    missedBlocks, currentHeight, coreTip)
                err := chainManager.CatchUpToTip()
                if errors.Is(err, indexer.ErrPrunedGap) {
                    return err
                }
                if err != nil {
                    log.Printf("⚠️  Catch-up after missed ZMQ failed: %v", err)
                } else {
                    log.Printf("✅ Caught up to tip %d", coreTip)
                }
            }

        case <-mempoolReconTicker.C:
            affected, err := reconcileMempool(rpcClient, mempool, writer)
            if err != nil {
                log.Printf("⚠️  Periodic mempool reconcile failed: %v", err)
                continue
            }
            if len(affected) > 0 {
                server.MarkScripthashDirtyMany(affected)
                for _, shHex := range affected {
                    server.NotifyScripthashStatusHex(shHex)
                }
            }

        case <-statsTicker.C:
            writerStats := writer.Stats()
            zmqStats := zmqSub.Stats()
            connCount := server.GetActiveConnectionCount()
            subScripthashes, subHeaders, subConns := server.
                GetSubscriptionManager().GetTotalSubscriptions()

            metrics.MempoolTxCount.Set(float64(mempool.Count()))
            metrics.Subscriptions.WithLabelValues("scripthash").
                Set(float64(subScripthashes))
            metrics.Subscriptions.WithLabelValues("header").
                Set(float64(subHeaders))
            metrics.Subscriptions.WithLabelValues("conn").
                Set(float64(subConns))

            log.Printf("📊 Stats: height=%d, blocks=%d, "+
                "mempool[rx=%d,idx=%d], conns=%d, "+
                "subs=[sh:%d,hdr:%d,conn:%d]",
                chainManager.GetCurrentHeight(),
                writerStats.BlocksIndexed,
                zmqStats.TxsReceived,
                txsIndexed,
                connCount,
                subScripthashes, subHeaders, subConns)
        }
    }
}

func isReorgError(err error) bool {
    if err == nil {
        return false
    }
    msg := err.Error()
    return strings.Contains(msg, "reorg") ||
        strings.Contains(msg, "does not connect") ||
        strings.Contains(msg, "gap detected") ||
        strings.Contains(msg, "rolled back")
}
