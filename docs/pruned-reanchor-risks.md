# Pruned re-anchor — wallet safety context

Context doc for documentation authors. Pull from this when updating the
README, operator docs, or the in-process recovery message shown by
`logPrunedGapRecovery` in `cmd/server/main.go`.

## Definitions

**Anchor / start height.** electrum-go is a forward-indexing server. At
first startup (or after a DB wipe), it picks a `start_height` — the
current bitcoind chain tip — and only indexes blocks from that height
forward. That height is the *anchor*. Every block below the anchor is
invisible to this server.

**Re-anchor.** The act of wiping `/var/lib/electrum-go/index.db` and
restarting the service, which causes it to pick a new (higher) anchor at
the current chain tip. On a pruned bitcoind, you cannot re-anchor lower
than the current tip because the historical blocks are no longer on
disk.

**Pruned gap.** The indexed tip has fallen below bitcoind's prune window.
Blocks needed to continue indexing are gone from disk. The server
refuses to serve clients and prints operator-recovery instructions. The
only recovery on a pruned node is wipe + re-anchor.

## Why a pre-existing wallet must not be connected to a re-anchored server

The server itself does not lose funds — bitcoind still holds the
complete UTXO set. The risk is that a wallet's entire view of reality
comes from the Electrum server it is connected to. When that view is
partial, user actions taken against it can lead to real, unrecoverable
loss.

Concrete loss paths:

### 1. Seed-recovery confusion

User restores a wallet from seed. Wallet queries the re-anchored server,
sees no history for its addresses, displays $0. User concludes the seed
is wrong or the funds are gone and discards the backup (paper, metal,
encrypted file). The seed was valid — the server simply could not see
pre-anchor history. Backup is now destroyed; funds are unrecoverable.

### 2. Gap-limit truncation

BIP44/49/84/86 wallets scan derivation paths until they hit N
consecutive unused addresses (default 20). On a re-anchored server,
pre-anchor activity is invisible, so addresses that *were* used look
unused. The wallet stops scanning early and never discovers
later-derived addresses that received funds before the anchor. Even
after the wallet is later reconnected to a full-history server, it may
never scan deep enough to find those funds without a manual gap-limit
override the user may not know to perform.

### 3. Address reuse

The wallet believes old receive/change addresses are fresh and reuses
them. Privacy loss is immediate. In multisig or coordinated signing
flows, reuse can cause accounting mismatches and coordination failures
that in edge cases prevent legitimate spends.

### 4. Acting on the wrong balance

User sees a lower-than-actual balance, treats it as ground truth,
transfers what they "have" elsewhere (or reports it to a counterparty,
cosigner, exchange, tax tool), and only later discovers the pre-anchor
UTXOs existed. Depending on what commitments were made against the
incorrect balance, real loss can follow.

## Safe practice after a wipe + re-anchor

- Use this server only with a **new wallet created after the re-anchor
  event**. That wallet has no pre-anchor history, so the server's view
  is complete for it.
- For any **pre-existing wallet**, connect to a server with full history
  for the relevant blocks:
  - a fully-synced (unpruned) Electrum server the user controls, or
  - a trusted public Electrum server.
- Do not rely on balance or history displayed by this server for any
  pre-existing wallet under any circumstance.

## Recovery paths

electrum-go is designed for operators with limited disk, running
alongside a pruned bitcoind. Any recovery path that requires a
non-pruned node works against that design intent, so the recommended
recovery is almost always option 1.

### 1. Use a new wallet (recommended)

Generate a fresh wallet after the server re-anchors and use that wallet
going forward with this server. Move pre-existing wallets off this
server entirely — see "Safe practice" above. This is the cleanest path
and aligns with the low-storage deployment this server targets.

### 2. Backfill bitcoind with full history, then re-anchor lower

If the operator needs a pre-existing wallet to work with this server
specifically, historical blocks must be restored on bitcoind first.
This is an escape hatch, not the intended mode of operation.

Important: **raising `prune=` on an already-pruned node does not bring
back blocks.** `prune=N` is a *retention cap* going forward, not a
history target. Bumping from 25GB to 100GB just makes Core prune less
aggressively from that point on; previously pruned blocks stay gone.

To actually restore historical blocks:

- Set `prune=0` in `bitcoin.conf` and restart bitcoind. Core recognizes
  the missing block data and re-downloads it from peers. Hours-to-days
  of bandwidth; ends with full history on disk (~600GB at current
  chain size).
- Or: wipe the bitcoind data dir and IBD from scratch with a larger
  `prune=` value (e.g., 500000 for effectively unpruned, 100000 to keep
  roughly the last six months once synced). Longer overall but a clean
  mental model.

Once bitcoind holds the required block range, on the electrum-go side:

1. Set `start_height` in the electrum-go config to a height at or below
   the earliest relevant wallet transaction.
2. Wipe `/var/lib/electrum-go/index.db`.
3. Restart electrum-go. It catches up forward from `start_height` to
   tip.
4. Once caught up, pruning can be re-enabled on bitcoind. Prune depth
   after catch-up does not affect electrum-go as long as the indexer
   stays near the tip — which the pruned-gap detection enforces.

### 3. Point electrum-go at a different, fully-synced node

If the operator has access to another bitcoind with full history, point
electrum-go at that node (via `rpc_host` / ZMQ endpoints in config) and
follow the same wipe + `start_height` + restart flow above. This
avoids touching the original pruned node.

## Prevention (roadmap)

The pruned-gap state ideally never opens in normal operation. Two
mitigations:

- **Gate startup on IBD completion** (roadmap item). Refuse to start
  indexing until `verificationprogress >= 0.9999`. Closes the common
  "started mid-IBD and got lapped by the prune window" path.
- **Operator-facing survival metric** (deferred). Surface
  `prune_headroom_blocks` (bitcoind prune height − indexed tip) so
  operators can alert *before* the gap opens and restart the service in
  time.

The halt + operator-wipe policy is the intentional terminal state; these
mitigations reduce the frequency at which operators hit it.
