package indexer

import (
    "path/filepath"
    "testing"
    "time"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/wire"

    "github.com/ripsline/electrum-go/internal/storage"
)

// newTestDB opens a fresh pebble DB in t.TempDir() and registers
// Close() as a cleanup. Each test gets an isolated database.
func newTestDB(t *testing.T) *storage.DB {
    t.Helper()
    db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
    if err != nil {
        t.Fatalf("storage.Open: %v", err)
    }
    t.Cleanup(func() {
        if err := db.Close(); err != nil {
            t.Errorf("db.Close: %v", err)
        }
    })
    return db
}

// coinbaseTx builds a coinbase transaction with one output. The
// scriptSig is the height-encoding required by BIP34 (a placeholder
// is fine for indexer tests — we don't verify consensus rules).
func coinbaseTx(height int32, pkScript []byte, value int64) *wire.MsgTx {
    tx := wire.NewMsgTx(wire.TxVersion)
    tx.AddTxIn(&wire.TxIn{
        PreviousOutPoint: wire.OutPoint{
            Hash:  chainhash.Hash{}, // null hash → coinbase
            Index: 0xffffffff,
        },
        SignatureScript: []byte{byte(height), 0x00, 0x00, 0x00},
        Sequence:        0xffffffff,
    })
    tx.AddTxOut(&wire.TxOut{Value: value, PkScript: pkScript})
    return tx
}

// spendingTx builds a non-coinbase transaction that spends a single
// previous outpoint and creates a single output.
func spendingTx(prev chainhash.Hash, prevVout uint32, pkScript []byte,
    value int64) *wire.MsgTx {
    tx := wire.NewMsgTx(wire.TxVersion)
    tx.AddTxIn(&wire.TxIn{
        PreviousOutPoint: wire.OutPoint{Hash: prev, Index: prevVout},
        SignatureScript:  []byte{0x00},
        Sequence:         0xffffffff,
    })
    tx.AddTxOut(&wire.TxOut{Value: value, PkScript: pkScript})
    return tx
}

// newTestBlock builds a wire.MsgBlock with a minimal but parseable
// header. The merkle root is left zero — IndexBlock does not verify
// it. nonce varies per call so block hashes differ even when txs and
// prev hashes match.
var nonce uint32

func newTestBlock(prev chainhash.Hash, txs ...*wire.MsgTx) *wire.MsgBlock {
    nonce++
    return &wire.MsgBlock{
        Header: wire.BlockHeader{
            Version:    1,
            PrevBlock:  prev,
            MerkleRoot: chainhash.Hash{},
            Timestamp:  time.Unix(1_700_000_000, 0),
            Bits:       0x1d00ffff,
            Nonce:      nonce,
        },
        Transactions: txs,
    }
}
