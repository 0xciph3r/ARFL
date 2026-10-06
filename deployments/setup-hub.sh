#!/usr/bin/env bash
# ARFL Hub Server Setup
# Run this on a fresh Ubuntu server to bootstrap an ARFL hub host.
#
# Usage:
#   sudo bash deployments/setup-hub.sh
#
# Environment overrides (optional):
#   ARFL_LND_HOST, ARFL_LND_PORT, ARFL_LND_TLS_CERT_PATH, ARFL_LND_MACAROON_PATH
#   ARFL_RELAYS, ARFL_LISTEN_ADDR, ARFL_DB_PATH

set -euo pipefail

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "This script must be run as root (use sudo)."
  exit 1
fi

echo "=== ARFL Hub Server Setup ==="
echo ""

echo "[1/6] Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq

echo "[2/6] Installing dependencies..."
apt-get install -y -qq \
  ca-certificates \
  curl \
  git \
  build-essential \
  jq

echo "[3/6] Installing Go..."
GO_VERSION="1.26.3"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
if [[ ! -x /usr/local/go/bin/go ]] && ! command -v go >/dev/null 2>&1; then
  GO_SHA="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | jq -r --arg f "$GO_TARBALL" '.[] | .files[]? | select(.filename==$f) | .sha256' | head -n 1)"
  if [[ -z "$GO_SHA" || "$GO_SHA" == "null" ]]; then
    echo "Could not resolve checksum for $GO_TARBALL"
    exit 1
  fi
  curl -fsSL "https://go.dev/dl/${GO_TARBALL}" -o "/tmp/${GO_TARBALL}"
  echo "${GO_SHA}  /tmp/${GO_TARBALL}" | sha256sum -c -
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
  rm -f "/tmp/${GO_TARBALL}"
  cat >/etc/profile.d/go.sh <<'EOF'
export PATH=$PATH:/usr/local/go/bin
EOF
  export PATH="$PATH:/usr/local/go/bin"
fi
if [[ -x /usr/local/go/bin/go ]]; then
  GO_BIN="/usr/local/go/bin/go"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
else
  echo "Go is required but was not found after installation."
  exit 1
fi
echo "  Go installed: $($GO_BIN version)"

echo "[4/6] Cloning/updating ARFL source..."
mkdir -p /opt/arfl/src
if [[ ! -d /opt/arfl/src/.git ]]; then
  git clone https://github.com/0xciph3r/ARFL.git /opt/arfl/src
fi
cd /opt/arfl/src
git fetch origin --prune
git checkout -q main
git reset --hard origin/main

echo "[5/6] Building binaries..."
"$GO_BIN" build -o /usr/local/bin/arfl-hub ./cmd/arfl-hub
"$GO_BIN" build -o /usr/local/bin/arfl ./cmd/arfl
chmod 755 /usr/local/bin/arfl-hub /usr/local/bin/arfl

echo "[6/6] Generating /opt/arfl/data/hub.json..."
mkdir -p /opt/arfl/data /opt/arfl/data/keys /opt/arfl/creds
chmod 700 /opt/arfl/creds
chmod 700 /opt/arfl/data/keys

LND_HOST="${ARFL_LND_HOST:-}"
LND_PORT="${ARFL_LND_PORT:-8080}"
LND_TLS_CERT_PATH="${ARFL_LND_TLS_CERT_PATH:-/opt/arfl/creds/lnd-tls.cert}"
LND_MACAROON_PATH="${ARFL_LND_MACAROON_PATH:-/opt/arfl/creds/lnd-admin.macaroon}"
RELAYS="${ARFL_RELAYS:-wss://relay.damus.io,wss://nos.lol}"
LISTEN_ADDR="${ARFL_LISTEN_ADDR:-0.0.0.0:8080}"
DB_PATH="${ARFL_DB_PATH:-/opt/arfl/data/arfl.db}"

if [[ -f /opt/arfl/data/hub.json && "${ARFL_REGENERATE_HUB_CONFIG:-0}" != "1" ]]; then
  echo "  Existing /opt/arfl/data/hub.json detected; keeping current hub identity and keys."
  echo "  Set ARFL_REGENERATE_HUB_CONFIG=1 only if you intentionally want new hub secrets."
else
  if [[ -z "$LND_HOST" ]]; then
    read -r -p "LND host (example: your-node.m.voltageapp.io): " LND_HOST
  fi
  if [[ -z "$LND_HOST" ]]; then
    echo "LND host is required."
    exit 1
  fi

  INIT_ARGS=()
  if [[ "${ARFL_REGENERATE_HUB_CONFIG:-0}" == "1" ]]; then
    INIT_ARGS+=(--force)
  fi

  /usr/local/bin/arfl init hub \
    --non-interactive \
    "${INIT_ARGS[@]}" \
    --output /opt/arfl/data/hub.json \
    --listen-addr "$LISTEN_ADDR" \
    --relays "$RELAYS" \
    --db-path "$DB_PATH" \
    --blind-key-dir /opt/arfl/data/keys \
    --lnd-host "$LND_HOST" \
    --lnd-port "$LND_PORT" \
    --lnd-tls-cert-path "$LND_TLS_CERT_PATH" \
    --lnd-macaroon-path "$LND_MACAROON_PATH"

  chmod 600 /opt/arfl/data/hub.json
fi

echo ""
echo "=== Setup complete ==="
echo ""
echo "Next steps:"
echo "  1. Copy LND credentials onto this server:"
echo "     - $LND_TLS_CERT_PATH"
echo "     - $LND_MACAROON_PATH"
echo "  2. Start the hub:"
echo "     arfl-hub --config /opt/arfl/data/hub.json"
echo "  3. Validate health and config:"
echo "     arfl doctor hub --config /opt/arfl/data/hub.json --url http://127.0.0.1:8080"
