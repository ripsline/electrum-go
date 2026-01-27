package electrum

import (
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "sort"

    "electrum-go/internal/indexer"
    "electrum-go/internal/storage"
)

func GetScripthashHistory(db *storage.DB, mempool *indexer.MempoolOverlay,
    scripthash []byte) ([]map[string]interface{}, error) {
    history := make([]map[string]interface{}, 0)
    seen := make(map[string]bool)

    prefix, err := storage.MakeHistoryPrefix(scripthash)
    if err != nil {
        return nil, err
    }

    iter, err := db.NewPrefixIterator(prefix)
    if err != nil {
        return nil, err
    }
    defer iter.Close()

    txidCache := make(map[int32][][]byte)

    for iter.First(); iter.Valid(); iter.Next() {
        _, height, txIndex, _, err := storage.ParseHistoryKey(iter.Key())
        if err != nil {
            continue
        }

        txids, ok := txidCache[height]
        if !ok {
            txids, err = db.GetBlockTxids(height)
            if err != nil {
                continue
            }
            txidCache[height] = txids
        }

        if int(txIndex) >= len(txids) {
            continue
        }

        txidHex := indexer.TxidToHex(txids[txIndex])
        if seen[txidHex] {
            continue
        }
        seen[txidHex] = true

        history = append(history, map[string]interface{}{
            "tx_hash": txidHex,
            "height":  int(height),
        })
    }

    mempoolTxids := mempool.GetScripthashTransactions(scripthash)
    for _, txidHex := range mempoolTxids {
        if seen[txidHex] {
            continue
        }
        seen[txidHex] = true

        history = append(history, map[string]interface{}{
            "tx_hash": txidHex,
            "height":  0,
        })
    }

    sort.Slice(history, func(i, j int) bool {
        hi := history[i]["height"].(int)
        hj := history[j]["height"].(int)

        if hi == 0 && hj != 0 {
            return false
        }
        if hi != 0 && hj == 0 {
            return true
        }
        return hi < hj
    })

    return history, nil
}

func GetScripthashBalance(db *storage.DB, mempool *indexer.MempoolOverlay,
    scripthash []byte) (map[string]interface{}, error) {
    var confirmed int64
    var unconfirmed int64

    utxoPrefix, err := storage.MakeUTXOPrefix(scripthash)
    if err != nil {
        return nil, err
    }

    iter, err := db.NewPrefixIterator(utxoPrefix)
    if err != nil {
        return nil, err
    }
    defer iter.Close()

    for iter.First(); iter.Valid(); iter.Next() {
        _, txid, vout, err := storage.ParseUTXOKey(iter.Key())
        if err != nil {
            continue
        }

        valueCopy := make([]byte, len(iter.Value()))
        copy(valueCopy, iter.Value())

        utxo, err := storage.DecodeUTXOValue(valueCopy)
        if err != nil {
            continue
        }

        if mempool.IsOutputSpent(txid, vout) {
            unconfirmed -= utxo.Value
        } else {
            confirmed += utxo.Value
        }
    }

    mempoolBalance := mempool.GetBalance(scripthash)
    unconfirmed += mempoolBalance

    return map[string]interface{}{
        "confirmed":   confirmed,
        "unconfirmed": unconfirmed,
    }, nil
}

func GetScripthashUnspent(db *storage.DB, mempool *indexer.MempoolOverlay,
    scripthash []byte) ([]map[string]interface{}, error) {
    utxos := make([]map[string]interface{}, 0)

    utxoPrefix, err := storage.MakeUTXOPrefix(scripthash)
    if err != nil {
        return nil, err
    }

    iter, err := db.NewPrefixIterator(utxoPrefix)
    if err != nil {
        return nil, err
    }
    defer iter.Close()

    for iter.First(); iter.Valid(); iter.Next() {
        _, txid, vout, err := storage.ParseUTXOKey(iter.Key())
        if err != nil {
            continue
        }

        valueCopy := make([]byte, len(iter.Value()))
        copy(valueCopy, iter.Value())

        utxo, err := storage.DecodeUTXOValue(valueCopy)
        if err != nil {
            continue
        }

        if mempool.IsOutputSpent(txid, vout) {
            continue
        }

        utxos = append(utxos, map[string]interface{}{
            "tx_hash": indexer.TxidToHex(txid),
            "tx_pos":  vout,
            "height":  utxo.Height,
            "value":   utxo.Value,
        })
    }

    mempoolOutputs := mempool.GetUnspentOutputs(scripthash)
    for _, output := range mempoolOutputs {
        utxos = append(utxos, map[string]interface{}{
            "tx_hash": indexer.TxidToHex(output.Txid),
            "tx_pos":  output.Vout,
            "height":  0,
            "value":   output.Value,
        })
    }

    return utxos, nil
}

func ComputeScripthashStatus(db *storage.DB, mempool *indexer.MempoolOverlay,
    scripthash []byte) (string, error) {
    history, err := GetScripthashHistory(db, mempool, scripthash)
    if err != nil {
        return "", err
    }

    if len(history) == 0 {
        return "", nil
    }

    var statusBuilder bytes.Buffer
    for _, entry := range history {
        txHash := entry["tx_hash"].(string)
        height := entry["height"].(int)
        fmt.Fprintf(&statusBuilder, "%s:%d:", txHash, height)
    }

    hash := sha256.Sum256(statusBuilder.Bytes())
    return hex.EncodeToString(hash[:]), nil
}