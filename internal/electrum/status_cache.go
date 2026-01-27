package electrum

import "sync"

type StatusCache struct {
    mu     sync.RWMutex
    status map[string]string
    dirty  map[string]bool
}

func NewStatusCache() *StatusCache {
    return &StatusCache{
        status: make(map[string]string),
        dirty:  make(map[string]bool),
    }
}

// GetIfClean returns cached status only if it is not marked dirty.
func (c *StatusCache) GetIfClean(scripthashHex string) (string, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()

    if c.dirty[scripthashHex] {
        return "", false
    }
    status, ok := c.status[scripthashHex]
    return status, ok
}

// SetClean stores status and clears the dirty flag.
func (c *StatusCache) SetClean(scripthashHex string, status string) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.status[scripthashHex] = status
    delete(c.dirty, scripthashHex)
}

// MarkDirty marks a scripthash as dirty.
func (c *StatusCache) MarkDirty(scripthashHex string) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.dirty[scripthashHex] = true
}

// MarkDirtyMany marks many scripthashes as dirty.
func (c *StatusCache) MarkDirtyMany(scripthashHexes []string) {
    c.mu.Lock()
    defer c.mu.Unlock()

    for _, sh := range scripthashHexes {
        c.dirty[sh] = true
    }
}