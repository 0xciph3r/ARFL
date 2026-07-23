# ARFL

**Privacy-respecting bandwidth marketplace** — A decentralised VPN protocol powered by Bitcoin.

ARFL is a decentralised VPN protocol that combines WireGuard, Nostr, the Bitcoin Lightning Network, and Cashu ecash into a self-sustaining privacy network. No accounts. No subscriptions. No logs. No token.

Users pay per-gigabyte via Lightning. Node operators earn passive income on bandwidth they already own. The hub coordinates sessions but **mathematically cannot link buyers to their browsing activity** thanks to Cashu blind signatures (BDHKE).

[![CI](https://github.com/Radi-Labs/ARFL/actions/workflows/ci.yml/badge.svg)](https://github.com/Radi-Labs/ARFL/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Radi-Labs/ARFL)](https://github.com/Radi-Labs/ARFL/releases/latest)

## How It Works

```
┌──────────┐     Lightning      ┌──────────┐     Nostr relays     ┌──────────┐
│  Client   │ ──── pay ────────► │   Hub    │ ◄── announcements ── │  Nodes   │
│           │ ◄── Cashu tokens ─ │  (mint)  │                      │          │
└─────┬─────┘                    └──────────┘                      └────┬─────┘
      │                                                                 │
      │  NIP-44 encrypted token                                         │
      └────────────────── WireGuard tunnel ────────────────────────────►│
                          (entry → exit → internet)
```

1. **Client** pays a Lightning invoice for bandwidth (e.g., 500 sats for 1 GB)
2. **Hub** mints Cashu ecash tokens — blind-signed via BDHKE (Blind Diffie-Hellman Key Exchange)
3. **Client** unblinds tokens locally — Hub mathematically cannot link tokens to the buyer
4. **Client** encrypts tokens with NIP-44 and delivers to **Node** via Nostr relay
5. **Node** decrypts, verifies proofs with the Hub's `/v1/redeem`, grants WireGuard access
6. Traffic flows through a **nested two-hop WireGuard tunnel** (entry → exit → internet)

### Privacy Properties

| Property | How |
|---|---|
| **Buyer-session unlinkability** | Cashu BDHKE — Hub signs blinded messages, cannot link tokens to buyers |
| **Token delivery privacy** | NIP-44 encryption — Hub cannot read token contents in transit |
| **Entry node can't see destinations** | Inner WireGuard tunnel encrypts traffic end-to-end to the exit |
| **Exit node can't see client IP** | Only sees the entry node's IP (NAT'd) |
| **No accounts or identity** | Lightning payments require no personal information |
| **No logs by design** | Nodes track byte counters only; no destination logging |

### Honest Threat Model

ARFL is a **privacy-respecting bandwidth marketplace** — not an untraceable VPN. Key caveats:

- The Hub is the real-time arbiter for double-spend detection. Nodes must contact it for `/v1/redeem` checks.
- The residential node economics model works for flat-rate fiber operators. Commercial cloud hosting is explicitly not viable at current pricing.
- Two-hop routing means every GB costs 2x bandwidth. At $5/250GB, nodes clear ~$0.008/GB each — viable for unmetered pipes, not cloud servers.
- The hub cannot link buyers to sessions, but a compromised hub could potentially correlate timing. Future work: client-side node pairing from the Nostr relay index.

## Installation

### Prerequisites

- **Go 1.23+** — [install](https://go.dev/dl/)
- **WireGuard** — `apt install wireguard wireguard-tools` (Linux) or `brew install wireguard-tools` (macOS)
- **nftables** (Linux nodes only) — `apt install nftables` (for kernel-level quota enforcement)
- **LND** (hub, production) — Lightning node with REST API enabled ([Polar](https://lightningpolar.com) for local dev)

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
  "min_payout_sats": 1000,
  "lnd_host": "localhost",
  "lnd_port": 8080,
  "lnd_tls_cert_path": "~/.lnd/tls.cert",
  "lnd_macaroon_path": "~/.lnd/data/chain/bitcoin/mainnet/admin.macaroon",
  "lnd_fee_limit_sat": 100
}
```

#### Environment Variable Overrides

Sensitive config can be set via environment variables (recommended for production). These take priority over `hub.json`:

| Env Var | Overrides | Purpose |
|---|---|---|
| `ARFL_LND_HOST` | `lnd_host` | LND REST hostname |
| `ARFL_LND_PORT` | `lnd_port` | LND REST port |
| `ARFL_LND_TLS_CERT_PATH` | `lnd_tls_cert_path` | Path to LND's tls.cert |
| `ARFL_LND_MACAROON_PATH` | `lnd_macaroon_path` | Path to admin.macaroon |
| `ARFL_LND_FEE_LIMIT_SAT` | `lnd_fee_limit_sat` | Max routing fee per payment |
| `ARFL_CREDENTIAL_KEY` | `credential_key` | HMAC secret for tickets |
| `ARFL_NOSTR_PRIVKEY` | `nostr_privkey` | Hub's Nostr private key |
| `ARFL_DB_PATH` | `db_path` | SQLite database path |

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
  "hub_pubkey_file": "keys/key-100mb.pub.json",
  "connect_addr": "0.0.0.0:9091"
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
  "admin_addr": "127.0.0.1:9090",
  "connect_addr": "0.0.0.0:9091",
  "endpoint": "<public-ip>:51821",
  "upload_mbps": 100,
  "download_mbps": 100,
  "capacity": 50,
  "nostr_privkey": "<64-char hex>",
  "relays": ["wss://relay.damus.io"],
  "attestation": "<hub-issued attestation JSON>",
  "hub_url": "http://<hub-ip>:8080",
  "hub_pubkey_file": "keys/key-100mb.pub.json",
  "connect_addr": "0.0.0.0:9091"
}
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

```bash
sudo ./arfl-node --config node-entry.json
sudo ./arfl-node --config node-exit.json
```

## Docker Testnet

One-command local testnet with hub, 2 nodes, and a Nostr relay:

```bash
cd deploy
./init-testnet.sh          # generate WG keys, Nostr keys, configs
docker compose up --build  # start all services
./smoke-test.sh            # verify everything is healthy
```

Uses distroless base images for minimal attack surface. The hub image has zero shell access.

**With Polar (real Lightning):** Install [Polar](https://lightningpolar.com), create a network with 2 LND nodes, then update `data/hub/hub.json` with the LND connection details. See `deploy/docker-compose.yml` for the volume mount pattern.

## Architecture

```
cmd/
  arfl-hub/          Hub binary — discovery, payments, Cashu mint
  arfl-node/         Node binary — WireGuard tunnels, Cashu-gated /cashu-connect
  arfl-client/       Client binary — purchase flow, tunnel management
internal/
  client/            Bandwidth SDK + CashuConnector (node-side verifier)
  config/            JSON config loader for all components
  control/           Node admin API (POST /cashu-connect, /peers, /quota)
  credentials/       RSA blind signatures, HMAC tickets, key persistence
  discovery/         Nostr-based node discovery + attestation + Cashu API
  ecash/             Cashu ecash mint (BDHKE, NUT-01→07, worker pool)
  lightning/         Lightning client interface (LND REST adapter + circuit breaker)
  nostr/             Nostr relay pool, NIP-44 encryption, keypairs, events
  payments/          Purchase API, settlement engine, node payouts
  quota/             Kernel-level bandwidth enforcement (nftables)
  routing/           IP forwarding + NAT setup
  store/             SQLite storage (invoices, proofs, keysets, quotes, settlements)
  wg/                WireGuard interface management (wgctrl)
pkg/
  protocol/          Protocol constants (MTU, ports, intervals)
  types/             Shared types (NodeInfo, NodeRole)
test/
  integration/       E2E tests (full Cashu privacy flow)
  loadtest/          Performance load tests (/v1/redeem throughput)
deployments/         systemd units, nftables rules, setup scripts
deploy/              Docker Compose testnet (hub + nodes + relay)
docs/                Architecture, API spec, deployment guide
```

See [docs/architecture.md](./docs/architecture.md) for the nested WireGuard two-hop design, encryption layers, and routing rules.

## Testing

408 tests across 10 packages, all passing with `-race`:

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
   │  POST /v1/mint/quote/bolt11  │                              │
   │  {amount: 500, unit: "sat"}  │                              │
   │ ────────────────────────────►│                              │
   │  ◄─── Lightning invoice ─── │                              │
   │                              │                              │
   │  [pays invoice externally]   │                              │
   │                              │  settlement listener         │
   │                              │  marks quote as PAID         │
   │                              │                              │
   │  POST /v1/mint/bolt11        │                              │
   │  {quote, blinded_messages}   │                              │
   │ ────────────────────────────►│                              │
   │  ◄─── blind_signatures ──── │                              │
   │                              │                              │
   │  [unblinds signatures        │                              │
   │   locally — Hub never        │                              │
   │   sees token secrets]        │                              │
   │                              │                              │
   │  NIP-44 encrypted event      │                              │
   │  {proofs, wg_pubkey, role}   │                              │
   │ ─────────────────── via Nostr relay ──────────────────────►│
   │                              │                              │
   │                              │  POST /v1/redeem             │
   │                              │  {proofs, node_pubkey}       │
   │                              │ ◄──────────────────────────  │
   │                              │  ── {ok, bytes_allowed} ──► │
   │                              │                              │
   │  ◄──── {tunnel_ip, pubkey, endpoint} ───────────────────── │
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
- [x] **Phase 6** — Token→connect flow, LND adapter, Docker testnet
- [x] **Phase 9** — Client-side node selection + Cashu token redemption
- [x] **Phase 10** — Node-side Cashu gate + hub redeemer
- [x] **Phase 11** — NIP-44 encrypted token delivery via Nostr
- [x] **Phase 12** — E2E integration test (full privacy chain)
- [x] **Phase 13** — VPS deployment (3 servers, systemd)
- [x] **Phase 14** — Performance engineering (benchmarks, pprof, load test, optimization)
- [ ] **Phase 15** — LNbits extension (wallet integration)
- [ ] **Phase 16** — Mobile app (gomobile bindings)
- [ ] **Phase 17** — Multi-hop routing (>2 hops)

## Performance

Benchmarked on Apple M1 Pro (single core):

| Operation | Throughput | Latency |
|-----------|-----------|---------|
| Token redeem (hub) | 15,000/sec | p50: 862µs |
| VerifyProofs (single) | 6,000/sec | 167µs |
| NIP-44 Encrypt | 360,000/sec | 2.8µs |
| ECDH (per session) | 7,300/sec | 137µs |

## Links

- [Whitepaper (PDF)](./ARFL_Whitepaper.pdf)
- [Architecture](./docs/architecture.md)
- [API Specification](./docs/api-spec.md)
- [Deployment Guide](./docs/deployment-guide.md)
- [Releases](https://github.com/Radi-Labs/ARFL/releases)

## License

[MIT](./LICENSE) — Radi Labs