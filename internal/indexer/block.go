package indexer

import (
    "bytes"
    "encoding/hex"
    "fmt"
    "log"

    "github.com/btcsuite/btcd/wire"
    "github.com/cockroachdb/pebble"

    "github.com/ripsline/electrum-go/internal/storage"
)

type BlockIndexer struct {
    db      *storage.DB
    mempool *MempoolOverlay
}

func NewBlockIndexer(db *storage.DB, mempool *MempoolOverlay) *BlockIndexer {
    return &BlockIndexer{
        db:      db,
        mempool: mempool,
    }
}

func (bi *BlockIndexer) IndexBlock(block *wire.MsgBlock, height int32) error {
    blockHash := block.BlockHash()
    blockHashBytes := BlockHashFromHash(&blockHash)
    blockHashHex := blockHash.String()
    prevHashBytes := BlockHashFromHash(&block.Header.PrevBlock)

    var headerBuf bytes.Buffer
    if err := block.Header.Serialize(&headerBuf); err != nil {
        return fmt.Errorf("failed to serialize header: %w", err)
    }
    headerBytes := headerBuf.Bytes()

    undo := &storage.UndoBlock{
        Height:         height,
        BlockHash:      blockHashBytes,
        PrevBlockHash:  prevHashBytes,
        SpentOutputs:   make([]storage.UndoOutput, 0),
        CreatedOutputs: make([]storage.UndoOutput, 0),
        HistoryEntries: make([]storage.UndoHistory, 0),
    }

    batch := bi.db.NewBatch()
    defer batch.Close()

    historyAdded := make(map[string]bool)

    txids := make([][]byte, 0, len(block.Transactions))

    // Compact tx blob storage
    txBlob := make([]byte, 0)
    txOffsets := make([]uint32, len(block.Transactions))

    var historyIndex uint32 = 0

    for txIndex, tx := range block.Transactions {
        txHash := tx.TxHash()
        txidBytes := TxidFromHash(&txHash)
        txidHex := txHash.String()
        isCoinbase := txIndex == 0

        txids = append(txids, txidBytes)

        // Serialize tx into compact blob and track offset
        txOffsets[txIndex] = uint32(len(txBlob))
        var txBuf bytes.Buffer
        if err := tx.Serialize(&txBuf); err != nil {
            return fmt.Errorf("failed to serialize tx: %w", err)
        }
        txBlob = append(txBlob, txBuf.Bytes()...)

        if err := bi.db.SaveTxPosInBatch(batch, txidBytes, height, uint32(txIndex)); err != nil {
            return fmt.Errorf("failed to save tx position: %w", err)
        }

        txScripthashes := make(map[string]bool)

        if !isCoinbase {
            for _, txIn := range tx.TxIn {
                if IsCoinbaseInput(txIn) {
                    continue
                }

                prevTxid := TxidFromHash(&txIn.PreviousOutPoint.Hash)
                prevVout := txIn.PreviousOutPoint.Index

                scripthash, err := bi.db.GetScripthashForOutpoint(prevTxid, prevVout)
                if err != nil {
                    return fmt.Errorf("failed to lookup spent outpoint %s:%d: %w",
                        TxidToHex(prevTxid), prevVout, err)
                }

                if scripthash == nil {
                    continue
                }

                utxo, err := bi.db.GetUTXO(scripthash, prevTxid, prevVout)
                if err != nil {
                    return fmt.Errorf("failed to get UTXO for undo: %w", err)
                }

                if utxo != nil {
                    undo.SpentOutputs = append(undo.SpentOutputs, storage.UndoOutput{
                        Scripthash: scripthash,
                        Txid:       prevTxid,
                        Vout:       prevVout,
                        Value:      utxo.Value,
                        Height:     utxo.Height,
                        BlockHash:  utxo.BlockHash,
                    })

                    utxoKey, err := storage.MakeUTXOKey(scripthash, prevTxid, prevVout)
                    if err != nil {
                        return fmt.Errorf("failed to make UTXO key for deletion: %w",
                            err)
                    }
                    if err := batch.Delete(utxoKey, nil); err != nil {
                        return fmt.Errorf("failed to delete UTXO: %w", err)
                    }

                    txIndexKey, err := storage.MakeTxIndexKey(prevTxid, prevVout)
                    if err != nil {
                        return fmt.Errorf("failed to make tx index key for deletion: %w",
                            err)
                    }
                    if err := batch.Delete(txIndexKey, nil); err != nil {
                        return fmt.Errorf("failed to delete tx index: %w", err)
                    }

                    txScripthashes[hex.EncodeToString(scripthash)] = true
                }

                bi.mempool.RemoveOutput(prevTxid, prevVout)
            }
        }

        for vout, txOut := range tx.TxOut {
            if IsOpReturn(txOut.PkScript) {
                continue
            }

            if txOut.Value == 0 {
                continue
            }

            scripthash := ComputeScripthash(txOut.PkScript)
            scripthashHex := hex.EncodeToString(scripthash)

            utxoKey, err := storage.MakeUTXOKey(scripthash, txidBytes, uint32(vout))
            if err != nil {
                return fmt.Errorf("failed to make UTXO key: %w", err)
            }

            utxoValue := &storage.UTXOValue{
                Value:     txOut.Value,
                Height:    height,
                BlockHash: blockHashBytes,
            }
            encodedValue := storage.EncodeUTXOValue(utxoValue)

            if err := batch.Set(utxoKey, encodedValue, nil); err != nil {
                return fmt.Errorf("failed to write UTXO: %w", err)
            }

            txIndexKey, err := storage.MakeTxIndexKey(txidBytes, uint32(vout))
            if err != nil {
                return fmt.Errorf("failed to make tx index key: %w", err)
            }
            if err := batch.Set(txIndexKey, scripthash, nil); err != nil {
                return fmt.Errorf("failed to write tx index: %w", err)
            }

            undo.CreatedOutputs = append(undo.CreatedOutputs, storage.UndoOutput{
                Scripthash: scripthash,
                Txid:       txidBytes,
                Vout:       uint32(vout),
                Value:      txOut.Value,
                Height:     height,
                BlockHash:  blockHashBytes,
            })

            txScripthashes[scripthashHex] = true
        }

        for scripthashHex := range txScripthashes {
            historyKey := scripthashHex + ":" + txidHex
            if historyAdded[historyKey] {
                continue
            }
            historyAdded[historyKey] = true

            scripthash, err := hex.DecodeString(scripthashHex)
            if err != nil {
                continue
            }

            hKey, err := storage.MakeHistoryKey(scripthash, height,
                uint32(txIndex), historyIndex)
            if err != nil {
                return fmt.Errorf("failed to make history key: %w", err)
            }

            if err := batch.Set(hKey, []byte{}, nil); err != nil {
                return fmt.Errorf("failed to write history entry: %w", err)
            }

            undo.HistoryEntries = append(undo.HistoryEntries, storage.UndoHistory{
                Scripthash: scripthash,
                Height:     height,
                TxIndex:    uint32(txIndex),
                Index:      historyIndex,
            })

            historyIndex++
        }

        bi.mempool.RemoveTransaction(txidBytes)
    }

    if err := bi.db.SaveHeaderInBatch(batch, height, headerBytes); err != nil {
        return fmt.Errorf("failed to save header: %w", err)
    }

    if err := bi.db.SaveBlockTxidsInBatch(batch, height, txids); err != nil {
        return fmt.Errorf("failed to save block txids: %w", err)
    }

    if err := bi.db.SaveTxBlobInBatch(batch, height, txBlob); err != nil {
        return fmt.Errorf("failed to save tx blob: %w", err)
    }

    if err := bi.db.SaveTxOffsetsInBatch(batch, height, txOffsets); err != nil {
        return fmt.Errorf("failed to save tx offsets: %w", err)
    }

    if err := bi.db.SaveUndoInBatch(batch, undo, blockHashBytes); err != nil {
        return fmt.Errorf("failed to save undo data: %w", err)
    }

    prevCheckpoint, _ := bi.db.LoadCheckpoint()
    checkpoint := storage.Checkpoint{
        Height:      height,
        BlockHash:   blockHashHex,
        StartHeight: prevCheckpoint.StartHeight,
    }
    if err := bi.db.SaveCheckpointInBatch(batch, checkpoint); err != nil {
        return fmt.Errorf("failed to save checkpoint: %w", err)
    }

    if err := batch.Commit(pebble.Sync); err != nil {
        return fmt.Errorf("failed to commit block batch: %w", err)
    }

    log.Printf("✅ Indexed block %d: %s (%d txs, %d outputs created, %d spent)",
        height, blockHashHex[:16]+"...",
        len(block.Transactions),
        len(undo.CreatedOutputs),
        len(undo.SpentOutputs))

    return nil
}