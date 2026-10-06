package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/internal/nostr"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

func TestPresetForRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role       string
		listenPort int
		tunnelIP   string
		iface      string
		wantErr    bool
	}{
		{role: "entry", listenPort: 51820, tunnelIP: "10.100.0.1/24", iface: "wg-entry"},
		{role: "exit", listenPort: 51821, tunnelIP: "10.200.0.1/24", iface: "wg-exit"},
		{role: "both", listenPort: 51820, tunnelIP: "10.150.0.1/24", iface: "wg-both"},
		{role: "invalid", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.role, func(t *testing.T) {
			t.Parallel()
			got, err := presetForRole(tc.role)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for role %q", tc.role)
				}
				return
			}
			if err != nil {
				t.Fatalf("presetForRole(%q): %v", tc.role, err)
			}
			if got.ListenPort != tc.listenPort || got.TunnelIP != tc.tunnelIP || got.Interface != tc.iface {
				t.Fatalf("unexpected preset: %+v", got)
			}
		})
	}
}

func TestBuildHubConfigGeneratesSecrets(t *testing.T) {
	t.Parallel()

	cfg, pub, err := buildHubConfig(hubInitOptions{
		ListenAddr:      "0.0.0.0:8080",
		Relays:          []string{"wss://relay.damus.io"},
		DBPath:          "arfl.db",
		BlindKeyDir:     "keys",
		SettlementHours: 6,
		MinPayoutSats:   1000,
		HubMarginPct:    20,
		LNDHost:         "localhost",
		LNDPort:         8080,
		LNDTLSCertPath:  "~/.lnd/tls.cert",
		LNDMacaroonPath: "~/.lnd/admin.macaroon",
		LNDFeeLimitSat:  100,
	})
	if err != nil {
		t.Fatalf("buildHubConfig: %v", err)
	}
	if len(cfg.NostrPrivkey) != 64 {
		t.Fatalf("unexpected nostr privkey length: %d", len(cfg.NostrPrivkey))
	}
	if len(pub) != 64 {
		t.Fatalf("unexpected nostr pubkey length: %d", len(pub))
	}
	if _, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey); err != nil {
		t.Fatalf("nostr key invalid: %v", err)
	}
	rawCred, err := hex.DecodeString(cfg.CredentialKey)
	if err != nil {
		t.Fatalf("credential key is not hex: %v", err)
	}
	if len(rawCred) != 32 {
		t.Fatalf("credential key must be 32 bytes, got %d", len(rawCred))
	}
}

func TestBuildNodeConfigSetsWireGuardMetadata(t *testing.T) {
	t.Parallel()

	cfg, nodePub, wgPub, err := buildNodeConfig(nodeInitOptions{
		Role:          "entry",
		ListenPort:    51820,
		TunnelIP:      "10.100.0.1/24",
		Interface:     "wg-entry",
		OutInterface:  "eth0",
		AdminAddr:     "127.0.0.1:9090",
		MTU:           1280,
		Endpoint:      "203.0.113.10:51820",
		ConnectAddr:   "0.0.0.0:9091",
		UploadMbps:    100,
		DownloadMbps:  100,
		Capacity:      50,
		NostrRelays:   []string{"wss://relay.damus.io"},
		HubURL:        "http://203.0.113.20:8080",
		HubPubkeyFile: "keys/key-100mb.pub.json",
	})
	if err != nil {
		t.Fatalf("buildNodeConfig: %v", err)
	}

	if cfg.Role != "entry" {
		t.Fatalf("unexpected role %q", cfg.Role)
	}
	if _, err := wg.ParseKey(cfg.PrivateKey); err != nil {
		t.Fatalf("invalid generated private key: %v", err)
	}
	if _, err := nostr.KeyPairFromPrivHex(cfg.NostrPrivkey); err != nil {
		t.Fatalf("invalid generated nostr key: %v", err)
	}
	if len(nodePub) != 64 {
		t.Fatalf("unexpected node nostr pubkey length: %d", len(nodePub))
	}
	if wgPub == "" {
		t.Fatalf("expected non-empty WireGuard public key")
	}
	if len(cfg.EnabledTransports) != 1 || cfg.EnabledTransports[0] != "wireguard" {
		t.Fatalf("unexpected enabled transports: %v", cfg.EnabledTransports)
	}
	if cfg.TransportEndpoints["wireguard"] != "203.0.113.10:51820" {
		t.Fatalf("unexpected wireguard endpoint: %q", cfg.TransportEndpoints["wireguard"])
	}
	if cfg.TransportConnectAddr["wireguard"] != "0.0.0.0:9091" {
		t.Fatalf("unexpected wireguard connect addr: %q", cfg.TransportConnectAddr["wireguard"])
	}
}

func TestParseCSVDedupesAndTrims(t *testing.T) {
	t.Parallel()

	got := parseCSV(" wss://a , wss://b, wss://a ,,")
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(got), got)
	}
	if got[0] != "wss://a" || got[1] != "wss://b" {
		t.Fatalf("unexpected entries: %v", got)
	}
}

func TestGuardOutputPathRefusesOverwriteWithoutForce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := guardOutputPath(p, false); err == nil {
		t.Fatal("expected overwrite refusal error")
	}
}

func TestGuardOutputPathForceCreatesBackup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := guardOutputPath(p, true); err != nil {
		t.Fatalf("guardOutputPath(force=true): %v", err)
	}
	matches, err := filepath.Glob(p + ".bak.*")
	if err != nil {
		t.Fatalf("glob backup: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 backup file, got %d (%v)", len(matches), matches)
	}
}

func TestWriteJSONEnforcesPermissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(p, []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := writeJSON(p, map[string]any{"ok": true}, 0o600); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected permissions 0600, got %o", got)
	}
}

func TestValidateNodeConfig_EnabledTransportsDiagnostics(t *testing.T) {
	t.Parallel()

	kp, err := wg.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	base := &config.NodeConfig{
		Role:          "entry",
		ListenPort:    51820,
		PrivateKey:    kp.PrivateKey,
		Endpoint:      "203.0.113.10:51820",
		ConnectAddr:   "0.0.0.0:9091",
		HubURL:        "http://127.0.0.1:8080",
		HubPubkeyFile: "/tmp/missing.pub.json",
		Relays:        []string{"wss://relay.damus.io"},
	}

	t.Run("all invalid fails", func(t *testing.T) {
		cfg := *base
		cfg.EnabledTransports = []string{"carrier-pigeon"}
		check := findCheckByName(validateNodeConfig(&cfg), "enabled_transports")
		if check == nil || check.Status != doctorFail {
			t.Fatalf("expected fail status, got %#v", check)
		}
	})

	t.Run("partially valid warns", func(t *testing.T) {
		cfg := *base
		cfg.EnabledTransports = []string{"wireguard", "hysteria2"}
		cfg.TransportEndpoints = map[string]string{
			"wireguard": "203.0.113.10:51820",
		}
		check := findCheckByName(validateNodeConfig(&cfg), "enabled_transports")
		if check == nil || check.Status != doctorWarn {
			t.Fatalf("expected warn status, got %#v", check)
		}
		if !strings.Contains(check.Detail, "hysteria2") {
			t.Fatalf("expected dropped transport detail, got %#v", check)
		}
	})

	t.Run("valid passes", func(t *testing.T) {
		cfg := *base
		cfg.EnabledTransports = []string{"wireguard"}
		cfg.TransportEndpoints = map[string]string{
			"wireguard": "203.0.113.10:51820",
		}
		check := findCheckByName(validateNodeConfig(&cfg), "enabled_transports")
		if check == nil || check.Status != doctorPass {
			t.Fatalf("expected pass status, got %#v", check)
		}
	})
}

func findCheckByName(checks []doctorCheck, name string) *doctorCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}
