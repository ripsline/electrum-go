package storage

import (
    "encoding/binary"
    "errors"
    "fmt"
)

const (
    PrefixUTXO    byte = 'u'
    PrefixHistory byte = 'h'
    PrefixTxIndex byte = 't'
    PrefixUndo    byte = 'd'
    PrefixHeader  byte = 'b'
    PrefixMempool byte = 'm'
    PrefixBlockTx byte = 'x'

    PrefixTxPos  byte = 'p'
    PrefixTxBlob byte = 'r'
    PrefixTxOffs byte = 'o'

    KeyCheckpoint = "c"
)

const (
    ScripthashLength = 32
    TxidLength       = 32
    VoutLength       = 4
    HeightLength     = 4
    BlockHashLength  = 32
)

func MakeUTXOKey(scripthash, txid []byte, vout uint32) ([]byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }
    if len(txid) != TxidLength {
        return nil, fmt.Errorf("invalid txid length: got %d, want %d",
            len(txid), TxidLength)
    }

    key := make([]byte, 1+ScripthashLength+TxidLength+VoutLength)
    key[0] = PrefixUTXO
    copy(key[1:33], scripthash)
    copy(key[33:65], txid)
    binary.BigEndian.PutUint32(key[65:69], vout)

    return key, nil
}

func ParseUTXOKey(key []byte) (scripthash, txid []byte, vout uint32,
    err error) {
    expectedLen := 1 + ScripthashLength + TxidLength + VoutLength
    if len(key) != expectedLen {
        return nil, nil, 0,
            fmt.Errorf("invalid UTXO key length: got %d, want %d",
                len(key), expectedLen)
    }
    if key[0] != PrefixUTXO {
        return nil, nil, 0,
            fmt.Errorf("invalid UTXO key prefix: got %c, want %c",
                key[0], PrefixUTXO)
    }

    scripthash = make([]byte, ScripthashLength)
    txid = make([]byte, TxidLength)
    copy(scripthash, key[1:33])
    copy(txid, key[33:65])
    vout = binary.BigEndian.Uint32(key[65:69])

    return scripthash, txid, vout, nil
}

func MakeUTXOPrefix(scripthash []byte) ([]byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }

    prefix := make([]byte, 1+ScripthashLength)
    prefix[0] = PrefixUTXO
    copy(prefix[1:], scripthash)

    return prefix, nil
}

// History key format:
// h + scripthash(32) + height(4) + txIndex(4) + index(4)
func MakeHistoryKey(scripthash []byte, height int32, txIndex, index uint32) (
    []byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }

    key := make([]byte, 1+ScripthashLength+HeightLength+VoutLength+VoutLength)
    key[0] = PrefixHistory
    copy(key[1:33], scripthash)
    binary.BigEndian.PutUint32(key[33:37], uint32(height))
    binary.BigEndian.PutUint32(key[37:41], txIndex)
    binary.BigEndian.PutUint32(key[41:45], index)

    return key, nil
}

func ParseHistoryKey(key []byte) (scripthash []byte, height int32,
    txIndex uint32, index uint32, err error) {
    expectedLen := 1 + ScripthashLength + HeightLength + VoutLength + VoutLength
    if len(key) != expectedLen {
        return nil, 0, 0, 0,
            fmt.Errorf("invalid history key length: got %d, want %d",
                len(key), expectedLen)
    }
    if key[0] != PrefixHistory {
        return nil, 0, 0, 0,
            fmt.Errorf("invalid history key prefix: got %c, want %c",
                key[0], PrefixHistory)
    }

    scripthash = make([]byte, ScripthashLength)
    copy(scripthash, key[1:33])
    height = int32(binary.BigEndian.Uint32(key[33:37]))
    txIndex = binary.BigEndian.Uint32(key[37:41])
    index = binary.BigEndian.Uint32(key[41:45])

    return scripthash, height, txIndex, index, nil
}

func MakeHistoryPrefix(scripthash []byte) ([]byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }

    prefix := make([]byte, 1+ScripthashLength)
    prefix[0] = PrefixHistory
    copy(prefix[1:], scripthash)

    return prefix, nil
}

func MakeTxIndexKey(txid []byte, vout uint32) ([]byte, error) {
    if len(txid) != TxidLength {
        return nil, fmt.Errorf("invalid txid length: got %d, want %d",
            len(txid), TxidLength)
    }

    key := make([]byte, 1+TxidLength+VoutLength)
    key[0] = PrefixTxIndex
    copy(key[1:33], txid)
    binary.BigEndian.PutUint32(key[33:37], vout)

    return key, nil
}

func ParseTxIndexKey(key []byte) (txid []byte, vout uint32, err error) {
    expectedLen := 1 + TxidLength + VoutLength
    if len(key) != expectedLen {
        return nil, 0, fmt.Errorf("invalid tx index key length: got %d, want %d",
            len(key), expectedLen)
    }
    if key[0] != PrefixTxIndex {
        return nil, 0, fmt.Errorf("invalid tx index key prefix: got %c, want %c",
            key[0], PrefixTxIndex)
    }

    txid = make([]byte, TxidLength)
    copy(txid, key[1:33])
    vout = binary.BigEndian.Uint32(key[33:37])

    return txid, vout, nil
}

func MakeTxPosKey(txid []byte) ([]byte, error) {
    if len(txid) != TxidLength {
        return nil, fmt.Errorf("invalid txid length: got %d, want %d",
            len(txid), TxidLength)
    }

    key := make([]byte, 1+TxidLength)
    key[0] = PrefixTxPos
    copy(key[1:33], txid)
    return key, nil
}

func MakeTxBlobKey(height int32) ([]byte, error) {
    if height < 0 {
        return nil, errors.New("tx blob key height cannot be negative")
    }

    key := make([]byte, 1+HeightLength)
    key[0] = PrefixTxBlob
    binary.BigEndian.PutUint32(key[1:5], uint32(height))
    return key, nil
}

func MakeTxOffsetsKey(height int32) ([]byte, error) {
    if height < 0 {
        return nil, errors.New("tx offsets key height cannot be negative")
    }

    key := make([]byte, 1+HeightLength)
    key[0] = PrefixTxOffs
    binary.BigEndian.PutUint32(key[1:5], uint32(height))
    return key, nil
}

func MakeUndoKey(height int32, blockHash []byte) ([]byte, error) {
    if len(blockHash) != BlockHashLength {
        return nil, fmt.Errorf("invalid block hash length: got %d, want %d",
            len(blockHash), BlockHashLength)
    }
    if height < 0 {
        return nil, errors.New("undo key height cannot be negative")
    }

    key := make([]byte, 1+HeightLength+BlockHashLength)
    key[0] = PrefixUndo
    binary.BigEndian.PutUint32(key[1:5], uint32(height))
    copy(key[5:37], blockHash)

    return key, nil
}

func ParseUndoKey(key []byte) (height int32, blockHash []byte, err error) {
    expectedLen := 1 + HeightLength + BlockHashLength
    if len(key) != expectedLen {
        return 0, nil, fmt.Errorf("invalid undo key length: got %d, want %d",
            len(key), expectedLen)
    }
    if key[0] != PrefixUndo {
        return 0, nil, fmt.Errorf("invalid undo key prefix: got %c, want %c",
            key[0], PrefixUndo)
    }

    height = int32(binary.BigEndian.Uint32(key[1:5]))
    blockHash = make([]byte, BlockHashLength)
    copy(blockHash, key[5:37])

    return height, blockHash, nil
}

func MakeHeaderKey(height int32) ([]byte, error) {
    if height < 0 {
        return nil, errors.New("header key height cannot be negative")
    }

    key := make([]byte, 1+HeightLength)
    key[0] = PrefixHeader
    binary.BigEndian.PutUint32(key[1:5], uint32(height))

    return key, nil
}

func ParseHeaderKey(key []byte) (height int32, err error) {
    expectedLen := 1 + HeightLength
    if len(key) != expectedLen {
        return 0, fmt.Errorf("invalid header key length: got %d, want %d",
            len(key), expectedLen)
    }
    if key[0] != PrefixHeader {
        return 0, fmt.Errorf("invalid header key prefix: got %c, want %c",
            key[0], PrefixHeader)
    }

    height = int32(binary.BigEndian.Uint32(key[1:5]))
    return height, nil
}

func MakeMempoolKey(scripthash, txid []byte, vout uint32) ([]byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }
    if len(txid) != TxidLength {
        return nil, fmt.Errorf("invalid txid length: got %d, want %d",
            len(txid), TxidLength)
    }

    key := make([]byte, 1+ScripthashLength+TxidLength+VoutLength)
    key[0] = PrefixMempool
    copy(key[1:33], scripthash)
    copy(key[33:65], txid)
    binary.BigEndian.PutUint32(key[65:69], vout)

    return key, nil
}

func ParseMempoolKey(key []byte) (scripthash, txid []byte, vout uint32,
    err error) {
    expectedLen := 1 + ScripthashLength + TxidLength + VoutLength
    if len(key) != expectedLen {
        return nil, nil, 0,
            fmt.Errorf("invalid mempool key length: got %d, want %d",
                len(key), expectedLen)
    }
    if key[0] != PrefixMempool {
        return nil, nil, 0,
            fmt.Errorf("invalid mempool key prefix: got %c, want %c",
                key[0], PrefixMempool)
    }

    scripthash = make([]byte, ScripthashLength)
    txid = make([]byte, TxidLength)
    copy(scripthash, key[1:33])
    copy(txid, key[33:65])
    vout = binary.BigEndian.Uint32(key[65:69])

    return scripthash, txid, vout, nil
}

func MakeMempoolPrefix(scripthash []byte) ([]byte, error) {
    if len(scripthash) != ScripthashLength {
        return nil, fmt.Errorf("invalid scripthash length: got %d, want %d",
            len(scripthash), ScripthashLength)
    }

    prefix := make([]byte, 1+ScripthashLength)
    prefix[0] = PrefixMempool
    copy(prefix[1:], scripthash)

    return prefix, nil
}

// Block txid list key: x + height(4)
func MakeBlockTxidsKey(height int32) ([]byte, error) {
    if height < 0 {
        return nil, errors.New("block txids key height cannot be negative")
    }

    key := make([]byte, 1+HeightLength)
    key[0] = PrefixBlockTx
    binary.BigEndian.PutUint32(key[1:5], uint32(height))

    return key, nil
}

func ParseBlockTxidsKey(key []byte) (height int32, err error) {
    expectedLen := 1 + HeightLength
    if len(key) != expectedLen {
        return 0, fmt.Errorf("invalid block txids key length: got %d, want %d",
            len(key), expectedLen)
    }
    if key[0] != PrefixBlockTx {
        return 0, fmt.Errorf("invalid block txids key prefix: got %c, want %c",
            key[0], PrefixBlockTx)
    }

    height = int32(binary.BigEndian.Uint32(key[1:5]))
    return height, nil
}

func PrefixUpperBound(prefix []byte) []byte {
    if len(prefix) == 0 {
        return nil
    }

    end := make([]byte, len(prefix))
    copy(end, prefix)

    for i := len(end) - 1; i >= 0; i-- {
        end[i]++
        if end[i] != 0 {
            return end
        }
    }

    return nil
}