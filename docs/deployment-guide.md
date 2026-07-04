# ARFL Deployment Guide

Step-by-step guide to deploy a full ARFL network: hub, entry node, exit node, and Lightning payments.

---

## Prerequisites

- 3 × Linux VPS (Ubuntu 22.04+, 1GB+ RAM each)
- Domain name with DNS access
- Lightning node with REST API (self-hosted LND or [Voltage](https://voltage.cloud))
- SSH access to all servers

### Server Roles

| Server | Role | Ports to Open |
|--------|------|---------------|
| Hub | API server, payment processing | 8080/tcp, 80/tcp, 443/tcp |
| Entry Node | First WireGuard hop | 51820/udp, 9091/tcp |
| Exit Node | Second WireGuard hop, NAT to internet | 51821/udp, 9091/tcp |

---

## 1. Install Dependencies (all servers)

Run on each server:

```bash
apt update && apt install -y wireguard wireguard-tools nftables curl

# Install Go
curl -sL https://go.dev/dl/go1.24.5.linux-amd64.tar.gz | tar -C /usr/local -xz
echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version
```

## 2. Build ARFL (all servers)

```bash
mkdir -p /opt/arfl/src
cd /opt/arfl/src

# Clone the repo (or upload source)
git clone https://github.com/Radi-Labs/ARFL.git .

# Build both binaries
go build -o /usr/local/bin/arfl-hub ./cmd/arfl-hub/
go build -o /usr/local/bin/arfl-node ./cmd/arfl-node/
```

## 3. Set Up Lightning (Voltage or self-hosted)

### Option A: Voltage (recommended for demos)

1. Create an account at [voltage.cloud](https://voltage.cloud)
2. Select **Infrastructure** → create a new **LND node** on **mainnet**
3. Wait for the node to sync (usually 5-10 minutes)
4. Download from the Voltage dashboard:
   - `tls.cert` — TLS certificate
   - `admin.macaroon` — authentication credential
5. Note your REST API endpoint (e.g. `your-node.m.voltageapp.io:8080`)
6. Purchase inbound liquidity via Voltage's LSP (in the dashboard)

### Option B: Self-hosted LND

Requires 4GB+ RAM. Install `bitcoind` (pruned) + LND. Enable the REST API.
You'll need to acquire inbound liquidity via LNBIG, Magma, or Lightning Loop.

### Upload credentials to hub

```bash
# On hub server
mkdir -p /opt/arfl/creds
chmod 700 /opt/arfl/creds

# From your local machine
scp tls.cert   root@<hub-ip>:/opt/arfl/creds/lnd-tls.cert
scp admin.macaroon root@<hub-ip>:/opt/arfl/creds/lnd-admin.macaroon

# Lock down permissions
ssh root@<hub-ip> "chmod 600 /opt/arfl/creds/*"
```

## 4. Generate Keys

### WireGuard keys (on each node)

```bash
# On entry node
wg genkey | tee /opt/arfl/data/wg-private.key | wg pubkey > /opt/arfl/data/wg-public.key
chmod 600 /opt/arfl/data/wg-private.key

# On exit node (same commands)
wg genkey | tee /opt/arfl/data/wg-private.key | wg pubkey > /opt/arfl/data/wg-public.key
chmod 600 /opt/arfl/data/wg-private.key
```

### Nostr keys (on each node and hub)

```bash
# Generate a random 32-byte hex key for each
openssl rand -hex 32 > /opt/arfl/data/nostr-privkey.txt
chmod 600 /opt/arfl/data/nostr-privkey.txt
```

### Credential secret (hub only)

```bash
openssl rand -hex 32 > /opt/arfl/data/credential-key.txt
chmod 600 /opt/arfl/data/credential-key.txt
```

## 5. Configure the Hub

Create `/opt/arfl/data/hub.json`:

```json
{
  "nostr_privkey": "<contents of nostr-privkey.txt>",
  "listen_addr": "0.0.0.0:8080",
  "relays": ["wss://relay.damus.io", "wss://nos.lol"],
  "db_path": "/opt/arfl/data/arfl.db",
  "credential_key": "<contents of credential-key.txt>",
  "blind_key_dir": "/opt/arfl/data/keys/",
  "settlement_hours": 6,
  "min_payout_sats": 1000,
  "lnd_host": "<your-voltage-endpoint>",
  "lnd_port": 8080,
  "lnd_tls_cert_path": "/opt/arfl/creds/lnd-tls.cert",
  "lnd_macaroon_path": "/opt/arfl/creds/lnd-admin.macaroon",
  "lnd_fee_limit_sat": 100
}
```

Alternatively, use environment variables for sensitive fields:

```bash
export ARFL_CREDENTIAL_KEY="<hex secret>"
export ARFL_NOSTR_PRIVKEY="<hex key>"
export ARFL_LND_HOST="your-node.m.voltageapp.io"
export ARFL_LND_MACAROON_PATH="/opt/arfl/creds/lnd-admin.macaroon"
```

Create the keys directory:

```bash
mkdir -p /opt/arfl/data/keys
```

## 6. Start the Hub

```bash
# Test run (foreground)
arfl-hub --config /opt/arfl/data/hub.json

# You should see:
# [hub] listening on 0.0.0.0:8080
# [hub] LND connected to <endpoint>
# [hub] denomination key generated: key-100mb
```

Verify it works:

```bash
curl http://localhost:8080/health
# {"status":"ok","nodes":{"online":0,"total":0}}
```

## 7. Configure the Entry Node

Create `/opt/arfl/data/node.json` on the entry node:

```json
{
  "role": "entry",
  "listen_port": 51820,
  "private_key": "<contents of wg-private.key>",
  "tunnel_ip": "10.100.0.1/24",
  "interface": "wg-entry",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "connect_addr": "0.0.0.0:9091",
  "endpoint": "<entry-public-ip>:51820",
  "hub_url": "http://<hub-ip>:8080",
  "hub_pubkey_file": "/opt/arfl/data/keys/key-100mb.pub.json",
  "nostr_privkey": "<contents of nostr-privkey.txt>",
  "relays": ["wss://relay.damus.io", "wss://nos.lol"],
  "upload_mbps": 1000,
  "download_mbps": 1000,
  "capacity": 100
}
```

**Note:** Replace `eth0` with your actual network interface name. Check with `ip link show`.

### Copy the hub's blind signature public key

The hub auto-generates `/opt/arfl/data/keys/key-100mb.pub.json` on first run.
Copy it to both nodes:

```bash
# From hub
scp root@<hub-ip>:/opt/arfl/data/keys/key-100mb.pub.json /tmp/

# To entry node
ssh root@<entry-ip> "mkdir -p /opt/arfl/data/keys"
scp /tmp/key-100mb.pub.json root@<entry-ip>:/opt/arfl/data/keys/

# To exit node
ssh root@<exit-ip> "mkdir -p /opt/arfl/data/keys"
scp /tmp/key-100mb.pub.json root@<exit-ip>:/opt/arfl/data/keys/
```

## 8. Configure the Exit Node

Create `/opt/arfl/data/node.json` on the exit node:

```json
{
  "role": "exit",
  "listen_port": 51821,
  "private_key": "<contents of wg-private.key>",
  "tunnel_ip": "10.200.0.1/24",
  "interface": "wg-exit",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "connect_addr": "0.0.0.0:9091",
  "endpoint": "<exit-public-ip>:51821",
  "hub_url": "http://<hub-ip>:8080",
  "hub_pubkey_file": "/opt/arfl/data/keys/key-100mb.pub.json",
  "nostr_privkey": "<contents of nostr-privkey.txt>",
  "relays": ["wss://relay.damus.io", "wss://nos.lol"],
  "upload_mbps": 1000,
  "download_mbps": 1000,
  "capacity": 100
}
```

### Enable IP forwarding (exit node only)

The exit node must forward traffic to the internet:

```bash
sysctl -w net.ipv4.ip_forward=1
echo "net.ipv4.ip_forward=1" >> /etc/sysctl.d/99-arfl.conf
```

## 9. Generate Attestations

The hub must vouch for each node before they can join the network.
This prevents Sybil attacks — only hub-attested nodes are accepted.

Use the built-in `attest` subcommand on the hub:

```bash
arfl-hub attest --config /opt/arfl/data/hub.json \
  --node-pubkey <node_nostr_pubkey_hex> \
  --node-wg-key <node_wireguard_pubkey_base64> \
  --operator "my-org" \
  --role entry \
  --out /tmp/entry-attestation.json
```

To get the values you need:

```bash
# Node's Nostr pubkey (from its startup log)
journalctl -u arfl-node | grep "Nostr pubkey"

# Node's WireGuard pubkey
wg show wg-entry public-key   # or wg-exit
```

Run this once for each node (changing `--role` to `entry` or `exit`).
Use `--role both` if a node serves both roles.

Then inject the attestation into the node's config:

```bash
# Copy attestation to the node
scp /tmp/entry-attestation.json root@<entry-ip>:/opt/arfl/data/

# Inject into config
ssh root@<entry-ip> 'python3 -c "
import json
cfg = json.load(open(\"/opt/arfl/data/node.json\"))
cfg[\"attestation\"] = open(\"/opt/arfl/data/entry-attestation.json\").read().strip()
json.dump(cfg, open(\"/opt/arfl/data/node.json\", \"w\"), indent=2)
"'
```

Repeat for both entry and exit nodes. Attestations expire after 30 days —
regenerate with the same command when needed.

## 10. Start the Nodes

```bash
# Test run (foreground)
arfl-node --config /opt/arfl/data/node.json

# You should see:
# [node] WireGuard interface wg-entry created
# [node] loaded hub public key: key-100mb
# [admin] token-gated /connect enabled
# [announcer] published to 2 relay(s) | role=entry
```

Check the hub sees the nodes:

```bash
curl http://<hub-ip>:8080/health
# {"status":"ok","nodes":{"online":2,"total":2}}
```

## 11. Set Up systemd Services

### Hub service (`/etc/systemd/system/arfl-hub.service`)

```ini
[Unit]
Description=ARFL Hub
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/arfl-hub --config /opt/arfl/data/hub.json
WorkingDirectory=/opt/arfl/data
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

### Node service (`/etc/systemd/system/arfl-node.service`)

```ini
[Unit]
Description=ARFL Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/arfl-node --config /opt/arfl/data/node.json
WorkingDirectory=/opt/arfl/data
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
systemctl daemon-reload
systemctl enable arfl-hub   # or arfl-node
systemctl start arfl-hub    # or arfl-node
journalctl -u arfl-hub -f   # watch logs
```

## 12. Open Firewall Ports

```bash
# Hub
ufw allow 8080/tcp    # API
ufw allow 80/tcp      # HTTP (for HTTPS redirect)
ufw allow 443/tcp     # HTTPS

# Entry node
ufw allow 51820/udp   # WireGuard
ufw allow 9091/tcp    # /connect API

# Exit node
ufw allow 51821/udp   # WireGuard
ufw allow 9091/tcp    # /connect API
```

## 13. DNS Setup (optional but recommended)

Point A records to your servers:

| Record | Type | Value |
|--------|------|-------|
| `hub.yourdomain.com` | A | Hub IP |
| `entry.yourdomain.com` | A | Entry node IP |
| `exit.yourdomain.com` | A | Exit node IP |

## 14. Verify the Full Flow

### Purchase bandwidth

```bash
curl -X POST http://<hub-ip>:8080/purchase \
  -H "Content-Type: application/json" \
  -d '{"tier_id":"1gb"}'
```

You'll get a response with a BOLT11 Lightning invoice:

```json
{
  "payment_hash": "abc123...",
  "payment_request": "lnbc5u1p...",
  "amount_sats": 500,
  "tier": "1gb"
}
```

### Pay the invoice

Scan the `payment_request` with any Lightning wallet (Wallet of Satoshi,
Phoenix, Muun, etc.) and pay it.

### Check for tokens

After payment settles (a few seconds), fetch your blind tokens:

```bash
curl http://<hub-ip>:8080/tokens?ticket_id=<ticket_id>
```

### Connect to a node

Present a token to the entry node:

```bash
# Generate a WireGuard keypair
WG_PRIV=$(wg genkey)
WG_PUB=$(echo $WG_PRIV | wg pubkey)

curl -X POST http://<entry-ip>:9091/connect \
  -H "Content-Type: application/json" \
  -d "{\"token\": <one token from above>, \"public_key\": \"$WG_PUB\"}"
```

The response contains your WireGuard peer config (endpoint, allowed IPs,
server public key). Create a `.conf` file and import it into WireGuard.

---

## Troubleshooting

### Hub says "nodes: 0"
- Check node logs: `journalctl -u arfl-node -f`
- Verify attestation is in the node config
- Ensure Nostr relays are reachable from the node

### Purchase returns error
- Check hub can reach LND: `curl --cacert tls.cert https://<lnd-host>:8080/v1/getinfo -H "Grpc-Metadata-macaroon: $(xxd -p admin.macaroon | tr -d '\n')"`
- Verify macaroon permissions (needs invoice + router)

### Payment not settling
- LND node must have inbound liquidity
- Check LND channel status in Voltage dashboard or `lncli listchannels`

### WireGuard tunnel not working
- Verify IP forwarding: `sysctl net.ipv4.ip_forward`
- Check nftables rules: `nft list ruleset`
- Ensure the `out_interface` in node config matches your actual interface (`ip link show`)

### Node can't announce
- Check Nostr relay connectivity
- Verify `nostr_privkey` is valid 64-char hex
- Ensure attestation hasn't expired (TTL is 30 days — regenerate if needed)
