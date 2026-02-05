# Testnet4 Complete Setup Guide

This guide walks you through setting up a complete testnet4 environment from scratch, including Tor, Bitcoin Core, and electrum-go.

**Why Testnet4?** Test your setup without risking real bitcoin. Testnet4 is much smaller and syncs in hours instead of days.

## Prerequisites

- Fresh Ubuntu 24.04+ server (or similar Linux distribution)
- At least 4GB RAM
- At least 50GB disk space
- sudo access

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

Tor provides privacy by routing Bitcoin network traffic through the Tor network and allows you to expose your Electrum server as a hidden service.

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
sudo systemctl enable tor
sudo systemctl start tor
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
```

### 2. Verify checksums (optional but recommended)

```bash
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

### 5. Configure Bitcoin Core for Testnet4

Create Bitcoin data directory and config:

```bash
mkdir -p ~/.bitcoin
nano ~/.bitcoin/bitcoin.conf
```

Add this configuration:

```ini
# Main settings (apply to all networks)
server=1
prune=10000
dbcache=512
maxmempool=300
disablewallet=1

# Tor configuration (optional, recommended for privacy)
proxy=127.0.0.1:9050
listen=1

# Testnet4
testnet4=1

[testnet4]
bind=127.0.0.1
rpcuser=electrumgo
rpcpassword=CHANGE_THIS_PASSWORD
rpcbind=127.0.0.1
rpcallowip=127.0.0.1

# ZMQ for real-time notifications (required by electrum-go)
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28333
```

**Important:** Change `CHANGE_THIS_PASSWORD` to a strong password!

**Security notes:**
- Generate a strong password: `openssl rand -base64 32`
- Keep your config file secure: `chmod 600 ~/.bitcoin/bitcoin.conf`

Save and exit.

### 6. Create systemd service

```bash
sudo nano /etc/systemd/system/bitcoind.service
```

```ini
[Unit]
Description=Bitcoin Core Testnet4
After=network.target

[Service]
Type=simple
User=ripsline
ExecStart=/usr/local/bin/bitcoind -testnet4 -conf=/home/ripsline/.bitcoin/bitcoin.conf
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
sudo systemctl status bitcoind
```

### 8. Monitor sync progress

```bash
bitcoin-cli -testnet4 getblockchaininfo
```

Look for:
- `"chain": "testnet4"` - Confirms you're on testnet4
- `"blocks"` - Current block count
- `"initialblockdownload": true` - Still syncing
- `"verificationprogress"` - Percentage complete

**Note:** Initial sync takes a few hours. Wait until `"initialblockdownload": false` before proceeding.

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

### 3. Clone and build electrum-go

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

Update these settings:

```toml
[server]
listen = "127.0.0.1:50001"
max_connections = 100
request_timeout_seconds = 1200

[bitcoin]
rpc_host = "127.0.0.1:48332"
rpc_user = "electrumgo"
rpc_pass = "CHANGE_THIS_PASSWORD"  # Must match bitcoin.conf
zmq_block_addr = "tcp://127.0.0.1:28332"
zmq_tx_addr = "tcp://127.0.0.1:28333"

[storage]
db_path = "./data/index.db"
max_reorg_depth = 144

[indexer]
# Start from current tip (recommended)
start_height = -1

# OR start from specific height
# start_height = 120000

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

**Important:** Ensure Bitcoin Core is fully synced first!

```bash
bitcoin-cli -testnet4 getblockchaininfo | grep initialblockdownload
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

You should see logs indicating the server is indexing blocks and listening on port 50001.

## Part 5: Set Up Tor Hidden Service (Optional)

To access your server remotely via Tor:

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

## Part 6: Connect with Sparrow Wallet

### 1. Download Sparrow

Download from: https://sparrowwallet.com/download/

### 2. Configure Sparrow for Testnet4

1. Open Sparrow
2. Go to **Tools → Restart In → Testnet4**
3. Go to **Sparrow → Settings → Server**
4. Set **Server Type**: **Private Electrum**
5. Set **URL**: `127.0.0.1:50001` (or your server IP if remote)
6. **Use SSL**: Off (for testing)
7. Click **Test Connection**

You should see a green checkmark!

### 3. Connect via Tor (if you set up hidden service)

In Sparrow:
1. **Sparrow → Settings → Server**: Select **Private Electrum**
2. **URL**: `abc123def456ghi789.onion:50001`
3. **Test Connection**

Your connection is now fully private through Tor!

### 4. Create a new wallet

**Important:** Create a NEW wallet. Don't import existing wallets, as they won't have full history.

1. File → New Wallet
2. Follow the prompts to create a new wallet
3. Your wallet will now sync with your electrum-go server!

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
du -sh ~/.bitcoin/testnet4
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

## Next Steps

- Test sending/receiving testnet bitcoin
- Experiment with different start heights
- Try mainnet deployment (see [MAINNET.md](MAINNET.md))

## Getting Testnet4 Bitcoin

- Testnet4 Faucet: https://faucet.testnet4.dev/
- Ask in Bitcoin development communities

## Support

For issues specific to electrum-go, please open an issue on GitHub: https://github.com/ripsline/electrum-go/issues