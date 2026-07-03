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

# Runtime stage — slim image with WireGuard tools.
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        wireguard-tools \
        iproute2 \
        iptables \
        nftables \
        curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/arfl-hub    /usr/local/bin/
COPY --from=builder /out/arfl-node   /usr/local/bin/
COPY --from=builder /out/arfl-client /usr/local/bin/

ENTRYPOINT ["/bin/sh", "-c"]
