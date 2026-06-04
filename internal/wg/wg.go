package wg

import (
	"net"
	"time"
)

// Manager handles WireGuard interface lifecycle and peer management.
type Manager interface {
	// CreateInterface creates and brings up a WireGuard interface.
	CreateInterface(cfg InterfaceConfig) error

	// DeleteInterface removes a WireGuard interface.
	DeleteInterface(name string) error

	// AddPeer adds a peer to the specified WireGuard interface.
	AddPeer(iface string, peer PeerConfig) error

	// RemovePeer removes a peer by public key from the interface.
	RemovePeer(iface string, pubkey string) error

	// GetPeerStats returns byte counter stats for all peers on an interface.
	GetPeerStats(iface string) ([]PeerStats, error)

	// Close releases resources held by the manager.
	Close() error
}

// InterfaceConfig defines parameters for creating a WireGuard interface.
type InterfaceConfig struct {
	Name       string // e.g. "wg-entry", "wg-exit", "wg-outer", "wg-inner"
	PrivateKey string // Base64-encoded Curve25519 private key
	ListenPort int    // UDP listen port (0 = no listen, client mode)
	Address    string // Tunnel IP with prefix, e.g. "10.100.0.1/24"
	MTU        int    // Tunnel MTU (default 1280 for nested WG)
}

// PeerConfig defines parameters for adding a WireGuard peer.
type PeerConfig struct {
	PublicKey  string   // Base64-encoded Curve25519 public key
	Endpoint  string   // IP:port (empty for clients that connect to us)
	AllowedIPs []string // CIDR ranges, e.g. ["10.200.0.2/32"] or ["0.0.0.0/0"]
	Keepalive  int      // Persistent keepalive interval in seconds (0 = off)
}

// PeerStats holds byte counter data for a WireGuard peer.
type PeerStats struct {
	PublicKey      string
	Endpoint       net.UDPAddr
	ReceiveBytes   int64
	TransmitBytes  int64
	TotalBytes     int64
	LastHandshake  time.Time
	AllowedIPs     []net.IPNet
}

// KeyPair holds a WireGuard Curve25519 key pair.
type KeyPair struct {
	PrivateKey string // Base64-encoded
	PublicKey  string // Base64-encoded
}
