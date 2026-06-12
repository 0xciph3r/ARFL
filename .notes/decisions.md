# ARFL — Architecture Decisions

These are the decisions made during development and the reasoning behind them.
Use this to understand the codebase and defend choices when asked.

---

## Decision 1: Why Go?

The whitepaper specifies Go, and it's the right choice for three reasons:

1. **WireGuard's control library (`wgctrl-go`) is written in Go.** It talks
   directly to the WireGuard kernel module via generic netlink. Using Go means
   no foreign function glue — no cgo bindings, no Python ctypes, no wrapper
   libraries. We use the same code that WireGuard's own tools use internally.

2. **Single binary deployment.** `go build` produces one statically-linked
   file. You ship it to a node operator's server and it runs. No runtime to
   install, no `node_modules`, no virtualenv. For a protocol where random
   people run nodes on their home servers, this is critical.

3. **Concurrency is native.** The node daemon does four things simultaneously:
   manages WireGuard peers, polls byte counters every 5 seconds, serves an
   admin API over HTTP, and listens for shutdown signals. Go's goroutines
   make this trivial — `go pollByteCounters(ctx, mgr, iface)` launches a
   concurrent task in one line. In Python you'd need asyncio or threading.
   In Rust you'd need tokio. In Go it's built into the language.

**Defend it as:** "Go gives us single-binary deployment, native WireGuard
integration, and trivial concurrency — the three things a node daemon needs
most."

---

## Decision 2: Monorepo with `internal/` and `pkg/`

All four binaries (arfl-node, arfl-client, arfl-hub, arflctl) live in one
repository with shared packages.

```
arfl/
  cmd/          ← each binary gets its own main.go
  internal/     ← shared code, ONLY importable within this module
  pkg/          ← shared code, importable by anyone
```

**Why monorepo?** Because the node, client, and hub must all agree on
protocol constants, event types, and WireGuard config formats. If protocol
constants lived in `arfl-node` and `arfl-client` was a separate repo, they
could drift. One repo = one source of truth. When the bundle size changes,
it changes everywhere in one commit.

**Why `internal/` vs `pkg/`?**
- `internal/` is a Go enforcement mechanism. Code inside `internal/` can
  ONLY be imported by packages within the same module. If someone forks
  ARFL and tries to import `internal/wg` from their own project, the Go
  compiler will refuse. This is where implementation details live — things
  that are ours and could change without notice.
- `pkg/` is public. Protocol constants and types that external tools,
  auditors, or third-party hubs might want to import.

**Defend it as:** "Monorepo prevents protocol drift between components.
internal/ vs pkg/ gives us a clear boundary between implementation details
and the public protocol specification."

---

## Decision 3: Protocol Constants File (`pkg/protocol/constants.go`)

Every magic number from the whitepaper — 250 GB bundle, 30-day expiry,
40/40/20 revenue split, 60-second ping interval, 51820/51821 ports — is
defined ONCE in this file.

**Why?** Three reasons:
1. **Single source of truth.** When someone asks "where does the 250 GB
   come from?", the answer is one file. No grep needed.
2. **Change management.** If the community votes to change the bundle size,
   it's one constant, not a find-and-replace across 50 files.
3. **Auditability.** Reviewers can read one file and verify every protocol
   parameter matches the whitepaper.

**Defend it as:** "Protocol parameters are defined once and referenced
everywhere. This prevents drift and makes the protocol auditable."

---

## Decision 4: The `Manager` Interface Pattern (`internal/wg/wg.go`)

Instead of putting WireGuard operations directly into functions, we define
an interface — a contract that says "anything that manages WireGuard must
be able to do these 6 things":

```go
type Manager interface {
    CreateInterface(cfg InterfaceConfig) error
    DeleteInterface(name string) error
    AddPeer(iface string, peer PeerConfig) error
    RemovePeer(iface string, pubkey string) error
    GetPeerStats(iface string) ([]PeerStats, error)
    Close() error
}
```

**Why an interface instead of concrete functions?**

1. **Testing.** We can write a `MockManager` that pretends to manage
   WireGuard without needing actual root permissions or a kernel module.
   This lets us test the node daemon's logic on any machine — even in CI.

2. **Platform flexibility.** The real implementation (`WgctrlManager`)
   uses `wgctrl-go` which talks to the Linux kernel module. On macOS
   it uses `wireguard-go` userspace. The interface hides this difference
   from everything that uses it.

3. **Future-proofing.** If we ever want a different WireGuard backend
   (e.g. a Rust implementation via FFI), we only change the struct that
   implements the interface. Every consumer (node daemon, client, hub)
   keeps working without changes.

**Defend it as:** "We program against contracts, not implementations. The
node daemon doesn't know or care how WireGuard is managed — it just knows
what operations are available. This makes testing possible and backends
swappable."

---

## Decision 5: `wgctrl-go` vs. Shelling Out to `wg` CLI

The whitepaper says on page 7: "The CLI is never used in production."

`wgctrl-go` is a Go library that talks directly to the WireGuard kernel
module via **generic netlink** — a socket-based protocol that Linux uses
for kernel-to-userspace communication.

The alternative would be running shell commands:
`exec("wg set wg0 peer ABC123 allowed-ips 10.0.0.2/32")`

**Why wgctrl-go wins:**

| Concern       | Shell commands (`wg set`)  | wgctrl-go (netlink)         |
|---------------|----------------------------|-----------------------------|
| Performance   | Fork a process per call    | Direct kernel socket        |
| Type safety   | Parse string output        | Returns typed Go structs    |
| Concurrency   | Race conditions possible   | Atomic kernel operations    |
| Error handling | Parse stderr strings       | Typed Go errors             |
| Dependencies  | Requires `wg` CLI installed| Just a Go library           |

**The split:** We use wgctrl-go for CONFIGURING interfaces (set private
key, add/remove peers, read byte counters). But we still use OS commands
for CREATING interfaces (`ip link add ... type wireguard` on Linux)
because wgctrl doesn't do interface creation — that's the kernel's job
via a different subsystem (netlink route, not generic netlink).

**Defend it as:** "wgctrl-go uses the same kernel interface that WireGuard's
own tools use. Shelling out to the CLI in production is fragile — string
parsing breaks, concurrent calls race, and there's no type safety."

---

## Decision 6: `runtime.GOOS` Switches vs. Build Tags

We handle platform differences in two ways, depending on how different the
code is:

### Small Differences → `runtime.GOOS` switch (same file)

When the logic is the same but one command is different:

```go
func (m *WgctrlManager) createOSInterface(name string) error {
    switch runtime.GOOS {
    case "linux":
        return run("ip", "link", "add", name, "type", "wireguard")
    case "darwin":
        return run("wireguard-go", name)
    }
}
```

This keeps both platforms visible in one place. Easy to read, easy to review.

### Completely Different Implementations → Build Tags (separate files)

When the entire implementation is different and one can't even compile on
the other platform:

```
nftables_linux.go   → //go:build linux    (real nftables kernel calls)
noop.go             → //go:build !linux   (logs "would have done X")
```

The `//go:build linux` directive tells the Go compiler: "Don't even try
to compile this file on macOS." This matters because nftables imports and
code literally don't exist on macOS — the compiler would error.

### Factory Functions → Glue Between Build-Tagged Code

Since `NftablesEnforcer` only exists on Linux and `NoopEnforcer` only
exists on macOS, we need build-tagged factory functions too:

```
new_linux.go  → func NewEnforcer(iface) Enforcer { return NftablesEnforcer }
new_other.go  → func NewEnforcer(iface) Enforcer { return NoopEnforcer }
```

The consuming code just calls `quota.NewEnforcer(iface)` and gets the
right implementation for whatever OS it's running on.

**Defend it as:** "runtime.GOOS switches for small differences — keeps
both paths visible. Build tags for fundamentally different code — prevents
compilation errors on unsupported platforms."

---

## Decision 7: The Admin API (HTTP on localhost)

The node daemon exposes a simple HTTP API on `127.0.0.1:9090`:

```
POST /peers          → add a WireGuard peer
DELETE /peers/{key}  → remove a peer
GET /peers           → list all peers with byte counter stats
POST /quota          → set bandwidth quota for a tunnel IP
GET /health          → health check
```

**Why HTTP?** Because every language can speak HTTP. The hub (Go) calls
it. A shell script can call it with `curl`. A monitoring tool can call
it. It's the lowest-friction protocol for a control plane.

**Why localhost only?** The admin API has no authentication in Phase 1.
Binding to `127.0.0.1` means only processes on the same machine can
reach it. In Phase 2+, the hub communicates over the internet — at which
point we'd add mTLS or API keys. But for the PoC, localhost-only is
secure and simple.

**Why not gRPC / WebSocket / custom protocol?** Over-engineering. This
API handles maybe 10 requests per minute (add peer, remove peer, health
check). HTTP with JSON is perfectly adequate. We'd only switch to gRPC
if we needed streaming or binary performance — neither is needed here.

**Defend it as:** "HTTP on localhost is the simplest control plane that
works. No auth needed because it's local-only. Every tool speaks HTTP."

---

## Decision 8: Goroutine Architecture in the Node Daemon

The node daemon runs four concurrent tasks:

```
main goroutine          → setup, then waits for shutdown signal
go pollByteCounters()   → polls WireGuard counters every 5 seconds
go adminServer.Listen() → serves HTTP admin API
defer cleanup()         → runs on shutdown
```

**Why goroutines, not threads?** Goroutines are Go's concurrency primitive.
They're lighter than OS threads (2KB stack vs 1MB), scheduled by Go's
runtime (not the OS), and communicate via channels rather than locks.
For our use case, the distinction barely matters — we only have 3-4
concurrent tasks. But goroutines are idiomatic Go.

**Why `context.Context` for cancellation?** The `ctx` passed to
`pollByteCounters` is a cancellation token. When the user presses Ctrl-C,
`cancel()` is called, which causes `ctx.Done()` to fire, which makes the
polling loop exit cleanly. Without this, the goroutine would keep running
after main() exits, causing a messy shutdown.

```go
select {
case <-ctx.Done():      // shutdown signal received
    return
case <-ticker.C:        // 5 seconds elapsed, poll counters
    stats, err := mgr.GetPeerStats(iface)
}
```

The `select` statement waits for EITHER event — whichever happens first.
This is how Go handles "do X every N seconds, but stop when told to."

**Defend it as:** "Goroutines with context-based cancellation are
idiomatic Go for concurrent daemons. The polling loop, admin API, and
shutdown handler all run independently, coordinated through context
cancellation."

---

## Decision 9: Nested WireGuard Tunnels (The Core Privacy Model)

This is the most important architectural decision in ARFL.

### The Problem

A naive two-hop VPN:
```
Client → Entry Node → Exit Node → Internet
```
If the entry just forwards decrypted packets, it can read the destination
IP headers. The entry would know both WHO you are (your IP) and WHERE
you're going. Privacy broken.

### The Solution: Nested Encryption

The client builds TWO WireGuard tunnels, one inside the other:

```
Your HTTP request to example.com
  ↓ encrypt with exit node's key (inner tunnel)
  ↓ encrypt AGAIN with entry node's key (outer tunnel)
  ↓ send to entry node

Entry node:
  ↓ decrypt outer layer → sees encrypted blob going to exit IP
  ↓ forwards to exit (CANNOT read the inner layer)

Exit node:
  ↓ decrypt inner layer → sees your HTTP request
  ↓ forwards to example.com
```

**What each node sees:**
- Entry: Your real IP + encrypted blobs destined for exit's IP. CANNOT see
  what websites you visit.
- Exit: Website destinations + your tunnel IP (e.g. 10.200.0.2). CANNOT
  see your real IP.
- Nobody: The complete picture.

### How It Works in Practice (Client)

The client creates two WireGuard interfaces:

```
wg-outer: Client (10.100.0.2) ↔ Entry (10.100.0.1)
  Purpose: Carries the inner tunnel packets
  AllowedIPs: 10.100.0.0/24, exit_public_ip/32

wg-inner: Client (10.200.0.2) ↔ Exit (10.200.0.1)
  Purpose: Carries your actual internet traffic
  AllowedIPs: 0.0.0.0/0  (everything)
```

The routing trick: the exit node's public IP is routed through wg-outer.
So when wg-inner tries to send encrypted packets to the exit, they go
through wg-outer first — getting encrypted a second time.

```
Routes:
  entry_ip/32     → via default gateway (real internet)
  exit_ip/32      → via wg-outer (through entry tunnel)
  0.0.0.0/0       → via wg-inner (all traffic through exit)
```

### The Nuance: Nodes Know Each Other's IPs

With nested tunnels, the entry DOES see packets going to exit's IP, and
the exit DOES see packets arriving from entry's IP. They can infer each
other's identity.

**Why this is acceptable:** Neither node gets both your identity AND your
destination. The security property is compartmentalisation — no single
node has the complete picture. This is the same model as Tor (guard node
knows your IP, exit node knows your destination, neither knows both).
With many nodes in the network, statistical correlation is impractical.

**Defend it as:** "Nested WireGuard tunnels provide the same compartment-
alisation as Tor's three-hop model. Entry sees who you are but not where
you go. Exit sees where you go but not who you are. The privacy property
is enforced by encryption — not by trust."

---

## Decision 10: MTU Set to 1280

We set `TunnelMTU = 1280` (the IPv6 minimum, universally safe).

**Why?** Each WireGuard layer adds ~60 bytes of overhead (headers +
encryption). With two nested layers that's ~120 bytes. Standard Ethernet
MTU is 1500. If we used 1500 on the tunnels, packets would be 1500 + 120
= 1620 bytes — too big for the physical network. This causes either
fragmentation (slow, unreliable) or packet drops.

1280 is deliberately conservative:
- 1280 + 60 (inner WG) = 1340
- 1340 + 60 (outer WG) = 1400
- 1400 < 1500 ✓ — fits within Ethernet without fragmentation.

**Defend it as:** "1280 is the IPv6 minimum MTU guaranteed to work
everywhere. With 120 bytes of nested WireGuard overhead, it safely fits
within standard 1500-byte Ethernet frames without fragmentation."

---

## Decision 11: Quota Enforcement — Two Layers

Bandwidth quotas use two enforcement layers:

| Layer     | Tool              | Job                        | Speed       |
|-----------|-------------------|----------------------------|-------------|
| Kernel    | nftables          | Hard cutoff — drops packets | Nanoseconds |
| Userspace | wgctrl (Go)       | Billing accuracy            | Every 5 sec |

**Why two layers?** The Go daemon polls byte counters every 5 seconds.
In 5 seconds on a 1 Gbps connection, a client could push ~625 MB. If
their balance is nearly zero, they'd get free bandwidth until the next
poll.

nftables solves this with 256 MB "slabs" enforced at the kernel level.
When a slab is consumed, the kernel drops packets instantly — no
userspace delay. The Go daemon then checks: does the user have balance
left? If yes, refresh the slab. If no, remove the peer.

**Why 256 MB slabs?** It's a balance between:
- Too small (e.g. 16 MB): constant slab refreshes, high overhead
- Too large (e.g. 1 GB): potential overshoot before kernel catches up
- 256 MB: refreshed roughly once per minute at typical speeds. Low
  overhead, minimal overshoot.

**Defend it as:** "The kernel enforces the hard stop. Userspace handles
the accounting. This eliminates the polling overshoot window — the kernel
drops packets at wire speed while the daemon reconciles the books."

---

## Decision 12: Passphrase-Encrypted Key Storage

The user's WireGuard private key is their sole identity in ARFL. Losing it
means losing your bandwidth balance. Someone stealing it means they can
impersonate you. So we encrypt it.

### What We Use

- **Argon2id** for key derivation (passphrase → encryption key).
  Winner of the Password Hashing Competition (2015). Memory-hard, which
  means GPUs and ASICs can't brute-force it efficiently. Same category as
  bcrypt/scrypt but newer and stronger.

- **AES-256-GCM** for encryption. Authenticated encryption — encrypts AND
  detects tampering. If someone modifies the file or uses the wrong
  passphrase, decryption fails cleanly rather than producing garbage.

### Security Properties

1. Key is never stored in plaintext on disk.
2. File has 0600 permissions (owner-only) as defense in depth.
3. Each key file has a unique random salt — prevents rainbow table attacks.
4. Each encryption uses a unique random nonce — prevents replay attacks.
5. Wrong passphrase produces an error, not corrupted output.

### The Bitcoin Parallel

This is exactly how Bitcoin Core encrypts wallet.dat:
- Bitcoin: passphrase → key derivation → AES-256-CBC → encrypted privkeys
- ARFL:   passphrase → Argon2id → AES-256-GCM → encrypted WG privkey

We use GCM instead of CBC because GCM provides authentication (tamper
detection) that CBC lacks. This is a strictly stronger choice.

### What This Does NOT Protect Against

- Malware reading the key from memory while the client is running.
- Keyloggers capturing the passphrase as you type it.
- A compromised device (at that point, everything is lost).

For those threats, you need secure enclave / hardware key storage (Phase 4+).

**Defend it as:** "Private keys are encrypted at rest with AES-256-GCM,
keyed by Argon2id. Same security model as Bitcoin Core wallets. The key
never touches disk in plaintext."

---

## Decision 13: Split Default Route (0.0.0.0/1 + 128.0.0.0/1)

Instead of routing 0.0.0.0/0 through the tunnel (which would REPLACE the
default route), we add two /1 routes that together cover all IPs but are
more specific than the /0 default.

Why: The outer tunnel still needs to reach the entry node via the real
internet. If we replace the default route, the outer tunnel can't reach
its endpoint. The /1 split lets both coexist:

  /32 routes → real internet (entry node reachable)
  /1 routes  → tunnel (all other traffic)
  /0 route   → still exists but never wins

This is the standard VPN routing trick used by OpenVPN, WireGuard's
wg-quick, Tailscale, and every other VPN client.

**Defend it as:** "We use /1 split routes instead of replacing the default
route. This keeps the entry node reachable over the real internet while
tunnelling everything else. Same technique used by every major VPN client."

---

## Decision 14: Node IP Forwarding and NAT

Nodes need two kernel configurations to actually route traffic:

1. IP forwarding (sysctl net.ipv4.ip_forward=1): tells the kernel it's OK
   to route packets between interfaces. Without it, packets addressed to
   other machines are silently dropped.

2. NAT/MASQUERADE: rewrites the source IP on outbound packets from the
   private tunnel IP (10.x.x.x) to the node's public IP. Without it,
   remote servers can't reply because they don't know how to reach 10.x.x.x.

Both are set up on startup and cleaned up on shutdown. The node doesn't
leave stale iptables rules behind.

**Defend it as:** "IP forwarding and NAT are required for any router or
VPN node. We enable them on startup and clean them on shutdown. This is
standard Linux networking, not ARFL-specific."

---

## Decision 15: BIP-340 Schnorr Signatures (Not EC-Schnorr-DCRv0)

Nostr uses **BIP-340** Schnorr signatures — the same variant used by
Bitcoin Taproot. During development we initially used `dcrd/secp256k1`
which implements **EC-Schnorr-DCRv0** (a Decred-specific variant using
BLAKE-256). Signatures were valid but incompatible with any Nostr client.

We switched to `btcec/v2` from btcsuite, which implements actual BIP-340
with tagged SHA-256 hashes. This means:
- Our events are verifiable by any Nostr client (Damus, Amethyst, etc.)
- Our keypairs are valid Bitcoin Taproot identities
- A Nostr identity IS a Bitcoin identity — same curve, same signatures

**The lesson:** Not all Schnorr signatures are the same. The math is
identical, but the hash function and nonce derivation differ. Always
check which variant a library implements before trusting it.

**Defend it as:** "We use BIP-340 via btcec/v2 — the same Schnorr variant
as Bitcoin Taproot and Nostr. Our events are interoperable with the entire
Nostr ecosystem, and our identity keys are valid Bitcoin keys."

---

## Decision 16: Hub Vouching (v1) with Decentralisation Path

In v1, only hub-attested nodes appear in the network. The hub signs a
structured **attestation** proving it has verified the node. This IS
centralised — and that's intentional.

**Why accept centralisation?** Without a trust anchor, Sybil attacks are
trivial. Anyone can create 10,000 Nostr keys and flood fake node
announcements. A user connects to an attacker's "entry" and "exit" —
privacy broken. You can't have privacy without first solving identity.

**The decentralisation path:**

| Version | Trust Anchor | Who can be a node? |
|---------|-------------|-------------------|
| v1 | Single hub vouches | Hub-approved nodes only |
| v2 | Multiple competing hubs | Any hub can vouch |
| v3 | Federation deposit receipt | Lock sats as proof (Meyer's kind 30081 design) |
| v4 | Reputation + stake | Earned reputation over time |

**Key design:** The attestation is a signed struct with an `expires_at`
field and a `protocol` version string. The client checks "is this signed
by a key I trust?" In v1, that's one hub key. In v3, it's a federation
threshold signature. The verification interface stays the same — only
the trust source changes.

**Defend it as:** "Hub vouching is the training wheel. The bike is designed
so the training wheel unbolts cleanly. But shipping without training wheels
means users fall into Sybil attacks on day one."

---

## Decision 17: Structured Attestation with 6-Hour TTL

The hub attestation binds to SPECIFIC node metadata, not just a pubkey.
It includes:
- Protocol version ("arfl-node-attestation-v1")
- Hub pubkey, node Nostr pubkey, node WireGuard pubkey
- Operator ID (hub-assigned, prevents same-operator entry/exit)
- Allowed roles (entry/exit/both)
- Issued and expiry timestamps (6-hour TTL)

**Why 6 hours (not 24h or 7 days)?**
If a node is compromised, maximum 6 hours of damage. The re-attestation
is automated (node calls hub API, hub re-signs) so the operational
overhead is negligible — the node already pings every 60 seconds.

**Side effect:** If the hub goes down for 6+ hours, ALL nodes become
unattested and disappear. This is a FEATURE — if the hub is down, you
don't want stale nodes accepting traffic they can't settle.

**Why bind to specific metadata?**
Without binding, a signed pubkey is a "forever pass." A compromised
node could change its WireGuard key, endpoint, or role while keeping
the old attestation. Binding means: change anything → get a new
attestation → the hub decides if the change is allowed.

**Defend it as:** "Short-lived, metadata-bound attestations limit the
blast radius of compromise to 6 hours and prevent credential reuse
after key rotation or role changes."

---

## Decision 18: Operator ID for Real Diversity

"Different Nostr keys ≠ different operators." One person can run 10
nodes with 10 different keypairs. If the client just checks "entry
pubkey ≠ exit pubkey," a single operator could control both hops and
correlate traffic.

**Fix:** The hub assigns an `OperatorID` in the attestation. This is
a hub-controlled identifier that groups all nodes belonging to the same
physical operator. The client enforces:

```
entry.OperatorID ≠ exit.OperatorID
```

**Why hub-assigned, not self-declared?** Because a malicious operator
would just declare different IDs for each node. The hub knows who
registered each node (KYC, deposit address, IP range analysis) and
assigns the ID accordingly.

**Defend it as:** "Operator diversity is enforced through hub-assigned
operator IDs in the attestation, not self-reported metadata. This
prevents a single operator from controlling both hops of a session."

---

## Decision 19: Client Verifies Signatures (Trustless Discovery)

When the client calls the hub's discovery API, the hub returns raw
signed Nostr events + attestations — not just a plain NodeInfo list.
The client verifies every signature locally before trusting any node.

**Why?** A malicious hub could manipulate the node list:
- Omit honest nodes and only return colluding nodes
- Inflate capacity of preferred nodes
- Fabricate fake NodeInfo without real signatures

By returning signed events, the hub CANNOT lie. The client checks:
1. Event signature → really from this node?
2. Attestation signature → really from a trusted hub?
3. Node binding → do the event, attestation, and NodeInfo all match?

**The gap:** This doesn't prevent the hub from OMITTING nodes (returning
a subset). That's a transparency problem, solvable in v2+ with a
signed snapshot or Merkle tree of all attested nodes.

**Defend it as:** "The client verifies event and attestation signatures
independently. The hub serves the data but can't forge it. This is
trust-but-verify: the hub is convenient, not trusted."

---

## Decision 20: Nostr Relay Pool (Redundancy + Graceful Degradation)

Nodes publish to MULTIPLE Nostr relays simultaneously. The hub subscribes
to all of them and deduplicates (same event from 3 relays = processed
once).

**Why multiple relays?**
1. Availability: if relay A goes down, relay B still has the events
2. Censorship resistance: one relay can't suppress all announcements
3. Performance: geographically distributed relays reduce latency

**Relay list is configurable.** Default includes public relays (damus,
nos.lol). The operator can add their own relay for reliability with zero
code changes.

**Graceful degradation:** If 2 of 3 relays are down, the pool still
works with the one remaining relay. Only fails if ALL relays are
unreachable.

**Security hardening:**
- 64KB read limit on WebSocket prevents memory exhaustion DoS
- Malformed relay messages are logged and discarded, never crash the client
- Connection drops mark the relay as offline, don't affect other relays

**Defend it as:** "Publishing to multiple relays provides redundancy and
censorship resistance. The client degrades gracefully — it works with
any subset of relays that are online."

---

## Decision 21: Federation Deposit Receipt (Kind 30081) — Meyer's Design

Instead of hub vouching alone, the Fedimint federation can publish a
Nostr event (kind 30081) proving a node deposited sats. The federation's
pubkey is a threshold key (3-of-5 or 4-of-7), so no single member can
forge a receipt.

**Event structure:**
```json
{
  "kind": 30081,
  "pubkey": "<federation_threshold_pubkey>",
  "tags": [
    ["d", "<node_nostr_pubkey>"],
    ["deposit_sats", "50000"],
    ["deposit_confirmed_at", "<unix_timestamp>"],
    ["expires_at", "<unix_timestamp>"]
  ],
  "sig": "<threshold_schnorr_signature>"
}
```

**Why this matters:**
- Replaces hub vouching with economic proof (Sybil cost = 50k sats × N nodes)
- Federation is multi-party trust (no single point of control)
- Lives on Nostr relays (publicly auditable, censorship-resistant)
- `expires_at` forces periodic re-staking

**Implementation:** Reserved as kind 30081 in protocol constants.
`MinNodeDepositSats = 50000`. Implementation deferred to Phase 5.

**Defend it as:** "Federation deposit receipts replace hub vouching with
economic Sybil resistance. The cost to attack with 100 fake nodes is
5,000,000 sats. The receipt is a threshold signature — no single
federation member can forge it."

---

## Decision 22: Dynamic NodeInfo via Callback Function

The announcer takes a `func() types.NodeInfo` callback — not a static
`NodeInfo` struct. Every 60 seconds when it's time to announce, it calls
the function to get fresh data.

**Why?** A node's `Load` and `Capacity` change constantly as clients
connect and disconnect. If we passed a static struct at startup, every
announcement would report the same load as when the node first started.

```go
nodeInfoFn := func() types.NodeInfo {
    return types.NodeInfo{
        Load:     int(atomic.LoadInt32(&currentLoad)),
        Capacity: cfg.Capacity,
        ...
    }
}
announcer := discovery.NewAnnouncer(nodeKP, nodeInfoFn, att, pool)
```

This is the **closure pattern** — the function captures a reference to
the `currentLoad` variable. When the announcer calls `nodeInfoFn()` at
minute 5, it reads the CURRENT value, not the value at minute 0.

**Defend it as:** "Dynamic node info via closures ensures announcements
reflect real-time load. A static struct would advertise stale data and
mislead the hub's node index."

---

## Decision 23: Atomic Load Counter

The byte counter poller (goroutine A) writes `currentLoad`. The
announcer callback (goroutine B) reads it. They run concurrently with
no shared lock.

We use `sync/atomic` instead of a mutex:

```go
var currentLoad int32
atomic.StoreInt32(&currentLoad, activePeers)  // writer
atomic.LoadInt32(&currentLoad)                 // reader
```

**Why atomic, not a mutex?**
- This is a single integer, not a complex struct. Atomics are designed
  for exactly this — single-value concurrent access.
- No lock contention. A mutex would block the poller while the announcer
  reads (or vice versa). With atomics, both proceed without waiting.
- Go's race detector understands atomics — it won't flag false positives.

**When to use a mutex instead:** When you need to read-modify-write
multiple fields atomically (e.g., "read load AND capacity AND update
both"). For single values, atomics are simpler and faster.

**Defend it as:** "Atomic operations for single-value concurrent counters.
No lock contention, no deadlock risk, race-detector clean."

---

## Decision 24: Hub Daemon Architecture (3 Goroutines)

The hub daemon runs three concurrent tasks coordinated by `context.Context`:

```
main goroutine          → setup, then waits for SIGINT/SIGTERM
go processAnnouncements → reads from Nostr relay subscription channel,
                          validates events, updates the node index
go pruneOffline         → every 60s, marks stale nodes as offline
go api.ListenAndServe   → serves GET /nodes and GET /health to clients
```

**Why separate goroutines?**
1. The relay subscription is a blocking read loop — it can't share a
   goroutine with the pruner or the API.
2. The pruner runs on a timer (every 60s). If it ran in the same
   goroutine as the event processor, a slow validation step would
   delay pruning.
3. The HTTP server is inherently concurrent — Go's `http.Server`
   spawns a goroutine per request internally.

**Shutdown order matters:** When Ctrl-C is pressed:
1. `cancel()` fires, which triggers `ctx.Done()` in all goroutines
2. Event processor stops reading from the relay
3. Pruner stops its ticker
4. `server.Shutdown(ctx)` drains in-flight HTTP requests (5s timeout)
5. `pool.Close()` disconnects from Nostr relays
6. Process exits

This is the same pattern as the node daemon (Decision 8), extended
with the relay subscriber and pruner.

**Defend it as:** "Three goroutines for three independent concerns:
event processing, periodic pruning, and API serving. Context-based
shutdown ensures clean teardown in the right order."

---

## Decision 25: Client Dual-Mode (Static Session vs Dynamic Discovery)

The client supports two modes:

**Phase 1 (static):** `arfl-client --session session.json`
- Reads entry/exit node info from a JSON file
- No network discovery, no hub contact
- Used for testing and manual configuration

**Phase 2 (dynamic):** `arfl-client --discover http://hub:8080 --hub-pubkeys <hex>`
- Calls the hub's discovery API to get verified node list
- Verifies all signatures client-side (trustless)
- Selects entry + exit pair with operator diversity
- No session file needed

**Why keep both?** Backward compatibility and debuggability. If the hub
is down, or during development, you can still connect manually using a
session file. The static mode is also useful for integration tests where
you control both nodes directly.

The `--discover` flag takes the hub URL. The `--hub-pubkeys` flag is a
comma-separated list of trusted hub pubkeys. The client will ONLY accept
nodes attested by one of these pubkeys.

**Defend it as:** "Dual-mode client: static for testing and fallback,
dynamic for production. Both paths end up creating the same nested
WireGuard tunnels — only the node selection differs."

---

## Decision 26: Config Evolution (Backward Compatible)

The `NodeConfig` struct grew from 8 fields (Phase 1) to 16 fields
(Phase 2). All new fields are optional — a Phase 1 config file still
works. The node just won't announce to Nostr:

```go
if cfg.NostrPrivkey != "" && len(cfg.Relays) > 0 {
    // Phase 2: start announcer
} else {
    log.Println("[node] Nostr discovery disabled")
}
```

**Why not a separate config file?** One config file per daemon is
simpler to manage. Node operators edit `node.json`, add the new
fields when they're ready, and the node picks them up on restart.

**New configs added in Phase 2:**
- `NodeConfig`: nostr_privkey, hub_pubkey, attestation, relays,
  endpoint, upload_mbps, download_mbps, capacity
- `HubConfig`: nostr_privkey, listen_addr, relays (new struct)
- `ClientConfig`: hub_url, hub_pubkeys (new struct)

**Defend it as:** "Config grows additively. Old configs keep working.
New features activate when their config fields are present. No migration
scripts, no breaking changes."

---

## Decision 27: readLoop takes conn/ctx as parameters (not from struct)

**Date:** 2026-06-06
**Phase:** Post Phase 2 (bugfix)

**Problem:** `readLoop()` goroutine captured `r.conn` by re-acquiring the mutex.
Race window: `connect()` releases lock → `close()` acquires lock, nils `r.conn` →
`readLoop()` acquires lock, captures nil → `conn.Read()` panics.

**Fix:** Pass `conn` and `ctx` as function parameters from `connect()`'s local
scope while the lock is still held. Go evaluates goroutine arguments before
scheduling, so the values are guaranteed non-nil.

**Why not just a nil check?** A nil check is a band-aid. Passing parameters
*structurally eliminates* the race — there's no code path where conn can be nil.
Structural > defensive when you can get it.

**Defend it as:** "We don't trust scheduling. The goroutine receives its
dependencies as arguments, not by reading shared state. The race window
doesn't exist because the values are captured at creation time."

---

## Decision 28: readLoop ownership check prevents stale goroutine interference

**Date:** 2026-06-06
**Phase:** Post Phase 2 (bugfix)

**Problem:** When readLoop's defer sets `r.connected = false`, a stale goroutine
from a previous connection could mark a freshly-reconnected relay as offline.

**Fix:** readLoop defer checks `r.conn == conn` before updating state. If the
active connection has changed (reconnect happened), the old goroutine is a ghost
and should not touch relay state.

**Defend it as:** "Goroutine identity. Each readLoop knows which connection it
owns. If the relay has moved on to a new connection, the old goroutine cleans
up silently without corrupting the new state."

---

## Decision 29: truncateID for safe eventID logging

**Date:** 2026-06-06
**Phase:** Post Phase 2 (bugfix)

**Problem:** `eventID[:8]` in handleMessage panics if a malicious relay sends an
OK message with a short event ID (e.g., `["OK","bad",false,"rejected"]`).

**Fix:** `truncateID()` helper checks length before slicing. Same STRIDE/Tampering
category as our 64KB read limit — never trust relay-supplied data.

**Defend it as:** "Every field from an external source gets bounds-checked before
use. This is the same principle as the 64KB read limit — we assume relays are
adversarial."

---

## Decision 30: Append-only settlement ledger (source of truth)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "If SQLite is corrupted, can you reconstruct every sat?"

**Decision:** The settlement ledger is append-only. No UPDATE, no DELETE on
financial records. Balances are derived by summing events, never stored
directly. Like a blockchain — replay events to get state.

**Why not mutable balances?** A bug that sets balance = 0 loses money
silently. An append-only log is auditable — you can always replay from
the beginning. Every sat has a traceable path from purchase to payout.

**Defend it as:** "We never update financial records. We append new events.
If something goes wrong, we replay the log. You can't lose what you
can trace."

---

## Decision 31: Atomic tickets (no partial spend)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "If a user consumes 20MB of a 100MB ticket, what happens
to the remaining 80MB?"

**Decision:** The ticket is destroyed. 80MB is forfeit. A ticket is either
fully consumed or not consumed at all.

**Why waste bandwidth?** Because:
1. Partial-spend accounting is an entire class of bugs (what if a node
   claims 60MB used, client says 20MB? who's right?)
2. Blind signatures in Phase 4 require atomic tokens — you can't "partially
   unblind" a Chaumian token
3. Maximum waste per ticket = 100MB = ~0.5 sats at current rates
4. The simplicity pays for itself in zero accounting disputes

**Defend it as:** "A ticket is like a bus token. You use it or you don't.
There's no half-ride. The waste is bounded and trivial compared to the
accounting complexity it eliminates."

---

## Decision 32: Configurable ticket size (100MB default)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "Is 100MB chosen because of measurement or because it
feels right?"

**Decision:** 100MB is the starting default, stored as a protocol constant.
It's configurable without code changes. Real usage data will tell us if
it needs adjustment.

**Trade-off:** Smaller tickets = less waste but more redemption overhead
(more round-trips to Hub). Larger tickets = less overhead but more waste
on short sessions. 100MB is a hypothesis — we'll measure and adjust.

**Defend it as:** "We picked a reasonable default and made it tunable.
We'll adjust based on real data, not guesswork."

---

## Decision 33: Payout state machine (money never in limbo)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "If keysend fails, where does that money live?"

**Decision:** Every payout has exactly one state: pending → paid | failed →
retrying → paid | failed. Max 3 retries before manual review flag.

**Why explicit states?** Because "the payment probably went through" is
how you lose sats. LND's SendPaymentV2 can return PENDING, SUCCEEDED,
FAILED, or IN_FLIGHT. Each maps to a payout state. No ambiguity.

**Defend it as:** "Every sat has a state. If you ask 'where is this money?'
we can answer with one database query. Always."

---

## Decision 34: Idempotent settlement (crash-safe)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "What happens if the Hub crashes during settlement?"

**Decision:** Settlement operations are keyed by (settlement_period, node_id).
Running settlement twice for the same period produces the same result.
INSERT OR IGNORE, not INSERT. The Hub can crash and restart safely.

**Defend it as:** "You can pull the power cord during settlement. When it
comes back, it picks up where it left off. No double-pays, no missed pays."

---

## Decision 35: Signed usage reports (STRIDE/Spoofing)

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "What exact data must a node submit before it gets paid?"

**Decision:** Minimum evidence = {session_id, ticket_id, node_role,
bytes_reported, timestamp, node_signature}. The BIP-340 signature over
these fields prevents a node from forging reports for another node.

**Why sign reports?** Without signatures, a compromised Hub or MITM could
fabricate usage reports. With signatures, the node's Nostr keypair (which
we already have from Phase 2) proves authorship. No new key material needed.

**Defend it as:** "Nodes sign their work. If there's a dispute, we check
the signature. It's the same key they use for Nostr — one identity,
one reputation."

---

## Decision 36: Credential interface isolates Phase 4 migration

**Date:** 2026-06-06
**Phase:** Phase 3 (pre-implementation)

**Challenge:** "What is the biggest thing that could force a rewrite?"

**Decision:** The credential issuance interface is the ONLY component that
changes in Phase 4. Ticket redemption, settlement, payouts — all stay
identical. If Phase 4 requires changes downstream, the abstraction failed.

**Interface boundaries:**
- CredentialIssuer: creates tickets (Phase 3: HMAC, Phase 4: blind sig)
- CredentialVerifier: validates tickets (Phase 3: HMAC check, Phase 4: blind verify)
- Everything else: unchanged

**Defend it as:** "We drew the line at issuance and verification. Everything
downstream — redemption, settlement, payouts — is phase-agnostic. The
blind signature migration is a surgical swap, not a rewrite."

## Decision 37: Length-prefixed payload for usage report signatures

**Date:** 2026-06-06
**Phase:** Phase 3

**Challenge:** Simple delimiter-joined payloads (e.g. `a|b|c`) are ambiguous
when field values contain the delimiter. An attacker could craft two different
report structs that produce identical payloads.

**Decision:** Use length-prefixed fields: `"7:sess-01"` — each field's byte
length is prepended, making the encoding unambiguous regardless of content.

**Defend it as:** "Length-prefixed encoding is injection-proof by construction.
No field content can ever collide with another field's encoding, even if it
contains pipe characters, nulls, or any other byte."

## Decision 38: Domain-separated usage report signatures

**Date:** 2026-06-06
**Phase:** Phase 3

**Challenge:** The same BIP-340 Schnorr key signs Nostr events, usage reports,
and potentially other messages. Without domain separation, a valid signature
on one message type could be replayed as another (cross-protocol attack).

**Decision:** Prefix the usage report payload with `ARFL_USAGE_REPORT_V1`.
Full signed payload: `ARFL_USAGE_REPORT_V1|7:sess-01|...`

**STRIDE mapping:** Spoofing — prevents cross-protocol signature replay.

**Defend it as:** "A Nostr event signature can never be mistaken for a usage
report signature, and vice versa. The domain prefix makes them cryptographically
incompatible. The version suffix (V1) allows future format upgrades."

## Decision 39: Broadcast subscriber model for Lightning mock

**Date:** 2026-06-06
**Phase:** Phase 3

**Challenge:** Using a single shared channel for all subscribers means they
compete for events instead of each receiving every settlement. This hides
bugs in settlement flows that depend on multiple consumers.

**Decision:** Track subscribers in a `map[chan *Invoice]struct{}`. Settlement
broadcasts a copy to every registered channel. Channels are cleaned up on
context cancellation.

**Defend it as:** "Real LND broadcasts to all gRPC subscribers. Our mock
mirrors that behavior — every subscriber independently receives every
settlement notification. This catches bugs where multiple settlement
consumers accidentally interfere with each other."

## Decision 40: SignRaw/VerifyRaw for non-Nostr BIP-340 signatures

**Date:** 2026-06-06
**Phase:** Phase 3

**Challenge:** Nostr events have a specific signing flow (compute ID → sign ID).
Usage reports and other protocol messages need BIP-340 signatures on arbitrary
payloads, not Nostr event IDs.

**Decision:** Added `SignRaw(kp, message)` and `VerifyRaw(pubkeyHex, message,
sig)` to the nostr package. These hash the message with SHA-256 before signing
(BIP-340 requires 32-byte input). Reuses the same btcec/v2 Schnorr primitives.

**Defend it as:** "Same key, same algorithm, different signing contexts. Nostr
events sign their computed ID. Usage reports sign their length-prefixed payload.
Domain separation ensures they can't cross-contaminate."

## Decision 41: Keysend for node payouts (not BOLT11)

**Date:** 2026-06-07
**Phase:** Phase 3

**Challenge:** Nodes are passive bandwidth providers. They don't generate
invoices. How does the Hub pay them?

**Decision:** Use Lightning Keysend (spontaneous payment by public key).
No invoice required — the Hub pushes sats directly to the node's pubkey.
Added `Keysend(ctx, destPubkey, amountSats)` to the Client interface.

**STRIDE mapping:** Spoofing — Phase 3 assumes Lightning pubkey = Nostr
pubkey. Phase 4 should add pubkey mapping with verification.

**Defend it as:** "Keysend is the only option for paying someone who hasn't
given you an invoice. The node just runs — it doesn't need to actively request
payment. The Hub pushes earnings to them."

## Decision 42: in_flight payout status for crash safety

**Date:** 2026-06-07
**Phase:** Phase 3

**Challenge:** If the Hub crashes after sending a Lightning payment but before
recording success, the payout is stuck forever in 'pending'. Retrying might
double-pay.

**Decision:** Added `in_flight` status to the payout state machine:
`pending → in_flight → paid | failed → retrying → in_flight → paid | failed`

Before every network call, mark as `in_flight`. If the Hub restarts and finds
`in_flight` payouts, they require manual reconciliation against the Lightning
node (check payment status). Never blindly retry an `in_flight` payment.

**Defend it as:** "The in_flight state is the crash boundary. It says: 'we
sent money, but we don't know the outcome yet.' That's fundamentally different
from 'we never tried' (pending) or 'it failed' (failed)."

## Decision 43: Aggregate settlement entries by (period, node)

**Date:** 2026-06-07
**Phase:** Phase 3

**Challenge:** The database enforces UNIQUE(settlement_period, node_pubkey).
If we tried to insert one entry per session per node, INSERT OR IGNORE would
silently skip all but the first session — severe underpayment.

**Decision:** Aggregate all sessions for a node within a period FIRST, then
insert exactly one settlement entry per node. The entry sums: total billable
bytes, total sats earned, entry/exit byte totals, tickets redeemed.

**Defend it as:** "Aggregation happens in code, insertion is atomic and
idempotent. Each node gets exactly one settlement entry per period — no
duplicates, no underpayment."

## Decision 44: Pre-flight budget check before payouts

**Date:** 2026-06-07
**Phase:** Phase 3

**Challenge:** The economic invariant says `total payouts ≤ total purchases`.
If we check AFTER sending payments, funds are already gone.

**Decision:** Before creating/sending payouts, compute:
`committed (paid + pending + in_flight + retrying) + proposed ≤ purchased`
If the proposed payout would exceed the budget, halt settlement. This is a
hard stop — no exceptions.

**Defend it as:** "We enforce the economic invariant BEFORE spending money,
not after. If the math doesn't work out, no payout is attempted. This
guarantees we can never pay more than we received."

## Decision 45: Cumulative reporting with MAX aggregation

**Date:** 2026-06-07
**Phase:** Phase 3

**Challenge:** Nodes may submit multiple usage reports per session (periodic
reporting, network retries, duplicate submissions). How to aggregate?

**Decision:** bytes_reported is cumulative. Settlement uses MAX(bytes_reported)
per role per session. This means:
- Duplicates are harmless (same value = same MAX)
- Partial reports are superseded by later ones
- No risk of double-counting

**Defend it as:** "MAX is the only aggregation function that's idempotent
under duplicates. If a node reports 50MB, then 80MB, then 80MB again, the
answer is 80MB — not 210MB."

---

## Decision 46: SettleInvoice is idempotent (STRIDE/DoS)

**Date:** 2026-06-07
**Context:** If Hub crashes after marking an invoice settled but before issuing
tickets, subsequent settlement events would fail because SettleInvoice errored
on already-settled invoices. Tickets would never be issued for a paid invoice.

**Decision:** SettleInvoice now returns nil on already-settled invoices (idempotent).
It only errors on not-found or invalid transitions (e.g., settling an expired invoice).
onInvoiceSettled checks ticket existence FIRST, before calling SettleInvoice, so the
crash-recovery path works: tickets missing + invoice already settled → still issues tickets.

**STRIDE mapping:** Denial of Service — crash permanently loses paid tickets.

**Defend it as:** "Any operation involving money must be crash-safe. If the Hub can crash
between two steps, the second step must be retryable without the first step blocking it."

---

## Decision 47: Payout state transitions return errors on invalid state

**Date:** 2026-06-07
**Context:** MarkPayoutInFlight, MarkPayoutPaid, and MarkPayoutFailed silently
returned nil when 0 rows were affected (payout not in expected state). Callers
proceeded with network calls thinking the state transition succeeded.

**Decision:** All three methods now check RowsAffected and return an error when
the transition didn't actually happen. This prevents:
- Double-sending payouts (MarkPayoutInFlight from wrong state)
- False success records (MarkPayoutPaid from non-in_flight)
- Stuck payouts with no error trace (MarkPayoutFailed silently failing)

**STRIDE mapping:** Elevation of Privilege — silent no-ops let callers bypass the
state machine and proceed as if a transition occurred.

**Defend it as:** "In a payment system, a state machine that silently ignores invalid
transitions is worse than one that errors. Silent failures create money in ambiguous
states — the one thing our economic invariants forbid."

---

## Decision 48: PaymentInFlight leaves payout in in_flight state

**Date:** 2026-06-07
**Context:** sendPayout treated any non-Succeeded Lightning result as a failure,
including PaymentInFlight. An in-flight payment may still settle later on the
Lightning Network, so marking it failed and retrying risks double-payment.

**Decision:** When Keysend returns PaymentInFlight, the payout stays in in_flight
state. It is NOT marked failed, NOT retried automatically. Manual reconciliation
is required. GetRetryablePayouts only returns 'failed' payouts, not 'in_flight'.

**STRIDE mapping:** Tampering — double-payment violates invariant 2 (total payouts ≤ purchases).

**Defend it as:** "The in_flight state IS the crash-safety boundary. Treating it as
a failure by moving to a different state defeats the purpose. Unknown state → do nothing
until a human confirms."

---

## Decision 49: MarkPayoutFailed errors are surfaced

**Date:** 2026-06-07
**Context:** sendPayout called MarkPayoutFailed but ignored its return value.
If the DB write failed, the payout remained stuck in in_flight with no error
recorded and no way for retry logic to find it.

**Decision:** All MarkPayout* call sites now check the error. On failure, a CRITICAL
log is emitted with both the original error and the DB error. This ensures stuck
payouts are at least visible in logs for operational alerting.

**STRIDE mapping:** Repudiation — payout stuck in untracked state without audit trail.

**Defend it as:** "Ignoring the error on a financial state transition is the same as
not having the state machine. Every write to the ledger must be confirmed."

---

## Decision 50: Credential key requires --dev flag (STRIDE/Spoofing)

**Date:** 2026-06-07
**Context:** parseCredentialKey fell back to a deterministic, hard-coded key
when no credential_key was configured. Anyone who knows this string can forge
valid tickets, completely breaking the payment system.

**Decision:** Missing credential_key now fatals with an explicit error message.
The --dev flag is required to opt into the insecure key, making the dangerous
behavior impossible to trigger accidentally in production.

**STRIDE mapping:** Spoofing — attacker forges tickets with known dev key.

**Defend it as:** "Security defaults must be secure. Convenience features that
weaken security must require an explicit opt-in that cannot happen accidentally."

---

## Decision 51: Payouts table has immutable field trigger

**Date:** 2026-06-07
**Context:** The payouts table blocked DELETE via trigger but allowed UPDATE on
financial fields (amount_sats, node_pubkey, settlement_entry_id). A bug or manual
SQL tampering could rewrite payout history.

**Decision:** Added payouts_immutable_fields trigger blocking changes to amount_sats,
node_pubkey, and settlement_entry_id. Only status, payment_hash, attempt_count,
last_error, and updated_at may change (the state machine fields).

**STRIDE mapping:** Tampering — rewriting payout history to redirect or inflate payments.

**Defend it as:** "Every ledger table gets the same treatment: financial fields are
immutable after creation. The trigger is the enforcement layer that doesn't depend
on application code being correct."

---

## Decision 52: Rate limit aligned to economic invariant 15

**Date:** 2026-06-07
**Context:** Code enforced 10 requests per minute per IP. Economic invariant 15
specified 10 per hour. The code was 60x stricter than documented.

**Decision:** Changed rate window from 1 minute to 1 hour, matching the invariant.

**Defend it as:** "Code and documentation must agree. When they diverge, the invariant
document is the source of truth — it's what was designed and reviewed."

---

## Decision 53: GetSessionUsageSummaries uses correct GROUP BY

**Date:** 2026-06-07
**Context:** Subqueries grouped by session_id but selected non-aggregated ticket_id
and node_pubkey. SQLite's behavior with non-aggregated columns returns an arbitrary
row from the group, which can mismatch the MAX(bytes_reported) value.

**Decision:** Added ticket_id and node_pubkey to GROUP BY. In the normal case
(one entry node per session, one ticket per session) this changes nothing. In
adversarial cases (multiple nodes claiming entry for the same session) they are
treated as separate rows, which is correct.

**Defend it as:** "Standard SQL requires all non-aggregated columns to be in GROUP BY.
Relying on SQLite's arbitrary-row extension is a portability bug and a correctness
bug when adversarial inputs are possible."

---

## Decision 54: Atomic ticket insertion via transaction

**Date:** 2026-06-07
**Context:** Ticket insertion looped over individual INSERT calls. A crash after
inserting 3 of 10 tickets left a partial set. On restart, CountTickets returned
3 > 0 and skipped — client got 3/10 tickets for a fully-paid invoice.

**Decision:** InsertTicketsBatch wraps all ticket INSERTs in a single SQLite
transaction. Crash mid-insert rolls back all inserts (count stays 0), so the
next onInvoiceSettled call re-issues all 10 tickets.

**STRIDE mapping:** Denial of Service — partial ticket set denies paid bandwidth.

**Defend it as:** "Ticket issuance is an atomic operation. Either all tickets exist
or none do. Transactions give us that guarantee for free."

---

## Decision 55: issueMu mutex serializes ticket issuance

**Date:** 2026-06-07
**Context:** Concurrent settlement events for the same invoice could both see
CountTickets==0 and both issue tickets, creating 2N tickets for 1 payment.

**Decision:** onInvoiceSettled acquires issueMu before checking/issuing. Combined
with the transactional InsertTicketsBatch, this prevents both the concurrency
race and the partial-insert crash.

Single Hub instance: mutex sufficient. Multi-Hub: would need distributed lock
(out of scope for Phase 3).

**STRIDE mapping:** Tampering — double-issuance violates invariant 4 (ticket redeemed once).

**Defend it as:** "The mutex serializes the check-then-insert. The transaction
makes the insert atomic. Together they close both the race and the crash gap."

---

## Decision 56: HAVING filters reject adversarial multi-node sessions

**Date:** 2026-06-07
**Context:** A session with reports from two different entry nodes could create
cross-products in the settlement query, overpaying or misattributing usage.

**Decision:** GetSessionUsageSummaries uses HAVING COUNT(DISTINCT node_pubkey)=1
AND COUNT(DISTINCT ticket_id)=1 per role. Sessions with conflicting reports are
silently filtered out — they indicate adversarial or buggy input.

**STRIDE mapping:** Elevation of Privilege — attacker injects fake entry reports
to redirect payment to their node.

**Defend it as:** "A session has exactly one entry node and one exit node. If the
data says otherwise, the data is adversarial. Reject, don't guess."

---

## Decision 57: Budget tracks committed from InsertPayout, not sendPayout

**Date:** 2026-06-07
**Context:** The executePayouts loop only incremented the local `committed`
counter on successful payouts. In-flight payouts (sats already left the node)
were not counted, allowing the budget guard to under-count and overspend.

**Decision:** `committed += entry.AmountSats` is applied immediately after
InsertPayout, before sendPayout. This ensures the budget guard accounts for
all payouts created in the current run, regardless of their outcome.

**STRIDE mapping:** Tampering — budget under-count allows total payouts > purchases.

**Defend it as:** "Sats are committed the moment we create the payout record, not
when the Lightning payment succeeds. The budget guard must reflect this."

---

### Decision 58: MaxBytesReader on API endpoints

**Context:** DoS via oversized JSON bodies flagged by code reviewer.

**Decision:** `/purchase` uses `http.MaxBytesReader(w, r.Body, 1024)` (1KB),
`/report` uses `http.MaxBytesReader(w, r.Body, 4096)` (4KB). Both are generous
for their payloads but prevent memory exhaustion from multi-GB bodies.

**STRIDE mapping:** Denial of Service — unbounded request bodies.

---

### Decision 59: Invoice forward-transition trigger

**Context:** DB schema enforced immutable financial fields but not status direction.
A bug could reopen a settled/expired invoice.

**Decision:** `invoices_forward_transition` trigger: only invoices with `status='open'`
can have their status changed. Once settled or expired, the status is immutable at
the DB level (not just application level).

**STRIDE mapping:** Tampering — reverting settlement status.

---

### Decision 60: Exit-side ticket_id enforcement in query

**Context:** Exit subquery didn't validate ticket_id. Entry and exit could report
different tickets for same session, allowing cross-pollination.

**Decision:** Exit subquery selects ticket_id with HAVING COUNT(DISTINCT ticket_id)=1.
JOIN uses both session_id AND ticket_id. Mismatched tickets = no settlement.

**STRIDE mapping:** Spoofing — adversarial nodes mixing tickets across sessions.

---

### Decision 61: Tier-aware computePayoutSats using invoice data

**Context:** computePayoutSats hardcoded 1gb rate. 10gb/50gb tiers have volume discounts
(20%/40% cheaper per byte). Using 1gb rate overstates earnings for discount tiers.

**Decision:** computePayoutSats takes (billableBytes, invoiceAmountSats, invoiceBytesAllowed).
Uses the originating invoice's rate, making it immune to tier config changes.
Sats are computed per node share (billable/2), accumulated per node.

**STRIDE mapping:** Elevation — overstating node earnings breaks budget guard.

---

### Decision 62: Context cancellation leaves payout in_flight

**Context:** Keysend returning context.Canceled/DeadlineExceeded means payment outcome
is unknown — the node may have been paid but RPC response was lost.

**Decision:** After MarkPayoutInFlight, context errors are treated like PaymentInFlight:
payout stays in in_flight, logged for reconciliation. Pre-flight ctx.Err() check
prevents entering in_flight when context is already canceled.

**STRIDE mapping:** Tampering — marking unknown-outcome as "failed" enables double-pay on retry.

---

### Decision 63: UNIQUE constraint on payouts(settlement_entry_id)

**Context:** Without uniqueness, concurrent settlement cycles or bugs could create
duplicate payout rows for the same settlement entry, risking double payment.

**Decision:** UNIQUE INDEX on settlement_entry_id. One payout per entry enforced at DB layer.
RetryFailedPayouts reuses same row (MarkPayoutRetrying), so UNIQUE is compatible.

**STRIDE mapping:** Tampering — duplicate payout creation.

---

### Decision 64: go-sqlite3 as direct dependency

**Context:** go-sqlite3 is blank-imported in store.go (CGo driver registration).
go.mod marked it indirect, which can confuse tooling and go mod tidy.

**Decision:** Moved to direct require block. Matches actual usage pattern.

---

## Phase 4 — Blind Signatures

---

### Decision 65: RSA blind signatures via cryptoballot/rsablind

**Context:** Need Chaumian ecash-style unlinkability for bandwidth tokens.
Hub signs blinded messages without seeing the token secret, preventing
buyer-session linkage.

**Decision:** Full-Domain-Hash RSA blind signatures using cryptoballot/rsablind
(2048-bit RSA, 1536-bit FDH). Not production-audited — acceptable for PoC
and grant applications. Production requires formal audit.

**Why not Cashu NUTs?** ARFL's product is bandwidth, not ecash. Cashu
compatibility would add complexity without benefit. We use the same
cryptographic primitive but with our own wire format.

---

### Decision 66: Denomination-keyed model (key_id → bytes_per_token)

**Context:** Need to encode bandwidth value into tokens without revealing it
during signing (Hub is blind to content).

**Decision:** Each RSA signing key maps to a fixed denomination via immutable
config. key_id "key-100mb" always means 100,000,000 bytes. The mapping MUST NOT
change after tokens are issued — changing it would alter the value of outstanding
tokens.

**Trade-off:** Less flexible than encoding denomination in the signed message,
but simpler and avoids the need for denomination-in-message verification.
Matches the Cashu model.

---

### Decision 67: System model is online bounded-risk authorization

**Context:** Pure offline ecash would allow unlimited double-spending across
nodes. Pure online ecash would require Hub connectivity for every byte.

**Decision:** Hybrid model — nodes verify signatures locally (offline-capable),
then call Hub /spend for double-spend check (online requirement). Bounded risk:
during Hub unavailability, each token can be replayed to at most N offline nodes,
capped at 100MB × N per token. This is explicitly stated in the architecture.

**Grant narrative:** "Online bounded-risk authorization for bandwidth" — NOT
"offline anonymous ecash." Reviewers will assume the latter unless stated.

---

### Decision 68: Payment preimage as /redeem authentication

**Context:** Need to prove the caller paid the invoice without linking identity.

**Decision:** POST /redeem requires the payment preimage. SHA256(preimage) must
match a payment_hash with a settled entitlement. Only the payer knows the
preimage (Lightning protocol guarantee), so this is proof-of-payment without
identity.

**Why not payment_hash?** Anyone can observe a payment_hash on the network.
The preimage is a secret revealed only to the payer upon successful payment.

---

### Decision 69: Atomic entitlement consumption before signing

**Context:** If signing fails after tokens are consumed, those tokens are lost.
If tokens are consumed after signing, a crash could lead to free tokens.

**Decision:** Consume first, sign second. Tokens consumed but unsigned is a
loss scenario that requires manual remediation (contact support). Tokens signed
but unconsumed would be free bandwidth — worse outcome. The "consume then sign"
ordering is safer.

**CRITICAL failure mode:** If signing fails after consumption, log at CRITICAL
level and include "tokens consumed — contact support" in the error response.

---

### Decision 70: Nonce + request_hash idempotency for /redeem

**Context:** Network failures during /redeem could cause the client to retry.
Without idempotency, retries would consume additional tokens.

**Decision:** Each redemption has a client-provided nonce. The Hub stores
(nonce, request_hash, blind_signatures). On retry:
- Same nonce + same request → return cached signatures (no token consumption)
- Same nonce + different request → 409 Conflict

The request_hash = SHA256(key_id + "|" + join(blinded_messages, "|")).

---

### Decision 71: Token ID derivation with domain separation

**Context:** Token IDs must be unique across key versions, protocol versions,
and denomination keys.

**Decision:** SHA256("ARFL|v1|{key_id}|{token_secret_hex}"). The domain prefix
prevents cross-version collisions, key_id prevents cross-denomination collisions,
and token_secret ensures uniqueness per token.

---

### Decision 72: Separated /redeem from payment (two-step redemption)

**Context:** The rubber-duck review identified that combining payment
confirmation, entitlement tracking, and blind signing in one endpoint
creates fragility.

**Decision:** POST /purchase creates invoice + (on settlement) entitlement.
POST /redeem consumes entitlement → blind-signs. Financial logic and
cryptographic logic are isolated. The /redeem endpoint does only cryptographic
work after the entitlement guard.

---

### Decision 73: /spend returns first_spend flag (not error on double-spend)

**Context:** A double-spend attempt could be malicious (replay attack) or
benign (node retrying after network failure).

**Decision:** POST /spend always returns 200 with {first_spend: bool}. The
node decides how to act: first_spend=true → serve traffic,
first_spend=false → reject or investigate. This avoids the Hub making
policy decisions for nodes.

---

### Decision 74: EnableBlindSignatures opt-in method

**Context:** Phase 3 HMAC tickets and Phase 4 blind signatures must coexist.
Not all deployments will have blind sig keys.

**Decision:** PurchaseAPI.EnableBlindSignatures(mint, verifier, defaultKeyID)
registers /redeem and /spend routes. If not called, these routes don't exist.
The settlement listener creates entitlements only when blindMint != nil.

---

### Decision 75: Mock Lightning client generates preimage→hash pairs

**Context:** The /redeem endpoint authenticates via SHA256(preimage) == payment_hash.
The mock was generating random hashes without corresponding preimages.

**Decision:** MockClient.CreateInvoice now generates a random 32-byte preimage,
computes SHA256(preimage) as the payment_hash, and stores both. GetPreimage()
returns the preimage for end-to-end testing. This properly simulates the
Lightning payment flow.

---

### Decision 76: Settlement listener creates entitlements atomically

**Context:** When an invoice is settled, the Hub needs to create both Phase 3
tickets and Phase 4 entitlements.

**Decision:** onInvoiceSettled creates the entitlement after ticket insertion
(Step 6). Idempotent: if the entitlement already exists (concurrent/retry),
the duplicate insert is caught and logged. The entitlement ID is
"ent-{payment_hash}" for deterministic deduplication.

---

### Decision 77: STRIDE test coverage for blind sig paths

**Context:** Grant reviewers (HRF, OpenSats) value security rigor. STRIDE
analysis is uncommon in crypto protocol implementations.

**Decision:** 14 STRIDE tests covering:
- Spoofing: fake preimage, forged signature
- Tampering: tampered token secret, oversized body
- Repudiation: redemption audit trail, spent token audit trail
- DoS: message count limit
- Elevation: cross-key redemption, entitlement overdraw, cross-key spend
- Concurrent: no overdraw under concurrent redeem, exactly-one first_spend
- Info disclosure: payment hash not leaked in errors
- Replay: nonce reuse across payments blocked
