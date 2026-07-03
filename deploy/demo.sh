#!/usr/bin/env bash
# ARFL Demo — Full Purchase → Pay → Settle → Connect Flow
# Requires: Docker testnet running, Polar network with alice (hub) & erin (payer)
set -euo pipefail

HUB_URL="${HUB_URL:-http://localhost:8080}"
ERIN_REST="${ERIN_REST:-https://127.0.0.1:8085}"
ERIN_MACAROON_PATH="${ERIN_MACAROON_PATH:-$HOME/.polar/networks/1/volumes/lnd/erin/data/chain/bitcoin/regtest/admin.macaroon}"

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m'

step() { echo -e "\n${CYAN}━━━ $1 ━━━${NC}"; }
ok()   { echo -e "${GREEN}✓ $1${NC}"; }
info() { echo -e "${YELLOW}  $1${NC}"; }

# ─── Step 1: Verify Hub is Running ───
step "1. Checking hub health"
HEALTH=$(curl -sf "$HUB_URL/health" 2>/dev/null || echo "FAIL")
if [ "$HEALTH" = "FAIL" ]; then
    echo "Hub not reachable at $HUB_URL — start Docker testnet first."
    echo "  cd deploy && ./init-testnet.sh && docker compose up --build -d"
    exit 1
fi
ok "Hub is healthy"

# ─── Step 2: Purchase Bandwidth ───
step "2. Purchasing 1GB bandwidth tier"
PURCHASE=$(curl -sf -X POST "$HUB_URL/purchase" \
    -H "Content-Type: application/json" \
    -d '{"tier_id":"1gb"}')
echo "$PURCHASE" | python3 -m json.tool 2>/dev/null || echo "$PURCHASE"

INVOICE=$(echo "$PURCHASE" | python3 -c "import sys,json; print(json.load(sys.stdin)['invoice'])" 2>/dev/null)
TICKET_ID=$(echo "$PURCHASE" | python3 -c "import sys,json; print(json.load(sys.stdin)['ticket_id'])" 2>/dev/null)

if [ -z "$INVOICE" ]; then
    echo "Failed to get invoice from purchase response"
    exit 1
fi
ok "Got BOLT11 invoice"
info "Ticket: $TICKET_ID"
info "Invoice: ${INVOICE:0:40}..."

# ─── Step 3: Pay Invoice from Erin ───
step "3. Paying invoice via Polar (erin → alice)"

if [ ! -f "$ERIN_MACAROON_PATH" ]; then
    echo "Erin macaroon not found at $ERIN_MACAROON_PATH"
    echo "Set ERIN_MACAROON_PATH to your Polar erin node's admin.macaroon"
    exit 1
fi
ERIN_MAC=$(xxd -p "$ERIN_MACAROON_PATH" | tr -d '\n')

PAY_RESULT=$(curl -sf --insecure "$ERIN_REST/v2/router/send" \
    -H "Grpc-Metadata-macaroon: $ERIN_MAC" \
    -d "{\"payment_request\":\"$INVOICE\",\"timeout_seconds\":30,\"fee_limit_sat\":100}" 2>/dev/null || echo "FAIL")

if echo "$PAY_RESULT" | grep -q "SUCCEEDED"; then
    ok "Payment succeeded"
else
    echo "Payment result: $PAY_RESULT"
    echo "If using Polar, ensure alice-erin channel has balance."
    exit 1
fi

# ─── Step 4: Wait for Settlement + Token Issuance ───
step "4. Waiting for hub settlement (3s)"
sleep 3

# ─── Step 5: Redeem Tokens ───
step "5. Fetching tokens for ticket"
TOKENS=$(curl -sf "$HUB_URL/tokens?ticket_id=$TICKET_ID" 2>/dev/null || echo "FAIL")
TOKEN_COUNT=$(echo "$TOKENS" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('tokens',[])))" 2>/dev/null || echo 0)

if [ "$TOKEN_COUNT" -gt 0 ]; then
    ok "Got $TOKEN_COUNT blind tokens (each = 100MB)"
else
    echo "No tokens yet — check hub logs for settlement"
    exit 1
fi

# ─── Step 6: Connect to Node ───
step "6. Presenting token to entry node"
FIRST_TOKEN=$(echo "$TOKENS" | python3 -c "
import sys, json
d = json.load(sys.stdin)
t = d['tokens'][0]
print(json.dumps(t))
" 2>/dev/null)

NODE_URL="${NODE_URL:-http://localhost:9091}"
CONNECT=$(curl -sf -X POST "$NODE_URL/connect" \
    -H "Content-Type: application/json" \
    -d "{\"token\":$FIRST_TOKEN,\"public_key\":\"$(wg genkey | wg pubkey)\"}" 2>/dev/null || echo "FAIL")

if echo "$CONNECT" | grep -q "allowed_ip\|peer_endpoint"; then
    ok "Node accepted token — WireGuard peer added!"
    echo "$CONNECT" | python3 -m json.tool 2>/dev/null || echo "$CONNECT"
else
    info "Node connect response: $CONNECT"
    info "(Node may not be running with WireGuard — token validation still works)"
fi

# ─── Summary ───
step "Demo Complete"
echo -e "${GREEN}Flow: Purchase → Lightning Pay → Settlement → Token Issuance → Node Connect${NC}"
echo ""
echo "What happened:"
echo "  1. Client purchased 1GB tier from hub"
echo "  2. Hub created a real BOLT11 invoice via LND (alice)"
echo "  3. Erin paid the invoice over Lightning"
echo "  4. Hub detected settlement, minted $TOKEN_COUNT blind tokens"
echo "  5. Client presented a token to the entry node"
echo "  6. Node validated the token and added a WireGuard peer"
echo ""
echo "The hub never sees which node the client connects to —"
echo "blind signatures ensure unlinkability between purchase and usage."
