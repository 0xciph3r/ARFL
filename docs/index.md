---
layout: default
title: Home
---

# ARFL

**Privacy-respecting bandwidth marketplace powered by Bitcoin.**

ARFL is a decentralised VPN protocol that combines WireGuard, Nostr, the Bitcoin Lightning Network, and Cashu ecash into a self-sustaining privacy network.

**No accounts. No subscriptions. No logs.**

---

## How It Works

```
Client ──── Lightning ────► Hub (Cashu mint) ◄── Nostr ── Nodes
  │                                                         │
  └──── NIP-44 encrypted token ─── WireGuard tunnel ──────►│
```

1. Pay a Lightning invoice for bandwidth
2. Receive Cashu ecash tokens (blind-signed, unlinkable)
3. Deliver tokens to a node via encrypted Nostr event
4. Node verifies → grants WireGuard access
5. Traffic routes through a nested two-hop tunnel

---

## Privacy Guarantees

| Property | Mechanism |
|----------|-----------|
| Buyer-session unlinkability | Cashu BDHKE blind signatures |
| Token delivery privacy | NIP-44 end-to-end encryption |
| Entry can't see destinations | Nested WireGuard (inner tunnel) |
| Exit can't see client IP | Only sees entry node's NAT IP |
| No identity required | Lightning = no personal info |

---

## Performance

Benchmarked on a single CPU core:

| Operation | Throughput |
|-----------|-----------|
| Token redeem | **15,000/sec** |
| Proof verification | **6,000/sec** |
| NIP-44 encrypt/decrypt | **360,000/sec** |

---

## Quick Start

### Download

Get the latest binaries from [Releases](https://github.com/Radi-Labs/ARFL/releases/latest).

```bash
# Verify checksums
sha256sum -c checksums.txt

# Run hub
./arfl-hub-linux-amd64 --config hub.json

# Run node
./arfl-node-linux-amd64 --config node.json
```

### Build from Source

```bash
git clone https://github.com/Radi-Labs/ARFL.git
cd ARFL
go build ./...
```

---

## Documentation

- [Architecture](./architecture) — Nested WireGuard two-hop design
- [API Specification](./api-spec) — Hub HTTP endpoints (NUT-01→07, discovery, payments)
- [Deployment Guide](./deployment-guide) — Production setup on VPS

---

## Technology Stack

| Component | Technology |
|-----------|-----------|
| Transport | WireGuard (nested two-hop) |
| Discovery | Nostr relays (kind 30078) |
| Payments | Bitcoin Lightning Network |
| Privacy | Cashu ecash (BDHKE) |
| Token delivery | NIP-44 encryption |
| Storage | SQLite |
| Language | Go |

---

## Economics

- Users pay per-GB via Lightning (~500 sats/GB)
- Node operators earn passive income on bandwidth they already own
- Hub takes a configurable margin (default 20%)
- Designed for residential/unmetered connections — not cloud servers

---

## Links

- [GitHub Repository](https://github.com/Radi-Labs/ARFL)
- [Releases](https://github.com/Radi-Labs/ARFL/releases)
- [Whitepaper (PDF)](https://github.com/Radi-Labs/ARFL/blob/main/ARFL_Whitepaper.pdf)

---

## License

[MIT](https://github.com/Radi-Labs/ARFL/blob/main/LICENSE) — Radi Labs
