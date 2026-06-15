# ARFL

**Privacy in the Dark Cloud** — A decentralised VPN protocol powered by Bitcoin.

ARFL is a decentralised VPN protocol that combines WireGuard, Nostr, the Bitcoin Lightning Network, and blind signatures into a self-sustaining privacy network. No accounts. No subscriptions. No logs. No token.

Users pay per-gigabyte via Lightning. Node operators earn passive income on bandwidth they already own. The hub coordinates sessions but **mathematically cannot link buyers to their browsing activity** thanks to Chaumian blind signatures.

[![CI](https://github.com/Radi-Labs/ARFL/actions/workflows/ci.yml/badge.svg)](https://github.com/Radi-Labs/ARFL/actions/workflows/ci.yml)

## How It Works

```
┌──────────┐     Lightning      ┌──────────┐     Nostr relays     ┌──────────┐
│  Client   │ ──── pay ────────► │   Hub    │ ◄── announcements ── │  Nodes   │
│           │ ◄── blind tokens ─ │          │                      │          │
└─────┬─────┘                    └──────────┘                      └────┬─────┘
      │                                                                 │
      │  present token                                                  │
      └────────────────── WireGuard tunnel ────────────────────────────►│
                          (entry → exit → internet)
```

1. **Client** pays a Lightning invoice for bandwidth (e.g., 500 sats for 1 GB)
2. **Hub** issues blind-signed tokens — it signs without seeing the token secrets
3. **Client** presents a token to a **Node** via `POST /connect`
4. **Node** verifies the RSA signature locally, then checks with the Hub for double-spend
5. **Node** grants WireGuard access with a bandwidth quota matching the token
6. Traffic flows through a **nested two-hop WireGuard tunnel** (entry → exit → internet)

### Privacy Properties

| Property | How |
|---|---|
| **Buyer-session unlinkability** | Blind signatures — Hub signs blinded messages, cannot link tokens to buyers |
| **Entry node can't see destinations** | Inner WireGuard tunnel encrypts traffic end-to-end to the exit |
| **Exit node can't see client IP** | Only sees the entry node's IP (NAT'd) |
| **No accounts or identity** | Lightning payments require no personal information |
| **No logs by design** | Nodes track byte counters only; no destination logging |

### Honest Threat Model

ARFL is an **online bounded-risk authorization system for bandwidth** — not offline ecash. Key caveats:

- The Hub is the real-time arbiter for double-spend detection. Nodes must contact it for `/spend` checks.
- During a Hub outage, nodes fall back to offline verification (`VerifyOnly`). Risk is bounded: a replayed token can steal at most 100 MB × N_offline_nodes.
- The residential node economics model works for flat-rate fiber operators. Commercial cloud hosting is explicitly not viable at current pricing.
- Fedimint federation custody (v1) runs 5 nodes operated by the protocol team. Concrete decentralisation timeline is a known gap.

## Installation

### Prerequisites

- **Go 1.23+** — [install](https://go.dev/dl/)
- **WireGuard** — `apt install wireguard wireguard-tools` (Linux) or `brew install wireguard-tools` (macOS)
- **nftables** (Linux nodes only) — `apt install nftables` (for kernel-level quota enforcement)

### Build from Source

```bash
git clone https://github.com/Radi-Labs/ARFL.git
cd ARFL
go build ./...
```

This produces three binaries in the current directory:

| Binary | Purpose |
|---|---|
| `arfl-hub` | Coordination hub — discovery, payments, blind signing |
| `arfl-node` | Node daemon — WireGuard tunnel endpoint (entry or exit) |
| `arfl-client` | Client CLI — purchase bandwidth, connect to nodes |

### Automated Node Setup (Ubuntu)

For production node servers, the setup script installs WireGuard, nftables, enables IP forwarding, and opens the UDP port:

```bash
sudo bash deployments/setup-node.sh
```

## Configuration

### Hub (`hub.json`)

```json
{
  "nostr_privkey": "<64-char hex Nostr private key>",
  "listen_addr": "0.0.0.0:8080",
  "relays": ["wss://relay.damus.io", "wss://nos.lol"],
  "db_path": "arfl.db",
  "credential_key": "<64-char hex HMAC secret>",
  "blind_key_dir": "keys/",
  "settlement_hours": 6,
  "min_payout_sats": 1000
}
```

```bash
# Development mode (auto-generates credential key — NOT for production):
./arfl-hub --config hub.json --dev

# Production:
./arfl-hub --config hub.json
```

On first run, the Hub generates an RSA denomination key in `keys/key-100mb.json` and exports the public key to `keys/key-100mb.pub.json`. **Distribute the `.pub.json` file to all nodes.**

### Node (`node.json`)

```bash
# Generate a WireGuard keypair:
./arfl-node --genkey
```

**Entry node** (`node-entry.json`):
```json
{
  "role": "entry",
  "listen_port": 51820,
  "private_key": "<base64 WireGuard private key>",
  "tunnel_ip": "10.100.0.1/24",
  "interface": "wg-entry",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "endpoint": "<public-ip>:51820",
  "upload_mbps": 100,
  "download_mbps": 100,
  "capacity": 50,
  "nostr_privkey": "<64-char hex>",
  "relays": ["wss://relay.damus.io"],
  "attestation": "<hub-issued attestation JSON>",
  "hub_url": "http://<hub-ip>:8080",
  "hub_pubkey_file": "keys/key-100mb.pub.json"
}
```

**Exit node** (`node-exit.json`):
```json
{
  "role": "exit",
  "listen_port": 51821,
  "private_key": "<base64 WireGuard private key>",
  "tunnel_ip": "10.200.0.1/24",
  "interface": "wg-exit",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9091",
  "endpoint": "<public-ip>:51821",
  "upload_mbps": 100,
  "download_mbps": 100,
  "capacity": 50,
  "nostr_privkey": "<64-char hex>",
  "relays": ["wss://relay.damus.io"],
  "attestation": "<hub-issued attestation JSON>",
  "hub_url": "http://<hub-ip>:8080",
  "hub_pubkey_file": "keys/key-100mb.pub.json"
}
```

```bash
sudo ./arfl-node --config node-entry.json
sudo ./arfl-node --config node-exit.json
```

### Client

**Purchase bandwidth:**
```bash
./arfl-client --purchase 1gb \
  --hub-url http://<hub-ip>:8080 \
  --hub-key keys/key-100mb.pub.json
```

This will:
1. Create a Lightning invoice (displayed as BOLT11)
2. Wait for you to pay it
3. Prompt for the payment preimage
4. Redeem blind tokens and save to `tokens.json`

**Connect to the network (Phase 1 static mode):**
```bash
sudo ./arfl-client --session session.json --key client.key
```

**Discover nodes dynamically (Phase 2):**
```bash
sudo ./arfl-client --discover http://<hub-ip>:8080 \
  --hub-pubkeys <hub-nostr-pubkey> \
  --key client.key
```

### Bandwidth Tiers

| Tier | Data | Price | Tokens |
|---|---|---|---|
| 1 GB | 1,000 MB | 500 sats | 10 × 100 MB |
| 10 GB | 10,000 MB | 4,500 sats | 100 × 100 MB |
| 50 GB | 50,000 MB | 20,000 sats | 500 × 100 MB |

## Architecture

```
cmd/
  arfl-hub/          Hub binary — discovery, payments, blind signing
  arfl-node/         Node binary — WireGuard tunnels, token-gated /connect
  arfl-client/       Client binary — purchase flow, tunnel management
internal/
  client/            Bandwidth SDK + TokenGate (node-side verifier)
  config/            JSON config loader for all components
  control/           Node admin API (POST /connect, /peers, /quota)
  credentials/       Blind signatures (RSA mint/verifier), HMAC tickets, key persistence
  discovery/         Nostr-based node discovery + attestation
  lightning/         Lightning client interface + mock
  nostr/             Nostr relay pool, keypairs, event handling
  payments/          Purchase API, settlement engine, blind sig endpoints
  quota/             Kernel-level bandwidth enforcement (nftables)
  routing/           IP forwarding + NAT setup
  store/             SQLite storage (invoices, tickets, entitlements, spent tokens)
  wg/                WireGuard interface management (wgctrl)
pkg/
  protocol/          Protocol constants (MTU, ports, intervals)
  types/             Shared types (NodeInfo, NodeRole)
deployments/         systemd units, nftables rules, setup scripts
test/integration/    End-to-end integration tests
.notes/              Architecture decisions (87 documented decisions)
```

See [docs/architecture.md](./docs/architecture.md) for the nested WireGuard two-hop design, encryption layers, and routing rules.

## Testing

383 tests across 10 packages, all passing with `-race`:

```bash
go test -race ./...
```

### Security Testing (STRIDE)

ARFL includes dedicated threat model tests across the STRIDE categories:

| Category | Server-side | Client-side | Total |
|---|---|---|---|
| Spoofing | 2 | 3 | 5 |
| Tampering | 2 | 5 | 7 |
| Repudiation | 2 | 1 | 3 |
| Information Disclosure | 1 | 4 | 5 |
| Denial of Service | 2 | 3 | 5 |
| Elevation of Privilege | 3 | 2 | 5 |
| Concurrent / Race | 2 | 2 | 4 |
| **Total** | **14** | **20** | **34** |

Run STRIDE tests specifically:
```bash
go test -race -v ./... -run STRIDE
```

### Integration Tests

End-to-end tests that exercise the full protocol in-process (no infrastructure required):

```bash
go test -race -v ./test/integration/
```

Covers: purchase → pay → redeem → verify → spend → double-spend detection → unlinkability proof.

## Protocol Flow (Detailed)

```
 Client                          Hub                           Node
   │                              │                              │
   │  POST /purchase {tier}       │                              │
   │ ────────────────────────────►│                              │
   │  ◄─── Lightning invoice ─── │                              │
   │                              │                              │
   │  [pays invoice externally]   │                              │
   │                              │  settlement listener         │
   │                              │  creates entitlement         │
   │                              │                              │
   │  POST /redeem                │                              │
   │  {preimage, blinded_msgs}    │                              │
   │ ────────────────────────────►│                              │
   │  ◄─── blind_signatures ──── │                              │
   │                              │                              │
   │  [unblinds signatures        │                              │
   │   locally — Hub never        │                              │
   │   sees token secrets]        │                              │
   │                              │                              │
   │  POST /connect               │                              │
   │  {token, wg_pubkey}          │                              │
   │ ─────────────────────────────────────────────────────────► │
   │                              │  POST /spend {token}         │
   │                              │ ◄──────────────────────────  │
   │                              │  ──── first_spend: true ──► │
   │                              │                              │
   │  ◄──── {tunnel_ip, pubkey, bytes_allowed} ──────────────── │
   │                              │                              │
   │  [WireGuard tunnel established]                             │
   │ ═══════════════ encrypted traffic ════════════════════════► │
```

## Development

```bash
# Build all binaries
go build ./...

# Run all tests with race detection
go test -race ./...

# Check formatting
gofmt -l .

# Vet
go vet ./...
```

### Decision Log

87 architecture decisions are documented in [`.notes/decisions.md`](./.notes/decisions.md), covering:
- Phase 1: WireGuard transport design
- Phase 2: Nostr discovery protocol
- Phase 3: Lightning payment flow
- Phase 4: Blind signature architecture
- Phase 5: Client integration wiring

## Roadmap

- [x] **Phase 1** — WireGuard transport (nested two-hop tunnels)
- [x] **Phase 2** — Nostr discovery (node announcements, attestation)
- [x] **Phase 3** — Lightning payments (invoices, settlement, tickets)
- [x] **Phase 4** — Blind signatures (RSA mint, Chaumian tokens, STRIDE)
- [x] **Phase 5** — Client integration (SDK, TokenGate, binary wiring)
- [ ] **Phase 6** — Fedimint federation deposits
- [ ] **Phase 7** — Mobile app (gomobile bindings)
- [ ] **Phase 8** — Multi-hop routing (>2 hops)

## Links

- [Whitepaper (PDF)](./ARFL_Whitepaper.pdf)
- [Architecture](./docs/architecture.md)
- [Decision Log](./.notes/decisions.md)

## License

[MIT](./LICENSE) — Radi Labs