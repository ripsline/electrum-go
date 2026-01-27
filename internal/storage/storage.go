package storage

import (
    "bytes"
    "encoding/binary"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"

    "github.com/cockroachdb/pebble"
    "github.com/klauspost/compress/zstd"
)

type DB struct {
    pebble *pebble.DB
    path   string
    cache  *pebble.Cache
}

type Checkpoint struct {
    Height int32 `json:"height"`

    BlockHash string `json:"block_hash"`

    StartHeight int32 `json:"start_height,omitempty"`
}

type UTXOValue struct {
    Value int64

    Height int32

    BlockHash []byte
}

const UTXOValueEncodedSize = 8 + 4 + 32

func EncodeUTXOValue(u *UTXOValue) []byte {
    buf := make([]byte, UTXOValueEncodedSize)

    binary.LittleEndian.PutUint64(buf[0:8], uint64(u.Value))
    binary.LittleEndian.PutUint32(buf[8:12], uint32(u.Height))

    if len(u.BlockHash) == 32 {
        copy(buf[12:44], u.BlockHash)
    }

    return buf
}

func DecodeUTXOValue(data []byte) (*UTXOValue, error) {
    if len(data) != UTXOValueEncodedSize {
        return nil, fmt.Errorf("invalid UTXO value size: got %d, want %d",
            len(data), UTXOValueEncodedSize)
    }

    u := &UTXOValue{
        Value:     int64(binary.LittleEndian.Uint64(data[0:8])),
        Height:    int32(binary.LittleEndian.Uint32(data[8:12])),
        BlockHash: make([]byte, 32),
    }
    copy(u.BlockHash, data[12:44])

    return u, nil
}

func (u *UTXOValue) BlockHashHex() string {
    if len(u.BlockHash) != 32 {
        return ""
    }
    reversed := make([]byte, 32)
    for i := 0; i < 32; i++ {
        reversed[i] = u.BlockHash[31-i]
    }
    return hex.EncodeToString(reversed)
}

func (u *UTXOValue) SetBlockHashFromHex(hexStr string) error {
    if len(hexStr) != 64 {
        return errors.New("invalid block hash hex length")
    }

    bytes, err := hex.DecodeString(hexStr)
    if err != nil {
        return err
    }

    u.BlockHash = make([]byte, 32)
    for i := 0; i < 32; i++ {
        u.BlockHash[i] = bytes[31-i]
    }

    return nil
}

type MempoolValue struct {
    Value int64

    FirstSeen int64

    Fee int64
}

const MempoolValueEncodedSize = 8 + 8 + 8

func EncodeMempoolValue(m *MempoolValue) []byte {
    buf := make([]byte, MempoolValueEncodedSize)

    binary.LittleEndian.PutUint64(buf[0:8], uint64(m.Value))
    binary.LittleEndian.PutUint64(buf[8:16], uint64(m.FirstSeen))
    binary.LittleEndian.PutUint64(buf[16:24], uint64(m.Fee))

    return buf
}

func DecodeMempoolValue(data []byte) (*MempoolValue, error) {
    if len(data) != MempoolValueEncodedSize {
        return nil, fmt.Errorf("invalid mempool value size: got %d, want %d",
            len(data), MempoolValueEncodedSize)
    }

    return &MempoolValue{
        Value:     int64(binary.LittleEndian.Uint64(data[0:8])),
        FirstSeen: int64(binary.LittleEndian.Uint64(data[8:16])),
        Fee:       int64(binary.LittleEndian.Uint64(data[16:24])),
    }, nil
}

func Open(path string) (*DB, error) {
    cache := pebble.NewCache(256 << 20)

    opts := &pebble.Options{
        Cache: cache,

        MemTableSize: 64 << 20,

        Levels: []pebble.LevelOptions{
            {Compression: pebble.SnappyCompression},
            {Compression: pebble.SnappyCompression},
            {Compression: pebble.ZstdCompression},
            {Compression: pebble.ZstdCompression},
            {Compression: pebble.ZstdCompression},
            {Compression: pebble.ZstdCompression},
            {Compression: pebble.ZstdCompression},
        },

        L0CompactionThreshold: 4,
        L0StopWritesThreshold: 12,

        DisableWAL: false,

        WALBytesPerSync: 0,

        FormatMajorVersion: pebble.FormatNewest,
    }

    db, err := pebble.Open(path, opts)
    if err != nil {
        cache.Unref()
        return nil, fmt.Errorf("failed to open pebble database at %s: %w",
            path, err)
    }

    return &DB{
        pebble: db,
        path:   path,
        cache:  cache,
    }, nil
}

func (db *DB) Close() error {
    if db.pebble == nil {
        return nil
    }

    if err := db.pebble.Close(); err != nil {
        return fmt.Errorf("failed to close database: %w", err)
    }

    db.pebble = nil

    if db.cache != nil {
        db.cache.Unref()
        db.cache = nil
    }

    return nil
}

func (db *DB) Pebble() *pebble.DB {
    return db.pebble
}

func (db *DB) NewBatch() *pebble.Batch {
    return db.pebble.NewBatch()
}

func (db *DB) LoadCheckpoint() (Checkpoint, error) {
    var checkpoint Checkpoint

    value, closer, err := db.pebble.Get([]byte(KeyCheckpoint))
    if err == pebble.ErrNotFound {
        return checkpoint, nil
    }
    if err != nil {
        return checkpoint, fmt.Errorf("failed to load checkpoint: %w", err)
    }
    defer closer.Close()

    if err := json.Unmarshal(value, &checkpoint); err != nil {
        return checkpoint, fmt.Errorf("failed to parse checkpoint: %w", err)
    }

    return checkpoint, nil
}

func (db *DB) SaveCheckpoint(checkpoint Checkpoint) error {
    data, err := json.Marshal(checkpoint)
    if err != nil {
        return fmt.Errorf("failed to marshal checkpoint: %w", err)
    }

    if err := db.pebble.Set([]byte(KeyCheckpoint), data, pebble.Sync); err != nil {
        return fmt.Errorf("failed to save checkpoint: %w", err)
    }

    return nil
}

func (db *DB) SaveCheckpointInBatch(batch *pebble.Batch,
    checkpoint Checkpoint) error {
    data, err := json.Marshal(checkpoint)
    if err != nil {
        return fmt.Errorf("failed to marshal checkpoint: %w", err)
    }

    if err := batch.Set([]byte(KeyCheckpoint), data, nil); err != nil {
        return fmt.Errorf("failed to add checkpoint to batch: %w", err)
    }

    return nil
}

func (db *DB) SaveHeader(height int32, header []byte) error {
    if len(header) != 80 {
        return fmt.Errorf("invalid header length: got %d, want 80",
            len(header))
    }

    key, err := MakeHeaderKey(height)
    if err != nil {
        return fmt.Errorf("failed to make header key: %w", err)
    }

    if err := db.pebble.Set(key, header, pebble.Sync); err != nil {
        return fmt.Errorf("failed to save header at height %d: %w", height, err)
    }

    return nil
}

func (db *DB) SaveHeaderInBatch(batch *pebble.Batch, height int32,
    header []byte) error {
    if len(header) != 80 {
        return fmt.Errorf("invalid header length: got %d, want 80",
            len(header))
    }

    key, err := MakeHeaderKey(height)
    if err != nil {
        return fmt.Errorf("failed to make header key: %w", err)
    }

    if err := batch.Set(key, header, nil); err != nil {
        return fmt.Errorf("failed to add header to batch: %w", err)
    }

    return nil
}

func (db *DB) GetHeader(height int32) ([]byte, error) {
    key, err := MakeHeaderKey(height)
    if err != nil {
        return nil, fmt.Errorf("failed to make header key: %w", err)
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("header not found at height %d", height)
    }
    if err != nil {
        return nil, fmt.Errorf("failed to get header at height %d: %w",
            height, err)
    }
    defer closer.Close()

    header := make([]byte, len(value))
    copy(header, value)

    return header, nil
}

func (db *DB) GetHeaderHex(height int32) (string, error) {
    header, err := db.GetHeader(height)
    if err != nil {
        return "", err
    }

    return hex.EncodeToString(header), nil
}

func (db *DB) DeleteHeaderInBatch(batch *pebble.Batch, height int32) error {
    key, err := MakeHeaderKey(height)
    if err != nil {
        return fmt.Errorf("failed to make header key: %w", err)
    }

    if err := batch.Delete(key, nil); err != nil {
        return fmt.Errorf("failed to add header deletion to batch: %w", err)
    }

    return nil
}

func (db *DB) SaveBlockTxidsInBatch(batch *pebble.Batch, height int32,
    txids [][]byte) error {
    key, err := MakeBlockTxidsKey(height)
    if err != nil {
        return err
    }

    buf := make([]byte, 0, len(txids)*TxidLength)
    for _, txid := range txids {
        if len(txid) != TxidLength {
            return fmt.Errorf("invalid txid length: got %d, want %d",
                len(txid), TxidLength)
        }
        buf = append(buf, txid...)
    }

    if err := batch.Set(key, buf, nil); err != nil {
        return fmt.Errorf("failed to save block txids: %w", err)
    }

    return nil
}

func (db *DB) GetBlockTxids(height int32) ([][]byte, error) {
    key, err := MakeBlockTxidsKey(height)
    if err != nil {
        return nil, err
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("block txids not found at height %d", height)
    }
    if err != nil {
        return nil, fmt.Errorf("failed to get block txids at height %d: %w",
            height, err)
    }
    defer closer.Close()

    if len(value)%TxidLength != 0 {
        return nil, fmt.Errorf("invalid block txids value length: %d",
            len(value))
    }

    count := len(value) / TxidLength
    txids := make([][]byte, 0, count)
    for i := 0; i < count; i++ {
        start := i * TxidLength
        end := start + TxidLength
        txid := make([]byte, TxidLength)
        copy(txid, value[start:end])
        txids = append(txids, txid)
    }

    return txids, nil
}

func (db *DB) DeleteBlockTxidsInBatch(batch *pebble.Batch,
    height int32) error {
    key, err := MakeBlockTxidsKey(height)
    if err != nil {
        return err
    }
    if err := batch.Delete(key, nil); err != nil {
        return fmt.Errorf("failed to delete block txids: %w", err)
    }
    return nil
}

func (db *DB) SaveTxPosInBatch(batch *pebble.Batch, txid []byte, height int32, txIndex uint32) error {
    key, err := MakeTxPosKey(txid)
    if err != nil {
        return err
    }

    buf := make([]byte, 8)
    binary.BigEndian.PutUint32(buf[0:4], uint32(height))
    binary.BigEndian.PutUint32(buf[4:8], txIndex)

    if err := batch.Set(key, buf, nil); err != nil {
        return fmt.Errorf("failed to save tx position: %w", err)
    }

    return nil
}

func (db *DB) GetTxPos(txid []byte) (int32, uint32, bool, error) {
    key, err := MakeTxPosKey(txid)
    if err != nil {
        return 0, 0, false, err
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return 0, 0, false, nil
    }
    if err != nil {
        return 0, 0, false, fmt.Errorf("failed to get tx position: %w", err)
    }
    defer closer.Close()

    if len(value) != 8 {
        return 0, 0, false, fmt.Errorf("invalid tx position value length: %d", len(value))
    }

    height := int32(binary.BigEndian.Uint32(value[0:4]))
    txIndex := binary.BigEndian.Uint32(value[4:8])

    return height, txIndex, true, nil
}

func (db *DB) SaveTxBlobInBatch(batch *pebble.Batch, height int32, blob []byte) error {
    key, err := MakeTxBlobKey(height)
    if err != nil {
        return err
    }

    compressed, err := compressZstd(blob)
    if err != nil {
        return fmt.Errorf("failed to compress tx blob: %w", err)
    }

    if err := batch.Set(key, compressed, nil); err != nil {
        return fmt.Errorf("failed to save tx blob: %w", err)
    }
    return nil
}

func (db *DB) GetTxBlob(height int32) ([]byte, error) {
    key, err := MakeTxBlobKey(height)
    if err != nil {
        return nil, err
    }

    value, closer, err := db.pebble.Get(key)
    if err != pebble.ErrNotFound && err != nil {
        return nil, fmt.Errorf("failed to get tx blob: %w", err)
    }
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("tx blob not found for height %d", height)
    }
    defer closer.Close()

    dataCopy := make([]byte, len(value))
    copy(dataCopy, value)

    blob, err := decompressZstd(dataCopy)
    if err != nil {
        return nil, fmt.Errorf("failed to decompress tx blob: %w", err)
    }

    return blob, nil
}

func (db *DB) SaveTxOffsetsInBatch(batch *pebble.Batch, height int32, offsets []uint32) error {
    key, err := MakeTxOffsetsKey(height)
    if err != nil {
        return err
    }

    buf := make([]byte, len(offsets)*4)
    for i, off := range offsets {
        binary.BigEndian.PutUint32(buf[i*4:(i+1)*4], off)
    }

    if err := batch.Set(key, buf, nil); err != nil {
        return fmt.Errorf("failed to save tx offsets: %w", err)
    }

    return nil
}

func (db *DB) GetTxOffsets(height int32) ([]uint32, error) {
    key, err := MakeTxOffsetsKey(height)
    if err != nil {
        return nil, err
    }

    value, closer, err := db.pebble.Get(key)
    if err != pebble.ErrNotFound && err != nil {
        return nil, fmt.Errorf("failed to get tx offsets: %w", err)
    }
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("tx offsets not found for height %d", height)
    }
    defer closer.Close()

    if len(value)%4 != 0 {
        return nil, fmt.Errorf("invalid tx offsets value length: %d", len(value))
    }

    count := len(value) / 4
    offsets := make([]uint32, count)
    for i := 0; i < count; i++ {
        offsets[i] = binary.BigEndian.Uint32(value[i*4 : (i+1)*4])
    }

    return offsets, nil
}

func compressZstd(data []byte) ([]byte, error) {
    enc, err := zstd.NewWriter(nil)
    if err != nil {
        return nil, err
    }
    defer enc.Close()
    out := enc.EncodeAll(data, nil)
    return out, nil
}

func decompressZstd(data []byte) ([]byte, error) {
    dec, err := zstd.NewReader(nil)
    if err != nil {
        return nil, err
    }
    defer dec.Close()
    out, err := dec.DecodeAll(data, nil)
    if err != nil {
        return nil, err
    }
    return out, nil
}

func (db *DB) GetUTXO(scripthash, txid []byte, vout uint32) (*UTXOValue,
    error) {
    key, err := MakeUTXOKey(scripthash, txid, vout)
    if err != nil {
        return nil, fmt.Errorf("failed to make UTXO key: %w", err)
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("failed to get UTXO: %w", err)
    }
    defer closer.Close()

    data := make([]byte, len(value))
    copy(data, value)

    return DecodeUTXOValue(data)
}

func (db *DB) GetScripthashForOutpoint(txid []byte, vout uint32) ([]byte,
    error) {
    key, err := MakeTxIndexKey(txid, vout)
    if err != nil {
        return nil, fmt.Errorf("failed to make tx index key: %w", err)
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return nil, nil
    }
    if err != nil {
        return nil, fmt.Errorf("failed to get scripthash for outpoint: %w",
            err)
    }
    defer closer.Close()

    scripthash := make([]byte, len(value))
    copy(scripthash, value)

    return scripthash, nil
}

func (db *DB) NewPrefixIterator(prefix []byte) (*pebble.Iterator, error) {
    opts := &pebble.IterOptions{
        LowerBound: prefix,
        UpperBound: PrefixUpperBound(prefix),
    }

    iter, err := db.pebble.NewIter(opts)
    if err != nil {
        return nil, fmt.Errorf("failed to create iterator: %w", err)
    }

    return iter, nil
}

func (db *DB) NewRangeIterator(lower, upper []byte) (*pebble.Iterator,
    error) {
    opts := &pebble.IterOptions{
        LowerBound: lower,
        UpperBound: upper,
    }

    iter, err := db.pebble.NewIter(opts)
    if err != nil {
        return nil, fmt.Errorf("failed to create iterator: %w", err)
    }

    return iter, nil
}

// Undo code below is unchanged from your current file

type UndoBlock struct {
    Height int32

    BlockHash []byte

    PrevBlockHash []byte

    SpentOutputs []UndoOutput

    CreatedOutputs []UndoOutput

    HistoryEntries []UndoHistory
}

type UndoOutput struct {
    Scripthash []byte
    Txid       []byte
    Vout       uint32
    Value      int64
    Height     int32
    BlockHash  []byte
}

type UndoHistory struct {
    Scripthash []byte
    Height     int32
    TxIndex    uint32
    Index      uint32
}

func EncodeUndoBlock(undo *UndoBlock) ([]byte, error) {
    buf := &bytes.Buffer{}

    writeU32 := func(v uint32) {
        _ = binary.Write(buf, binary.LittleEndian, v)
    }
    writeI32 := func(v int32) {
        _ = binary.Write(buf, binary.LittleEndian, v)
    }
    writeI64 := func(v int64) {
        _ = binary.Write(buf, binary.LittleEndian, v)
    }

    writeI32(undo.Height)

    if len(undo.BlockHash) != BlockHashLength {
        return nil, fmt.Errorf("invalid undo block hash length: %d",
            len(undo.BlockHash))
    }
    buf.Write(undo.BlockHash)

    if len(undo.PrevBlockHash) != BlockHashLength {
        return nil, fmt.Errorf("invalid undo prev block hash length: %d",
            len(undo.PrevBlockHash))
    }
    buf.Write(undo.PrevBlockHash)

    writeU32(uint32(len(undo.SpentOutputs)))
    writeU32(uint32(len(undo.CreatedOutputs)))
    writeU32(uint32(len(undo.HistoryEntries)))

    for _, o := range undo.SpentOutputs {
        if len(o.Scripthash) != ScripthashLength {
            return nil, fmt.Errorf("invalid undo spent scripthash length")
        }
        if len(o.Txid) != TxidLength {
            return nil, fmt.Errorf("invalid undo spent txid length")
        }
        if len(o.BlockHash) != BlockHashLength {
            return nil, fmt.Errorf("invalid undo spent blockhash length")
        }
        buf.Write(o.Scripthash)
        buf.Write(o.Txid)
        writeU32(o.Vout)
        writeI64(o.Value)
        writeI32(o.Height)
        buf.Write(o.BlockHash)
    }

    for _, o := range undo.CreatedOutputs {
        if len(o.Scripthash) != ScripthashLength {
            return nil, fmt.Errorf("invalid undo created scripthash length")
        }
        if len(o.Txid) != TxidLength {
            return nil, fmt.Errorf("invalid undo created txid length")
        }
        if len(o.BlockHash) != BlockHashLength {
            return nil, fmt.Errorf("invalid undo created blockhash length")
        }
        buf.Write(o.Scripthash)
        buf.Write(o.Txid)
        writeU32(o.Vout)
        writeI64(o.Value)
        writeI32(o.Height)
        buf.Write(o.BlockHash)
    }

    for _, h := range undo.HistoryEntries {
        if len(h.Scripthash) != ScripthashLength {
            return nil, fmt.Errorf("invalid undo history scripthash length")
        }
        buf.Write(h.Scripthash)
        writeI32(h.Height)
        writeU32(h.TxIndex)
        writeU32(h.Index)
    }

    return buf.Bytes(), nil
}

func DecodeUndoBlock(data []byte) (*UndoBlock, error) {
    readU32 := func(r *bytes.Reader) (uint32, error) {
        var v uint32
        err := binary.Read(r, binary.LittleEndian, &v)
        return v, err
    }
    readI32 := func(r *bytes.Reader) (int32, error) {
        var v int32
        err := binary.Read(r, binary.LittleEndian, &v)
        return v, err
    }
    readI64 := func(r *bytes.Reader) (int64, error) {
        var v int64
        err := binary.Read(r, binary.LittleEndian, &v)
        return v, err
    }

    r := bytes.NewReader(data)

    height, err := readI32(r)
    if err != nil {
        return nil, err
    }

    blockHash := make([]byte, BlockHashLength)
    if _, err := r.Read(blockHash); err != nil {
        return nil, err
    }

    prevBlockHash := make([]byte, BlockHashLength)
    if _, err := r.Read(prevBlockHash); err != nil {
        return nil, err
    }

    spentCount, err := readU32(r)
    if err != nil {
        return nil, err
    }
    createdCount, err := readU32(r)
    if err != nil {
        return nil, err
    }
    historyCount, err := readU32(r)
    if err != nil {
        return nil, err
    }

    undo := &UndoBlock{
        Height:         height,
        BlockHash:      blockHash,
        PrevBlockHash:  prevBlockHash,
        SpentOutputs:   make([]UndoOutput, 0, spentCount),
        CreatedOutputs: make([]UndoOutput, 0, createdCount),
        HistoryEntries: make([]UndoHistory, 0, historyCount),
    }

    for i := uint32(0); i < spentCount; i++ {
        sh := make([]byte, ScripthashLength)
        if _, err := r.Read(sh); err != nil {
            return nil, err
        }
        txid := make([]byte, TxidLength)
        if _, err := r.Read(txid); err != nil {
            return nil, err
        }
        vout, err := readU32(r)
        if err != nil {
            return nil, err
        }
        value, err := readI64(r)
        if err != nil {
            return nil, err
        }
        h, err := readI32(r)
        if err != nil {
            return nil, err
        }
        bh := make([]byte, BlockHashLength)
        if _, err := r.Read(bh); err != nil {
            return nil, err
        }

        undo.SpentOutputs = append(undo.SpentOutputs, UndoOutput{
            Scripthash: sh,
            Txid:       txid,
            Vout:       vout,
            Value:      value,
            Height:     h,
            BlockHash:  bh,
        })
    }

    for i := uint32(0); i < createdCount; i++ {
        sh := make([]byte, ScripthashLength)
        if _, err := r.Read(sh); err != nil {
            return nil, err
        }
        txid := make([]byte, TxidLength)
        if _, err := r.Read(txid); err != nil {
            return nil, err
        }
        vout, err := readU32(r)
        if err != nil {
            return nil, err
        }
        value, err := readI64(r)
        if err != nil {
            return nil, err
        }
        h, err := readI32(r)
        if err != nil {
            return nil, err
        }
        bh := make([]byte, BlockHashLength)
        if _, err := r.Read(bh); err != nil {
            return nil, err
        }

        undo.CreatedOutputs = append(undo.CreatedOutputs, UndoOutput{
            Scripthash: sh,
            Txid:       txid,
            Vout:       vout,
            Value:      value,
            Height:     h,
            BlockHash:  bh,
        })
    }

    for i := uint32(0); i < historyCount; i++ {
        sh := make([]byte, ScripthashLength)
        if _, err := r.Read(sh); err != nil {
            return nil, err
        }
        h, err := readI32(r)
        if err != nil {
            return nil, err
        }
        txIndex, err := readU32(r)
        if err != nil {
            return nil, err
        }
        idx, err := readU32(r)
        if err != nil {
            return nil, err
        }

        undo.HistoryEntries = append(undo.HistoryEntries, UndoHistory{
            Scripthash: sh,
            Height:     h,
            TxIndex:    txIndex,
            Index:      idx,
        })
    }

    return undo, nil
}

func (db *DB) SaveUndoInBatch(batch *pebble.Batch, undo *UndoBlock,
    blockHash []byte) error {
    key, err := MakeUndoKey(undo.Height, blockHash)
    if err != nil {
        return fmt.Errorf("failed to make undo key: %w", err)
    }

    data, err := EncodeUndoBlock(undo)
    if err != nil {
        return fmt.Errorf("failed to encode undo data: %w", err)
    }

    if err := batch.Set(key, data, nil); err != nil {
        return fmt.Errorf("failed to add undo data to batch: %w", err)
    }

    return nil
}

func (db *DB) GetUndo(height int32, blockHash []byte) (*UndoBlock, error) {
    key, err := MakeUndoKey(height, blockHash)
    if err != nil {
        return nil, fmt.Errorf("failed to make undo key: %w", err)
    }

    value, closer, err := db.pebble.Get(key)
    if err == pebble.ErrNotFound {
        return nil, fmt.Errorf("undo data not found for block %d", height)
    }
    if err != nil {
        return nil, fmt.Errorf("failed to get undo data: %w", err)
    }
    defer closer.Close()

    undo, err := DecodeUndoBlock(value)
    if err != nil {
        return nil, fmt.Errorf("failed to parse undo data: %w", err)
    }

    return undo, nil
}

func (db *DB) DeleteUndoInBatch(batch *pebble.Batch, height int32,
    blockHash []byte) error {
    key, err := MakeUndoKey(height, blockHash)
    if err != nil {
        return fmt.Errorf("failed to make undo key: %w", err)
    }

    if err := batch.Delete(key, nil); err != nil {
        return fmt.Errorf("failed to add undo deletion to batch: %w", err)
    }

    return nil
}

func (db *DB) PruneUndoData(maxHeightToKeep int32) (int, error) {
    prefix := []byte{PrefixUndo}
    iter, err := db.NewPrefixIterator(prefix)
    if err != nil {
        return 0, fmt.Errorf("failed to create undo iterator: %w", err)
    }
    defer iter.Close()

    batch := db.NewBatch()
    defer batch.Close()

    pruned := 0
    for iter.First(); iter.Valid(); iter.Next() {
        height, _, err := ParseUndoKey(iter.Key())
        if err != nil {
            continue
        }

        if height < maxHeightToKeep {
            key := make([]byte, len(iter.Key()))
            copy(key, iter.Key())

            if err := batch.Delete(key, nil); err != nil {
                return pruned, fmt.Errorf("failed to delete undo at height %d: %w",
                    height, err)
            }
            pruned++
        }
    }

    if err := iter.Error(); err != nil {
        return pruned, fmt.Errorf("iterator error during prune: %w", err)
    }

    if pruned > 0 {
        if err := batch.Commit(pebble.Sync); err != nil {
            return 0, fmt.Errorf("failed to commit prune batch: %w", err)
        }
    }

    return pruned, nil
}