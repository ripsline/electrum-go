// Package config provides configuration loading and validation for the Electrum server.
//
// Configuration can be loaded from a TOML file and/or overridden via command-line flags.
// The config is validated at load time to catch common mistakes early.
package config

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/pelletier/go-toml/v2"
)

// Config holds all configuration for the Electrum server.
// Fields are organized by component for clarity.
type Config struct {
    Server  ServerConfig  `toml:"server"`
    Bitcoin BitcoinConfig `toml:"bitcoin"`
    Storage StorageConfig `toml:"storage"`
    Indexer IndexerConfig `toml:"indexer"`
    Logging LoggingConfig `toml:"logging"`
}

// ServerConfig holds Electrum protocol server settings.
type ServerConfig struct {
    // Listen address in "host:port" format.
    // Use "127.0.0.1:50001" for local-only, "0.0.0.0:50001" for all interfaces.
    Listen string `toml:"listen"`

    // Maximum concurrent client connections.
    // Prevents resource exhaustion from too many clients.
    MaxConnections int `toml:"max_connections"`

    // How long to wait for client requests before timing out.
    // Parsed from seconds in config file.
    RequestTimeoutSeconds int `toml:"request_timeout_seconds"`

    // Computed field: RequestTimeout as time.Duration
    RequestTimeout time.Duration `toml:"-"`
}

// BitcoinConfig holds Bitcoin Core RPC and ZMQ settings.
type BitcoinConfig struct {
    // RPC endpoint in "host:port" format (e.g., "127.0.0.1:8332").
    RPCHost string `toml:"rpc_host"`

    // RPC authentication credentials.
    // Must match rpcuser/rpcpassword in bitcoin.conf.
    RPCUser string `toml:"rpc_user"`
    RPCPass string `toml:"rpc_pass"`

    // ZMQ endpoint for raw block notifications.
    // Must match zmqpubrawblock in bitcoin.conf.
    ZMQBlockAddr string `toml:"zmq_block_addr"`

    // ZMQ endpoint for raw transaction notifications.
    // Must match zmqpubrawtx in bitcoin.conf.
    ZMQTxAddr string `toml:"zmq_tx_addr"`
}

// StorageConfig holds Pebble database settings.
type StorageConfig struct {
    // Path to the Pebble database directory.
    // Will be created if it doesn't exist.
    DBPath string `toml:"db_path"`

    // Maximum number of blocks to keep undo data for.
    // Determines how deep a reorg we can handle.
    // 144 blocks ≈ 1 day on mainnet.
    MaxReorgDepth int `toml:"max_reorg_depth"`
}

// IndexerConfig holds blockchain indexing settings.
type IndexerConfig struct {
    // Block height to start indexing from.
    // -1 = current chain tip (forward-indexing mode)
    //  0 = genesis block
    // >0 = specific height
    StartHeight int `toml:"start_height"`

    // How often to save checkpoints during sync (in blocks).
    CheckpointInterval int `toml:"checkpoint_interval"`

    // How often to prune old undo data (in blocks).
    UndoPruneInterval int `toml:"undo_prune_interval"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
    // Log level: "debug", "info", "warn", "error"
    Level string `toml:"level"`

    // Whether to log each client request (very verbose).
    LogRequests bool `toml:"log_requests"`
}

// DefaultConfig returns a configuration with sensible defaults.
// These defaults are suitable for testnet4 development.
func DefaultConfig() *Config {
    return &Config{
        Server: ServerConfig{
            Listen:                "127.0.0.1:50001",
            MaxConnections:        100,
            RequestTimeoutSeconds: 30,
            RequestTimeout:        30 * time.Second,
        },
        Bitcoin: BitcoinConfig{
            RPCHost:      "127.0.0.1:48332",
            RPCUser:      "electrumgo",
            RPCPass:      "",
            ZMQBlockAddr: "tcp://127.0.0.1:28332",
            ZMQTxAddr:    "tcp://127.0.0.1:28333",
        },
        Storage: StorageConfig{
            DBPath:        "./data/index.db",
            MaxReorgDepth: 144,
        },
        Indexer: IndexerConfig{
            StartHeight:        -1, // Forward-indexing mode
            CheckpointInterval: 100,
            UndoPruneInterval:  1000,
        },
        Logging: LoggingConfig{
            Level:       "info",
            LogRequests: false,
        },
    }
}

// LoadFromFile reads configuration from a TOML file.
// Missing fields retain their default values.
// Returns an error if the file cannot be read or parsed.
func LoadFromFile(path string) (*Config, error) {
    // Start with defaults
    cfg := DefaultConfig()

    // Read file contents
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
    }

    // Parse TOML into config struct
    if err := toml.Unmarshal(data, cfg); err != nil {
        return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
    }

    // Compute derived fields
    cfg.Server.RequestTimeout = time.Duration(cfg.Server.RequestTimeoutSeconds) * time.Second

    // Validate the configuration
    if err := cfg.Validate(); err != nil {
        return nil, fmt.Errorf("invalid configuration: %w", err)
    }

    return cfg, nil
}

// Validate checks the configuration for common errors.
// Returns a descriptive error if validation fails.
func (c *Config) Validate() error {
    var errs []string

    // Server validation
    if c.Server.Listen == "" {
        errs = append(errs, "server.listen is required")
    }
    if c.Server.MaxConnections <= 0 {
        errs = append(errs, "server.max_connections must be positive")
    }
    if c.Server.RequestTimeoutSeconds <= 0 {
        errs = append(errs, "server.request_timeout_seconds must be positive")
    }

    // Warn about exposed server (not an error, just a warning)
    if strings.HasPrefix(c.Server.Listen, "0.0.0.0") || strings.HasPrefix(c.Server.Listen, ":") {
        fmt.Println("⚠️  WARNING: Server will be exposed to network without TLS encryption")
        fmt.Println("   Consider using a reverse proxy with TLS for production use")
    }

    // Bitcoin validation
    if c.Bitcoin.RPCHost == "" {
        errs = append(errs, "bitcoin.rpc_host is required")
    }
    if c.Bitcoin.RPCUser == "" {
        errs = append(errs, "bitcoin.rpc_user is required")
    }
    if c.Bitcoin.RPCPass == "" {
        errs = append(errs, "bitcoin.rpc_pass is required")
    }
    if c.Bitcoin.ZMQBlockAddr == "" {
        errs = append(errs, "bitcoin.zmq_block_addr is required")
    }
    if !strings.HasPrefix(c.Bitcoin.ZMQBlockAddr, "tcp://") {
        errs = append(errs, "bitcoin.zmq_block_addr must start with tcp://")
    }
    if c.Bitcoin.ZMQTxAddr == "" {
        errs = append(errs, "bitcoin.zmq_tx_addr is required")
    }
    if !strings.HasPrefix(c.Bitcoin.ZMQTxAddr, "tcp://") {
        errs = append(errs, "bitcoin.zmq_tx_addr must start with tcp://")
    }

    // Storage validation
    if c.Storage.DBPath == "" {
        errs = append(errs, "storage.db_path is required")
    }
    if c.Storage.MaxReorgDepth <= 0 {
        errs = append(errs, "storage.max_reorg_depth must be positive")
    }
    if c.Storage.MaxReorgDepth > 1000 {
        // Not strictly wrong, but unusual and might indicate a config mistake
        fmt.Printf("⚠️  WARNING: storage.max_reorg_depth=%d is unusually large\n", c.Storage.MaxReorgDepth)
        fmt.Println("   This will increase disk usage. Typical value is 144 (1 day).")
    }

    // Indexer validation
    if c.Indexer.StartHeight < -1 {
        errs = append(errs, "indexer.start_height must be >= -1")
    }
    if c.Indexer.CheckpointInterval <= 0 {
        errs = append(errs, "indexer.checkpoint_interval must be positive")
    }
    if c.Indexer.UndoPruneInterval <= 0 {
        errs = append(errs, "indexer.undo_prune_interval must be positive")
    }

    // Logging validation
    validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
    if !validLevels[strings.ToLower(c.Logging.Level)] {
        errs = append(errs, "logging.level must be one of: debug, info, warn, error")
    }

    // Return combined errors
    if len(errs) > 0 {
        return errors.New(strings.Join(errs, "; "))
    }

    return nil
}

// EnsureDBDirectory creates the database directory if it doesn't exist.
// Returns an error if the directory cannot be created.
func (c *Config) EnsureDBDirectory() error {
    dir := filepath.Dir(c.Storage.DBPath)
    if dir == "" || dir == "." {
        dir = c.Storage.DBPath
    }

    if err := os.MkdirAll(dir, 0750); err != nil {
        return fmt.Errorf("failed to create database directory %s: %w", dir, err)
    }

    return nil
}

// String returns a human-readable representation of the config.
// Sensitive fields (passwords) are masked.
func (c *Config) String() string {
    passDisplay := "****"
    if c.Bitcoin.RPCPass == "" {
        passDisplay = "(empty)"
    }

    return fmt.Sprintf(`Configuration:
  Server:
    Listen:           %s
    Max Connections:  %d
    Request Timeout:  %s
  Bitcoin:
    RPC Host:         %s
    RPC User:         %s
    RPC Pass:         %s
    ZMQ Block:        %s
    ZMQ Tx:           %s
  Storage:
    DB Path:          %s
    Max Reorg Depth:  %d blocks
  Indexer:
    Start Height:     %d
    Checkpoint Every: %d blocks
    Prune Undo Every: %d blocks
  Logging:
    Level:            %s
    Log Requests:     %v`,
        c.Server.Listen,
        c.Server.MaxConnections,
        c.Server.RequestTimeout,
        c.Bitcoin.RPCHost,
        c.Bitcoin.RPCUser,
        passDisplay,
        c.Bitcoin.ZMQBlockAddr,
        c.Bitcoin.ZMQTxAddr,
        c.Storage.DBPath,
        c.Storage.MaxReorgDepth,
        c.Indexer.StartHeight,
        c.Indexer.CheckpointInterval,
        c.Indexer.UndoPruneInterval,
        c.Logging.Level,
        c.Logging.LogRequests,
    )
}
