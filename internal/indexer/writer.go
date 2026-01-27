// writer.go provides serialized write access to the database.
//
// Why serialize writes?
// ====================
// Multiple goroutines may want to write to the database simultaneously:
// - ZMQ block handler receiving new blocks
// - ZMQ transaction handler receiving mempool transactions
// - Periodic maintenance tasks (pruning, etc.)
//
// While Pebble itself is thread-safe, allowing concurrent writes can lead to:
// 1. Race conditions in application logic (e.g., reading then writing)
// 2. Unpredictable ordering of operations
// 3. Difficulty reasoning about state during reorgs
//
// The Writer serializes all write operations through a single goroutine,
// ensuring operations are processed in a predictable order and eliminating
// race conditions in our application logic.
//
// Usage:
// - Submit work via IndexBlock() or IndexMempoolTx()
// - These methods block until the work is complete
// - Errors are returned to the caller
// - Shutdown gracefully with Close()

package indexer

import (
    "context"
    "encoding/hex"
    "fmt"
    "log"
    "sync"
    "time"

    "github.com/btcsuite/btcd/wire"

    "electrum-go/internal/storage"
)

// Writer handles serialized writes to the database.
// All database modifications should go through the Writer to ensure
// consistent ordering and prevent race conditions.
type Writer struct {
    chainManager *ChainManager
    mempool      *MempoolOverlay
    db           *storage.DB

    // Job queue for incoming work
    jobs chan writeJob

    // Context for shutdown signaling
    ctx    context.Context
    cancel context.CancelFunc

    // WaitGroup to track the worker goroutine
    wg sync.WaitGroup

    // Metrics
    blocksIndexed   int64
    mempoolTxsAdded int64
    lastBlockTime   time.Time
}

// writeJob represents a unit of work for the writer.
type writeJob struct {
    // Type of job
    jobType jobType

    // Block data (for block jobs)
    block *wire.MsgBlock

    // Transaction data (for mempool jobs)
    tx *wire.MsgTx

    // Response channel - caller blocks on this
    result chan writeResult
}

// jobType identifies the type of write job.
type jobType int

const (
    jobTypeBlock jobType = iota
    jobTypeMempoolTx
    jobTypePrune
)

// writeResult contains the result of a write operation.
type writeResult struct {
    // Height of indexed block (for block jobs)
    height int32

    // Affected scripthashes (for block and mempool jobs)
    affectedScripthashes []string

    // Error if operation failed
    err error
}

// NewWriter creates a new write serializer.
//
// Parameters:
// - db: Database handle
// - chainManager: Manages chain state and block indexing
// - mempool: Mempool overlay for unconfirmed transactions
//
// The Writer starts a background goroutine that processes jobs.
// Call Close() to shut down gracefully.
func NewWriter(db *storage.DB, chainManager *ChainManager,
    mempool *MempoolOverlay) *Writer {
    ctx, cancel := context.WithCancel(context.Background())

    w := &Writer{
        chainManager: chainManager,
        mempool:      mempool,
        db:           db,
        jobs:         make(chan writeJob, 100), // Buffer for burst handling
        ctx:          ctx,
        cancel:       cancel,
    }

    // Start the worker goroutine
    w.wg.Add(1)
    go w.processLoop()

    log.Println("✅ Writer started")

    return w
}

// processLoop is the main worker loop that processes write jobs.
// It runs in a dedicated goroutine and processes jobs sequentially.
func (w *Writer) processLoop() {
    defer w.wg.Done()

    for {
        select {
        case <-w.ctx.Done():
            // Shutdown requested
            log.Println("📝 Writer shutting down...")

            // Drain remaining jobs with errors
            w.drainJobsOnShutdown()
            return

        case job := <-w.jobs:
            // Process the job
            result := w.processJob(job)

            // Send result back to caller
            if job.result != nil {
                job.result <- result
                close(job.result)
            }
        }
    }
}

// processJob handles a single write job.
func (w *Writer) processJob(job writeJob) writeResult {
    switch job.jobType {
    case jobTypeBlock:
        return w.processBlockJob(job.block)

    case jobTypeMempoolTx:
        return w.processMempoolJob(job.tx)

    case jobTypePrune:
        return w.processPruneJob()

    default:
        return writeResult{err: fmt.Errorf("unknown job type: %d", job.jobType)}
    }
}

// processBlockJob indexes a block.
func (w *Writer) processBlockJob(block *wire.MsgBlock) writeResult {
    if block == nil {
        return writeResult{err: fmt.Errorf("nil block")}
    }

    affected := w.collectAffectedScripthashes(block)

    // Use chain manager to process (handles validation, reorgs, etc.)
    height, err := w.chainManager.ProcessBlock(block)
    if err != nil {
        return writeResult{err: err}
    }

    // Update metrics
    w.blocksIndexed++
    w.lastBlockTime = time.Now()

    return writeResult{
        height:               height,
        affectedScripthashes: affected,
    }
}

// processMempoolJob adds a transaction to the mempool.
func (w *Writer) processMempoolJob(tx *wire.MsgTx) writeResult {
    if tx == nil {
        return writeResult{err: fmt.Errorf("nil transaction")}
    }

    affected, err := w.mempool.AddTransaction(tx)
    if err != nil {
        // Don't treat validation failures as errors to propagate
        // (double spends, missing inputs, etc. are expected)
        return writeResult{err: nil}
    }

    w.mempoolTxsAdded++

    return writeResult{affectedScripthashes: affected}
}

// processPruneJob prunes old undo data.
func (w *Writer) processPruneJob() writeResult {
    pruned, err := w.chainManager.PruneOldUndoData()
    if err != nil {
        return writeResult{err: fmt.Errorf("prune failed: %w", err)}
    }

    if pruned > 0 {
        log.Printf("🗑️  Pruned %d old undo records", pruned)
    }

    return writeResult{}
}

// collectAffectedScripthashes returns all scripthashes touched by a block.
func (w *Writer) collectAffectedScripthashes(block *wire.MsgBlock) []string {
    affected := make(map[string]struct{})

    for _, tx := range block.Transactions {
        for _, txIn := range tx.TxIn {
            if IsCoinbaseInput(txIn) {
                continue
            }

            prevTxid := TxidFromHash(&txIn.PreviousOutPoint.Hash)
            prevVout := txIn.PreviousOutPoint.Index

            scripthash, err := w.db.GetScripthashForOutpoint(prevTxid, prevVout)
            if err != nil || scripthash == nil {
                continue
            }

            affected[hex.EncodeToString(scripthash)] = struct{}{}
        }

        for _, txOut := range tx.TxOut {
            if IsOpReturn(txOut.PkScript) {
                continue
            }
            if txOut.Value == 0 {
                continue
            }

            sh := ComputeScripthash(txOut.PkScript)
            affected[hex.EncodeToString(sh)] = struct{}{}
        }
    }

    result := make([]string, 0, len(affected))
    for shHex := range affected {
        result = append(result, shHex)
    }

    return result
}

// drainJobsOnShutdown clears the job queue during shutdown.
func (w *Writer) drainJobsOnShutdown() {
    for {
        select {
        case job := <-w.jobs:
            if job.result != nil {
                job.result <- writeResult{err: fmt.Errorf("writer shutting down")}
                close(job.result)
            }
        default:
            return
        }
    }
}

// IndexBlock submits a block for indexing and waits for completion.
//
// This method blocks until:
// - The block is successfully indexed
// - An error occurs
// - The writer is shut down
//
// Returns the height and affected scripthashes, or an error.
func (w *Writer) IndexBlock(block *wire.MsgBlock) (int32, []string, error) {
    result := make(chan writeResult, 1)

    job := writeJob{
        jobType: jobTypeBlock,
        block:   block,
        result:  result,
    }

    // Submit job
    select {
    case w.jobs <- job:
        // Job submitted, wait for result
    case <-w.ctx.Done():
        return 0, nil, fmt.Errorf("writer is shut down")
    }

    // Wait for result
    select {
    case res := <-result:
        return res.height, res.affectedScripthashes, res.err
    case <-w.ctx.Done():
        return 0, nil, fmt.Errorf("writer shut down while processing")
    }
}

// IndexMempoolTx submits a mempool transaction for indexing.
//
// This method blocks until the transaction is processed.
// Returns affected scripthashes on success (may be empty).
// Returns an error only for internal failures.
func (w *Writer) IndexMempoolTx(tx *wire.MsgTx) ([]string, error) {
    result := make(chan writeResult, 1)

    job := writeJob{
        jobType: jobTypeMempoolTx,
        tx:      tx,
        result:  result,
    }

    // Submit job
    select {
    case w.jobs <- job:
        // Job submitted
    case <-w.ctx.Done():
        return nil, fmt.Errorf("writer is shut down")
    }

    // Wait for result
    select {
    case res := <-result:
        return res.affectedScripthashes, res.err
    case <-w.ctx.Done():
        return nil, fmt.Errorf("writer shut down while processing")
    }
}

// TriggerPrune submits a prune job.
// This is non-blocking - it just queues the job.
func (w *Writer) TriggerPrune() {
    job := writeJob{
        jobType: jobTypePrune,
        result:  nil, // Don't wait for result
    }

    // Non-blocking send
    select {
    case w.jobs <- job:
        // Queued
    default:
        // Queue full, skip this prune cycle
    }
}

// Close shuts down the writer gracefully.
//
// It signals the worker to stop, waits for pending jobs to complete,
// and then returns. After Close() returns, no more jobs will be processed.
func (w *Writer) Close() error {
    log.Println("📝 Closing writer...")

    // Signal shutdown
    w.cancel()

    // Wait for worker to finish
    w.wg.Wait()

    log.Printf("📝 Writer closed (indexed %d blocks, %d mempool txs)",
        w.blocksIndexed, w.mempoolTxsAdded)

    return nil
}

// Stats returns current writer statistics.
func (w *Writer) Stats() WriterStats {
    return WriterStats{
        BlocksIndexed:   w.blocksIndexed,
        MempoolTxsAdded: w.mempoolTxsAdded,
        LastBlockTime:   w.lastBlockTime,
        QueueLength:     len(w.jobs),
    }
}

// WriterStats contains writer statistics.
type WriterStats struct {
    BlocksIndexed   int64
    MempoolTxsAdded int64
    LastBlockTime   time.Time
    QueueLength     int
}

// String returns a human-readable representation of writer stats.
func (s WriterStats) String() string {
    timeSinceBlock := "never"
    if !s.LastBlockTime.IsZero() {
        timeSinceBlock = time.Since(s.LastBlockTime).Round(time.Second).
            String()
    }

    return fmt.Sprintf("Writer: %d blocks, %d mempool txs, last block %s ago, queue: %d",
        s.BlocksIndexed, s.MempoolTxsAdded, timeSinceBlock, s.QueueLength)
}