package main

import (
    "log"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/rpcclient"

    "github.com/ripsline/electrum-go/internal/indexer"
)

// reconcileMempool removes stale entries and backfills any missing txs.
func reconcileMempool(client *rpcclient.Client,
    mempool *indexer.MempoolOverlay, writer *indexer.Writer) ([]string, error) {

    txids, err := client.GetRawMempool()
    if err != nil {
        return nil, err
    }

    valid := make(map[string]bool, len(txids))
    for _, h := range txids {
        valid[h.String()] = true
    }

    removed, affected := mempool.ReconcileWith(valid)
    if removed > 0 {
        log.Printf("🔄 Mempool reconciled: removed %d stale entry(s)", removed)
    }

    affectedSet := make(map[string]struct{}, len(affected))
    for _, sh := range affected {
        affectedSet[sh] = struct{}{}
    }

    missing := 0
    for _, h := range txids {
        txidHex := h.String()
        if mempool.HasTx(txidHex) {
            continue
        }

        hash, err := chainhash.NewHashFromStr(txidHex)
        if err != nil {
            continue
        }

        rawTx, err := client.GetRawTransaction(hash)
        if err != nil {
            continue
        }

        aff, err := writer.IndexMempoolTx(rawTx.MsgTx())
        if err != nil {
            continue
        }

        for _, sh := range aff {
            affectedSet[sh] = struct{}{}
        }
        missing++
    }

    if missing > 0 {
        log.Printf("🔄 Mempool backfill: added %d missing tx(s)", missing)
    }

    result := make([]string, 0, len(affectedSet))
    for sh := range affectedSet {
        result = append(result, sh)
    }

    return result, nil
}
