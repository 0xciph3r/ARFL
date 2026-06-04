#!/usr/bin/env bash
# ARFL Node Server Setup
# Run this on each Ubuntu server (entry node AND exit node).
#
# Usage:
#   curl -sSf https://raw.githubusercontent.com/Radi-Labs/ARFL/main/deployments/setup-node.sh | sudo bash
#
# Or clone the repo and run:
#   sudo bash deployments/setup-node.sh
#
# After running this script, you need to:
#   1. Generate a keypair:   ./arfl-node --genkey > node.json.tmp
#   2. Create node.json config (see template below)
#   3. Start the node:       sudo ./arfl-node --config node.json

set -euo pipefail

echo "=== ARFL Node Server Setup ==="
echo ""

# 1. Update system
echo "[1/5] Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq

# 2. Install WireGuard
echo "[2/5] Installing WireGuard..."
apt-get install -y -qq wireguard wireguard-tools

# Verify WireGuard kernel module loads
modprobe wireguard 2>/dev/null || echo "  Note: wireguard module may be built-in on this kernel"
echo "  WireGuard installed: $(wg --version)"

# 3. Install nftables (for quota enforcement)
echo "[3/5] Installing nftables..."
apt-get install -y -qq nftables
systemctl enable nftables
systemctl start nftables

# 4. Enable IP forwarding (persist across reboots)
echo "[4/5] Enabling IP forwarding..."
cat > /etc/sysctl.d/99-arfl.conf << 'EOF'
# ARFL: enable IPv4 forwarding for VPN traffic routing
net.ipv4.ip_forward = 1
EOF
sysctl -p /etc/sysctl.d/99-arfl.conf

# 5. Install Go (for building arfl-node)
echo "[5/5] Installing Go..."
GO_VERSION="1.23.4"
if ! command -v go &>/dev/null; then
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
    export PATH=$PATH:/usr/local/go/bin
fi
echo "  Go installed: $(go version)"

echo ""
echo "=== Setup complete ==="
echo ""
echo "Next steps:"
echo ""
echo "  1. Clone the repo:"
echo "     git clone https://github.com/Radi-Labs/ARFL.git"
echo "     cd ARFL"
echo ""
echo "  2. Build the node daemon:"
echo "     go build -o arfl-node ./cmd/arfl-node"
echo ""
echo "  3. Generate a keypair:"
echo "     ./arfl-node --genkey"
echo "     (save the output — you need the private key for node.json)"
echo ""
echo "  4. Create node.json (see template below)"
echo ""
echo "  5. Open the WireGuard port in your firewall:"
echo "     # For entry node:"
echo "     ufw allow 51820/udp"
echo "     # For exit node:"
echo "     ufw allow 51821/udp"
echo ""
echo "  6. Start the node:"
echo "     sudo ./arfl-node --config node.json"
echo ""
echo "=== node.json template (ENTRY node) ==="
cat << 'TEMPLATE'
{
  "role": "entry",
  "listen_port": 51820,
  "private_key": "<paste your private key from step 3>",
  "tunnel_ip": "10.100.0.1/24",
  "interface": "wg-entry",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9090",
  "mtu": 1280
}
TEMPLATE

echo ""
echo "=== node.json template (EXIT node) ==="
cat << 'TEMPLATE'
{
  "role": "exit",
  "listen_port": 51821,
  "private_key": "<paste your private key from step 3>",
  "tunnel_ip": "10.200.0.1/24",
  "interface": "wg-exit",
  "out_interface": "eth0",
  "admin_addr": "127.0.0.1:9091",
  "mtu": 1280
}
TEMPLATE
