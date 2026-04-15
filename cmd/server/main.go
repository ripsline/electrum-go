// Package main is the entry point for the electrum-go server.
package main

import (
    "context"
    "errors"
    "flag"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/ripsline/electrum-go/internal/electrum"
    "github.com/ripsline/electrum-go/internal/indexer"
    "github.com/ripsline/electrum-go/internal/metrics"
    "github.com/ripsline/electrum-go/internal/storage"
)

var (
    Version   = "0.1.0"
    GitCommit = "unknown"
    BuildTime = "unknown"
)

func main() {
    configFile := flag.String("config", "", "Path to config file (TOML)")
    listenAddr := flag.String("listen", "", "Electrum server listen address")
    rpcHost := flag.String("rpc-host", "", "Bitcoin Core RPC host:port")
    rpcUser := flag.String("rpc-user", "", "Bitcoin Core RPC username")
    rpcPass := flag.String("rpc-pass", "", "Bitcoin Core RPC password")
    rpcCookie := flag.String("rpc-cookie", "", "Path to Bitcoin Core RPC cookie file")
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

    applyOverrides(cfg, *listenAddr, *rpcHost, *rpcUser, *rpcPass, *rpcCookie,
        *dbPath, *startHeight)
    // Re-validate after CLI overrides so conflicting flags (e.g. both cookie
    // and user/pass) are caught before we try to connect.
    if err := cfg.Validate(); err != nil {
        log.Fatalf("❌ Invalid configuration after applying flags: %v", err)
    }

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

    if cfg.Metrics.Enabled {
        metrics.Init(info.Chain, Version, GitCommit)
        go func() {
            if err := metrics.ServeMetrics(cfg.Metrics.Listen); err != nil {
                log.Printf("⚠️  Metrics server error: %v", err)
            }
        }()
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

    if info.Pruned && info.PruneHeight > 0 {
        chainManager.SetPruneHeight(int32(info.PruneHeight))
    }

    // Seed gauges from the loaded state so /metrics reflects reality during
    // the initial catch-up, not just after the first new block is indexed.
    metrics.ChainHeight.Set(float64(chainManager.GetCurrentHeight()))
    metrics.BitcoinCoreHeight.Set(float64(info.Blocks))
    metrics.MempoolTxCount.Set(float64(mempool.Count()))

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

    if cfg.Metrics.Enabled {
        metrics.StartDBSizeSampler(ctx, cfg.Storage.DBPath, 60*time.Second)
    }

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

    go func() {
        sig := <-sigChan
        log.Printf("\n🛑 Received signal %v, shutting down...", sig)
        cancel()
    }()

    log.Println("⏳ Catching up with blockchain...")
    if err := chainManager.CatchUpToTip(); errors.Is(err, indexer.ErrPrunedGap) {
        haltForOperator(ctx, err, cfg.Storage.DBPath)
        log.Println("🛑 Shutting down...")
        return
    } else if err != nil {
        log.Fatalf("❌ Catch-up failed: %v", err)
    }

    go func() {
        if err := electrumServer.Start(); err != nil {
            log.Printf("❌ Electrum server error: %v", err)
            cancel()
        }
    }()

    if err := zmqSub.Start(); err != nil {
        log.Fatalf("❌ Failed to start ZMQ: %v", err)
    }

    log.Println("🔄 Starting live processing...")
    loopErr := runMainLoop(ctx, cfg, writer, zmqSub, electrumServer, chainManager,
        client, mempool)

    log.Println("🛑 Shutting down...")

    if err := electrumServer.Stop(); err != nil {
        log.Printf("⚠️  Error stopping Electrum server: %v", err)
    }

    if errors.Is(loopErr, indexer.ErrPrunedGap) {
        haltForOperator(ctx, loopErr, cfg.Storage.DBPath)
    }

    log.Println("✅ Shutdown complete")
}
