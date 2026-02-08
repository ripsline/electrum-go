// Package main is the entry point for the electrum-go server.
package main

import (
    "bytes"
    "context"
    "encoding/hex"
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/btcsuite/btcd/chaincfg/chainhash"
    "github.com/btcsuite/btcd/rpcclient"

    "github.com/ripsline/electrum-go/internal/config"
    "github.com/ripsline/electrum-go/internal/electrum"
    "github.com/ripsline/electrum-go/internal/indexer"
    "github.com/ripsline/electrum-go/internal/storage"
)

var (
    Version   = "0.1.0"
    GitCommit = "unknown"
    BuildTime = "unknown"
)

type BlockchainInfo struct {
    Chain                string   `json:"chain"`
    Blocks               int64    `json:"blocks"`
    Headers              int64    `json:"headers"`
    BestBlockHash        string   `json:"bestblockhash"`
    Difficulty           float64  `json:"difficulty"`
    Time                 int64    `json:"time"`
    MedianTime           int64    `json:"mediantime"`
    VerificationProgress float64  `json:"verificationprogress"`
    InitialBlockDownload bool     `json:"initialblockdownload"`
    ChainWork            string   `json:"chainwork"`
    SizeOnDisk           int64    `json:"size_on_disk"`
    Pruned               bool     `json:"pruned"`
    PruneHeight          int64    `json:"pruneheight,omitempty"`
    AutomaticPruning     bool     `json:"automatic_pruning,omitempty"`
    PruneTargetSize      int64    `json:"prune_target_size,omitempty"`
    Warnings             []string `json:"warnings"`
}

func getBlockchainInfo(client *rpcclient.Client) (*BlockchainInfo, error) {
    result, err := client.RawRequest("getblockchaininfo", nil)
    if err != nil {
        return nil, fmt.Errorf("getblockchaininfo RPC failed: %w", err)
    }

    var info BlockchainInfo
    if err := json.Unmarshal(result, &info); err != nil {
        var legacyInfo struct {
            BlockchainInfo
            Warnings string `json:"warnings"`
        }
        if err2 := json.Unmarshal(result, &legacyInfo); err2 != nil {
            return nil, fmt.Errorf("failed to parse getblockchaininfo: %w", err)
        }
        info = legacyInfo.BlockchainInfo
        if legacyInfo.Warnings != "" {
            info.Warnings = []string{legacyInfo.Warnings}
        }
    }

    return &info, nil
}

func main() {
    configFile := flag.String("config", "", "Path to config file (TOML)")
    listenAddr := flag.String("listen", "", "Electrum server listen address")
    rpcHost := flag.String("rpc-host", "", "Bitcoin Core RPC host:port")
    rpcUser := flag.String("rpc-user", "", "Bitcoin Core RPC username")
    rpcPass := flag.String("rpc-pass", "", "Bitcoin Core RPC password")
    dbPath := flag.String("db-path", "", "Path to database")
    startHeight := flag.Int("start-height", -9999, "Start height (-1=tip, 0=genesis)")
    showVersion := flag.Bool("version", false, "Show version and exit")

    flag.Parse()

    if *showVersion {
        fmt.Printf("electrum-go %s\n", Version)
        fmt.Printf("  Git commit: %s\n", GitCommit)
        fmt.Printf("  Build time: %s\n", BuildTime)
        os.Exit(0)
    }

    printBanner()

    cfg, err := loadConfig(*configFile)
    if err != nil {
        log.Fatalf("❌ Failed to load configuration: %v", err)
    }

    applyOverrides(cfg, *listenAddr, *rpcHost, *rpcUser, *rpcPass, *dbPath,
        *startHeight)

    log.Println(cfg.String())
    log.Println()

    if err := cfg.EnsureDBDirectory(); err != nil {
        log.Fatalf("❌ Failed to create database directory: %v", err)
    }

    log.Println("📂 Opening database...")
    db, err := storage.Open(cfg.Storage.DBPath)
    if err != nil {
        log.Fatalf("❌ Failed to open database: %v", err)
    }
    defer func() {
        log.Println("📂 Closing database...")
        if err := db.Close(); err != nil {
            log.Printf("⚠️  Error closing database: %v", err)
        }
    }()

    log.Println("🔗 Connecting to Bitcoin Core...")
    client, err := connectToBitcoinCore(cfg)
    if err != nil {
        log.Fatalf("❌ Failed to connect to Bitcoin Core: %v", err)
    }
    defer client.Shutdown()

    info, err := getBlockchainInfo(client)
    if err != nil {
        log.Fatalf("❌ Failed to get blockchain info: %v", err)
    }

    log.Printf("✅ Connected to Bitcoin Core")
    log.Printf("   Chain:       %s", info.Chain)
    log.Printf("   Blocks:      %d", info.Blocks)
    log.Printf("   Headers:     %d", info.Headers)
    log.Printf("   Pruned:      %v", info.Pruned)
    if info.Pruned {
        log.Printf("   Prune height: %d", info.PruneHeight)
    }
    log.Printf("   Verification: %.2f%%", info.VerificationProgress*100)
    if len(info.Warnings) > 0 {
        for _, w := range info.Warnings {
            log.Printf("   ⚠️  Warning: %s", w)
        }
    }
    log.Println()

    if info.VerificationProgress < 0.9999 {
        log.Println("⚠️  WARNING: Bitcoin Core is still syncing!")
        log.Println("   Indexing will proceed but may encounter issues.")
        log.Println()
    }

    mempool := indexer.NewMempoolOverlay(db)

    if err := mempool.LoadFromDatabase(); err != nil {
        log.Printf("⚠️  Failed to load mempool from database: %v", err)
    }

    chainManager := indexer.NewChainManager(db, client, mempool,
        cfg.Storage.MaxReorgDepth)

    if err := chainManager.Initialize(); err != nil {
        log.Fatalf("❌ Failed to initialize chain manager: %v", err)
    }

    // Forward-indexing empty state check
    checkpoint, _ := db.LoadCheckpoint()
    if checkpoint.Height == 0 && checkpoint.BlockHash == "" {
        startFrom := determineStartHeight(cfg, info)
        if startFrom > 0 {
            if err := chainManager.SetStartHeight(startFrom); err != nil {
                log.Fatalf("❌ Failed to set start height: %v", err)
            }
        }
    }

    writer := indexer.NewWriter(db, chainManager, mempool)
    defer writer.Close()

    // Full mempool sync on startup, including backfill of missed txs
    if _, err := reconcileMempool(client, mempool, writer); err != nil {
        log.Printf("⚠️  Mempool reconciliation failed: %v", err)
    }

    zmqConfig := indexer.ZMQConfig{
        BlockAddr:     cfg.Bitcoin.ZMQBlockAddr,
        TxAddr:        cfg.Bitcoin.ZMQTxAddr,
        BlockChanSize: 10,
        TxChanSize:    1000,
    }

    zmqSub, err := indexer.NewZMQSubscriber(zmqConfig)
    if err != nil {
        log.Fatalf("❌ Failed to initialize ZMQ: %v\n\n"+
            "Make sure Bitcoin Core has ZMQ enabled in bitcoin.conf:\n"+
            "  zmqpubrawblock=%s\n"+
            "  zmqpubrawtx=%s\n"+
            "Then restart Bitcoin Core.", err, cfg.Bitcoin.ZMQBlockAddr,
            cfg.Bitcoin.ZMQTxAddr)
    }
    defer zmqSub.Stop()

    electrumServer := electrum.NewServer(cfg, db, client, mempool)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        sig := <-sigChan
        log.Printf("\n🛑 Received signal %v, shutting down...", sig)
        cancel()
    }()

    go func() {
        if err := electrumServer.Start(); err != nil {
            log.Printf("❌ Electrum server error: %v", err)
            cancel()
        }
    }()

    log.Println("⏳ Catching up with blockchain...")
    if err := chainManager.CatchUpToTip(); err != nil {
        log.Fatalf("❌ Catch-up failed: %v", err)
    }

    if err := zmqSub.Start(); err != nil {
        log.Fatalf("❌ Failed to start ZMQ: %v", err)
    }

    log.Println("🔄 Starting live processing...")
    runMainLoop(ctx, cfg, writer, zmqSub, electrumServer, chainManager, client,
        mempool)

    log.Println("🛑 Shutting down...")

    if err := electrumServer.Stop(); err != nil {
        log.Printf("⚠️  Error stopping Electrum server: %v", err)
    }

    log.Println("✅ Shutdown complete")
}

func printBanner() {
    log.Println("╔══════════════════════════════════════════════════════════════╗")
    log.Println("║                     electrum-go Server                       ║")
    log.Println("║         Forward-Indexing • Pruned Node Compatible            ║")
    log.Println("╚══════════════════════════════════════════════════════════════╝")
    log.Println()
}

func loadConfig(configFile string) (*config.Config, error) {
    if configFile != "" {
        return config.LoadFromFile(configFile)
    }

    defaultPaths := []string{
        "config.toml",
        "./config/config.toml",
        "/etc/electrum-go/config.toml",
    }

    for _, path := range defaultPaths {
        if _, err := os.Stat(path); err == nil {
            log.Printf("📄 Loading config from %s", path)
            return config.LoadFromFile(path)
        }
    }

    log.Println("📄 No config file found, using defaults")
    return config.DefaultConfig(), nil
}

func applyOverrides(cfg *config.Config, listen, rpcHost, rpcUser, rpcPass,
    dbPath string, startHeight int) {
    if listen != "" {
        cfg.Server.Listen = listen
    }
    if rpcHost != "" {
        cfg.Bitcoin.RPCHost = rpcHost
    }
    if rpcUser != "" {
        cfg.Bitcoin.RPCUser = rpcUser
    }
    if rpcPass != "" {
        cfg.Bitcoin.RPCPass = rpcPass
    }
    if dbPath != "" {
        cfg.Storage.DBPath = dbPath
    }
    if startHeight != -9999 {
        cfg.Indexer.StartHeight = startHeight
    }
}

func connectToBitcoinCore(cfg *config.Config) (*rpcclient.Client, error) {
    connCfg := &rpcclient.ConnConfig{
        Host:         cfg.Bitcoin.RPCHost,
        User:         cfg.Bitcoin.RPCUser,
        Pass:         cfg.Bitcoin.RPCPass,
        HTTPPostMode: true,
        DisableTLS:   true,
    }

    client, err := rpcclient.New(connCfg, nil)
    if err != nil {
        return nil, fmt.Errorf("failed to create RPC client: %w", err)
    }

    _, err = client.GetBlockCount()
    if err != nil {
        client.Shutdown()
        return nil, fmt.Errorf("failed to connect: %w", err)
    }

    return client, nil
}

func determineStartHeight(cfg *config.Config, info *BlockchainInfo) int32 {
    if cfg.Indexer.StartHeight == -1 {
        log.Printf("📍 Forward-indexing mode: starting from current tip %d",
            info.Blocks)
        return int32(info.Blocks)
    }

    if cfg.Indexer.StartHeight == 0 {
        log.Println("📍 Full indexing mode: starting from genesis")
        return 0
    }

    if cfg.Indexer.StartHeight > 0 {
        log.Printf("📍 Starting from specified height %d",
            cfg.Indexer.StartHeight)
        return int32(cfg.Indexer.StartHeight)
    }

    log.Printf("📍 Defaulting to forward-indexing from tip %d", info.Blocks)
    return int32(info.Blocks)
}

// reconcileMempool removes stale entries and backfills any missing txs.
func reconcileMempool(client *rpcclient.Client,
    mempool *indexer.MempoolOverlay, writer *indexer.Writer) ([]string, error) {

    txids, err := client.GetRawMempool()
    if err != nil {
        return nil, err
    }

    valid := make(map[string]bool, len(txids))
    for _, h := range txids {
        valid[h.String()] = true
    }

    removed, affected := mempool.ReconcileWith(valid)
    if removed > 0 {
        log.Printf("🔄 Mempool reconciled: removed %d stale entry(s)", removed)
    }

    affectedSet := make(map[string]struct{}, len(affected))
    for _, sh := range affected {
        affectedSet[sh] = struct{}{}
    }

    // Backfill missing mempool txs
    missing := 0
    for _, h := range txids {
        txidHex := h.String()
        if mempool.HasTx(txidHex) {
            continue
        }

        hash, err := chainhash.NewHashFromStr(txidHex)
        if err != nil {
            continue
        }

        rawTx, err := client.GetRawTransaction(hash)
        if err != nil {
            continue
        }

        aff, err := writer.IndexMempoolTx(rawTx.MsgTx())
        if err != nil {
            continue
        }

        for _, sh := range aff {
            affectedSet[sh] = struct{}{}
        }
        missing++
    }

    if missing > 0 {
        log.Printf("🔄 Mempool backfill: added %d missing tx(s)", missing)
    }

    result := make([]string, 0, len(affectedSet))
    for sh := range affectedSet {
        result = append(result, sh)
    }

    return result, nil
}

func runMainLoop(
    ctx context.Context,
    cfg *config.Config,
    writer *indexer.Writer,
    zmqSub *indexer.ZMQSubscriber,
    server *electrum.Server,
    chainManager *indexer.ChainManager,
    rpcClient *rpcclient.Client,
    mempool *indexer.MempoolOverlay,
) {
    pruneTicker := time.NewTicker(time.Duration(cfg.Indexer.UndoPruneInterval) *
        time.Minute)
    defer pruneTicker.Stop()

    statsTicker := time.NewTicker(1 * time.Minute)
    defer statsTicker.Stop()

    reorgCheckTicker := time.NewTicker(30 * time.Second)
    defer reorgCheckTicker.Stop()

    mempoolReconTicker := time.NewTicker(5 * time.Minute)
    defer mempoolReconTicker.Stop()

    blocksProcessed := 0
    txsIndexed := 0

    log.Println("✅ Live processing active")

    for {
        select {
        case <-ctx.Done():
            log.Println("📍 Main loop exiting...")
            return

        case block, ok := <-zmqSub.BlockChan():
            if !ok {
                return
            }
            height, affected, err := writer.IndexBlock(block)
            if err != nil {
                log.Printf("⚠️  Block processing error: %v", err)

                if isReorgError(err) {
                    log.Println("🔄 Reorg detected, catching up to tip...")
                    if catchUpErr := chainManager.CatchUpToTip(); catchUpErr != nil {
                        log.Printf("⚠️  Catch-up after reorg failed: %v", catchUpErr)
                    } else {
                        log.Println("✅ Catch-up after reorg complete")
                    }
                }
                continue
            }

            blocksProcessed++

            var headerBuf bytes.Buffer
            if err := block.Header.Serialize(&headerBuf); err != nil {
                log.Printf("⚠️  Failed to serialize header: %v", err)
                continue
            }
            headerHex := hex.EncodeToString(headerBuf.Bytes())

            server.NotifyNewBlock(height, headerHex)

            if len(affected) > 0 {
                server.MarkScripthashDirtyMany(affected)
                for _, shHex := range affected {
                    server.NotifyScripthashStatusHex(shHex)
                }
            }

            if blocksProcessed%cfg.Indexer.UndoPruneInterval == 0 {
                writer.TriggerPrune()
            }

        case tx, ok := <-zmqSub.TxChan():
            if !ok {
                return
            }
            affected, err := writer.IndexMempoolTx(tx)
            if err == nil {
                txsIndexed++
                if len(affected) > 0 {
                    server.MarkScripthashDirtyMany(affected)
                    for _, shHex := range affected {
                        server.NotifyScripthashStatusHex(shHex)
                    }
                }
            }

        case gap, ok := <-zmqSub.GapChan():
            if !ok {
                return
            }
            if gap.Stream == "block" {
                log.Printf("⚠️  ZMQ block sequence gap detected: expected %d, got %d",
                    gap.Expected, gap.Got)
                if err := chainManager.CatchUpToTip(); err != nil {
                    log.Printf("⚠️  Catch-up after ZMQ gap failed: %v", err)
                }
            } else if gap.Stream == "tx" {
                log.Printf("⚠️  ZMQ tx sequence gap detected: expected %d, got %d",
                    gap.Expected, gap.Got)
                if _, err := reconcileMempool(rpcClient, mempool, writer); err != nil {
                    log.Printf("⚠️  Mempool reconcile after ZMQ gap failed: %v", err)
                }
            }

        case <-pruneTicker.C:
            writer.TriggerPrune()

        case <-reorgCheckTicker.C:
            blockCount, err := rpcClient.GetBlockCount()
            if err != nil {
                log.Printf("⚠️  Periodic tip check RPC failed: %v", err)
                continue
            }

            currentHeight := chainManager.GetCurrentHeight()
            coreTip := int32(blockCount)

            if coreTip > currentHeight {
                missedBlocks := coreTip - currentHeight
                log.Printf("🔄 ZMQ safety check: missed %d block(s), "+
                    "catching up (%d -> %d)",
                    missedBlocks, currentHeight, coreTip)
                if err := chainManager.CatchUpToTip(); err != nil {
                    log.Printf("⚠️  Catch-up after missed ZMQ failed: %v", err)
                } else {
                    log.Printf("✅ Caught up to tip %d", coreTip)
                }
            }

        case <-mempoolReconTicker.C:
            affected, err := reconcileMempool(rpcClient, mempool, writer)
            if err != nil {
                log.Printf("⚠️  Periodic mempool reconcile failed: %v", err)
                continue
            }
            if len(affected) > 0 {
                server.MarkScripthashDirtyMany(affected)
                for _, shHex := range affected {
                    server.NotifyScripthashStatusHex(shHex)
                }
            }

        case <-statsTicker.C:
            writerStats := writer.Stats()
            zmqStats := zmqSub.Stats()
            connCount := server.GetActiveConnectionCount()
            subScripthashes, subHeaders, subConns := server.
                GetSubscriptionManager().GetTotalSubscriptions()

            log.Printf("📊 Stats: height=%d, blocks=%d, "+
                "mempool[rx=%d,idx=%d], conns=%d, "+
                "subs=[sh:%d,hdr:%d,conn:%d]",
                chainManager.GetCurrentHeight(),
                writerStats.BlocksIndexed,
                zmqStats.TxsReceived,
                txsIndexed,
                connCount,
                subScripthashes, subHeaders, subConns)
        }
    }
}

func isReorgError(err error) bool {
    if err == nil {
        return false
    }
    errStr := err.Error()
    return contains(errStr, "reorg") ||
        contains(errStr, "does not connect") ||
        contains(errStr, "gap detected") ||
        contains(errStr, "rolled back")
}

func contains(s, substr string) bool {
    return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
    for i := 0; i <= len(s)-len(substr); i++ {
        if s[i:i+len(substr)] == substr {
            return true
        }
    }
    return false
}