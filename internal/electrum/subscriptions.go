package electrum

import (
    "encoding/json"
    "log"
    "sync"
)

type SubscriptionManager struct {
    mu sync.RWMutex

    scripthashSubs map[string]map[*ConnWriter]bool
    connScripthash map[*ConnWriter]map[string]bool

    headerSubs map[*ConnWriter]bool
}

func NewSubscriptionManager() *SubscriptionManager {
    return &SubscriptionManager{
        scripthashSubs: make(map[string]map[*ConnWriter]bool),
        connScripthash: make(map[*ConnWriter]map[string]bool),
        headerSubs:     make(map[*ConnWriter]bool),
    }
}

func (sm *SubscriptionManager) SubscribeScripthash(w *ConnWriter, scripthash string) {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if sm.scripthashSubs[scripthash] == nil {
        sm.scripthashSubs[scripthash] = make(map[*ConnWriter]bool)
    }
    sm.scripthashSubs[scripthash][w] = true

    if sm.connScripthash[w] == nil {
        sm.connScripthash[w] = make(map[string]bool)
    }
    sm.connScripthash[w][scripthash] = true
}

func (sm *SubscriptionManager) UnsubscribeScripthash(w *ConnWriter, scripthash string) {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if conns, ok := sm.scripthashSubs[scripthash]; ok {
        delete(conns, w)
        if len(conns) == 0 {
            delete(sm.scripthashSubs, scripthash)
        }
    }

    if shs, ok := sm.connScripthash[w]; ok {
        delete(shs, scripthash)
        if len(shs) == 0 {
            delete(sm.connScripthash, w)
        }
    }
}

func (sm *SubscriptionManager) SubscribeHeaders(w *ConnWriter) {
    sm.mu.Lock()
    defer sm.mu.Unlock()
    sm.headerSubs[w] = true
}

func (sm *SubscriptionManager) UnsubscribeHeaders(w *ConnWriter) {
    sm.mu.Lock()
    defer sm.mu.Unlock()
    delete(sm.headerSubs, w)
}

func (sm *SubscriptionManager) Unsubscribe(w *ConnWriter) {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if scripthashes, ok := sm.connScripthash[w]; ok {
        for scripthash := range scripthashes {
            if conns, ok := sm.scripthashSubs[scripthash]; ok {
                delete(conns, w)
                if len(conns) == 0 {
                    delete(sm.scripthashSubs, scripthash)
                }
            }
        }
        delete(sm.connScripthash, w)
    }

    delete(sm.headerSubs, w)
}

func (sm *SubscriptionManager) NotifyScripthash(scripthash string, status string) {
    sm.mu.RLock()
    conns := make([]*ConnWriter, 0)
    if subscribers, ok := sm.scripthashSubs[scripthash]; ok {
        for conn := range subscribers {
            conns = append(conns, conn)
        }
    }
    sm.mu.RUnlock()

    if len(conns) == 0 {
        return
    }

    var statusValue interface{} = status
    if status == "" {
        statusValue = nil
    }

    notification := map[string]interface{}{
        "jsonrpc": "2.0",
        "method":  "blockchain.scripthash.subscribe",
        "params":  []interface{}{scripthash, statusValue},
    }

    data, err := json.Marshal(notification)
    if err != nil {
        log.Printf("⚠️  Failed to marshal scripthash notification: %v", err)
        return
    }
    data = append(data, '\n')

    for _, conn := range conns {
        _ = conn.TrySend(data)
    }
}

func (sm *SubscriptionManager) NotifyNewBlock(height int32, headerHex string) {
    sm.mu.RLock()
    conns := make([]*ConnWriter, 0, len(sm.headerSubs))
    for conn := range sm.headerSubs {
        conns = append(conns, conn)
    }
    sm.mu.RUnlock()

    if len(conns) == 0 {
        return
    }

    notification := map[string]interface{}{
        "jsonrpc": "2.0",
        "method":  "blockchain.headers.subscribe",
        "params": []interface{}{
            map[string]interface{}{
                "height": height,
                "hex":    headerHex,
            },
        },
    }

    data, err := json.Marshal(notification)
    if err != nil {
        log.Printf("⚠️  Failed to marshal header notification: %v", err)
        return
    }
    data = append(data, '\n')

    for _, conn := range conns {
        _ = conn.TrySend(data)
    }
}

func (sm *SubscriptionManager) GetScripthashSubscriberCount(scripthash string) int {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    if conns, ok := sm.scripthashSubs[scripthash]; ok {
        return len(conns)
    }
    return 0
}

func (sm *SubscriptionManager) GetHeaderSubscriberCount() int {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    return len(sm.headerSubs)
}

func (sm *SubscriptionManager) GetTotalSubscriptions() (scripthashes int, headers int, connections int) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    return len(sm.scripthashSubs), len(sm.headerSubs), len(sm.connScripthash)
}

func (sm *SubscriptionManager) GetSubscribedScripthashes() []string {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    result := make([]string, 0, len(sm.scripthashSubs))
    for sh := range sm.scripthashSubs {
        result = append(result, sh)
    }
    return result
}

func (sm *SubscriptionManager) IsScripthashSubscribed(scripthash string) bool {
    sm.mu.RLock()
    defer sm.mu.RUnlock()

    conns, ok := sm.scripthashSubs[scripthash]
    return ok && len(conns) > 0
}