# Build stage — compiles all three binaries.
FROM golang:1.26-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO required for sqlite3 (mattn/go-sqlite3).
ENV CGO_ENABLED=1
RUN go build -o /out/arfl-hub    ./cmd/arfl-hub && \
    go build -o /out/arfl-node   ./cmd/arfl-node && \
    go build -o /out/arfl-client ./cmd/arfl-client

# ---------- Hub image (distroless — no shell, minimal attack surface) ----------
FROM gcr.io/distroless/base-debian12 AS hub

COPY --from=builder /out/arfl-hub /usr/local/bin/arfl-hub
ENTRYPOINT ["arfl-hub"]

# ---------- Node image (needs ip, nft, iptables for WireGuard + NAT) ----------
FROM gcr.io/distroless/base-debian12 AS node-base

# Copy network tools from a tools stage (distroless has no package manager).
FROM debian:bookworm-slim AS tools
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        wireguard-tools \
        iproute2 \
        iptables \
        nftables \
    && rm -rf /var/lib/apt/lists/*

FROM gcr.io/distroless/base-debian12 AS node

# Copy required network binaries and their library dependencies.
COPY --from=tools /usr/sbin/ip       /usr/sbin/ip
COPY --from=tools /usr/sbin/nft      /usr/sbin/nft
COPY --from=tools /usr/sbin/iptables /usr/sbin/iptables
COPY --from=tools /usr/bin/wg        /usr/bin/wg
COPY --from=tools /lib/              /lib/
COPY --from=tools /usr/lib/          /usr/lib/

COPY --from=builder /out/arfl-node   /usr/local/bin/arfl-node
COPY --from=builder /out/arfl-client /usr/local/bin/arfl-client
ENTRYPOINT ["arfl-node"]
