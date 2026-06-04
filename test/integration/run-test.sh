#!/usr/bin/env bash
# ARFL Integration Test
# Run this on the CLIENT machine after both nodes are running.
#
# Usage:
#   sudo bash test/integration/run-test.sh \
#     --entry-ip <ENTRY_PUBLIC_IP> \
#     --exit-ip <EXIT_PUBLIC_IP> \
#     --entry-wg-pubkey <ENTRY_WG_PUBKEY> \
#     --exit-wg-pubkey <EXIT_WG_PUBKEY>
#
# Prerequisites:
#   - Both nodes running (arfl-node on each server)
#   - Client peers added on both nodes via admin API
#   - WireGuard and curl installed on this machine

set -euo pipefail

# --- Parse arguments ---
ENTRY_IP=""
EXIT_IP=""
ENTRY_PUBKEY=""
EXIT_PUBKEY=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --entry-ip)     ENTRY_IP="$2"; shift 2 ;;
        --exit-ip)      EXIT_IP="$2"; shift 2 ;;
        --entry-wg-pubkey) ENTRY_PUBKEY="$2"; shift 2 ;;
        --exit-wg-pubkey)  EXIT_PUBKEY="$2"; shift 2 ;;
        *) echo "Unknown argument: $1"; exit 1 ;;
    esac
done

if [[ -z "$ENTRY_IP" || -z "$EXIT_IP" || -z "$ENTRY_PUBKEY" || -z "$EXIT_PUBKEY" ]]; then
    echo "Usage: $0 --entry-ip IP --exit-ip IP --entry-wg-pubkey KEY --exit-wg-pubkey KEY"
    exit 1
fi

PASS=0
FAIL=0

pass() { echo "  ✅ $1"; PASS=$((PASS + 1)); }
fail() { echo "  ❌ $1"; FAIL=$((FAIL + 1)); }

echo "=== ARFL Integration Test ==="
echo "Entry node: $ENTRY_IP"
echo "Exit node:  $EXIT_IP"
echo ""

# --- Step 1: Build and generate keypair ---
echo "[1/6] Building client and generating keypair..."
cd "$(dirname "$0")/../../"
go build -o arfl-client ./cmd/arfl-client

CLIENT_KEY=$(wg genkey)
CLIENT_PUBKEY=$(echo "$CLIENT_KEY" | wg pubkey)
echo "  Client public key: $CLIENT_PUBKEY"

# --- Step 2: Add client peer on entry node ---
echo "[2/6] Adding client peer on entry node..."
# This would normally be done by the hub. For testing, we call the admin API.
curl -sf "http://$ENTRY_IP:9090/peers" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "{
        \"public_key\": \"$CLIENT_PUBKEY\",
        \"allowed_ips\": [\"10.100.0.2/32\"],
        \"tunnel_ip\": \"10.100.0.2\",
        \"quota_bytes\": 268435456
    }" && pass "Client peer added on entry node" || fail "Failed to add peer on entry node"

# --- Step 3: Add client peer on exit node ---
echo "[3/6] Adding client peer on exit node..."
curl -sf "http://$EXIT_IP:9091/peers" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "{
        \"public_key\": \"$CLIENT_PUBKEY\",
        \"allowed_ips\": [\"10.200.0.2/32\"],
        \"tunnel_ip\": \"10.200.0.2\",
        \"quota_bytes\": 268435456
    }" && pass "Client peer added on exit node" || fail "Failed to add peer on exit node"

# --- Step 4: Create session config ---
echo "[4/6] Creating session config..."
cat > /tmp/arfl-session.json << EOF
{
    "entry_endpoint": "$ENTRY_IP:51820",
    "entry_wg_pubkey": "$ENTRY_PUBKEY",
    "exit_endpoint": "$EXIT_IP:51821",
    "exit_wg_pubkey": "$EXIT_PUBKEY",
    "outer_tunnel_ip": "10.100.0.2/24",
    "inner_tunnel_ip": "10.200.0.2/24"
}
EOF

# Save client key (plaintext for test, not the encrypted format)
cat > /tmp/arfl-client.key << EOF
{
    "private_key": "$CLIENT_KEY",
    "public_key": "$CLIENT_PUBKEY"
}
EOF
chmod 600 /tmp/arfl-client.key

# --- Step 5: Privacy verification (packet captures) ---
echo "[5/6] Running privacy verification..."
echo ""
echo "  === PRIVACY CHECKLIST ==="

# Test 1: Can we reach the entry node?
ping -c 1 -W 3 "$ENTRY_IP" > /dev/null 2>&1 \
    && pass "Entry node reachable" || fail "Entry node unreachable"

# Test 2: Can we reach the exit node?
ping -c 1 -W 3 "$EXIT_IP" > /dev/null 2>&1 \
    && pass "Exit node reachable" || fail "Exit node unreachable"

# Test 3: WireGuard handshake to entry node
# (This tests the outer tunnel — client can reach entry)
echo "  Note: Full tunnel test requires running arfl-client with the session."
echo "  Manual verification steps:"
echo ""
echo "  1. On this machine, run:"
echo "     sudo ./arfl-client --session /tmp/arfl-session.json --key /tmp/arfl-client.key"
echo ""
echo "  2. In another terminal, verify your public IP changed:"
echo "     curl -s https://ifconfig.me"
echo "     # Should show the EXIT node's IP ($EXIT_IP), not your real IP"
echo ""
echo "  3. On the ENTRY node, run tcpdump:"
echo "     sudo tcpdump -i wg-entry -n"
echo "     # Should show ONLY encrypted traffic to $EXIT_IP"
echo "     # Should NOT show destination websites"
echo ""
echo "  4. On the EXIT node, run tcpdump:"
echo "     sudo tcpdump -i wg-exit -n"
echo "     # Should show website traffic from 10.200.0.2"
echo "     # Should NOT show your real IP"
echo ""
echo "  5. DNS leak test:"
echo "     # While connected, visit: https://dnsleaktest.com"
echo "     # Should show Quad9 (9.9.9.9) only"
echo ""

# --- Step 6: Byte counter verification ---
echo "[6/6] Checking byte counters..."

# Query entry node stats
ENTRY_STATS=$(curl -sf "http://$ENTRY_IP:9090/peers" 2>/dev/null || echo "[]")
echo "  Entry node peers: $ENTRY_STATS"

# Query exit node stats
EXIT_STATS=$(curl -sf "http://$EXIT_IP:9091/peers" 2>/dev/null || echo "[]")
echo "  Exit node peers: $EXIT_STATS"

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [[ $FAIL -gt 0 ]]; then
    echo "Some tests failed. Check the output above."
    exit 1
fi

echo "Basic connectivity verified. Run the manual tunnel test above for full privacy verification."
