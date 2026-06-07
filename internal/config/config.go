package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// NodeConfig holds configuration for an ARFL node daemon.
type NodeConfig struct {
	Role         string `json:"role"`          // "entry", "exit", or "both"
	ListenPort   int    `json:"listen_port"`   // WireGuard UDP port
	PrivateKey   string `json:"private_key"`   // Base64 WireGuard private key
	TunnelIP     string `json:"tunnel_ip"`     // e.g. "10.100.0.1/24" or "10.200.0.1/24"
	Interface    string `json:"interface"`     // WireGuard interface name
	OutInterface string `json:"out_interface"` // Internet-facing interface, e.g. "eth0"
	AdminAddr    string `json:"admin_addr"`    // Admin API listen address, e.g. "127.0.0.1:9090"
	MTU          int    `json:"mtu"`           // Tunnel MTU (default 1280)

	// Phase 2: Nostr discovery
	NostrPrivkey    string   `json:"nostr_privkey"` // Hex-encoded Nostr private key
	HubPubkey       string   `json:"hub_pubkey"`    // Hub's Nostr pubkey (who we register with)
	AttestationJSON string   `json:"attestation"`   // JSON-encoded hub attestation
	Relays          []string `json:"relays"`        // Nostr relay URLs to publish to
	Endpoint        string   `json:"endpoint"`      // Public endpoint for node discovery (ip:port)
	UploadMbps      int      `json:"upload_mbps"`   // Advertised upload speed
	DownloadMbps    int      `json:"download_mbps"` // Advertised download speed
	Capacity        int      `json:"capacity"`      // Max concurrent peers
}

// HubConfig holds configuration for an ARFL hub daemon.
type HubConfig struct {
	NostrPrivkey string   `json:"nostr_privkey"` // Hub's Nostr private key (hex)
	ListenAddr   string   `json:"listen_addr"`   // Discovery API listen address
	Relays       []string `json:"relays"`        // Nostr relay URLs to subscribe to

	// Phase 3: Payment
	DBPath          string `json:"db_path"`           // SQLite database path (default: platform-specific)
	CredentialKey   string `json:"credential_key"`    // Hex-encoded HMAC secret for ticket issuance
	SettlementHours int    `json:"settlement_hours"`  // Settlement interval in hours (default: 6)
	MinPayoutSats   int64  `json:"min_payout_sats"`   // Minimum payout threshold (default: 1000)
}

// ClientConfig holds configuration for the ARFL client.
type ClientConfig struct {
	HubURL     string   `json:"hub_url"`     // Hub discovery API URL
	HubPubkeys []string `json:"hub_pubkeys"` // Trusted hub pubkeys for verification
}

// SessionFile is the static session config read by the client in Phase 1.
// In later phases this is generated dynamically by the hub.
type SessionFile struct {
	EntryEndpoint string `json:"entry_endpoint"`  // Entry node public IP:port
	EntryWGPubkey string `json:"entry_wg_pubkey"` // Entry node WG public key
	ExitEndpoint  string `json:"exit_endpoint"`   // Exit node public IP:port
	ExitWGPubkey  string `json:"exit_wg_pubkey"`  // Exit node WG public key
	OuterTunnelIP string `json:"outer_tunnel_ip"` // Client's outer tunnel IP, e.g. "10.100.0.2/24"
	InnerTunnelIP string `json:"inner_tunnel_ip"` // Client's inner tunnel IP, e.g. "10.200.0.2/24"
}

func LoadNodeConfig(path string) (*NodeConfig, error) {
	return loadJSON[NodeConfig](path)
}

func LoadHubConfig(path string) (*HubConfig, error) {
	return loadJSON[HubConfig](path)
}

func LoadClientConfig(path string) (*ClientConfig, error) {
	return loadJSON[ClientConfig](path)
}

func LoadSessionFile(path string) (*SessionFile, error) {
	return loadJSON[SessionFile](path)
}

func loadJSON[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg T
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}
