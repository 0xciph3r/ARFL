# ARFL Architecture — Phase 0 Spike

## 1. Nested WireGuard Two-Hop Design

### Why Nested?

A naive "client → entry → exit → internet" forwarding model lets the entry node
decrypt and inspect packets, seeing destination IPs. This breaks the core privacy
property.

Instead, ARFL uses **nested WireGuard tunnels**: the client builds an inner tunnel
to the exit node that is carried *inside* an outer tunnel to the entry node. The
entry sees only encrypted blobs. The exit sees only a tunnel IP, never the client's
real address.

```
┌─────────────────────────────────────────────────────────────────────┐
│                         CLIENT DEVICE                               │
│                                                                     │
│  App traffic ──► wg-inner (to exit) ──► wg-outer (to entry) ──►    │
│                  10.200.0.2              10.100.0.2          eth0   │
└─────────────────────────────────────┬───────────────────────────────┘
                                      │ client real IP
                                      ▼
┌─────────────────────────────────────────────────────────────────────┐
│                       ENTRY NODE                                    │
│                                                                     │
│  eth0 ◄── WireGuard outer ──► wg-entry ──► NAT ──► eth0            │
│  :51820       decrypt outer       10.100.0.1        forward to exit │
│                                                                     │
│  Sees: client real IP + encrypted inner WG packets to exit IP       │
│  Cannot see: destination websites, DNS queries, any cleartext       │
└─────────────────────────────────────┬───────────────────────────────┘
                                      │ entry public IP (NAT'd)
                                      ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        EXIT NODE                                    │
│                                                                     │
│  eth0 ◄── WireGuard inner ──► wg-exit ──► NAT ──► eth0             │
│  :51821       decrypt inner       10.200.0.1        forward to net  │
│                                                                     │
│  Sees: destination websites, DNS (9.9.9.9), client tunnel IP only   │
│  Cannot see: client real IP (sees entry IP or NAT)                  │
└─────────────────────────────────────────────────────────────────────┘
```

### Encryption Layers (Outbound Packet)

```
Layer 3 (cleartext):   [IP: 10.200.0.2 → 93.184.216.34] [TCP] [HTTP GET /]
Layer 2 (inner WG):    [WG header] [ChaCha20-Poly1305 encrypted blob]
                       src: 10.100.0.2  dst: exit_public_ip:51821
Layer 1 (outer WG):    [WG header] [ChaCha20-Poly1305 encrypted blob]
                       src: client_real_ip  dst: entry_public_ip:51820
```

Entry decrypts Layer 1 → sees Layer 2 (still encrypted). Forwards to exit.
Exit decrypts Layer 2 → sees Layer 3 (cleartext). Forwards to internet.

---

## 2. Interface Layout

### Client
| Interface  | Address      | Peer Endpoint              | AllowedIPs                    | Purpose          |
|------------|-------------|----------------------------|-------------------------------|------------------|
| `wg-outer` | 10.100.0.2  | entry_ip:51820             | 10.100.0.0/24, exit_ip/32     | Outer tunnel     |
| `wg-inner` | 10.200.0.2  | exit_ip:51821 (via outer)  | 0.0.0.0/0, ::/0              | Inner tunnel     |

### Entry Node
| Interface  | Address      | Listen Port | Purpose                    |
|------------|-------------|-------------|----------------------------|
| `wg-entry` | 10.100.0.1  | 51820       | Outer tunnel termination   |
| `eth0`     | public IP    | —           | Internet-facing            |

### Exit Node
| Interface  | Address      | Listen Port | Purpose                    |
|------------|-------------|-------------|----------------------------|
| `wg-exit`  | 10.200.0.1  | 51821       | Inner tunnel termination   |
| `eth0`     | public IP    | —           | Internet-facing            |

---

## 3. IP Addressing Scheme

```
Outer tunnel network:  10.100.0.0/16
  Entry nodes:         10.100.{node_id}.1
  Clients (per entry): 10.100.{node_id}.{2-254}

Inner tunnel network:  10.200.0.0/16
  Exit nodes:          10.200.{node_id}.1
  Clients (per exit):  10.200.{node_id}.{2-254}
```

Each session gets a unique pair of tunnel IPs. The node daemon allocates from its
pool and reclaims on session teardown.

---

## 4. Routing Rules

### Client
```bash
# Route exit node's IP through outer tunnel (so inner tunnel packets reach it)
ip route add $EXIT_PUBLIC_IP/32 dev wg-outer

# Route everything else through inner tunnel
ip route add 0.0.0.0/1 dev wg-inner
ip route add 128.0.0.0/1 dev wg-inner

# Keep entry node reachable via default gateway (not tunnelled)
ip route add $ENTRY_PUBLIC_IP/32 via $DEFAULT_GATEWAY dev eth0
```

### Entry Node
```bash
# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1

# NAT client traffic going to exit node
iptables -t nat -A POSTROUTING -s 10.100.0.0/16 -o eth0 -j MASQUERADE

# Forward traffic from wg-entry to eth0
iptables -A FORWARD -i wg-entry -o eth0 -j ACCEPT
iptables -A FORWARD -i eth0 -o wg-entry -m state --state RELATED,ESTABLISHED -j ACCEPT
```

### Exit Node
```bash
# Enable IP forwarding
sysctl -w net.ipv4.ip_forward=1

# NAT inner tunnel traffic to internet
iptables -t nat -A POSTROUTING -s 10.200.0.0/16 -o eth0 -j MASQUERADE

# Forward traffic from wg-exit to internet
iptables -A FORWARD -i wg-exit -o eth0 -j ACCEPT
iptables -A FORWARD -i eth0 -o wg-exit -m state --state RELATED,ESTABLISHED -j ACCEPT
```

---

## 5. nftables Quota Enforcement

Quotas are enforced by **client tunnel IP** on the exit node (where cleartext
traffic is visible and measurable). Entry node quotas are secondary.

### Strategy: 256 MB Kernel Slabs

The hub sets a 256 MB nftables quota per client tunnel IP. When consumed, traffic
is dropped at the kernel level — no userspace polling delay. The hub daemon
refreshes the slab when balance allows, or removes the peer when balance is zero.

```nft
table inet arfl {
    # Named quota set — one element per client tunnel IP
    set quotas {
        type ipv4_addr
        flags dynamic,timeout
        timeout 30m                    # auto-cleanup stale entries
    }

    chain forward {
        type filter hook forward priority 0; policy accept;

        # Match traffic FROM client tunnel IPs and enforce quota
        iifname "wg-exit" ip saddr @quotas counter drop comment "over quota"
    }
}

# Add quota for a new session (256 MB = 268435456 bytes)
nft add element inet arfl quotas { 10.200.0.2 : quota over 268435456 bytes }

# Refresh quota (hub daemon calls this when slab consumed + balance remaining)
nft delete element inet arfl quotas { 10.200.0.2 }
nft add element inet arfl quotas { 10.200.0.2 : quota over 268435456 bytes }

# Remove peer when balance exhausted (via wgctrl, not nftables)
# Hub calls: wgClient.ConfigureDevice("wg-exit", RemovePeer(clientPubKey))
```

### Accounting vs Enforcement

| Layer      | Mechanism           | Purpose                     | Granularity |
|------------|--------------------|-----------------------------|-------------|
| wgctrl     | ReceiveBytes/TransmitBytes | Accounting (billing)    | Per peer    |
| nftables   | quota elements      | Hard enforcement (cutoff)   | Per tunnel IP |

wgctrl counters are the source of truth for billing. nftables quotas are the
safety net that prevents overshoot between polling intervals.

---

## 6. Packet Capture Privacy Checklist

Run these captures during integration testing to prove the privacy model holds.

### On Entry Node — Public Interface
```bash
tcpdump -i eth0 -n host $CLIENT_REAL_IP
```
**Expected**: WireGuard UDP packets (port 51820) only. No cleartext HTTP/DNS.

### On Entry Node — Tunnel Interface
```bash
tcpdump -i wg-entry -n
```
**Expected**: Encrypted UDP packets to $EXIT_IP:51821. NO cleartext destination IPs.

### On Exit Node — Public Interface
```bash
tcpdump -i eth0 -n not host $EXIT_IP
```
**Expected**: Outbound traffic to destination websites. Source is EXIT node IP (NAT).
**Must NOT contain**: $CLIENT_REAL_IP anywhere.

### On Exit Node — Tunnel Interface
```bash
tcpdump -i wg-exit -n
```
**Expected**: Cleartext traffic from 10.200.0.2 (client tunnel IP) to destinations.
**Must NOT contain**: $CLIENT_REAL_IP.

### DNS Leak Check
```bash
# On exit node tunnel interface
tcpdump -i wg-exit -n port 53
```
**Expected**: DNS queries from 10.200.0.2 to 9.9.9.9 only. No other DNS.

### Summary Matrix
| Capture Point        | Sees Client Real IP? | Sees Destinations? | Sees Cleartext? |
|----------------------|---------------------|-------------------|-----------------|
| Entry eth0           | ✅ Yes              | ❌ No             | ❌ No           |
| Entry wg-entry       | ❌ No (tunnel IP)   | ❌ No             | ❌ No           |
| Exit eth0            | ❌ No (NAT'd)       | ✅ Yes            | ✅ Yes          |
| Exit wg-exit         | ❌ No (tunnel IP)   | ✅ Yes            | ✅ Yes          |

---

## 7. Session Lifecycle

```
1.  Client generates WireGuard keypair (first launch) or loads from secure storage.
2.  Client reads Nostr relays → builds node index (Phase 3; hardcoded in Phase 1).
3.  Client selects entry + exit node pair (score-based; static in Phase 1).
4.  Client requests session from hub (Phase 2+; manual setup in Phase 1).
5.  Hub/script allocates tunnel IPs for client on entry and exit.
6.  Hub/script adds client peer on entry node via admin API / wgctrl.
7.  Hub/script adds client peer on exit node via admin API / wgctrl.
8.  Client configures wg-outer (to entry) and wg-inner (to exit, via outer).
9.  Client sets routes: exit IP via outer, 0.0.0.0/0 via inner, entry via default gw.
10. Client sets DNS to 9.9.9.9.
11. Noise_IKpsk2 handshakes complete — both tunnels established.
12. Traffic flows: app → wg-inner → wg-outer → entry → exit → internet.
13. Node daemons poll byte counters every 5s, report to hub.
14. Hub manages quota slabs (256 MB nftables elements).
15. On balance exhaustion: hub removes peer, nftables blocks traffic.
16. On disconnect: client tears down interfaces, routes restored.
```

---

## 8. BandwidthCredential Abstraction (Phase 4 Prep)

Design the credential interface now so Phase 4 (signed) and Phase 5 (blinded) are
a clean swap:

```go
type BandwidthCredential struct {
    ValueGB      int       // GB this credential grants
    IssuerPubkey []byte    // Hub's public key
    Expiry       int64     // Unix timestamp
    Nullifier    []byte    // Unique spend ID (prevents double-spend)
    Signature    []byte    // Hub signature (normal in Phase 4, blind in Phase 5)
}

type CredentialIssuer interface {
    Issue(request []byte) (*BandwidthCredential, error)
}

type CredentialVerifier interface {
    Verify(cred *BandwidthCredential) error
    MarkSpent(nullifier []byte) error
    IsSpent(nullifier []byte) (bool, error)
}
```

In Phase 4, `Issue()` signs directly. In Phase 5, it performs blind signing.
The rest of the system doesn't change.

---

## 9. Known Limitations & Nuances

### Entry/Exit Mutual Visibility
With nested tunnels, the entry node sees encrypted packets destined for the exit
node's IP, and the exit node sees packets arriving from the entry node's IP. They
can infer each other's identity. This is analogous to Tor's guard/exit model.

**Why it's acceptable**: Neither node gets both client identity AND destination.
The security property is compartmentalisation, not mutual blindness. With many
nodes in the network, statistical correlation is impractical.

### NAT Traversal for Residential Nodes
Residential nodes behind NAT need either:
- UPnP/NAT-PMP port mapping (unreliable)
- STUN-based endpoint discovery
- A relay/rendezvous mechanism

This is a Phase 3+ concern. For Phase 1, nodes have public IPs.

### MTU Overhead
Nested WireGuard adds ~120 bytes overhead (60 per layer). Default MTU should be
set to 1280 (IPv6 minimum, safe everywhere) on both tunnel interfaces to prevent
fragmentation issues.

---

## 10. Project Structure

```
arfl/
├── cmd/
│   ├── arfl-node/       # Node daemon (entry/exit roles)
│   ├── arfl-client/     # Client CLI
│   ├── arfl-hub/        # Hub service (Phase 2+)
│   └── arflctl/         # Admin/orchestration tool
├── internal/
│   ├── wg/              # wgctrl-go wrapper
│   ├── routing/         # Nested tunnel routing logic
│   ├── quota/           # nftables quota enforcement
│   ├── nostr/           # Node discovery (Phase 3)
│   ├── payments/        # Lightning integration (Phase 4)
│   ├── credentials/     # BandwidthCredential (Phase 4-5)
│   ├── config/          # Configuration loading
│   └── control/         # Admin API server
├── pkg/
│   ├── protocol/        # Protocol constants, event types
│   └── types/           # Shared types
├── deployments/
│   ├── systemd/         # Service files
│   └── nftables/        # Quota rule templates
├── docs/
│   └── architecture.md  # This document
├── test/
│   └── integration/     # End-to-end tests
├── go.mod
├── go.sum
└── README.md
```
