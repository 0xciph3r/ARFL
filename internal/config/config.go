package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Environment variable names for sensitive configuration.
// These override JSON config values when set, keeping secrets out of files.
const (
	EnvLNDHost         = "ARFL_LND_HOST"
	EnvLNDPort         = "ARFL_LND_PORT"
	EnvLNDTLSCertPath  = "ARFL_LND_TLS_CERT_PATH"
	EnvLNDMacaroonPath = "ARFL_LND_MACAROON_PATH"
	EnvLNDFeeLimitSat  = "ARFL_LND_FEE_LIMIT_SAT"
	EnvCredentialKey   = "ARFL_CREDENTIAL_KEY"
	EnvNostrPrivkey    = "ARFL_NOSTR_PRIVKEY"
	EnvDBPath          = "ARFL_DB_PATH"
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

	// Phase 5: Blind token verification
	HubURL        string `json:"hub_url"`         // Hub API base URL (e.g. "http://hub:8080")
	HubPubkeyFile string `json:"hub_pubkey_file"` // Path to hub's blind sig public key file
	ConnectAddr   string `json:"connect_addr"`    // Public-facing /connect API listen address (e.g. "0.0.0.0:9091")
}

// HubConfig holds configuration for an ARFL hub daemon.
type HubConfig struct {
	NostrPrivkey string   `json:"nostr_privkey"` // Hub's Nostr private key (hex)
	ListenAddr   string   `json:"listen_addr"`   // Discovery API listen address
	Relays       []string `json:"relays"`        // Nostr relay URLs to subscribe to

	// Phase 3: Payment
	DBPath          string `json:"db_path"`          // SQLite database path (default: platform-specific)
	CredentialKey   string `json:"credential_key"`   // Hex-encoded HMAC secret for ticket issuance
	SettlementHours int    `json:"settlement_hours"` // Settlement interval in hours (default: 6)
	MinPayoutSats   int64  `json:"min_payout_sats"`  // Minimum payout threshold (default: 1000)

	// Phase 4: Blind signatures
	BlindKeyDir string `json:"blind_key_dir"` // Directory for RSA denomination keys (default: "keys/")

	// Phase 6: LND connection (omit for mock/dev mode)
	LNDHost         string `json:"lnd_host"`          // LND REST host (e.g. "localhost")
	LNDPort         int    `json:"lnd_port"`          // LND REST port (e.g. 8080)
	LNDTLSCertPath  string `json:"lnd_tls_cert_path"` // Path to LND's tls.cert
	LNDMacaroonPath string `json:"lnd_macaroon_path"` // Path to admin.macaroon
	LNDFeeLimitSat  int64  `json:"lnd_fee_limit_sat"` // Max routing fee per payment (default: 100)
}

// ClientConfig holds configuration for the ARFL client.
type ClientConfig struct {
	HubURL     string   `json:"hub_url"`     // Hub discovery API URL
	HubPubkeys []string `json:"hub_pubkeys"` // Trusted hub pubkeys for verification
}

// SessionFile is the static session config read by the client in Phase 1.
// In later phases this is generated dynamically by the hub.
type SessionFile struct {
	EntryEndpoint   string `json:"entry_endpoint"`    // Entry node public IP:port
	EntryWGPubkey   string `json:"entry_wg_pubkey"`   // Entry node WG public key
	EntryConnectURL string `json:"entry_connect_url"` // Entry node /connect API URL
	ExitEndpoint    string `json:"exit_endpoint"`     // Exit node public IP:port
	ExitWGPubkey    string `json:"exit_wg_pubkey"`    // Exit node WG public key
	ExitConnectURL  string `json:"exit_connect_url"`  // Exit node /connect API URL
	OuterTunnelIP   string `json:"outer_tunnel_ip"`   // Client's outer tunnel IP, e.g. "10.100.0.2/24"
	InnerTunnelIP   string `json:"inner_tunnel_ip"`   // Client's inner tunnel IP, e.g. "10.200.0.2/24"
}

func LoadNodeConfig(path string) (*NodeConfig, error) {
	return loadJSON[NodeConfig](path)
}

func LoadHubConfig(path string) (*HubConfig, error) {
	cfg, err := loadJSON[HubConfig](path)
	if err != nil {
		return nil, err
	}
	cfg.ApplyEnv()
	return cfg, nil
}

// ApplyEnv overrides config fields with environment variables when set.
// Priority: env var > JSON config file. This keeps secrets out of config files.
func (c *HubConfig) ApplyEnv() {
	if v := os.Getenv(EnvLNDHost); v != "" {
		c.LNDHost = v
	}
	if v := os.Getenv(EnvLNDPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.LNDPort = port
		}
	}
	if v := os.Getenv(EnvLNDTLSCertPath); v != "" {
		c.LNDTLSCertPath = v
	}
	if v := os.Getenv(EnvLNDMacaroonPath); v != "" {
		c.LNDMacaroonPath = v
	}
	if v := os.Getenv(EnvLNDFeeLimitSat); v != "" {
		if limit, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.LNDFeeLimitSat = limit
		}
	}
	if v := os.Getenv(EnvCredentialKey); v != "" {
		c.CredentialKey = v
	}
	if v := os.Getenv(EnvNostrPrivkey); v != "" {
		c.NostrPrivkey = v
	}
	if v := os.Getenv(EnvDBPath); v != "" {
		c.DBPath = v
	}
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
