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
