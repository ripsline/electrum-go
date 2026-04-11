# Deployment

This guide covers installing electrum-go as a systemd service running
alongside an existing Bitcoin Core node, using RPC cookie authentication.

The setup assumes:

- Bitcoin Core already running as system user `bitcoin`
- bitcoind datadir at `/var/lib/bitcoin`
- RPC on `127.0.0.1:8332` (mainnet) or `:48332` (testnet4)
- ZMQ rawblock/rawtx enabled in `bitcoin.conf`
- electrum-go will run as the same `bitcoin` user so it can read the
  RPC cookie file without extra group membership

If your setup differs, adjust paths accordingly.

## 1. Build the binary

On a build host (can be the same machine or a workstation with the same
architecture):

```sh
cd electrum-go
go build -o electrum-go ./cmd/server
```

Copy the binary to the target machine and install it:

```sh
sudo install -o root -g root -m 0755 electrum-go /usr/local/bin/electrum-go
```

## 2. Create the data directory

electrum-go's Pebble database lives here. The `bitcoin` user must own it.

```sh
sudo install -d -o bitcoin -g bitcoin -m 0750 /var/lib/electrum-go
```

## 3. Configure

Create `/etc/electrum-go/` and drop in a config file. The config references
bitcoind's cookie file for auth — no passwords in the config.

```sh
sudo install -d -o root -g bitcoin -m 0750 /etc/electrum-go
sudo cp config.example.toml /etc/electrum-go/config.toml
sudo chown root:bitcoin /etc/electrum-go/config.toml
sudo chmod 0640 /etc/electrum-go/config.toml
```

Edit `/etc/electrum-go/config.toml` to match your node. The important bits:

```toml
[bitcoin]
rpc_host        = "127.0.0.1:8332"                # mainnet; 48332 for testnet4
rpc_cookie_path = "/var/lib/bitcoin/.cookie"      # mainnet default
zmq_block_addr  = "tcp://127.0.0.1:28332"
zmq_tx_addr     = "tcp://127.0.0.1:28333"

[storage]
db_path = "/var/lib/electrum-go/index.db"
```

For testnet4, the cookie lives at `/var/lib/bitcoin/testnet4/.cookie`.

Verify the `bitcoin` user can read the cookie file:

```sh
sudo -u bitcoin test -r /var/lib/bitcoin/.cookie && echo ok
```

If that prints `ok`, you're set. If not, check that bitcoind is running and
that its datadir permissions are intact.

## 4. Install the systemd unit

```sh
sudo cp contrib/systemd/electrum-go.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now electrum-go
```

Check status and logs:

```sh
systemctl status electrum-go
journalctl -u electrum-go -f
```

## 5. Verify

From the same machine:

```sh
echo '{"id":0,"method":"server.version","params":["test","1.4"]}' \
  | nc -q1 127.0.0.1 50001
```

You should see a JSON response with the server version.

## Cookie rotation caveat

Bitcoin Core rewrites `.cookie` on every startup. electrum-go reads the cookie
once at startup, so if you restart bitcoind, you should also restart electrum-go:

```sh
sudo systemctl restart electrum-go
```

A watchdog / auto-reconnect loop is a planned improvement.

## Exposing to a network

The default `listen = "127.0.0.1:50001"` is local-only. If you need clients
on other machines, put electrum-go behind a TLS-terminating reverse proxy
(nginx, Caddy). electrum-go itself does not terminate TLS.
