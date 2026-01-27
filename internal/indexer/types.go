// Package indexer provides blockchain indexing functionality for the Electrum server.
//
// The indexer is responsible for:
// - Processing new blocks and extracting scripthash/UTXO data
// - Maintaining undo logs for reorg safety
// - Tracking mempool transactions
// - Handling chain reorganizations
//
// Key design principles:
// 1. All block processing is atomic - a block is either fully indexed or not at all
// 2. Undo logs enable safe rollback during reorgs
// 3. Mempool is an overlay that doesn't affect confirmed state
// 4. Write serialization prevents race conditions

package indexer

import (
    "crypto/sha256"
    "encoding/hex"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/wire"
)

// ChainState represents the current state of our indexed chain.
// This is tracked in memory and persisted via checkpoints.
type ChainState struct {
    // Height is the height of the last fully indexed block.
    // -1 means no blocks have been indexed yet.
    Height int32

    // BlockHash is the hash of the last fully indexed block.
    // Zero hash means no blocks have been indexed yet.
    BlockHash chainhash.Hash
}

// IsEmpty returns true if no blocks have been indexed.
func (cs *ChainState) IsEmpty() bool {
    return cs.Height < 0 || cs.BlockHash.IsEqual(&chainhash.Hash{})
}

// String returns a human-readable representation of the chain state.
func (cs *ChainState) String() string {
    if cs.IsEmpty() {
        return "ChainState{empty}"
    }
    return "ChainState{height=" + string(rune(cs.Height)) + ", hash=" + cs.BlockHash.String()[:16] + "...}"
}

// ProcessedOutput represents a transaction output after processing.
// This is an intermediate format used during block indexing.
type ProcessedOutput struct {
    // Scripthash is the Electrum-style scripthash (SHA256 of scriptPubKey, reversed).
    Scripthash []byte

    // Txid is the transaction ID (hash) as raw bytes.
    Txid []byte

    // Vout is the output index within the transaction.
    Vout uint32

    // Value is the output amount in satoshis.
    Value int64

    // ScriptPubKey is the raw output script (for debugging/validation).
    ScriptPubKey []byte
}

// ProcessedInput represents a transaction input after processing.
// This tracks which previous output is being spent.
type ProcessedInput struct {
    // PrevTxid is the txid of the output being spent.
    PrevTxid []byte

    // PrevVout is the output index being spent.
    PrevVout uint32

    // Scripthash of the spent output (looked up during processing).
    Scripthash []byte

    // Value of the spent output (looked up during processing).
    Value int64

    // Height where the spent output was created.
    Height int32

    // BlockHash of the block containing the spent output.
    BlockHash string
}

// ProcessedTransaction represents a transaction after full processing.
type ProcessedTransaction struct {
    // Txid is the transaction ID as raw bytes.
    Txid []byte

    // TxidHex is the transaction ID as a hex string (for logging).
    TxidHex string

    // Inputs lists all non-coinbase inputs with their spent output info.
    Inputs []ProcessedInput

    // Outputs lists all indexable outputs (excludes OP_RETURN, etc.).
    Outputs []ProcessedOutput

    // IsCoinbase is true if this is a coinbase transaction.
    IsCoinbase bool
}

// ProcessedBlock represents a block after full processing.
type ProcessedBlock struct {
    // Height of this block.
    Height int32

    // BlockHash as raw bytes.
    BlockHash []byte

    // BlockHashHex as hex string (for logging).
    BlockHashHex string

    // PrevBlockHash links to the previous block.
    PrevBlockHash []byte

    // Header is the raw 80-byte block header.
    Header []byte

    // Transactions is the list of processed transactions.
    Transactions []ProcessedTransaction

    // Stats for logging
    TotalInputs  int
    TotalOutputs int
}

// ComputeScripthash computes the Electrum-style scripthash for a script.
//
// The Electrum protocol uses a specific format:
// 1. SHA256 hash of the scriptPubKey
// 2. Bytes reversed (little-endian display)
//
// This is different from Bitcoin's standard hash formats and is specific
// to the Electrum protocol for efficient indexing by output script.
func ComputeScripthash(script []byte) []byte {
    // SHA256 of the script
    hash := sha256.Sum256(script)

    // Reverse bytes for Electrum format
    reversed := make([]byte, 32)
    for i := 0; i < 32; i++ {
        reversed[i] = hash[31-i]
    }

    return reversed
}

// ComputeScripthashHex returns the scripthash as a hex string.
// This is the format used in the Electrum protocol.
func ComputeScripthashHex(script []byte) string {
    return hex.EncodeToString(ComputeScripthash(script))
}

// ScripthashFromHex parses a hex-encoded scripthash.
// Returns an error if the hex is invalid or wrong length.
func ScripthashFromHex(s string) ([]byte, error) {
    bytes, err := hex.DecodeString(s)
    if err != nil {
        return nil, err
    }
    if len(bytes) != 32 {
        return nil, hex.ErrLength
    }
    return bytes, nil
}

// TxidFromHash converts a chainhash.Hash to raw bytes.
// The bytes are in the standard Bitcoin internal order.
func TxidFromHash(hash *chainhash.Hash) []byte {
    // chainhash.Hash is already in internal byte order
    bytes := make([]byte, 32)
    copy(bytes, hash[:])
    return bytes
}

// TxidToHex converts raw txid bytes to display hex format.
// Bitcoin displays txids in reverse byte order.
func TxidToHex(txid []byte) string {
    if len(txid) != 32 {
        return ""
    }
    // Reverse for display
    reversed := make([]byte, 32)
    for i := 0; i < 32; i++ {
        reversed[i] = txid[31-i]
    }
    return hex.EncodeToString(reversed)
}

// TxidFromHex parses a display-format txid hex string to raw bytes.
// Bitcoin displays txids in reverse byte order.
func TxidFromHex(s string) ([]byte, error) {
    bytes, err := hex.DecodeString(s)
    if err != nil {
        return nil, err
    }
    if len(bytes) != 32 {
        return nil, hex.ErrLength
    }
    // Reverse from display order to internal order
    reversed := make([]byte, 32)
    for i := 0; i < 32; i++ {
        reversed[i] = bytes[31-i]
    }
    return reversed, nil
}

// BlockHashFromHash converts a chainhash.Hash to raw bytes.
func BlockHashFromHash(hash *chainhash.Hash) []byte {
    bytes := make([]byte, 32)
    copy(bytes, hash[:])
    return bytes
}

// BlockHashToHex converts raw block hash bytes to display hex format.
func BlockHashToHex(hash []byte) string {
    if len(hash) != 32 {
        return ""
    }
    // Reverse for display
    reversed := make([]byte, 32)
    for i := 0; i < 32; i++ {
        reversed[i] = hash[31-i]
    }
    return hex.EncodeToString(reversed)
}

// BlockHashFromHex parses a display-format block hash to raw bytes.
func BlockHashFromHex(s string) ([]byte, error) {
    return TxidFromHex(s) // Same format as txid
}

// IsOpReturn checks if a script is an OP_RETURN output.
// OP_RETURN outputs are unspendable and shouldn't be indexed.
func IsOpReturn(script []byte) bool {
    return len(script) > 0 && script[0] == 0x6a
}

// IsCoinbaseInput checks if a transaction input is a coinbase input.
// Coinbase inputs have a null previous outpoint (all zeros).
func IsCoinbaseInput(txIn *wire.TxIn) bool {
    return txIn.PreviousOutPoint.Hash.IsEqual(&chainhash.Hash{})
}

// HistoryEntry represents a single entry in a scripthash's transaction history.
// This is returned by the Electrum protocol's get_history method.
type HistoryEntry struct {
    // TxHash is the transaction ID in display format (reversed hex).
    TxHash string `json:"tx_hash"`

    // Height is the confirmation height.
    // 0 = unconfirmed (in mempool)
    // -1 = unconfirmed with unconfirmed parents
    // >0 = confirmed at this height
    Height int32 `json:"height"`
}

// UTXOEntry represents an unspent output for the listunspent response.
type UTXOEntry struct {
    // TxHash is the transaction ID in display format.
    TxHash string `json:"tx_hash"`

    // TxPos is the output index (vout).
    TxPos uint32 `json:"tx_pos"`

    // Height is the confirmation height (0 for unconfirmed).
    Height int32 `json:"height"`

    // Value is the output amount in satoshis.
    Value int64 `json:"value"`
}

// Balance represents confirmed and unconfirmed balances.
type Balance struct {
    // Confirmed is the sum of confirmed UTXO values.
    Confirmed int64 `json:"confirmed"`

    // Unconfirmed is the sum of unconfirmed UTXO values.
    // Can be negative if confirmed UTXOs are spent in mempool.
    Unconfirmed int64 `json:"unconfirmed"`
}

// MempoolEntry represents a mempool transaction output.
type MempoolEntry struct {
    // Txid as raw bytes
    Txid []byte

    // Vout is the output index
    Vout uint32

    // Scripthash of this output
    Scripthash []byte

    // Value in satoshis
    Value int64

    // Fee for the transaction (if known)
    Fee int64

    // SpentBy is the txid that spends this output (if any), nil if unspent.
    // This handles mempool chains (unconfirmed spending unconfirmed).
    SpentBy []byte
}

// BlockEvent represents a notification about a new block.
// Used for subscription notifications.
type BlockEvent struct {
    Height    int32
    BlockHash []byte
    Header    []byte
}

// ScripthashEvent represents a notification about scripthash activity.
// Used for subscription notifications.
type ScripthashEvent struct {
    Scripthash []byte
    Status     string // The status hash for this scripthash
}
