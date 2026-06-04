# ARFL

**Privacy in the Dark Cloud** — A decentralised privacy protocol powered by Bitcoin.

ARFL is a decentralised VPN protocol that aggregates WireGuard, Nostr, the Bitcoin Lightning Network, and Fedimint into a self-sustaining privacy network. No accounts. No subscriptions. No logs. No token.

- [Whitepaper (PDF)](./ARFL_Whitepaper.pdf)
- [Architecture](./docs/architecture.md)

## Project Structure

```
cmd/arfl-node/      Node daemon (entry/exit roles)
cmd/arfl-client/    Client CLI
cmd/arfl-hub/       Hub service
cmd/arflctl/        Admin tool
internal/           Core packages (wg, routing, quota, nostr, payments, credentials)
pkg/protocol/       Protocol constants
pkg/types/          Shared types
deployments/        systemd + nftables configs
docs/               Architecture documentation
```

## Status

**Phase 0 — Architecture Spike** ✅ Complete

See [docs/architecture.md](./docs/architecture.md) for the nested WireGuard two-hop design, IP addressing, routing rules, quota enforcement, and privacy checklist.

## Build

```bash
go build ./...
```

## License

See [LICENSE](./LICENSE).