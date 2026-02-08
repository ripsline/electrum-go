package indexer

import (
    "encoding/hex"
    "fmt"
    "log"
    "sync"
    "time"

    "github.com/btcsuite/btcd/wire"
    "github.com/cockroachdb/pebble"

    "github.com/ripsline/electrum-go/internal/storage"
)

type MempoolOverlay struct {
    mu sync.RWMutex

    db *storage.DB

    transactions map[string]*MempoolTransaction
    byScripthash map[string]map[string]bool
    spentBy      map[string]string
    outputs      map[string]*MempoolOutput
}

type MempoolTransaction struct {
    Txid []byte

    TxidHex string

    RawTx *wire.MsgTx

    FirstSeen time.Time

    Fee int64

    Inputs []MempoolInput

    Outputs []MempoolOutput

    Scripthashes [][]byte
}

type MempoolInput struct {
    PrevTxid []byte

    PrevVout uint32

    Scripthash []byte

    Value int64

    FromMempool bool
}

type MempoolOutput struct {
    Txid []byte

    Vout uint32

    Scripthash []byte

    Value int64

    SpentByTxid []byte
}

func NewMempoolOverlay(db *storage.DB) *MempoolOverlay {
    return &MempoolOverlay{
        db:           db,
        transactions: make(map[string]*MempoolTransaction),
        byScripthash: make(map[string]map[string]bool),
        spentBy:      make(map[string]string),
        outputs:      make(map[string]*MempoolOutput),
    }
}

func (m *MempoolOverlay) AddTransaction(tx *wire.MsgTx) ([]string, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    txHash := tx.TxHash()
    txid := TxidFromHash(&txHash)
    txidHex := txHash.String()

    if _, exists := m.transactions[txidHex]; exists {
        return nil, nil
    }

    var inputs []MempoolInput
    var inputValue int64
    affectedScripthashes := make(map[string][]byte)

    for _, txIn := range tx.TxIn {
        if IsCoinbaseInput(txIn) {
            continue
        }

        prevTxid := TxidFromHash(&txIn.PreviousOutPoint.Hash)
        prevVout := txIn.PreviousOutPoint.Index
        outpointKey := makeOutpointKey(prevTxid, prevVout)

        if existingTxid, spent := m.spentBy[outpointKey]; spent {
            return nil, fmt.Errorf("double-spend: output %s:%d already spent by %s",
                TxidToHex(prevTxid), prevVout, existingTxid[:16])
        }

        input := MempoolInput{
            PrevTxid: prevTxid,
            PrevVout: prevVout,
        }

        if mempoolOut, exists := m.outputs[outpointKey]; exists {
            input.FromMempool = true
            input.Scripthash = mempoolOut.Scripthash
            input.Value = mempoolOut.Value
            inputValue += mempoolOut.Value

            shHex := hex.EncodeToString(mempoolOut.Scripthash)
            affectedScripthashes[shHex] = mempoolOut.Scripthash
        } else {
            scripthash, err := m.db.GetScripthashForOutpoint(prevTxid, prevVout)
            if err != nil {
                return nil, fmt.Errorf("failed to lookup input %s:%d: %w",
                    TxidToHex(prevTxid), prevVout, err)
            }

            if scripthash == nil {
                log.Printf("⚠️  Mempool tx %s spends unknown output %s:%d",
                    txidHex[:16], TxidToHex(prevTxid)[:16], prevVout)
            } else {
                input.Scripthash = scripthash

                utxo, err := m.db.GetUTXO(scripthash, prevTxid, prevVout)
                if err != nil {
                    return nil, fmt.Errorf("failed to get UTXO value: %w", err)
                }
                if utxo != nil {
                    input.Value = utxo.Value
                    inputValue += utxo.Value
                }

                shHex := hex.EncodeToString(scripthash)
                affectedScripthashes[shHex] = scripthash
            }
        }

        inputs = append(inputs, input)
    }

    var outputs []MempoolOutput
    var outputValue int64

    for vout, txOut := range tx.TxOut {
        if IsOpReturn(txOut.PkScript) {
            continue
        }

        if txOut.Value == 0 {
            continue
        }

        scripthash := ComputeScripthash(txOut.PkScript)
        scripthashHex := hex.EncodeToString(scripthash)

        output := MempoolOutput{
            Txid:       txid,
            Vout:       uint32(vout),
            Scripthash: scripthash,
            Value:      txOut.Value,
        }

        outputs = append(outputs, output)
        outputValue += txOut.Value

        affectedScripthashes[scripthashHex] = scripthash
    }

    var fee int64
    if inputValue > 0 && inputValue >= outputValue {
        fee = inputValue - outputValue
    }

    scripthashList := make([][]byte, 0, len(affectedScripthashes))
    for _, sh := range affectedScripthashes {
        scripthashList = append(scripthashList, sh)
    }

    mempoolTx := &MempoolTransaction{
        Txid:         txid,
        TxidHex:      txidHex,
        RawTx:        tx,
        FirstSeen:    time.Now(),
        Fee:          fee,
        Inputs:       inputs,
        Outputs:      outputs,
        Scripthashes: scripthashList,
    }

    m.transactions[txidHex] = mempoolTx

    for shHex := range affectedScripthashes {
        if m.byScripthash[shHex] == nil {
            m.byScripthash[shHex] = make(map[string]bool)
        }
        m.byScripthash[shHex][txidHex] = true
    }

    for _, input := range inputs {
        outpointKey := makeOutpointKey(input.PrevTxid, input.PrevVout)
        m.spentBy[outpointKey] = txidHex

        if mempoolOut, exists := m.outputs[outpointKey]; exists {
            mempoolOut.SpentByTxid = txid
        }
    }

    for i := range outputs {
        outpointKey := makeOutpointKey(outputs[i].Txid, outputs[i].Vout)
        m.outputs[outpointKey] = &outputs[i]
    }

    if err := m.persistTransaction(mempoolTx); err != nil {
        log.Printf("⚠️  Failed to persist mempool tx %s: %v", txidHex[:16], err)
    }

    shHexes := make([]string, 0, len(affectedScripthashes))
    for shHex := range affectedScripthashes {
        shHexes = append(shHexes, shHex)
    }

    return shHexes, nil
}

// HasTx checks if a txid exists in the mempool overlay.
func (m *MempoolOverlay) HasTx(txidHex string) bool {
    m.mu.RLock()
    defer m.mu.RUnlock()
    _, ok := m.transactions[txidHex]
    return ok
}

func (m *MempoolOverlay) RemoveTransaction(txid []byte) []string {
    m.mu.Lock()
    defer m.mu.Unlock()

    txidHex := TxidToHex(txid)
    return m.removeTransactionLocked(txidHex)
}

func (m *MempoolOverlay) removeTransactionLocked(txidHex string) []string {
    mempoolTx, exists := m.transactions[txidHex]
    if !exists {
        return nil
    }

    affected := make(map[string]struct{})

    for _, sh := range mempoolTx.Scripthashes {
        shHex := hex.EncodeToString(sh)
        affected[shHex] = struct{}{}
        if txids, ok := m.byScripthash[shHex]; ok {
            delete(txids, txidHex)
            if len(txids) == 0 {
                delete(m.byScripthash, shHex)
            }
        }
    }

    for _, input := range mempoolTx.Inputs {
        outpointKey := makeOutpointKey(input.PrevTxid, input.PrevVout)
        delete(m.spentBy, outpointKey)
    }

    for _, output := range mempoolTx.Outputs {
        outpointKey := makeOutpointKey(output.Txid, output.Vout)
        delete(m.outputs, outpointKey)
    }

    delete(m.transactions, txidHex)

    m.unpersistTransaction(mempoolTx)

    shHexes := make([]string, 0, len(affected))
    for shHex := range affected {
        shHexes = append(shHexes, shHex)
    }

    return shHexes
}

func (m *MempoolOverlay) RemoveOutput(txid []byte, vout uint32) {
    m.mu.Lock()
    defer m.mu.Unlock()

    outpointKey := makeOutpointKey(txid, vout)

    delete(m.outputs, outpointKey)
    delete(m.spentBy, outpointKey)
}

func (m *MempoolOverlay) GetScripthashTransactions(scripthash []byte) []string {
    m.mu.RLock()
    defer m.mu.RUnlock()

    shHex := hex.EncodeToString(scripthash)
    txids := m.byScripthash[shHex]

    result := make([]string, 0, len(txids))
    for txidHex := range txids {
        result = append(result, txidHex)
    }

    return result
}

func (m *MempoolOverlay) GetUnspentOutputs(scripthash []byte) []MempoolOutput {
    m.mu.RLock()
    defer m.mu.RUnlock()

    shHex := hex.EncodeToString(scripthash)

    var result []MempoolOutput

    for _, output := range m.outputs {
        if hex.EncodeToString(output.Scripthash) != shHex {
            continue
        }
        if output.SpentByTxid != nil && len(output.SpentByTxid) > 0 {
            continue
        }
        result = append(result, *output)
    }

    return result
}

func (m *MempoolOverlay) GetBalance(scripthash []byte) int64 {
    m.mu.RLock()
    defer m.mu.RUnlock()

    shHex := hex.EncodeToString(scripthash)
    txids := m.byScripthash[shHex]

    var balance int64

    for txidHex := range txids {
        mempoolTx := m.transactions[txidHex]
        if mempoolTx == nil {
            continue
        }

        for _, output := range mempoolTx.Outputs {
            if hex.EncodeToString(output.Scripthash) == shHex {
                if output.SpentByTxid == nil || len(output.SpentByTxid) == 0 {
                    balance += output.Value
                }
            }
        }

        for _, input := range mempoolTx.Inputs {
            if input.Scripthash != nil &&
                hex.EncodeToString(input.Scripthash) == shHex {
                balance -= input.Value
            }
        }
    }

    return balance
}

func (m *MempoolOverlay) IsOutputSpent(txid []byte, vout uint32) bool {
    m.mu.RLock()
    defer m.mu.RUnlock()

    outpointKey := makeOutpointKey(txid, vout)
    _, spent := m.spentBy[outpointKey]
    return spent
}

func (m *MempoolOverlay) Clear() {
    m.mu.Lock()
    defer m.mu.Unlock()

    m.transactions = make(map[string]*MempoolTransaction)
    m.byScripthash = make(map[string]map[string]bool)
    m.spentBy = make(map[string]string)
    m.outputs = make(map[string]*MempoolOutput)

    m.clearPersistedData()

    log.Println("🗑️  Mempool cleared")
}

func (m *MempoolOverlay) Count() int {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return len(m.transactions)
}

func (m *MempoolOverlay) Size() int {
    m.mu.RLock()
    defer m.mu.RUnlock()
    return len(m.outputs)
}

func (m *MempoolOverlay) ReconcileWith(validTxids map[string]bool) (int, []string) {
    m.mu.Lock()
    defer m.mu.Unlock()

    removed := 0
    affected := make(map[string]struct{})

    for txidHex := range m.transactions {
        if !validTxids[txidHex] {
            shHexes := m.removeTransactionLocked(txidHex)
            for _, shHex := range shHexes {
                affected[shHex] = struct{}{}
            }
            removed++
        }
    }

    for outpointKey, out := range m.outputs {
        txidHex := TxidToHex(out.Txid)
        if !validTxids[txidHex] {
            delete(m.outputs, outpointKey)

            shHex := hex.EncodeToString(out.Scripthash)
            affected[shHex] = struct{}{}

            if txids, ok := m.byScripthash[shHex]; ok {
                delete(txids, txidHex)
                if len(txids) == 0 {
                    delete(m.byScripthash, shHex)
                }
            }
            removed++
        }
    }

    for outpointKey, spender := range m.spentBy {
        if !validTxids[spender] {
            delete(m.spentBy, outpointKey)
        }
    }

    shHexes := make([]string, 0, len(affected))
    for shHex := range affected {
        shHexes = append(shHexes, shHex)
    }

    return removed, shHexes
}

func (m *MempoolOverlay) persistTransaction(mempoolTx *MempoolTransaction) error {
    if m.db == nil {
        return nil
    }

    batch := m.db.NewBatch()
    defer batch.Close()

    for _, output := range mempoolTx.Outputs {
        key, err := storage.MakeMempoolKey(output.Scripthash, output.Txid,
            output.Vout)
        if err != nil {
            return fmt.Errorf("failed to make mempool key: %w", err)
        }

        value := storage.MempoolValue{
            Value:     output.Value,
            FirstSeen: mempoolTx.FirstSeen.Unix(),
            Fee:       mempoolTx.Fee,
        }

        valueBin := storage.EncodeMempoolValue(&value)

        if err := batch.Set(key, valueBin, nil); err != nil {
            return fmt.Errorf("failed to write mempool entry: %w", err)
        }
    }

    return batch.Commit(pebble.Sync)
}

func (m *MempoolOverlay) unpersistTransaction(mempoolTx *MempoolTransaction) {
    if m.db == nil {
        return
    }

    batch := m.db.NewBatch()
    defer batch.Close()

    for _, output := range mempoolTx.Outputs {
        key, err := storage.MakeMempoolKey(output.Scripthash, output.Txid,
            output.Vout)
        if err != nil {
            continue
        }
        batch.Delete(key, nil)
    }

    batch.Commit(pebble.Sync)
}

func (m *MempoolOverlay) clearPersistedData() {
    if m.db == nil {
        return
    }

    prefix := []byte{storage.PrefixMempool}
    iter, err := m.db.NewPrefixIterator(prefix)
    if err != nil {
        log.Printf("⚠️  Failed to create mempool iterator: %v", err)
        return
    }
    defer iter.Close()

    batch := m.db.NewBatch()
    defer batch.Close()

    count := 0
    for iter.First(); iter.Valid(); iter.Next() {
        key := make([]byte, len(iter.Key()))
        copy(key, iter.Key())
        batch.Delete(key, nil)
        count++
    }

    if count > 0 {
        batch.Commit(pebble.Sync)
        log.Printf("🗑️  Cleared %d mempool entries from database", count)
    }
}

func makeOutpointKey(txid []byte, vout uint32) string {
    return fmt.Sprintf("%s:%d", hex.EncodeToString(txid), vout)
}

func (m *MempoolOverlay) LoadFromDatabase() error {
    m.mu.Lock()
    defer m.mu.Unlock()

    if m.db == nil {
        return nil
    }

    prefix := []byte{storage.PrefixMempool}
    iter, err := m.db.NewPrefixIterator(prefix)
    if err != nil {
        return fmt.Errorf("failed to create mempool iterator: %w", err)
    }
    defer iter.Close()

    count := 0
    for iter.First(); iter.Valid(); iter.Next() {
        scripthash, txid, vout, err := storage.ParseMempoolKey(iter.Key())
        if err != nil {
            continue
        }

        valueCopy := make([]byte, len(iter.Value()))
        copy(valueCopy, iter.Value())

        value, err := storage.DecodeMempoolValue(valueCopy)
        if err != nil {
            continue
        }

        outpointKey := makeOutpointKey(txid, vout)
        m.outputs[outpointKey] = &MempoolOutput{
            Txid:       txid,
            Vout:       vout,
            Scripthash: scripthash,
            Value:      value.Value,
        }

        shHex := hex.EncodeToString(scripthash)
        txidHex := TxidToHex(txid)
        if m.byScripthash[shHex] == nil {
            m.byScripthash[shHex] = make(map[string]bool)
        }
        m.byScripthash[shHex][txidHex] = true

        count++
    }

    if count > 0 {
        log.Printf("📥 Loaded %d mempool entries from database", count)
    }

    return nil
}