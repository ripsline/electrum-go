package electrum

import (
    "bufio"
    "context"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log"
    "net"
    "sync"
    "sync/atomic"
    "time"

    "github.com/btcsuite/btcd/rpcclient"

    "electrum-go/internal/config"
    "electrum-go/internal/indexer"
    "electrum-go/internal/storage"
)

type Server struct {
    config *config.Config

    db      *storage.DB
    client  *rpcclient.Client
    mempool *indexer.MempoolOverlay

    listener net.Listener

    subs *SubscriptionManager

    statusCache *StatusCache

    connCount       int64
    activeConnCount int64
    connLimiter     chan struct{}

    ctx    context.Context
    cancel context.CancelFunc
    wg     sync.WaitGroup
}

type Request struct {
    JsonRPC string          `json:"jsonrpc"`
    ID      interface{}     `json:"id"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params"`
}

type Response struct {
    JsonRPC string      `json:"jsonrpc"`
    ID      interface{} `json:"id"`
    Result  interface{} `json:"-"`
    Error   *Error      `json:"error,omitempty"`
}

func (r *Response) MarshalJSON() ([]byte, error) {
    type Alias Response

    if r.Error != nil {
        return json.Marshal(&struct {
            *Alias
        }{
            Alias: (*Alias)(r),
        })
    }

    return json.Marshal(&struct {
        *Alias
        Result interface{} `json:"result"`
    }{
        Alias:  (*Alias)(r),
        Result: r.Result,
    })
}

type Error struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

const (
    ErrCodeParse          = -32700
    ErrCodeInvalidRequest = -32600
    ErrCodeMethodNotFound = -32601
    ErrCodeInvalidParams  = -32602
    ErrCodeInternal       = -32603
)

// ConnWriter serializes all writes to a connection to avoid interleaving.
type ConnWriter struct {
    conn   net.Conn
    ch     chan []byte
    done   chan struct{}
    wg     sync.WaitGroup
    closed int32
}

func NewConnWriter(conn net.Conn, bufSize int) *ConnWriter {
    w := &ConnWriter{
        conn: conn,
        ch:   make(chan []byte, bufSize),
        done: make(chan struct{}),
    }
    w.wg.Add(1)
    go w.writeLoop()
    return w
}

func (w *ConnWriter) writeLoop() {
    defer w.wg.Done()
    for {
        select {
        case data, ok := <-w.ch:
            if !ok {
                return
            }
            _ = w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
            _, _ = w.conn.Write(data)
        case <-w.done:
            return
        }
    }
}

func (w *ConnWriter) Send(data []byte) error {
    if atomic.LoadInt32(&w.closed) == 1 {
        return fmt.Errorf("writer closed")
    }
    w.ch <- data
    return nil
}

func (w *ConnWriter) TrySend(data []byte) bool {
    if atomic.LoadInt32(&w.closed) == 1 {
        return false
    }
    select {
    case w.ch <- data:
        return true
    default:
        return false
    }
}

func (w *ConnWriter) Close() {
    if atomic.CompareAndSwapInt32(&w.closed, 0, 1) {
        close(w.done)
        close(w.ch)
        w.wg.Wait()
    }
}

func NewServer(
    cfg *config.Config,
    db *storage.DB,
    client *rpcclient.Client,
    mempool *indexer.MempoolOverlay,
) *Server {
    ctx, cancel := context.WithCancel(context.Background())

    return &Server{
        config:      cfg,
        db:          db,
        client:      client,
        mempool:     mempool,
        subs:        NewSubscriptionManager(),
        statusCache: NewStatusCache(),
        connLimiter: make(chan struct{}, cfg.Server.MaxConnections),
        ctx:         ctx,
        cancel:      cancel,
    }
}

func (s *Server) Start() error {
    var err error
    s.listener, err = net.Listen("tcp", s.config.Server.Listen)
    if err != nil {
        return fmt.Errorf("failed to listen on %s: %w", s.config.Server.Listen, err)
    }

    log.Printf("✅ Electrum server listening on %s", s.config.Server.Listen)
    log.Printf("   Max connections: %d", s.config.Server.MaxConnections)
    log.Printf("   Request timeout: %s", s.config.Server.RequestTimeout)

    for {
        conn, err := s.listener.Accept()
        if err != nil {
            select {
            case <-s.ctx.Done():
                return nil
            default:
                log.Printf("⚠️  Accept error: %v", err)
                continue
            }
        }

        select {
        case s.connLimiter <- struct{}{}:
            s.wg.Add(1)
            go s.handleConnection(conn)

        default:
            log.Printf("⚠️  Connection limit reached, rejecting %s", conn.RemoteAddr())
            conn.Close()
        }
    }
}

func (s *Server) Stop() error {
    log.Println("🛑 Stopping Electrum server...")

    s.cancel()

    if s.listener != nil {
        s.listener.Close()
    }

    done := make(chan struct{})
    go func() {
        s.wg.Wait()
        close(done)
    }()

    select {
    case <-done:
        log.Println("✅ All connections closed")
    case <-time.After(10 * time.Second):
        log.Println("⚠️  Timeout waiting for connections to close")
    }

    log.Printf("📊 Server stopped (handled %d total connections)", atomic.LoadInt64(&s.connCount))

    return nil
}

func (s *Server) handleConnection(conn net.Conn) {
    writer := NewConnWriter(conn, 1000)

    defer func() {
        <-s.connLimiter
        s.subs.Unsubscribe(writer)
        writer.Close()
        conn.Close()
        atomic.AddInt64(&s.activeConnCount, -1)
        s.wg.Done()
    }()

    connID := atomic.AddInt64(&s.connCount, 1)
    atomic.AddInt64(&s.activeConnCount, 1)
    remoteAddr := conn.RemoteAddr().String()

    log.Printf("📱 [%d] New connection from %s", connID, remoteAddr)

    handler := &ConnectionHandler{
        server:  s,
        conn:    conn,
        writer:  writer,
        connID:  connID,
        addr:    remoteAddr,
        logReqs: s.config.Logging.LogRequests,
    }

    handler.serve()

    log.Printf("📱 [%d] Connection closed: %s", connID, remoteAddr)
}

type ConnectionHandler struct {
    server  *Server
    conn    net.Conn
    writer  *ConnWriter
    connID  int64
    addr    string
    logReqs bool
}

func (h *ConnectionHandler) serve() {
    scanner := bufio.NewScanner(h.conn)

    const maxRequestSize = 10 * 1024 * 1024
    scanner.Buffer(make([]byte, 64*1024), maxRequestSize)

    for {
        select {
        case <-h.server.ctx.Done():
            return
        default:
        }

        _ = h.conn.SetReadDeadline(time.Now().Add(h.server.config.Server.RequestTimeout))

        if !scanner.Scan() {
            if err := scanner.Err(); err != nil {
                if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
                    if h.logReqs {
                        log.Printf("📱 [%d] Read error: %v", h.connID, err)
                    }
                }
            }
            return
        }

        line := scanner.Bytes()
        if len(line) == 0 {
            continue
        }

        responses := h.processRequest(line)
        if len(responses) == 0 {
            continue
        }

        h.sendResponses(responses)
    }
}

func (h *ConnectionHandler) processRequest(data []byte) []*Response {
    trimmed := bytesTrimSpace(data)
    if len(trimmed) == 0 {
        return nil
    }

    // Batch request
    if trimmed[0] == '[' {
        var reqs []Request
        if err := json.Unmarshal(trimmed, &reqs); err != nil {
            return []*Response{{
                JsonRPC: "2.0",
                ID:      nil,
                Error: &Error{
                    Code:    ErrCodeParse,
                    Message: fmt.Sprintf("Parse error: %v", err),
                },
            }}
        }

        if h.logReqs {
            log.Printf("📦 [%d] Batch request: %d calls", h.connID, len(reqs))
        }

        responses := make([]*Response, 0, len(reqs))
        for _, req := range reqs {
            if h.logReqs {
                log.Printf("📨 [%d] %s (id=%v)", h.connID, req.Method, req.ID)
            }
            result, err := h.handleMethod(req.Method, req.Params)
            if err != nil {
                responses = append(responses, &Response{
                    JsonRPC: "2.0",
                    ID:      req.ID,
                    Error:   err,
                })
            } else {
                responses = append(responses, &Response{
                    JsonRPC: "2.0",
                    ID:      req.ID,
                    Result:  result,
                })
            }
        }
        return responses
    }

    // Single request
    var req Request
    if err := json.Unmarshal(trimmed, &req); err != nil {
        return []*Response{{
            JsonRPC: "2.0",
            ID:      nil,
            Error: &Error{
                Code:    ErrCodeParse,
                Message: fmt.Sprintf("Parse error: %v", err),
            },
        }}
    }

    if h.logReqs {
        log.Printf("📨 [%d] %s (id=%v)", h.connID, req.Method, req.ID)
    }

    result, err := h.handleMethod(req.Method, req.Params)
    if err != nil {
        return []*Response{{
            JsonRPC: "2.0",
            ID:      req.ID,
            Error:   err,
        }}
    }

    return []*Response{{
        JsonRPC: "2.0",
        ID:      req.ID,
        Result:  result,
    }}
}

func (h *ConnectionHandler) sendResponses(resps []*Response) {
    var data []byte
    var err error

    if len(resps) == 1 {
        data, err = json.Marshal(resps[0])
    } else {
        data, err = json.Marshal(resps)
    }
    if err != nil {
        log.Printf("⚠️  [%d] Failed to marshal response: %v", h.connID, err)
        return
    }

    data = append(data, '\n')
    _ = h.writer.Send(data)
}

func bytesTrimSpace(b []byte) []byte {
    start := 0
    for start < len(b) && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
        start++
    }
    end := len(b)
    for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
        end--
    }
    return b[start:end]
}

func (s *Server) NotifyScripthash(scripthash string, status string) {
    s.subs.NotifyScripthash(scripthash, status)
}

func (s *Server) NotifyScripthashStatusHex(scripthashHex string) {
    status, err := s.ComputeScripthashStatusHex(scripthashHex)
    if err != nil {
        log.Printf("⚠️  Failed to compute status for %s: %v", scripthashHex, err)
        return
    }
    s.NotifyScripthash(scripthashHex, status)
}

func (s *Server) ComputeScripthashStatusHex(scripthashHex string) (string, error) {
    if status, ok := s.statusCache.GetIfClean(scripthashHex); ok {
        return status, nil
    }

    sh, err := hex.DecodeString(scripthashHex)
    if err != nil {
        return "", err
    }

    status, err := ComputeScripthashStatus(s.db, s.mempool, sh)
    if err != nil {
        return "", err
    }

    s.statusCache.SetClean(scripthashHex, status)
    return status, nil
}

func (s *Server) ComputeScripthashStatus(scripthash []byte) (string, error) {
    return s.ComputeScripthashStatusHex(hex.EncodeToString(scripthash))
}

func (s *Server) MarkScripthashDirty(scripthashHex string) {
    s.statusCache.MarkDirty(scripthashHex)
}

func (s *Server) MarkScripthashDirtyMany(scripthashHexes []string) {
    s.statusCache.MarkDirtyMany(scripthashHexes)
}

func (s *Server) NotifyNewBlock(height int32, headerHex string) {
    s.subs.NotifyNewBlock(height, headerHex)
}

func (s *Server) GetActiveConnectionCount() int64 {
    return atomic.LoadInt64(&s.activeConnCount)
}

func (s *Server) GetTotalConnectionCount() int64 {
    return atomic.LoadInt64(&s.connCount)
}

func (s *Server) GetSubscriptionManager() *SubscriptionManager {
    return s.subs
}