#!/usr/bin/env bash
# Generate all keys and config files for the ARFL Docker testnet.
# Run once before `docker compose up`.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
DATA="$DIR/data"

echo "=== ARFL Testnet Init ==="

# Clean previous state.
rm -rf "$DATA"
mkdir -p "$DATA/hub/keys" "$DATA/entry" "$DATA/exit"

# --- WireGuard keys ---
gen_wg_key() {
    priv=$(wg genkey)
    pub=$(echo "$priv" | wg pubkey)
    echo "$priv $pub"
}

echo "[1/4] Generating WireGuard keys..."
read -r ENTRY_WG_PRIV ENTRY_WG_PUB <<< "$(gen_wg_key)"
read -r EXIT_WG_PRIV EXIT_WG_PUB <<< "$(gen_wg_key)"
read -r CLIENT_WG_PRIV CLIENT_WG_PUB <<< "$(gen_wg_key)"

# --- Nostr keys (32 random hex bytes) ---
echo "[2/4] Generating Nostr keys..."
gen_hex32() { openssl rand -hex 32; }

HUB_NOSTR_PRIV=$(gen_hex32)
ENTRY_NOSTR_PRIV=$(gen_hex32)
EXIT_NOSTR_PRIV=$(gen_hex32)

# --- Credential key ---
echo "[3/4] Generating credential key..."
CRED_KEY=$(openssl rand -hex 32)

# --- Write config files ---
echo "[4/4] Writing config files..."

cat > "$DATA/hub/hub.json" <<EOF
{
  "nostr_privkey": "$HUB_NOSTR_PRIV",
  "listen_addr": "0.0.0.0:8080",
  "relays": ["ws://relay:7777"],
  "db_path": "/data/arfl.db",
  "credential_key": "$CRED_KEY",
  "settlement_hours": 1,
  "min_payout_sats": 100,
  "blind_key_dir": "/data/keys"
}
EOF

# Derive hub Nostr pubkey (first 64 hex chars of the privkey's secp256k1 pubkey).
# For testnet we use a placeholder — the hub binary derives it at startup.
cat > "$DATA/entry/node.json" <<EOF
{
  "role": "entry",
  "listen_port": 51820,
  "private_key": "$ENTRY_WG_PRIV",
  "tunnel_ip": "10.100.0.1/24",
  "interface": "wg-entry",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "connect_addr": "0.0.0.0:9091",
  "mtu": 1280,
  "nostr_privkey": "$ENTRY_NOSTR_PRIV",
  "relays": ["ws://relay:7777"],
  "endpoint": "entry-node:51820",
  "upload_mbps": 100,
  "download_mbps": 100,
  "capacity": 50,
  "hub_url": "http://hub:8080"
}
EOF

cat > "$DATA/exit/node.json" <<EOF
{
  "role": "exit",
  "listen_port": 51821,
  "private_key": "$EXIT_WG_PRIV",
  "tunnel_ip": "10.200.0.1/24",
  "interface": "wg-exit",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "connect_addr": "0.0.0.0:9091",
  "mtu": 1280,
  "nostr_privkey": "$EXIT_NOSTR_PRIV",
  "relays": ["ws://relay:7777"],
  "endpoint": "exit-node:51821",
  "upload_mbps": 100,
  "download_mbps": 100,
  "capacity": 50,
  "hub_url": "http://hub:8080"
}
EOF

# Write client session file (static for testnet — in production, hub generates this).
cat > "$DATA/session.json" <<EOF
{
  "entry_endpoint": "entry-node:51820",
  "entry_wg_pubkey": "$ENTRY_WG_PUB",
  "entry_connect_url": "http://entry-node:9091",
  "exit_endpoint": "exit-node:51821",
  "exit_wg_pubkey": "$EXIT_WG_PUB",
  "exit_connect_url": "http://exit-node:9091",
  "outer_tunnel_ip": "10.100.0.2/24",
  "inner_tunnel_ip": "10.200.0.2/24"
}
EOF

# Save client WG key for manual testing.
echo "$CLIENT_WG_PRIV" > "$DATA/client_wg.key"
chmod 600 "$DATA/client_wg.key"

echo ""
echo "=== Testnet initialized ==="
echo "  Hub config:    $DATA/hub/hub.json"
echo "  Entry config:  $DATA/entry/node.json"
echo "  Exit config:   $DATA/exit/node.json"
echo "  Client session: $DATA/session.json"
echo ""
echo "  Entry WG pub:  $ENTRY_WG_PUB"
echo "  Exit WG pub:   $EXIT_WG_PUB"
echo "  Client WG pub: $CLIENT_WG_PUB"
echo ""
echo "Run:  cd deploy && docker compose up"
