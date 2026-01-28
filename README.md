# electrum-go

A Go-based Electrum server designed to work with a **pruned Bitcoin Core node**. It treats Bitcoin Core as a live data feed (RPC + ZMQ) and maintains its own compact, reorg-safe index (scripthash history, UTXOs, headers, and bounded undo data) in Pebble.

**Goal:** Run on a cheap VPS (~100GB SSD) while preserving Electrum privacy benefits.

**Trade-off:** Only wallets created **after the server start height** have full history.

## Features

- Electrum protocol v1.4 support
- Forward-indexing from a configurable start height
- Pruned-node compatible
- ZMQ-based real-time block/tx ingestion
- Compact transaction storage (per-block tx blob + offsets, txid → (height, txIndex) lookup)
- Reorg handling with bounded undo logs
- Batch JSON-RPC support
- Per-connection write queue (prevents notification corruption)

## Prerequisites

- Ubuntu 24.04+ (or similar Linux distribution)
- Bitcoin Core (synced for your target network)
- Tor (optional, but recommended for privacy)
- Go 1.25.6 or later
- Git

## Dependencies

Install required system packages:

```bash
sudo apt update
sudo apt install -y git build-essential pkg-config libzmq3-dev
```

## Install Go

```bash
cd /tmp
wget https://go.dev/dl/go1.25.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.6.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
go version
```

## Installation

Clone the repository:

```bash
cd ~
git clone https://github.com/ripsline/electrum-go.git
cd electrum-go
```

Build the server:

```bash
go mod tidy
go build ./cmd/server
```

## Configuration

Copy the template configuration and customize for your setup:

```bash
cp config.template.toml config.toml
nano config.toml
```

Key settings to verify in your config:
- `[bitcoin] rpc_host` - Use `127.0.0.1:48332` for testnet4, `127.0.0.1:8332` for mainnet
- `[bitcoin] rpc_user/rpc_pass` - Must match your bitcoin.conf
- `[server] listen` - Use `127.0.0.1:50001` for local testing, `0.0.0.0:50001` for external access
- `[indexer] start_height` - Use `-1` for current tip, `0` for genesis, or specific block number

**Note:** `config.toml` is in `.gitignore` and will not be committed to version control.

### Bitcoin Core Setup (Testnet4 Example)

Add to `~/.bitcoin/bitcoin.conf`:
```ini
# Main settings (apply to all networks)
server=1
prune=20000
dbcache=512
maxmempool=300
disablewallet=1

# Tor configuration (optional)
proxy=127.0.0.1:9050
listen=1

# Testnet4
testnet4=1

[testnet4]
bind=127.0.0.1
rpcuser=electrumgo
rpcpassword=yourpass
rpcbind=127.0.0.1
rpcallowip=127.0.0.1

zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333

# --- MAINNET EXAMPLE (commented) ---
# [main]
# bind=127.0.0.1
# rpcuser=electrumgo
# rpcpassword=yourpass
# rpcbind=127.0.0.1
# rpcallowip=127.0.0.1
# rpcport=8332
# zmqpubrawblock=tcp://127.0.0.1:28332
# zmqpubrawtx=tcp://127.0.0.1:28333
```

Start Bitcoin Core:
```bash
bitcoind -daemon
```

For testnet4, verify sync status:
```bash
bitcoin-cli -testnet4 getblockchaininfo
```

## Running

Ensure Bitcoin Core is fully synced before starting the Electrum server:

```bash
bitcoin-cli -testnet4 getblockchaininfo | grep -E "chain|blocks|headers|verificationprogress"
```

Start the server:

```bash
./server -config config.toml
```

### Start Height Options

- `start_height = -1` → Start at current tip (forward-indexing only)
- `start_height = 0` → Full indexing from genesis (requires non-pruned Core)
- `start_height = 100000` → Start from specified height (e.g., block 100000)

**Important:** For pruned nodes, start height must be >= prune height.

## Privacy

electrum-go answers queries from its own index and compact transaction storage. Wallets only communicate with your server, not with external services.

## Limitations

- Wallets created before the start height will not have full history
- Some Electrum protocol methods are still unimplemented

## License

MIT License - see [LICENSE](LICENSE) file for details.