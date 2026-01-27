package electrum

import (
    "bytes"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/wire"

    "electrum-go/internal/indexer"
)

func (h *ConnectionHandler) handleMethod(method string,
    params json.RawMessage) (interface{}, *Error) {
    switch method {
    case "server.version":
        return h.handleServerVersion(params)
    case "server.banner":
        return h.handleServerBanner(params)
    case "server.donation_address":
        return h.handleServerDonationAddress(params)
    case "server.peers.subscribe":
        return h.handleServerPeersSubscribe(params)
    case "server.ping":
        return h.handleServerPing(params)
    case "server.features":
        return h.handleServerFeatures(params)

    case "blockchain.headers.subscribe":
        return h.handleHeadersSubscribe(params)
    case "blockchain.block.header":
        return h.handleBlockHeader(params)
    case "blockchain.block.headers":
        return h.handleBlockHeaders(params)
    case "blockchain.estimatefee":
        return h.handleEstimateFee(params)
    case "blockchain.relayfee":
        return h.handleRelayFee(params)

    case "blockchain.scripthash.get_history":
        return h.handleScripthashGetHistory(params)
    case "blockchain.scripthash.get_balance":
        return h.handleScripthashGetBalance(params)
    case "blockchain.scripthash.listunspent":
        return h.handleScripthashListUnspent(params)
    case "blockchain.scripthash.subscribe":
        return h.handleScripthashSubscribe(params)
    case "blockchain.scripthash.unsubscribe":
        return h.handleScripthashUnsubscribe(params)
    case "blockchain.scripthash.get_mempool":
        return h.handleScripthashGetMempool(params)

    case "blockchain.transaction.get":
        return h.handleTransactionGet(params)
    case "blockchain.transaction.broadcast":
        return h.handleTransactionBroadcast(params)
    case "blockchain.transaction.get_merkle":
        return h.handleTransactionGetMerkle(params)
    case "blockchain.transaction.id_from_pos":
        return h.handleTransactionIdFromPos(params)

    case "mempool.get_fee_histogram":
        return h.handleMempoolFeeHistogram(params)

    default:
        if h.logReqs {
            log.Printf("⚠️  [%d] Unknown method: %s", h.connID, method)
        }
        return nil, &Error{
            Code:    ErrCodeMethodNotFound,
            Message: fmt.Sprintf("unknown method: %s", method),
        }
    }
}

// ============================================================================
// Server Methods
// ============================================================================

func (h *ConnectionHandler) handleServerVersion(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil {
        args = []interface{}{}
    }

    clientName := "unknown"
    if len(args) > 0 {
        if name, ok := args[0].(string); ok {
            clientName = name
        }
    }

    if h.logReqs {
        log.Printf("   [%d] Client: %s", h.connID, clientName)
    }

    return []string{
        "electrum-go/0.1.0",
        "1.4",
    }, nil
}

func (h *ConnectionHandler) handleServerBanner(params json.RawMessage) (interface{}, *Error) {
    checkpoint, _ := h.server.db.LoadCheckpoint()
    start := checkpoint.StartHeight

    banner := fmt.Sprintf(`
         ╔════════════════════════════════════════════════════════════╗
         ║                    electrum-go Server                      ║
         ║              Forward-Indexing • Pruned Node Ready          ║
         ╠════════════════════════════════════════════════════════════╣
         ║ Indexed from block: %-10d                                  ║
         ║ Current height:     %-10d                                  ║
         ║                                                            ║
         ║ ⚠️  DO NOT IMPORT WALLETS CREATED BEFORE block %d          ║
         ║     create a fresh wallet.                                 ║
         ║                                                            ║
         ║ GitHub: github.com/ripsline/electrum-go                    ║
         ╚════════════════════════════════════════════════════════════╝
`, start, checkpoint.Height, start)

    return banner, nil
}

func (h *ConnectionHandler) handleServerDonationAddress(params json.RawMessage) (interface{}, *Error) {
    return "", nil
}

func (h *ConnectionHandler) handleServerPeersSubscribe(params json.RawMessage) (interface{}, *Error) {
    return []interface{}{}, nil
}

func (h *ConnectionHandler) handleServerPing(params json.RawMessage) (interface{}, *Error) {
    return nil, nil
}

func (h *ConnectionHandler) handleServerFeatures(params json.RawMessage) (interface{}, *Error) {
    genesisHash := ""
    if h.server.client != nil {
        if h0, err := h.server.client.GetBlockHash(0); err == nil {
            genesisHash = h0.String()
        }
    }

    return map[string]interface{}{
        "server_version": "electrum-go/0.1.0",
        "protocol_min":   "1.4",
        "protocol_max":   "1.4",
        "genesis_hash":   genesisHash,
        "hash_function":  "sha256",
        "pruning":        nil,
        "hosts":          map[string]interface{}{},
    }, nil
}

// ============================================================================
// Header Methods
// ============================================================================

func (h *ConnectionHandler) handleHeadersSubscribe(params json.RawMessage) (interface{}, *Error) {
    h.server.subs.SubscribeHeaders(h.writer)

    checkpoint, err := h.server.db.LoadCheckpoint()
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }

    if checkpoint.Height == 0 {
        return nil, &Error{Code: ErrCodeInternal, Message: "no blocks indexed"}
    }

    headerHex, err := h.server.db.GetHeaderHex(checkpoint.Height)
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }

    return map[string]interface{}{
        "height": checkpoint.Height,
        "hex":    headerHex,
    }, nil
}

func (h *ConnectionHandler) handleBlockHeader(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [height]"}
    }

    height, ok := args[0].(float64)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "height must be a number"}
    }

    headerHex, err := h.server.db.GetHeaderHex(int32(height))
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }

    if len(args) > 1 {
        return map[string]interface{}{
            "header": headerHex,
        }, nil
    }

    return headerHex, nil
}

func (h *ConnectionHandler) handleBlockHeaders(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 2 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [start_height, count]"}
    }

    startHeight, ok := args[0].(float64)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "start_height must be a number"}
    }

    count, ok := args[1].(float64)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "count must be a number"}
    }

    if count > 2016 {
        count = 2016
    }

    var headers bytes.Buffer
    actualCount := 0

    for i := int32(startHeight); i < int32(startHeight+count); i++ {
        header, err := h.server.db.GetHeader(i)
        if err != nil {
            break
        }
        headers.Write(header)
        actualCount++
    }

    return map[string]interface{}{
        "count": actualCount,
        "hex":   hex.EncodeToString(headers.Bytes()),
        "max":   2016,
    }, nil
}

// ============================================================================
// Fee Methods
// ============================================================================

func (h *ConnectionHandler) handleEstimateFee(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
        args = []interface{}{float64(6)}
    }

    numBlocks := int64(6)
    if n, ok := args[0].(float64); ok {
        numBlocks = int64(n)
    }

    result, err := h.server.client.EstimateSmartFee(numBlocks, nil)
    if err != nil {
        return float64(-1), nil
    }

    if result.FeeRate == nil {
        return float64(-1), nil
    }

    return *result.FeeRate, nil
}

func (h *ConnectionHandler) handleRelayFee(params json.RawMessage) (interface{}, *Error) {
    return 0.00001, nil
}

// ============================================================================
// Scripthash Methods
// ============================================================================

func (h *ConnectionHandler) handleScripthashGetHistory(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    history, queryErr := GetScripthashHistory(h.server.db, h.server.mempool,
        scripthash)
    if queryErr != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: queryErr.Error()}
    }

    return history, nil
}

func (h *ConnectionHandler) handleScripthashGetBalance(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    balance, queryErr := GetScripthashBalance(h.server.db, h.server.mempool,
        scripthash)
    if queryErr != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: queryErr.Error()}
    }

    return balance, nil
}

func (h *ConnectionHandler) handleScripthashListUnspent(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    utxos, queryErr := GetScripthashUnspent(h.server.db, h.server.mempool,
        scripthash)
    if queryErr != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: queryErr.Error()}
    }

    return utxos, nil
}

func (h *ConnectionHandler) handleScripthashSubscribe(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    scripthashHex := hex.EncodeToString(scripthash)

    h.server.subs.SubscribeScripthash(h.writer, scripthashHex)

    status, queryErr := h.server.ComputeScripthashStatus(scripthash)
    if queryErr != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: queryErr.Error()}
    }

    if status == "" {
        return nil, nil
    }

    return status, nil
}

func (h *ConnectionHandler) handleScripthashUnsubscribe(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    scripthashHex := hex.EncodeToString(scripthash)
    h.server.subs.UnsubscribeScripthash(h.writer, scripthashHex)

    return true, nil
}

func (h *ConnectionHandler) handleScripthashGetMempool(params json.RawMessage) (interface{}, *Error) {
    scripthash, err := h.parseScripthashParam(params)
    if err != nil {
        return nil, err
    }

    txids := h.server.mempool.GetScripthashTransactions(scripthash)

    result := make([]map[string]interface{}, 0, len(txids))
    for _, txid := range txids {
        result = append(result, map[string]interface{}{
            "tx_hash": txid,
            "height":  0,
            "fee":     0,
        })
    }

    return result, nil
}

// ============================================================================
// Transaction Methods
// ============================================================================

func (h *ConnectionHandler) handleTransactionGet(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [txid]"}
    }

    txidStr, ok := args[0].(string)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "txid must be a string"}
    }

    verbose := false
    if len(args) > 1 {
        if v, ok := args[1].(bool); ok {
            verbose = v
        }
    }

    if verbose {
        return nil, &Error{Code: ErrCodeMethodNotFound, Message: "verbose transaction.get not implemented"}
    }

    txidRaw, err := indexer.TxidFromHex(txidStr)
    if err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid txid"}
    }

    // Try local compact storage first
    height, txIndex, ok, err := h.server.db.GetTxPos(txidRaw)
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }
    if ok {
        blob, err := h.server.db.GetTxBlob(height)
        if err != nil {
            return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
        }
        offsets, err := h.server.db.GetTxOffsets(height)
        if err != nil {
            return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
        }

        if int(txIndex) >= len(offsets) {
            return nil, &Error{Code: ErrCodeInternal, Message: "tx index out of range"}
        }

        start := offsets[txIndex]
        var end uint32
        if int(txIndex)+1 < len(offsets) {
            end = offsets[txIndex+1]
        } else {
            end = uint32(len(blob))
        }

        if int(end) > len(blob) || end < start {
            return nil, &Error{Code: ErrCodeInternal, Message: "invalid tx offsets"}
        }

        txBytes := blob[start:end]
        return hex.EncodeToString(txBytes), nil
    }

    // Fall back to Core (mempool or very recent)
    txHash, err := chainhash.NewHashFromStr(txidStr)
    if err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid txid"}
    }

    rawTx, err := h.server.client.GetRawTransaction(txHash)
    if err != nil {
        return nil, &Error{
            Code:    ErrCodeInternal,
            Message: fmt.Sprintf("transaction not found: %v", err),
        }
    }

    var buf bytes.Buffer
    if err := rawTx.MsgTx().Serialize(&buf); err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }

    return hex.EncodeToString(buf.Bytes()), nil
}

func (h *ConnectionHandler) handleTransactionBroadcast(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [raw_tx]"}
    }

    rawTxHex, ok := args[0].(string)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "raw_tx must be a hex string"}
    }

    rawTxBytes, err := hex.DecodeString(rawTxHex)
    if err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid hex"}
    }

    var msgTx wire.MsgTx
    if err := msgTx.Deserialize(bytes.NewReader(rawTxBytes)); err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: fmt.Sprintf("invalid transaction: %v", err)}
    }

    txHash, err := h.server.client.SendRawTransaction(&msgTx, false)
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: fmt.Sprintf("broadcast failed: %v", err)}
    }

    log.Printf("📤 [%d] Broadcast tx: %s", h.connID, txHash.String())

    return txHash.String(), nil
}

func (h *ConnectionHandler) handleTransactionGetMerkle(params json.RawMessage) (interface{}, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 2 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [txid, height]"}
    }

    txidStr, ok := args[0].(string)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "txid must be a string"}
    }

    heightFloat, ok := args[1].(float64)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "height must be a number"}
    }

    txid, err := indexer.TxidFromHex(txidStr)
    if err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid txid"}
    }

    proof, err := GetTransactionMerkleProof(h.server.db, txid, int32(heightFloat))
    if err != nil {
        return nil, &Error{Code: ErrCodeInternal, Message: err.Error()}
    }

    return proof, nil
}

func (h *ConnectionHandler) handleTransactionIdFromPos(params json.RawMessage) (interface{}, *Error) {
    return nil, &Error{Code: ErrCodeMethodNotFound, Message: "id_from_pos not yet implemented"}
}

// ============================================================================
// Mempool Methods
// ============================================================================

func (h *ConnectionHandler) handleMempoolFeeHistogram(params json.RawMessage) (interface{}, *Error) {
    return [][]interface{}{}, nil
}

// ============================================================================
// Helper Methods
// ============================================================================

func (h *ConnectionHandler) parseScripthashParam(params json.RawMessage) ([]byte, *Error) {
    var args []interface{}
    if err := json.Unmarshal(params, &args); err != nil || len(args) < 1 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "expected [scripthash]"}
    }

    scripthashHex, ok := args[0].(string)
    if !ok {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "scripthash must be a hex string"}
    }

    scripthash, err := hex.DecodeString(scripthashHex)
    if err != nil {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "invalid scripthash hex"}
    }

    if len(scripthash) != 32 {
        return nil, &Error{Code: ErrCodeInvalidParams, Message: "scripthash must be 32 bytes"}
    }

    return scripthash, nil
}