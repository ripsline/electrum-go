package main

import (
    "encoding/json"
    "fmt"
    "log"
    "os"
    "time"

    "github.com/btcsuite/btcd/rpcclient"

    "github.com/ripsline/electrum-go/internal/config"
    "github.com/ripsline/electrum-go/internal/metrics"
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
    rpcStart := time.Now()
    result, err := client.RawRequest("getblockchaininfo", nil)
    metrics.ObserveBitcoinRPC("getblockchaininfo", rpcStart, err)
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
    rpcCookie, dbPath string, startHeight int) {
    if listen != "" {
        cfg.Server.Listen = listen
    }
    if rpcHost != "" {
        cfg.Bitcoin.RPCHost = rpcHost
    }
    if rpcCookie != "" {
        cfg.Bitcoin.RPCCookiePath = rpcCookie
        // Cookie auth wins over any user/pass carried from the config file.
        cfg.Bitcoin.RPCUser = ""
        cfg.Bitcoin.RPCPass = ""
    }
    if rpcUser != "" {
        cfg.Bitcoin.RPCUser = rpcUser
        cfg.Bitcoin.RPCCookiePath = ""
    }
    if rpcPass != "" {
        cfg.Bitcoin.RPCPass = rpcPass
        cfg.Bitcoin.RPCCookiePath = ""
    }
    if dbPath != "" {
        cfg.Storage.DBPath = dbPath
    }
    if startHeight != -9999 {
        cfg.Indexer.StartHeight = startHeight
    }
}

func connectToBitcoinCore(cfg *config.Config) (*rpcclient.Client, error) {
    user, pass := cfg.Bitcoin.RPCUser, cfg.Bitcoin.RPCPass
    if cfg.Bitcoin.RPCCookiePath != "" {
        // Read fresh on every connect so a bitcoind restart (which rotates
        // the cookie) is picked up without stale credentials.
        u, p, err := config.ReadRPCCookie(cfg.Bitcoin.RPCCookiePath)
        if err != nil {
            return nil, err
        }
        user, pass = u, p
        log.Printf("🔐 Using RPC cookie auth from %s", cfg.Bitcoin.RPCCookiePath)
    }

    connCfg := &rpcclient.ConnConfig{
        Host:         cfg.Bitcoin.RPCHost,
        User:         user,
        Pass:         pass,
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
