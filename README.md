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

## Requirements

- Go 1.23+
- Bitcoin Core with:
  - RPC enabled
  - ZMQ rawblock/rawtx enabled
  - Pruning allowed (txindex not required)

## Installation
```bash
go build ./cmd/server
```

## Configuration

Create a `config.toml` file (example coming soon) or see the Bitcoin Core configuration below.

### Bitcoin Core Setup (Testnet4 Example)

Add to `~/.bitcoin/bitcoin.conf`:
```ini
# Main settings (apply to all networks)
server=1
prune=20000
dbcache=512
maxmempool=300
disablewallet=1

# Testnet4
testnet4=1

[testnet4]
rpcuser=electrumgo
rpcpassword=yourpass
rpcbind=127.0.0.1
rpcallowip=127.0.0.1

zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333

# --- MAINNET EXAMPLE (commented) ---
# [main]
# rpcuser=electrumgo
# rpcpassword=yourpass
# rpcbind=127.0.0.1
# rpcallowip=127.0.0.1
# rpcport=8332
# zmqpubrawblock=tcp://127.0.0.1:28332
# zmqpubrawtx=tcp://127.0.0.1:28333
```

## Usage
```bash
./server -config config.toml
```

### Start Height Options

- `start_height = -1` → Start at current tip (forward-indexing only)
- `start_height = 0` → Full indexing from genesis (requires non-pruned Core)
- `start_height > 0` → Start from specified height

**Important:** For pruned nodes, start height must be >= prune height.

## Privacy

electrum-go answers queries from its own index and compact transaction storage. Wallets only communicate with your server, not with external services.

## Limitations

- Wallets created before the start height will not have full history
- Some Electrum protocol methods are still unimplemented

## License

MIT License - see [LICENSE](LICENSE) file for details.