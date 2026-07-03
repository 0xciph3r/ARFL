#!/usr/bin/env bash
# Smoke test for the ARFL Docker testnet.
# Verifies all services are healthy after `docker compose up`.
set -euo pipefail

PASS=0
FAIL=0
HUB="http://localhost:8080"
ENTRY_CONNECT="http://localhost:9091"
EXIT_CONNECT="http://localhost:9092"

check() {
    local name="$1"
    local url="$2"
    local expect="$3"

    if resp=$(curl -sf --max-time 5 "$url" 2>&1); then
        if echo "$resp" | grep -q "$expect"; then
            echo "  ✓ $name"
            PASS=$((PASS + 1))
        else
            echo "  ✗ $name — unexpected response: $resp"
            FAIL=$((FAIL + 1))
        fi
    else
        echo "  ✗ $name — connection failed"
        FAIL=$((FAIL + 1))
    fi
}

echo "=== ARFL Testnet Smoke Test ==="
echo ""

# Wait for services to start.
echo "Waiting for services (10s)..."
sleep 10

echo ""
echo "[Hub]"
check "Hub health" "$HUB/health" "ok"
check "Hub node list" "$HUB/nodes" "nodes"

echo ""
echo "[Entry Node]"
check "Entry /health" "$ENTRY_CONNECT/health" "ok"

echo ""
echo "[Exit Node]"
check "Exit /health" "$EXIT_CONNECT/health" "ok"

echo ""
echo "[Purchase Flow]"
# Create a purchase and verify we get an invoice back.
PURCHASE=$(curl -sf --max-time 5 -X POST "$HUB/purchase" \
    -H "Content-Type: application/json" \
    -d '{"tier":"basic"}' 2>&1 || echo "FAILED")
if echo "$PURCHASE" | grep -q "payment_hash\|invoice\|payment_request"; then
    echo "  ✓ Purchase API returns invoice"
    PASS=$((PASS + 1))
else
    echo "  ✗ Purchase API — response: $PURCHASE"
    FAIL=$((FAIL + 1))
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="

if [ "$FAIL" -gt 0 ]; then
    echo "SMOKE TEST FAILED"
    exit 1
fi
echo "ALL CHECKS PASSED"
