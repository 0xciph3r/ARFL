---
layout: default
title: API Specification
---

# ARFL Hub API Specification

**Version:** 0.1.0  
**Base URL:** `https://your-hub-domain.example` (replace with your hub's URL)

This document describes all public HTTP endpoints exposed by an ARFL hub.
Extension builders (LNbits, Layerz, etc.) should use this as the integration reference.

---

## Discovery

### GET /info

Returns hub metadata. Use this to discover hub capabilities before making other calls.

**Response:**
```json
{
  "name": "ARFL Hub",
  "version": "0.1.0",
  "node_count": 2,
  "hub_margin_pct": 20,
  "tiers": {
    "1gb": {
      "id": "1gb",
      "name": "1 GB",
      "bytes": 1000000000,
      "price_sats": 500,
      "ticket_count": 10,
      "ticket_bytes": 100000000
    },
    "10gb": { "..." : "..." },
    "50gb": { "..." : "..." }
  }
}
```

### GET /health

Returns hub health status.

**Response:**
```json
{
  "status": "ok",
  "timestamp": 1784037256
}
```

### GET /nodes

Returns all online, verified nodes with their signed Nostr events and attestations.
Clients verify signatures independently — the hub cannot manipulate the list undetected.

**Query parameters:**
- `role` (optional): `"entry"`, `"exit"`, or omit for all

**Response:**
```json
{
  "nodes": [
    {
      "info": {
        "nostr_pubkey": "7e63a1a6...",
        "wg_pubkey": "anF7eMAgCI...",
        "endpoint": "203.0.113.10:51820",
        "connect_url": "http://203.0.113.10:9091",
        "role": "entry",
        "upload_mbps": 1000,
        "download_mbps": 1000,
        "load": 0,
        "capacity": 100,
        "version": "0.1.0"
      },
      "event": { "...signed Nostr event..." }
    }
  ],
  "timestamp": 1784037256,
  "count": 2
}
```

---

## Purchases

### POST /purchase

Create a bandwidth purchase. Returns a Lightning invoice to pay.

**Request:**
```json
{
  "tier_id": "1gb"
}
```

Valid tier IDs: `"1gb"`, `"10gb"`, `"50gb"` (or as returned by `/info`).

**Response (201 Created):**
```json
{
  "payment_hash": "abc123...",
  "payment_request": "lnbc500n1...",
  "amount_sats": 500,
  "tier": "1gb",
  "expires_at": "2026-07-18T15:00:00Z"
}
```

### GET /purchase/{payment_hash}

Poll purchase status. Once the Lightning invoice is paid, tickets are returned.

**Response (pending):**
```json
{
  "status": "pending",
  "tier": "1gb",
  "amount_sats": 500
}
```

**Response (settled — tickets issued):**
```json
{
  "status": "settled",
  "tier": "1gb",
  "amount_sats": 500,
  "tickets": [
    {
      "id": "ticket-uuid",
      "bytes_value": 100000000,
      "mac": "hex-encoded-hmac",
      "issued_at": "2026-07-18T14:00:00Z",
      "expires_at": "2026-08-17T14:00:00Z"
    }
  ]
}
```

---

## Blind Signatures (Phase 4)

### POST /redeem

Redeem blind-signed tokens from an entitlement.

**Request:**
```json
{
  "payment_hash": "abc123...",
  "blinded_messages": ["base64-encoded-message", "..."],
  "nonce": "unique-request-id"
}
```

**Response:**
```json
{
  "blind_signatures": ["base64-encoded-sig", "..."],
  "key_id": "key-100mb"
}
```

### POST /spend

Spend a blind token at a node (node calls this to verify).

**Request:**
```json
{
  "token": "base64-encoded-token",
  "preimage": "base64-encoded-preimage",
  "key_id": "key-100mb",
  "node_pubkey": "964440ff..."
}
```

**Response (200):**
```json
{
  "valid": true,
  "bytes_value": 100000000
}
```

---

## Usage Reporting

### POST /report

Nodes submit signed usage reports for settlement.

**Request:**
```json
{
  "session_id": "session-uuid",
  "ticket_id": "ticket-uuid",
  "node_pubkey": "7e63a1a6...",
  "role": "entry",
  "bytes_reported": 50000000,
  "reported_at": "2026-07-18T14:30:00Z",
  "signature": "hex-encoded-schnorr-sig"
}
```

**Response (201):**
```json
{
  "status": "accepted"
}
```

---

## Node Operator API

### GET /node/{pubkey}/earnings

Returns aggregate earnings for a node.

**Response:**
```json
{
  "total_earned_sats": 1500,
  "pending_sats": 500,
  "paid_sats": 1000,
  "session_count": 42,
  "settlement_count": 7
}
```

### POST /node/wallet

Register or update a node's payout Lightning address.

**Request:**
```json
{
  "pubkey": "7e63a1a62eb3f1a655ff723da870544c5bb87d0844b1a6d359ea5b3cc855440e",
  "ln_address": "operator@walletofsatoshi.com"
}
```

**Response:**
```json
{
  "status": "ok"
}
```

### GET /node/wallet?pubkey={pubkey}

Retrieve a node's registered Lightning address.

**Response:**
```json
{
  "ln_address": "operator@walletofsatoshi.com"
}
```

### POST /node/withdraw

Request a withdrawal of earned sats.

**Request:**
```json
{
  "pubkey": "7e63a1a62eb3f1a655ff723da870544c5bb87d0844b1a6d359ea5b3cc855440e",
  "amount_sats": 1000
}
```

**Response (200 — success):**
```json
{
  "status": "paid",
  "amount_sats": 1000,
  "payment_hash": "abc123..."
}
```

**Response (402 — insufficient balance):**
```json
{
  "error": "insufficient balance",
  "available_sats": 500,
  "requested_sats": 1000
}
```

---

## Node Announcements

### POST /announce

Direct node announcement fallback when Nostr relays are unavailable.
Accepts a signed Nostr event (same format nodes publish to relays).

**Request:** Raw signed Nostr event JSON (kind 30078).

**Response (200):**
```json
{
  "status": "accepted"
}
```

### POST /attest/refresh

Nodes call this to refresh their attestation before it expires.
Requires a valid, non-expired attestation and an active lease.

**Request:**
```json
{
  "attestation": "base64-encoded-current-attestation"
}
```

**Response:**
```json
{
  "attestation": "base64-encoded-new-attestation"
}
```

---

## Revenue Model

- Hub sets bundle prices via tiers (configurable)
- Hub retains `hub_margin_pct` (default 20%) of each purchase
- Remaining 80% splits 50/50 between entry and exit nodes
- Settlement runs every 6 hours (configurable via `settlement_hours`)
- Nodes withdraw earned sats via `POST /node/withdraw`

## Authentication

MVP uses no authentication on public endpoints. Node identity is verified via:
- Nostr event signatures (node announcements)
- Hub-signed attestations (node authorization)
- Schnorr signatures (usage reports)

---

## Cashu Ecash Mint (NUT-01 → NUT-07)

The hub operates a Cashu-compatible ecash mint. These endpoints follow the [Cashu NUT specification](https://github.com/cashubtc/nuts).

### GET /v1/keys

Returns the mint's active public keys (NUT-01).

**Response:**
```json
{
  "keysets": [{
    "id": "00eb7476a759a27e",
    "unit": "sat",
    "keys": {
      "1": "02abc...",
      "2": "03def...",
      "4": "02fed..."
    }
  }]
}
```

### GET /v1/keysets

Returns all keysets with their status (NUT-02).

**Response:**
```json
{
  "keysets": [{
    "id": "00eb7476a759a27e",
    "unit": "sat",
    "active": true,
    "input_fee_ppk": 0
  }]
}
```

### POST /v1/mint/quote/bolt11

Request a mint quote — returns a Lightning invoice to pay (NUT-04).

**Request:**
```json
{
  "amount": 64,
  "unit": "sat"
}
```

**Response:**
```json
{
  "quote": "unique-quote-id",
  "request": "lnbc640n1...",
  "state": "UNPAID",
  "expiry": 1784041000
}
```

### POST /v1/mint/bolt11

After paying the invoice, mint tokens by providing blinded messages (NUT-04).

**Request:**
```json
{
  "quote": "unique-quote-id",
  "outputs": [
    {"amount": 64, "id": "00eb7476a759a27e", "B_": "02..."}
  ]
}
```

**Response:**
```json
{
  "signatures": [
    {"amount": 64, "id": "00eb7476a759a27e", "C_": "03..."}
  ]
}
```

### POST /v1/swap

Swap proofs for new blinded signatures (NUT-03). Used for splitting denominations.

**Request:**
```json
{
  "inputs": [{"amount": 64, "id": "...", "secret": "...", "C": "..."}],
  "outputs": [{"amount": 32, "id": "...", "B_": "..."}, {"amount": 32, "id": "...", "B_": "..."}]
}
```

### POST /v1/checkstate

Check if proofs are spent (NUT-07).

**Request:**
```json
{
  "Ys": ["02abc...", "03def..."]
}
```

### POST /v1/redeem

ARFL-specific: Node presents proofs for bandwidth redemption.

**Request:**
```json
{
  "proofs": [{"amount": 64, "id": "00eb7476a759a27e", "secret": "...", "C": "..."}],
  "node_pubkey": "node-nostr-pubkey-hex"
}
```

**Response:**
```json
{
  "ok": true,
  "bytes_allowed": 64000000,
  "sats_redeemed": 64
}
```

Extension builders should verify node events client-side using the hub's Nostr pubkey.
