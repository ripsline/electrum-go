package indexer

import (
    "bytes"
    "encoding/hex"
    "fmt"
    "log"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/rpcclient"
    "github.com/btcsuite/btcd/wire"
    "github.com/cockroachdb/pebble"

    "electrum-go/internal/storage"
)

type ReorgHandler struct {
    db            *storage.DB
    client        *rpcclient.Client
    mempool       *MempoolOverlay
    maxReorgDepth int
}

func NewReorgHandler(db *storage.DB, client *rpcclient.Client,
    mempool *MempoolOverlay, maxReorgDepth int) *ReorgHandler {
    return &ReorgHandler{
        db:            db,
        client:        client,
        mempool:       mempool,
        maxReorgDepth: maxReorgDepth,
    }
}

type ReorgResult struct {
    ReorgDetected    bool
    ForkHeight       int32
    ForkHash         []byte
    RolledBackBlocks int
    CurrentHeight    int32
    CurrentHash      []byte
}

func (rh *ReorgHandler) CheckAndHandle() (*ReorgResult, error) {
    result := &ReorgResult{}

    checkpoint, err := rh.db.LoadCheckpoint()
    if err != nil {
        return nil, fmt.Errorf("failed to load checkpoint: %w", err)
    }

    if checkpoint.Height == 0 && checkpoint.BlockHash == "" {
        log.Println("📋 No checkpoint found - fresh database")
        return result, nil
    }

    result.CurrentHeight = checkpoint.Height

    currentHash, err := BlockHashFromHex(checkpoint.BlockHash)
    if err != nil {
        return nil, fmt.Errorf("invalid checkpoint block hash: %w", err)
    }
    result.CurrentHash = currentHash

    coreHash, err := rh.client.GetBlockHash(int64(checkpoint.Height))
    if err != nil {
        return nil, fmt.Errorf("failed to get block hash from Core at height %d: %w",
            checkpoint.Height, err)
    }

    if coreHash.String() == checkpoint.BlockHash {
        log.Printf("✅ Chain tip verified: block %d %s",
            checkpoint.Height, checkpoint.BlockHash[:16]+"...")
        return result, nil
    }

    result.ReorgDetected = true
    log.Printf("🔄 REORG DETECTED at height %d", checkpoint.Height)
    log.Printf("   Our hash:  %s", checkpoint.BlockHash[:16]+"...")
    log.Printf("   Core hash: %s", coreHash.String()[:16]+"...")

    forkHeight, forkHash, err := rh.findForkPoint(checkpoint.Height)
    if err != nil {
        return nil, fmt.Errorf("failed to find fork point: %w", err)
    }

    result.ForkHeight = forkHeight
    result.ForkHash = forkHash

    log.Printf("   Fork point: block %d %s",
        forkHeight, BlockHashToHex(forkHash)[:16]+"...")

    reorgDepth := checkpoint.Height - forkHeight
    if int(reorgDepth) > rh.maxReorgDepth && forkHeight > 0 {
        return nil, fmt.Errorf("reorg depth %d exceeds maximum %d - manual intervention required",
            reorgDepth, rh.maxReorgDepth)
    }

    rolledBack, err := rh.rollbackToHeight(forkHeight)
    if err != nil {
        return nil, fmt.Errorf("rollback failed: %w", err)
    }

    result.RolledBackBlocks = rolledBack
    result.CurrentHeight = forkHeight
    result.CurrentHash = forkHash

    rh.mempool.Clear()

    log.Printf("✅ Rolled back %d blocks to height %d", rolledBack, forkHeight)

    return result, nil
}

func (rh *ReorgHandler) findForkPoint(startHeight int32) (int32, []byte, error) {
    checkpoint, _ := rh.db.LoadCheckpoint()
    startBoundary := checkpoint.StartHeight
    if startBoundary <= 0 {
        startBoundary = 1
    }

    for height := startHeight - 1; height >= startBoundary; height-- {
        if startHeight-height > int32(rh.maxReorgDepth) {
            break
        }

        ourHash, err := rh.getOurBlockHash(height)
        if err != nil {
            continue
        }

        coreHash, err := rh.client.GetBlockHash(int64(height))
        if err != nil {
            return 0, nil, fmt.Errorf("failed to get Core block hash at %d: %w", height, err)
        }

        if BlockHashToHex(ourHash) == coreHash.String() {
            return height, ourHash, nil
        }
    }

    // Forward-index boundary: reset to startBoundary - 1
    if startBoundary > 0 {
        prevHeight := startBoundary - 1
        if prevHeight < 0 {
            prevHeight = 0
        }

        coreHash, err := rh.client.GetBlockHash(int64(prevHeight))
        if err != nil {
            return 0, nil, fmt.Errorf("failed to get Core block hash at %d: %w",
                prevHeight, err)
        }

        return prevHeight, BlockHashFromHash(coreHash), nil
    }

    return 0, nil, fmt.Errorf("fork point not found")
}

func (rh *ReorgHandler) getOurBlockHash(height int32) ([]byte, error) {
    checkpoint, err := rh.db.LoadCheckpoint()
    if err == nil && checkpoint.Height == height && checkpoint.BlockHash != "" {
        return BlockHashFromHex(checkpoint.BlockHash)
    }

    // Try undo records
    prefix := make([]byte, 5)
    prefix[0] = storage.PrefixUndo
    prefix[1] = byte(height >> 24)
    prefix[2] = byte(height >> 16)
    prefix[3] = byte(height >> 8)
    prefix[4] = byte(height)

    iter, err := rh.db.NewPrefixIterator(prefix)
    if err != nil {
        return nil, fmt.Errorf("failed to create undo iterator: %w", err)
    }
    defer iter.Close()

    if iter.First() {
        _, blockHash, err := storage.ParseUndoKey(iter.Key())
        if err != nil {
            return nil, fmt.Errorf("failed to parse undo key: %w", err)
        }
        return blockHash, nil
    }

    // Try header if undo is missing
    header, err := rh.db.GetHeader(height)
    if err == nil && len(header) == 80 {
        var wireHeader wire.BlockHeader
        if err := wireHeader.Deserialize(bytes.NewReader(header)); err == nil {
            h := wireHeader.BlockHash()
            return BlockHashFromHash(&h), nil
        }
    }

    return nil, fmt.Errorf("no block data found for height %d", height)
}

func (rh *ReorgHandler) rollbackToHeight(targetHeight int32) (int, error) {
    checkpoint, err := rh.db.LoadCheckpoint()
    if err != nil {
        return 0, fmt.Errorf("failed to load checkpoint: %w", err)
    }

    rolledBack := 0

    for height := checkpoint.Height; height > targetHeight; height-- {
        blockHash, err := rh.getOurBlockHash(height)
        if err != nil {
            return rolledBack, fmt.Errorf("failed to get block hash at height %d: %w",
                height, err)
        }

        log.Printf("   Rolling back block %d: %s", height,
            BlockHashToHex(blockHash)[:16]+"...")

        if err := rh.rollbackBlock(height, blockHash); err != nil {
            return rolledBack, fmt.Errorf("failed to rollback block %d: %w",
                height, err)
        }

        rolledBack++
    }

    forkHash, err := rh.getOurBlockHash(targetHeight)
    if err != nil {
        coreHash, coreErr := rh.client.GetBlockHash(int64(targetHeight))
        if coreErr != nil {
            return rolledBack, fmt.Errorf("failed to get fork point hash: %w",
                err)
        }
        forkHash = BlockHashFromHash(coreHash)
    }

    newCheckpoint := storage.Checkpoint{
        Height:    targetHeight,
        BlockHash: BlockHashToHex(forkHash),
    }

    if err := rh.db.SaveCheckpoint(newCheckpoint); err != nil {
        return rolledBack, fmt.Errorf("failed to save checkpoint after rollback: %w",
            err)
    }

    return rolledBack, nil
}

func (rh *ReorgHandler) rollbackBlock(height int32, blockHash []byte) error {
    undo, err := rh.db.GetUndo(height, blockHash)
    if err != nil {
        return fmt.Errorf("failed to load undo data: %w", err)
    }

    batch := rh.db.NewBatch()
    defer batch.Close()

    for _, created := range undo.CreatedOutputs {
        utxoKey, err := storage.MakeUTXOKey(created.Scripthash, created.Txid,
            created.Vout)
        if err != nil {
            return fmt.Errorf("failed to make UTXO key for deletion: %w", err)
        }
        if err := batch.Delete(utxoKey, nil); err != nil {
            return fmt.Errorf("failed to delete created UTXO: %w", err)
        }

        txIndexKey, err := storage.MakeTxIndexKey(created.Txid, created.Vout)
        if err != nil {
            return fmt.Errorf("failed to make tx index key for deletion: %w",
                err)
        }
        if err := batch.Delete(txIndexKey, nil); err != nil {
            return fmt.Errorf("failed to delete tx index: %w", err)
        }
    }

    for _, spent := range undo.SpentOutputs {
        utxoKey, err := storage.MakeUTXOKey(spent.Scripthash, spent.Txid,
            spent.Vout)
        if err != nil {
            return fmt.Errorf("failed to make UTXO key for restoration: %w",
                err)
        }

        utxoValue := storage.UTXOValue{
            Value:     spent.Value,
            Height:    spent.Height,
            BlockHash: spent.BlockHash,
        }
        valueBin := storage.EncodeUTXOValue(&utxoValue)

        if err := batch.Set(utxoKey, valueBin, nil); err != nil {
            return fmt.Errorf("failed to restore spent UTXO: %w", err)
        }

        txIndexKey, err := storage.MakeTxIndexKey(spent.Txid, spent.Vout)
        if err != nil {
            return fmt.Errorf("failed to make tx index key for restoration: %w",
                err)
        }
        if err := batch.Set(txIndexKey, spent.Scripthash, nil); err != nil {
            return fmt.Errorf("failed to restore tx index: %w", err)
        }
    }

    for _, hist := range undo.HistoryEntries {
        histKey, err := storage.MakeHistoryKey(hist.Scripthash, hist.Height,
            hist.TxIndex, hist.Index)
        if err != nil {
            return fmt.Errorf("failed to make history key for deletion: %w",
                err)
        }
        if err := batch.Delete(histKey, nil); err != nil {
            return fmt.Errorf("failed to delete history entry: %w", err)
        }
    }

    if err := rh.db.DeleteHeaderInBatch(batch, height); err != nil {
        return fmt.Errorf("failed to delete header: %w", err)
    }

    if err := rh.db.DeleteBlockTxidsInBatch(batch, height); err != nil {
        return fmt.Errorf("failed to delete block txids: %w", err)
    }

    if err := rh.db.DeleteUndoInBatch(batch, height, blockHash); err != nil {
        return fmt.Errorf("failed to delete undo record: %w", err)
    }

    if err := batch.Commit(pebble.Sync); err != nil {
        return fmt.Errorf("failed to commit rollback batch: %w", err)
    }

    return nil
}

func (rh *ReorgHandler) PruneOldUndoData(currentHeight int32) (int, error) {
    if currentHeight <= int32(rh.maxReorgDepth) {
        return 0, nil
    }

    pruneBeforeHeight := currentHeight - int32(rh.maxReorgDepth)

    pruned, err := rh.db.PruneUndoData(pruneBeforeHeight)
    if err != nil {
        return 0, fmt.Errorf("failed to prune undo data: %w", err)
    }

    if pruned > 0 {
        log.Printf("🗑️  Pruned %d undo records (height < %d)", pruned,
            pruneBeforeHeight)
    }

    return pruned, nil
}

func (rh *ReorgHandler) ValidateBlockConnects(block *chainhash.Hash,
    prevHash *chainhash.Hash) error {
    checkpoint, err := rh.db.LoadCheckpoint()
    if err != nil {
        return fmt.Errorf("failed to load checkpoint: %w", err)
    }

    if checkpoint.Height == 0 && checkpoint.BlockHash == "" {
        return nil
    }

    if prevHash.String() != checkpoint.BlockHash {
        return fmt.Errorf("block does not connect: prevHash=%s, our tip=%s",
            prevHash.String()[:16]+"...", checkpoint.BlockHash[:16]+"...")
    }

    return nil
}

func (rh *ReorgHandler) GetExpectedHeight() (int32, error) {
    checkpoint, err := rh.db.LoadCheckpoint()
    if err != nil {
        return 0, fmt.Errorf("failed to load checkpoint: %w", err)
    }

    if checkpoint.Height == 0 && checkpoint.BlockHash == "" {
        return 0, nil
    }

    return checkpoint.Height + 1, nil
}

func (rh *ReorgHandler) GetChainState() (*ChainState, error) {
    checkpoint, err := rh.db.LoadCheckpoint()
    if err != nil {
        return nil, fmt.Errorf("failed to load checkpoint: %w", err)
    }

    state := &ChainState{
        Height: checkpoint.Height,
    }

    if checkpoint.BlockHash != "" {
        hashBytes, err := hex.DecodeString(checkpoint.BlockHash)
        if err == nil && len(hashBytes) == 32 {
            var hash chainhash.Hash
            for i := 0; i < 32; i++ {
                hash[i] = hashBytes[31-i]
            }
            state.BlockHash = hash
        }
    }

    return state, nil
}