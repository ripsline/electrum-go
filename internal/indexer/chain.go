// chain.go provides chain validation and block connection logic.
package indexer

import (
    "fmt"
    "log"
    "time"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/rpcclient"
    "github.com/btcsuite/btcd/wire"

    "github.com/ripsline/electrum-go/internal/storage"
)

// ChainManager coordinates chain state and block processing.
type ChainManager struct {
    db            *storage.DB
    client        *rpcclient.Client
    blockIndexer  *BlockIndexer
    reorgHandler  *ReorgHandler
    mempool       *MempoolOverlay

    currentHeight int32
    currentHash   chainhash.Hash

    // Track the starting height for gap detection
    startHeight int32
}

// NewChainManager creates a new chain manager.
func NewChainManager(
    db *storage.DB,
    client *rpcclient.Client,
    mempool *MempoolOverlay,
    maxReorgDepth int,
) *ChainManager {
    blockIndexer := NewBlockIndexer(db, mempool)
    reorgHandler := NewReorgHandler(db, client, mempool, maxReorgDepth)

    return &ChainManager{
        db:           db,
        client:       client,
        blockIndexer: blockIndexer,
        reorgHandler: reorgHandler,
        mempool:      mempool,
        startHeight:  0,
    }
}

// Initialize loads the current chain state and checks for reorgs.
func (cm *ChainManager) Initialize() error {
    log.Println("🔍 Initializing chain manager...")

    // Check for and handle any reorg that occurred while we were offline
    result, err := cm.reorgHandler.CheckAndHandle()
    if err != nil {
        return fmt.Errorf("reorg check failed: %w", err)
    }

    if result.ReorgDetected {
        log.Printf("🔄 Handled reorg: rolled back %d blocks to height %d",
            result.RolledBackBlocks, result.ForkHeight)
    }

    // Load current state
    state, err := cm.reorgHandler.GetChainState()
    if err != nil {
        return fmt.Errorf("failed to get chain state: %w", err)
    }

    cm.currentHeight = state.Height
    cm.currentHash = state.BlockHash

    // Load start height from checkpoint if available
    checkpoint, err := cm.db.LoadCheckpoint()
    if err == nil && checkpoint.StartHeight > 0 {
        cm.startHeight = checkpoint.StartHeight
    }

    if state.IsEmpty() {
        log.Println("📋 Chain state: empty (no blocks indexed)")
    } else {
        log.Printf("📋 Chain state: height=%d hash=%s",
            cm.currentHeight, cm.currentHash.String()[:16]+"...")
        if cm.startHeight > 0 {
            log.Printf("   Start height: %d (forward-indexing mode)", cm.startHeight)
        }
    }

    return nil
}

// ProcessBlock validates and indexes a new block.
func (cm *ChainManager) ProcessBlock(block *wire.MsgBlock) (int32, error) {
    blockHash := block.BlockHash()
    prevHash := block.Header.PrevBlock

    // Case 1: Block connects to our tip (normal case)
    if prevHash.IsEqual(&cm.currentHash) {
        newHeight := cm.currentHeight + 1

        if err := cm.blockIndexer.IndexBlock(block, newHeight); err != nil {
            return 0, fmt.Errorf("failed to index block at height %d: %w", newHeight, err)
        }

        cm.currentHeight = newHeight
        cm.currentHash = blockHash

        return newHeight, nil
    }

    // Case 2: Block doesn't connect - need to investigate
    log.Printf("⚠️  Block %s doesn't connect to our tip", blockHash.String()[:16]+"...")
    log.Printf("   Block's prevHash: %s", prevHash.String()[:16]+"...")
    log.Printf("   Our tip hash:     %s", cm.currentHash.String()[:16]+"...")

    // Check if this block is even in Core's best chain
    blockInfo, err := cm.client.GetBlockHeaderVerbose(&blockHash)
    if err != nil {
        return 0, fmt.Errorf("block not found in Core: %w", err)
    }

    actualHeight := int32(blockInfo.Height)

    // Case 2a: Block is behind our tip - this is a reorg signal
    if actualHeight <= cm.currentHeight {
        log.Printf("🔄 Block height %d <= our tip %d - reorg detected",
            actualHeight, cm.currentHeight)

        result, err := cm.reorgHandler.CheckAndHandle()
        if err != nil {
            return 0, fmt.Errorf("reorg handling failed: %w", err)
        }

        cm.currentHeight = result.CurrentHeight
        hashBytes := result.CurrentHash
        if len(hashBytes) == 32 {
            copy(cm.currentHash[:], hashBytes)
        }

        return 0, fmt.Errorf("reorg handled, rolled back to height %d - retry block processing",
            result.CurrentHeight)
    }

    // Case 2b: Block is ahead of us - we missed blocks, need to catch up
    if actualHeight > cm.currentHeight+1 {
        log.Printf("📥 Gap detected: block at height %d, our tip at %d",
            actualHeight, cm.currentHeight)

        if err := cm.CatchUpTo(actualHeight - 1); err != nil {
            return 0, fmt.Errorf("catch-up failed: %w", err)
        }

        return cm.ProcessBlock(block)
    }

    // Case 2c: Heights match but prevHash doesn't - reorg at our tip
    log.Printf("🔄 Reorg at tip detected (height %d)", actualHeight)

    result, err := cm.reorgHandler.CheckAndHandle()
    if err != nil {
        return 0, fmt.Errorf("reorg handling failed: %w", err)
    }

    cm.currentHeight = result.CurrentHeight
    if len(result.CurrentHash) == 32 {
        copy(cm.currentHash[:], result.CurrentHash)
    }

    return 0, fmt.Errorf("reorg handled, rolled back to height %d - retry block processing",
        result.CurrentHeight)
}

// CatchUpTo fetches and indexes all blocks from our tip to the target height.
func (cm *ChainManager) CatchUpTo(targetHeight int32) error {
    if targetHeight <= cm.currentHeight {
        return nil
    }

    blocksToFetch := targetHeight - cm.currentHeight
    log.Printf("📥 Catching up: fetching %d blocks (%d to %d)",
        blocksToFetch, cm.currentHeight+1, targetHeight)

    startTime := time.Now()
    blocksProcessed := 0

    for height := cm.currentHeight + 1; height <= targetHeight; height++ {
        hash, err := cm.client.GetBlockHash(int64(height))
        if err != nil {
            return fmt.Errorf("failed to get block hash at height %d: %w", height, err)
        }

        block, err := cm.client.GetBlock(hash)
        if err != nil {
            return fmt.Errorf("failed to get block at height %d: %w", height, err)
        }

        // Verify it connects to our tip
        if !block.Header.PrevBlock.IsEqual(&cm.currentHash) {
            log.Printf("⚠️  Catch-up block %d doesn't connect, checking for reorg", height)

            result, err := cm.reorgHandler.CheckAndHandle()
            if err != nil {
                return fmt.Errorf("reorg during catch-up: %w", err)
            }

            cm.currentHeight = result.CurrentHeight
            if len(result.CurrentHash) == 32 {
                copy(cm.currentHash[:], result.CurrentHash)
            }

            return cm.CatchUpTo(targetHeight)
        }

        if err := cm.blockIndexer.IndexBlock(block, height); err != nil {
            return fmt.Errorf("failed to index block %d: %w", height, err)
        }

        cm.currentHeight = height
        cm.currentHash = block.BlockHash()
        blocksProcessed++

        if blocksProcessed%100 == 0 {
            elapsed := time.Since(startTime)
            blocksPerSec := float64(blocksProcessed) / elapsed.Seconds()
            remaining := targetHeight - height
            eta := time.Duration(float64(remaining)/blocksPerSec) * time.Second

            log.Printf("   Progress: %d/%d blocks (%.1f blocks/sec, ETA: %s)",
                blocksProcessed, blocksToFetch, blocksPerSec, eta.Round(time.Second))
        }
    }

    elapsed := time.Since(startTime)
    blocksPerSec := float64(blocksProcessed) / elapsed.Seconds()

    log.Printf("✅ Catch-up complete: %d blocks in %s (%.1f blocks/sec)",
        blocksProcessed, elapsed.Round(time.Millisecond), blocksPerSec)

    return nil
}

// CatchUpToTip fetches and indexes all blocks from our tip to Core's current tip.
// BUG 2 FIX: Added gap detection before catching up.
func (cm *ChainManager) CatchUpToTip() error {
    // First, verify chain continuity and repair any gaps
    if err := cm.VerifyAndRepairGaps(); err != nil {
        return fmt.Errorf("gap repair failed: %w", err)
    }

    // Get Core's current block count
    blockCount, err := cm.client.GetBlockCount()
    if err != nil {
        return fmt.Errorf("failed to get block count: %w", err)
    }

    coreTip := int32(blockCount)

    if coreTip <= cm.currentHeight {
        log.Printf("✅ Already at tip (height %d)", cm.currentHeight)
        return nil
    }

    return cm.CatchUpTo(coreTip)
}

// VerifyAndRepairGaps checks for gaps in our indexed chain and repairs them.
// BUG 2 FIX: New method to detect and fill gaps on startup.
func (cm *ChainManager) VerifyAndRepairGaps() error {
    if cm.currentHeight <= 0 {
        return nil // Nothing to verify
    }

    // Determine the effective start of our indexed range
    effectiveStart := cm.startHeight
    if effectiveStart <= 0 {
        // Try to find the actual start by looking for the first header we have
        effectiveStart = cm.findFirstIndexedHeight()
        if effectiveStart <= 0 {
            effectiveStart = 1
        }
    }

    log.Printf("🔍 Verifying chain continuity from height %d to %d...",
        effectiveStart, cm.currentHeight)

    // Quick check: verify our tip matches Core's chain
    tipHash, err := cm.client.GetBlockHash(int64(cm.currentHeight))
    if err != nil {
        return fmt.Errorf("failed to get tip hash from Core: %w", err)
    }

    if !tipHash.IsEqual(&cm.currentHash) {
        log.Printf("⚠️  Tip hash mismatch at height %d", cm.currentHeight)
        log.Printf("   Our hash:  %s", cm.currentHash.String()[:16]+"...")
        log.Printf("   Core hash: %s", tipHash.String()[:16]+"...")

        // Find the last height where we match Core's chain
        matchHeight, matchHash, err := cm.findLastMatchingHeight(effectiveStart, cm.currentHeight)
        if err != nil {
            // No matching height found - we need to reset to our start point
            log.Printf("⚠️  No matching block found, resetting to start height %d", effectiveStart)

            // Get the hash at effectiveStart-1 to set as our "tip"
            if effectiveStart > 1 {
                prevHash, err := cm.client.GetBlockHash(int64(effectiveStart - 1))
                if err != nil {
                    return fmt.Errorf("failed to get hash at height %d: %w", effectiveStart-1, err)
                }
                cm.currentHeight = effectiveStart - 1
                cm.currentHash = *prevHash
            } else {
                // Reset to genesis
                cm.currentHeight = 0
                cm.currentHash = chainhash.Hash{}
            }

            // Save updated checkpoint
            checkpoint := storage.Checkpoint{
                Height:      cm.currentHeight,
                BlockHash:   cm.currentHash.String(),
                StartHeight: effectiveStart,
            }
            if err := cm.db.SaveCheckpoint(checkpoint); err != nil {
                log.Printf("⚠️  Failed to save checkpoint: %v", err)
            }

            log.Printf("✅ Reset to height %d, will catch up from there", cm.currentHeight)
            return nil
        }

        // Found a matching point - roll back to there
        log.Printf("✅ Found matching block at height %d", matchHeight)
        cm.currentHeight = matchHeight
        cm.currentHash = matchHash

        // Save updated checkpoint
        checkpoint := storage.Checkpoint{
            Height:      cm.currentHeight,
            BlockHash:   cm.currentHash.String(),
            StartHeight: cm.startHeight,
        }
        if err := cm.db.SaveCheckpoint(checkpoint); err != nil {
            log.Printf("⚠️  Failed to save checkpoint: %v", err)
        }

        return nil
    }

    // Check for gaps by sampling headers
    rangeSize := cm.currentHeight - effectiveStart + 1
    var step int32 = 1
    if rangeSize > 1000 {
        step = rangeSize / 100
        if step < 1 {
            step = 1
        }
    }

    var gapStart int32 = -1

    for height := effectiveStart; height <= cm.currentHeight; height += step {
        header, err := cm.db.GetHeader(height)
        if err != nil || len(header) != 80 {
            if gapStart == -1 {
                gapStart = height
            }
            continue
        }

        if gapStart != -1 {
            gapEnd := height - 1
            log.Printf("⚠️  Gap detected: heights %d to %d", gapStart, gapEnd)

            if err := cm.repairGap(gapStart, gapEnd); err != nil {
                return fmt.Errorf("failed to repair gap %d-%d: %w", gapStart, gapEnd, err)
            }
            gapStart = -1
        }
    }

    if gapStart != -1 {
        log.Printf("⚠️  Gap detected at end: heights %d to %d", gapStart, cm.currentHeight)
        if err := cm.repairGap(gapStart, cm.currentHeight); err != nil {
            return fmt.Errorf("failed to repair gap %d-%d: %w", gapStart, cm.currentHeight, err)
        }
    }

    if step > 1 {
        for height := effectiveStart; height <= cm.currentHeight; height++ {
            header, err := cm.db.GetHeader(height)
            if err != nil || len(header) != 80 {
                gapEnd := height
                for gapEnd < cm.currentHeight {
                    h, err := cm.db.GetHeader(gapEnd + 1)
                    if err == nil && len(h) == 80 {
                        break
                    }
                    gapEnd++
                }

                log.Printf("⚠️  Small gap detected: heights %d to %d", height, gapEnd)
                if err := cm.repairGap(height, gapEnd); err != nil {
                    return fmt.Errorf("failed to repair gap %d-%d: %w", height, gapEnd, err)
                }

                height = gapEnd
            }
        }
    }

    log.Printf("✅ Chain continuity verified")
    return nil
}

// findFirstIndexedHeight finds the lowest height for which we have a header.
// This is used when startHeight wasn't explicitly set.
func (cm *ChainManager) findFirstIndexedHeight() int32 {
    // Binary search would be more efficient, but for simplicity we'll
    // start from a reasonable point and search backwards/forwards

    // First check if we have height 1
    if _, err := cm.db.GetHeader(1); err == nil {
        return 1
    }

    // Search backwards from currentHeight
    for h := cm.currentHeight; h > 0; h-- {
        if _, err := cm.db.GetHeader(h); err != nil {
            // No header at h, so first indexed is h+1
            return h + 1
        }
    }

    return 1
}

// findLastMatchingHeight finds the highest height where our stored hash matches Core.
// Returns the height and hash, or an error if no match found.
func (cm *ChainManager) findLastMatchingHeight(startHeight, endHeight int32) (int32, chainhash.Hash, error) {
    // Search backwards from endHeight to find where we diverged
    for height := endHeight; height >= startHeight; height-- {
        // Get our stored header
        headerBytes, err := cm.db.GetHeader(height)
        if err != nil {
            // No header at this height, keep searching
            continue
        }

        if len(headerBytes) != 80 {
            continue
        }

        // Parse our header to get its hash
        var wireHeader wire.BlockHeader
        if err := wireHeader.Deserialize(newByteReader(headerBytes)); err != nil {
            continue
        }
        ourHash := wireHeader.BlockHash()

        // Get Core's hash at this height
        coreHash, err := cm.client.GetBlockHash(int64(height))
        if err != nil {
            continue
        }

        // Check if they match
        if ourHash.IsEqual(coreHash) {
            return height, ourHash, nil
        }
    }

    return 0, chainhash.Hash{}, fmt.Errorf("no matching block found in range %d-%d", startHeight, endHeight)
}

// repairGap fills in missing blocks in a gap.
func (cm *ChainManager) repairGap(startHeight, endHeight int32) error {
    log.Printf("🔧 Repairing gap: fetching blocks %d to %d", startHeight, endHeight)

    for height := startHeight; height <= endHeight; height++ {
        hash, err := cm.client.GetBlockHash(int64(height))
        if err != nil {
            return fmt.Errorf("failed to get block hash at %d: %w", height, err)
        }

        block, err := cm.client.GetBlock(hash)
        if err != nil {
            return fmt.Errorf("failed to get block at %d: %w", height, err)
        }

        // Index the block (this will also save the header)
        if err := cm.blockIndexer.IndexBlock(block, height); err != nil {
            return fmt.Errorf("failed to index block %d: %w", height, err)
        }

        if (height-startHeight+1)%100 == 0 {
            log.Printf("   Gap repair progress: %d/%d blocks",
                height-startHeight+1, endHeight-startHeight+1)
        }
    }

    log.Printf("✅ Gap repaired: %d blocks indexed", endHeight-startHeight+1)
    return nil
}

// GetCurrentHeight returns the current indexed height.
func (cm *ChainManager) GetCurrentHeight() int32 {
    return cm.currentHeight
}

// GetCurrentHash returns the current indexed block hash.
func (cm *ChainManager) GetCurrentHash() chainhash.Hash {
    return cm.currentHash
}

// GetBlockIndexer returns the block indexer for direct access if needed.
func (cm *ChainManager) GetBlockIndexer() *BlockIndexer {
    return cm.blockIndexer
}

// GetReorgHandler returns the reorg handler for direct access if needed.
func (cm *ChainManager) GetReorgHandler() *ReorgHandler {
    return cm.reorgHandler
}

// PruneOldUndoData prunes old undo records.
func (cm *ChainManager) PruneOldUndoData() (int, error) {
    return cm.reorgHandler.PruneOldUndoData(cm.currentHeight)
}

// SetStartHeight sets the starting height for initial sync.
func (cm *ChainManager) SetStartHeight(height int32) error {
    if cm.currentHeight > 0 {
        return fmt.Errorf("cannot set start height: already have indexed blocks")
    }

    if height <= 0 {
        return nil
    }

    prevHash, err := cm.client.GetBlockHash(int64(height - 1))
    if err != nil {
        return fmt.Errorf("failed to get block hash at height %d: %w", height-1, err)
    }

    cm.currentHeight = height - 1
    cm.currentHash = *prevHash
    cm.startHeight = height // Track where we started

    checkpoint := storage.Checkpoint{
        Height:      height - 1,
        BlockHash:   prevHash.String(),
        StartHeight: height, // Save start height in checkpoint
    }

    if err := cm.db.SaveCheckpoint(checkpoint); err != nil {
        return fmt.Errorf("failed to save starting checkpoint: %w", err)
    }

    log.Printf("📍 Set start height: will begin indexing from block %d", height)

    return nil
}

// VerifyChainIntegrity performs a basic integrity check of our indexed chain.
func (cm *ChainManager) VerifyChainIntegrity(sampleSize int) error {
    log.Println("🔍 Verifying chain integrity...")

    if cm.currentHeight <= 0 {
        log.Println("   No blocks to verify")
        return nil
    }

    effectiveStart := cm.startHeight
    if effectiveStart <= 0 {
        effectiveStart = 1
    }

    step := (cm.currentHeight - effectiveStart + 1) / int32(sampleSize)
    if step < 1 {
        step = 1
    }

    verified := 0
    for height := effectiveStart; height <= cm.currentHeight; height += step {
        header, err := cm.db.GetHeader(height)
        if err != nil {
            return fmt.Errorf("missing header at height %d: %w", height, err)
        }

        if len(header) != 80 {
            return fmt.Errorf("invalid header length at height %d: got %d, want 80",
                height, len(header))
        }

        coreHash, err := cm.client.GetBlockHash(int64(height))
        if err != nil {
            return fmt.Errorf("failed to get Core block hash at %d: %w", height, err)
        }

        var wireHeader wire.BlockHeader
        err = wireHeader.Deserialize(newByteReader(header))
        if err != nil {
            return fmt.Errorf("failed to deserialize header at %d: %w", height, err)
        }

        ourHash := wireHeader.BlockHash()
        if !ourHash.IsEqual(coreHash) {
            return fmt.Errorf("hash mismatch at height %d: ours=%s, core=%s",
                height, ourHash.String()[:16], coreHash.String()[:16])
        }

        verified++
    }

    log.Printf("✅ Verified %d blocks across height range %d-%d",
        verified, effectiveStart, cm.currentHeight)
    return nil
}

// byteReader is a minimal io.Reader for a byte slice.
type byteReader struct {
    data []byte
    pos  int
}

func (r *byteReader) Read(p []byte) (n int, err error) {
    if r.pos >= len(r.data) {
        return 0, fmt.Errorf("EOF")
    }
    n = copy(p, r.data[r.pos:])
    r.pos += n
    return n, nil
}

func newByteReader(data []byte) *byteReader {
    return &byteReader{data: data}
}
