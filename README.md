# electrum-go

A Go-based Electrum server designed to work with a **pruned Bitcoin Core node**. It treats Bitcoin Core as a live data feed (RPC + ZMQ) and maintains its own compact, reorg-safe index (scripthash history, UTXOs, headers, and bounded undo data) in Pebble.

**Goal:** Run on a cheap VPS or PC while preserving Electrum privacy benefits.

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

For complete setup instructions including Bitcoin Core and Tor installation, see:
- [Testnet4 Setup Guide](docs/TESTNET4.md) - Complete walkthrough for testing
- [Mainnet Setup Guide](docs/MAINNET.md) - Production deployment guide

## Quick Start

### 1. Install Dependencies

```bash
sudo apt update
sudo apt install -y git build-essential pkg-config libzmq3-dev
```

### 2. Install Go

```bash
cd /tmp
wget https://go.dev/dl/go1.23.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.6.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
go version
```

### 3. Clone and Build

```bash
cd ~
git clone https://github.com/ripsline/electrum-go.git
cd electrum-go
go mod tidy
go build ./cmd/server
```

### 4. Configure

Copy the template configuration:

```bash
cp config.template.toml config.toml
nano config.toml
```

Key settings to verify:
- `[bitcoin] rpc_host` - Use `127.0.0.1:48332` for testnet4, `127.0.0.1:8332` for mainnet
- `[bitcoin] rpc_user/rpc_pass` - Must match your bitcoin.conf
- `[server] listen` - Use `127.0.0.1:50001` for local testing
- `[indexer] start_height` - Use `-1` for current tip, or specific block number

## Start Height Options

- `start_height = -1` → Start at current tip (forward-indexing only)
- `start_height = 800000` → Start from specified height (e.g., block 800000)

**Important:** For pruned nodes, start height must be >= prune height.

**Note:** `config.toml` is in `.gitignore` and will not be committed to version control.

### 5. Bitcoin Core Configuration

Your `bitcoin.conf` needs these settings:

**For Testnet4:**
```ini
server=1
prune=10000

testnet4=1
[testnet4]
rpcuser=electrumgo
rpcpassword=yourpass
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333
```

**For Mainnet:**
```ini
server=1
prune=20000

[main]
rpcuser=electrumgo
rpcpassword=yourpass
rpcbind=127.0.0.1
rpcallowip=127.0.0.1
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333
```

### Run

Ensure Bitcoin Core is fully synced:

```bash
# Testnet4
bitcoin-cli -testnet4 getblockchaininfo | grep -E "chain|blocks|headers|verificationprogress"

# Mainnet
bitcoin-cli getblockchaininfo | grep -E "chain|blocks|headers|verificationprogress"
```

Start the server:

```bash
./server -config config.toml
```

## Privacy

electrum-go provides enhanced privacy compared to public Electrum servers by running your own private index. Your wallet derives addresses locally from your extended public key (xpub), which never leaves your device. When querying the server, only individual addresses are sent. Without access to your xpub, the server cannot link these addresses together or derive your other addresses.

**Key privacy features:**
- Your wallet transactions are never stored on the server in an identifiable way
- The server forgets your requests immediately after serving them
- No third-party services can monitor your wallet activity or balance
- Better privacy than Bitcoin Core descriptor wallets, which require importing your xpub and store your balance/transactions/public keys unencrypted on the node

## Limitations

- Importing wallets created before the start height will not have full history. This can be dangerous.
- Please generate a fresh wallet after server start.
- Some Electrum protocol methods are still unimplemented

## Documentation

- [Testnet4 Setup Guide](docs/TESTNET4.md) - Complete setup from scratch including Tor and Bitcoin Core
- [Mainnet Setup Guide](docs/MAINNET.md) - Production deployment guide

## License

MIT License - see [LICENSE](LICENSE) file for details.