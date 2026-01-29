# Mainnet Production Setup Guide

This guide walks you through deploying electrum-go on Bitcoin mainnet for production use.

**⚠️ Warning:** This involves real bitcoin. Test thoroughly on testnet4 first (see [TESTNET4.md](TESTNET4.md)).

## Prerequisites

- Ubuntu 24.04+ server (or similar Linux distribution)
- **At least 4GB RAM**
- **At least 100GB disk space** (with heavy pruning)
- sudo access
- Backup and security plan (write down your fresh seed!)
- **DO NOT IMPORT AN OLD WALLET UNLESS YOU KNOW WHAT YOU'RE DOING**

## User Setup

**Note:** This guide assumes you're setting up a fresh server with a dedicated user. We use `ripsline` as an example throughout this guide, but you can use any username you prefer (e.g., `bitcoin`, `electrum`, `yourname`, etc.).

**If you're setting up a fresh server:** Create your user during initial setup with sudo access.

**If adding to an existing server:** Create a new user first:

```bash
sudo adduser ripsline  # Replace with your preferred username
sudo usermod -aG sudo ripsline
sudo su - ripsline
```

**Throughout this guide, replace `ripsline` with your chosen username.**

All subsequent commands in this guide should be run as your chosen user (we'll use `ripsline` in examples).

## Important Production Considerations

### Disk Space

electrum-go is designed for **pruned nodes**. Configure `prune=` based on available space:
- Minimum: `prune=550` (keeps ~550MB of blocks)
- Recommended: `prune=10000` (~10GB)

**Note:** If you want to run a full archival node, use a more mature Electrum server implementation like Electrs or Fulcrum.

### Security

- **Strong passwords**: Use long, random passwords for RPC
- **Firewall**: Only expose necessary ports
- **SSL/TLS**: Required if exposing to public internet
- **Tor**: Recommended for privacy

### Start Height

For mainnet, choose carefully:

- `start_height = -1`: Start from current tip (recommended, ~1-2 hours to sync)
- `start_height = 840000`: Start from recent height (e.g., 2024 blocks)

**Remember:** Wallets created before your start height won't have full history.

## Part 1: Firewall Configuration

Secure your server before installing anything.

### Basic UFW setup

```bash
sudo apt install ufw

# Set default policies
sudo ufw default deny incoming
sudo ufw default allow outgoing

# Allow SSH (adjust port if needed)
sudo ufw allow 22/tcp

# Allow Electrum server (only if NOT using Tor)
# sudo ufw allow 50001/tcp

# Enable firewall
sudo ufw enable
sudo ufw status
```

**Note:** If using Tor hidden service (recommended), don't open port 50001 - Tor handles routing.

## Part 2: Install Tor

### 1. Install Tor

```bash
sudo apt update
sudo apt install tor
```

### 2. Configure Tor for Bitcoin Core

Edit the Tor configuration:

```bash
sudo nano /etc/tor/torrc
```

Add these lines at the end:

```
# Enable control port for Bitcoin Core
ControlPort 9051
CookieAuthentication 1
CookieAuthFileGroupReadable 1
```

Save and exit (Ctrl+X, Y, Enter).

### 3. Start Tor

```bash
sudo systemctl enable tor@default
sudo systemctl start tor@default
```

### 4. Verify Tor is running

```bash
sudo systemctl status tor@default
```

You should see "active (running)" and "Bootstrapped 100% (done)".

## Part 3: Install Bitcoin Core

### 1. Download Bitcoin Core

```bash
cd /tmp
wget https://bitcoincore.org/bin/bitcoin-core-28.1/bitcoin-28.1-x86_64-linux-gnu.tar.gz
wget https://bitcoincore.org/bin/bitcoin-core-28.1/SHA256SUMS
wget https://bitcoincore.org/bin/bitcoin-core-28.1/SHA256SUMS.asc
```

### 2. Verify signatures (strongly recommended for mainnet)

```bash
# Import Bitcoin Core signing keys
wget https://bitcoincore.org/keys/keys.txt
gpg --import keys.txt

# Verify the signatures
gpg --verify SHA256SUMS.asc

# Verify checksum
sha256sum --ignore-missing --check SHA256SUMS
```

### 3. Extract and install

```bash
tar -xzf bitcoin-28.1-x86_64-linux-gnu.tar.gz
sudo install -m 0755 -o root -g root -t /usr/local/bin bitcoin-28.1/bin/*
```

### 4. Verify installation

```bash
bitcoind --version
```

### 5. Configure Bitcoin Core for Mainnet

Create Bitcoin data directory and config:

```bash
mkdir -p ~/.bitcoin
nano ~/.bitcoin/bitcoin.conf
```

**Production configuration:**

```ini
# Main settings
server=1
prune=10000  # Adjust based on available disk space
dbcache=2048  # Adjust based on available RAM (in MB)
maxmempool=300
disablewallet=1

# Tor configuration (optional, recommended for privacy)
proxy=127.0.0.1:9050
listen=1

# Mainnet (default, but explicit is better)
[main]
bind=127.0.0.1
rpcuser=electrumgo
rpcpassword=CHANGE_TO_STRONG_PASSWORD  # Use a long, random password
rpcbind=127.0.0.1
rpcallowip=127.0.0.1

# ZMQ for real-time notifications
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333
```

**Security notes:**
- Generate a strong password: `openssl rand -base64 32`
- Never expose RPC to the internet (`rpcbind=127.0.0.1`)
- Keep your config file secure: `chmod 600 ~/.bitcoin/bitcoin.conf`

Save and exit.

### 6. Create systemd service

```bash
sudo nano /etc/systemd/system/bitcoind.service
```

```ini
[Unit]
Description=Bitcoin Core Mainnet
After=network.target

[Service]
Type=simple
User=ripsline
ExecStart=/usr/local/bin/bitcoind -conf=/home/ripsline/.bitcoin/bitcoin.conf
Restart=always
RestartSec=30
TimeoutStopSec=600

[Install]
WantedBy=multi-user.target
```

**Replace `ripsline` with your actual username in both the `User` field and the config path.**

### 7. Start Bitcoin Core

```bash
sudo systemctl enable bitcoind
sudo systemctl start bitcoind
```

### 8. Monitor sync (this takes days for mainnet)

```bash
bitcoin-cli getblockchaininfo
```

Look for:
- `"chain": "testnet4"` - Confirms you're on testnet4
- `"blocks"` - Current block count
- `"initialblockdownload": true` - Still syncing
- `"verificationprogress"` - Percentage complete

**Expected sync time:**
- Fast SSD + good connection: 2-3 days
- Slower hardware: 5-7 days
- With Tor proxy: Add 1-2 days

## Part 4: Install electrum-go

### 1. Install dependencies

```bash
sudo apt update
sudo apt install -y git build-essential pkg-config libzmq3-dev
```

### 2. Install Go

```bash
cd /tmp
wget https://go.dev/dl/go1.25.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.25.6.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
go version
```

### 3. Clone and build

```bash
cd ~
git clone https://github.com/ripsline/electrum-go.git
cd electrum-go
go mod tidy
go build ./cmd/server
```

### 4. Configure electrum-go

```bash
cp config.template.toml config.toml
nano config.toml
```

**Production configuration:**

```toml
[server]
listen = "127.0.0.1:50001"
max_connections = 100
request_timeout_seconds = 300

[bitcoin]
rpc_host = "127.0.0.1:8332"  # Mainnet RPC port
rpc_user = "electrumgo"
rpc_pass = "SAME_PASSWORD_AS_BITCOIN_CONF"

zmq_block_addr = "tcp://127.0.0.1:28332"
zmq_tx_addr = "tcp://127.0.0.1:28333"

[storage]
db_path = "./data/index.db"
max_reorg_depth = 144

[indexer]
# Start from current tip (recommended)
start_height = -1

# OR start from specific height
# start_height = 840000

checkpoint_interval = 100
undo_prune_interval = 1000

[logging]
level = "info"
log_requests = false
```

Save and exit.

### 5. Create systemd service

```bash
sudo nano /etc/systemd/system/electrum-go.service
```

```ini
[Unit]
Description=Electrum Go Server
After=bitcoind.service
Requires=bitcoind.service

[Service]
Type=simple
User=ripsline
WorkingDirectory=/home/ripsline/electrum-go
ExecStart=/home/ripsline/electrum-go/server -config /home/ripsline/electrum-go/config.toml
Restart=always
RestartSec=30

[Install]
WantedBy=multi-user.target
```

**Replace `ripsline` with your actual username in both the `User` field and all paths.**

### 6. Start electrum-go

**Wait until Bitcoin Core is fully synced!**

```bash
bitcoin-cli getblockchaininfo | grep initialblockdownload
```

Should return `"initialblockdownload": false`

Then start:

```bash
sudo systemctl enable electrum-go
sudo systemctl start electrum-go
sudo systemctl status electrum-go
```

Watch logs:

```bash
sudo journalctl -u electrum-go -f
```

## Part 5: Set Up Tor Hidden Service (Recommended)

### 1. Configure Tor hidden service

```bash
sudo nano /etc/tor/torrc
```

Add these lines at the end:

```
# Hidden service for Electrum server
HiddenServiceDir /var/lib/tor/electrum-hidden-service/
HiddenServicePort 50001 127.0.0.1:50001
```

### 2. Restart Tor

```bash
sudo systemctl restart tor@default
```

### 3. Get your onion address

```bash
sudo cat /var/lib/tor/electrum-hidden-service/hostname
```

This outputs your `.onion` address, e.g., `abc123def456ghi789.onion`

## Part 6: Connect Wallet

### 1. Download Sparrow

Download from: https://sparrowwallet.com/download/

### 2. Local connection

If on the same machine:
- **Sparrow → Settings → Server**: **Private Electrum**
- **URL**: `127.0.0.1:50001`
- **Use SSL**: Off
- **Test Connection**

### 3. Remote connection via Tor

From Sparrow wallet:
1. **Sparrow → Settings → Server**: Select **Private Electrum**
2. **URL**: `abc123def456ghi789.onion:50001` (your onion address)
3. **Test Connection**

Your connection is now fully private through Tor!

### 4. Create a new wallet

**Important:** Create a NEW wallet. Don't import existing wallets, as they won't have full history.

1. File → New Wallet
2. Follow the prompts to create a new wallet
3. **Write down your seed phrase and store it securely!**
4. Your wallet will now sync with your electrum-go server!

## Part 7: Monitoring and Maintenance

### Check service status

```bash
sudo systemctl status bitcoind
sudo systemctl status electrum-go
sudo systemctl status tor@default
```

### Monitor disk usage

```bash
df -h
du -sh ~/.bitcoin
du -sh ~/electrum-go/data
```

### View logs

```bash
# Bitcoin Core
sudo journalctl -u bitcoind -f

# Electrum-go
sudo journalctl -u electrum-go -f

# Tor
sudo journalctl -u tor@default -f
```

### Update electrum-go

```bash
cd ~/electrum-go
git pull origin main
go build ./cmd/server
sudo systemctl restart electrum-go
```

## Security Checklist

- [ ] Strong RPC password set
- [ ] bitcoin.conf has restrictive permissions (`chmod 600`)
- [ ] Firewall configured (UFW)
- [ ] Only necessary ports exposed
- [ ] Using Tor for privacy
- [ ] System updates automated
- [ ] SSH key authentication (disable password auth)
- [ ] fail2ban installed for SSH protection
- [ ] Wallet seed phrase backed up securely offline

## Performance Tuning

### For servers with more RAM

In `bitcoin.conf`:

```ini
dbcache=4096  # 4GB (adjust based on available RAM)
maxmempool=500
```

### For faster initial sync

Temporarily increase `dbcache` during initial block download, then reduce after sync completes.

## Troubleshooting

### Bitcoin Core sync is slow

- Check internet speed
- Increase `dbcache` if you have RAM available
- Consider temporarily disabling Tor proxy during IBD, then re-enable after sync

### electrum-go won't start

- Verify Bitcoin Core is fully synced
- Check RPC credentials match
- Verify ZMQ ports are correct
- Check logs: `sudo journalctl -u electrum-go -n 100`

### Out of disk space

- Reduce `prune=` value in bitcoin.conf
- Clean up old logs
- Expand disk or migrate to larger storage

## Additional Resources

- Bitcoin Core documentation: https://bitcoin.org/en/full-node
- Tor Project: https://www.torproject.org/
- Sparrow Wallet: https://sparrowwallet.com/

## Support

For issues specific to electrum-go, please open an issue on GitHub: https://github.com/ripsline/electrum-go/issues