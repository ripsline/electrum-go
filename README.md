# electrum-go

electrum-go is a Go-based Electrum server designed to work with a **pruned Bitcoin Core node**.  
It treats Bitcoin Core as a live data feed (RPC + ZMQ) and maintains its own compact,
reorg‑safe index (scripthash history, UTXOs, headers, and bounded undo data) in Pebble.

**Goal:** run on a cheap VPS (~100GB SSD) while preserving Electrum privacy benefits.  
**Trade‑off:** only wallets created **after the server start height** have full history.

---

## Features

- Electrum protocol v1.4
- Forward‑indexing from a configurable start height
- Pruned‑node compatible
- ZMQ-based real‑time block/tx ingestion
- Compact transaction storage:
  - per‑block tx blob + offsets
  - txid -> (height, txIndex) lookup
- Reorg handling with bounded undo logs
- Batch JSON‑RPC support
- Per‑connection write queue (prevents notification corruption)

---

## Requirements

- Go 1.23+
- Bitcoin Core with:
  - RPC enabled
  - ZMQ rawblock/rawtx enabled
  - Pruning allowed (txindex not required)

---

## Bitcoin Core example (testnet4)

`~/.bitcoin/bitcoin.conf`

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
rpcpassword=RrWjFBajVegxl3Z0NkWU0cTvQ
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

---

## Build &amp; Run

go build ./cmd/server
./server -config config.toml

---

## Start Height Rules

- `start_height = -1` → start at current tip (forward‑indexing)
- `start_height = 0`  → full indexing (requires non‑pruned Core)
- `start_height > h`  → start from that height

For pruned nodes: start height must be >= prune height.

---

## Notes on Privacy

electrum-go answers queries from its own index and compact tx storage.
Wallets only talk to your server.

---

## Limitations

- Wallets created before the start height will not have full history
- Some Electrum methods are still unimplemented

---