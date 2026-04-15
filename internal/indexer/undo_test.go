package indexer

import (
    "testing"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
)

// TestRollbackBlock_RestoresUTXOState exercises both undo paths in a
// single representative scenario:
//
//   block 1 (coinbase only) creates UTXO A
//   block 2 (coinbase + spend) spends A, creates UTXO B
//
// After indexing block 2, A is gone and B is present. Rolling back
// block 2 must restore A and remove B. If either undo path is broken,
// this test catches it.
func TestRollbackBlock_RestoresUTXOState(t *testing.T) {
    db := newTestDB(t)
    mempool := NewMempoolOverlay(db)
    indexer := NewBlockIndexer(db, mempool)
    // ReorgHandler with nil RPC client: rollbackBlock only reads undo
    // data from the local DB, never the client.
    reorg := NewReorgHandler(db, nil, mempool, 100)

    scriptA := []byte{0x51, 0xaa} // OP_1 + a marker byte
    scriptB := []byte{0x51, 0xbb}

    // Block 1: coinbase-only, creates UTXO A.
    cb1 := coinbaseTx(1, scriptA, 5_000_000_000)
    block1 := newTestBlock(chainhash.Hash{}, cb1)
    if err := indexer.IndexBlock(block1, 1); err != nil {
        t.Fatalf("IndexBlock(1): %v", err)
    }
    cb1Hash := cb1.TxHash()
    cb1Txid := TxidFromHash(&cb1Hash)
    scripthashA := ComputeScripthash(scriptA)

    if utxo, err := db.GetUTXO(scripthashA, cb1Txid, 0); err != nil ||
        utxo == nil {
        t.Fatalf("UTXO A missing after block 1 (err=%v)", err)
    }

    // Block 2: coinbase (creates a throwaway output we don't care
    // about) + a tx that spends A and creates B.
    cb2 := coinbaseTx(2, []byte{0x6a}, 5_000_000_000) // OP_RETURN, ignored
    spendA := spendingTx(cb1Hash, 0, scriptB, 4_999_999_000)
    block1Hash := block1.BlockHash()
    block2 := newTestBlock(block1Hash, cb2, spendA)
    if err := indexer.IndexBlock(block2, 2); err != nil {
        t.Fatalf("IndexBlock(2): %v", err)
    }
    spendHash := spendA.TxHash()
    spendTxid := TxidFromHash(&spendHash)
    scripthashB := ComputeScripthash(scriptB)

    // After block 2: A is spent, B exists.
    if utxo, err := db.GetUTXO(scripthashA, cb1Txid, 0); err != nil {
        t.Fatalf("GetUTXO(A) after block 2: %v", err)
    } else if utxo != nil {
        t.Fatalf("UTXO A still present after being spent in block 2")
    }
    if utxo, err := db.GetUTXO(scripthashB, spendTxid, 0); err != nil ||
        utxo == nil {
        t.Fatalf("UTXO B missing after block 2 (err=%v)", err)
    }

    // Roll back block 2.
    block2Hash := block2.BlockHash()
    block2HashBytes := BlockHashFromHash(&block2Hash)
    if err := reorg.rollbackBlock(2, block2HashBytes); err != nil {
        t.Fatalf("rollbackBlock(2): %v", err)
    }

    // After rollback: A restored, B gone.
    if utxo, err := db.GetUTXO(scripthashA, cb1Txid, 0); err != nil ||
        utxo == nil {
        t.Fatalf("UTXO A not restored after rollback (err=%v)", err)
    }
    if utxo, err := db.GetUTXO(scripthashB, spendTxid, 0); err != nil {
        t.Fatalf("GetUTXO(B) after rollback: %v", err)
    } else if utxo != nil {
        t.Fatalf("UTXO B still present after rollback")
    }

    // Undo record itself is consumed. GetUndo returns an error (not
    // a nil record) when the entry is absent, so absence == error.
    if undo, err := db.GetUndo(2, block2HashBytes); err == nil && undo != nil {
        t.Fatalf("undo record for block 2 not deleted after rollback")
    }

    // Header for block 2 is gone.
    if header, err := db.GetHeader(2); err == nil && len(header) > 0 {
        t.Fatalf("header for block 2 not deleted after rollback")
    }
}
