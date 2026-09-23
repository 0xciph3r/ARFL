package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Radi-Labs/ARFL/pkg/types"
)

func TestParseTransportNames_Valid(t *testing.T) {
	got, err := parseTransportNames("preferred_transports", []string{" wireguard ", "HYSTERIA2", "amneziawg"})
	if err != nil {
		t.Fatalf("parseTransportNames: %v", err)
	}
	want := []types.Transport{types.TransportWireGuard, types.TransportHysteria2, types.TransportAmneziaWG}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseTransportNames_Invalid(t *testing.T) {
	_, err := parseTransportNames("allowed_transports", []string{"wireguard", "badtransport"})
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
	if !strings.Contains(err.Error(), "allowed_transports") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadTransportPolicyFromClientConfig_DefaultMissing(t *testing.T) {
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	t.Setenv("ARFL_CLIENT_CONFIG", "")

	preferred, allowed, err := loadTransportPolicyFromClientConfig()
	if err != nil {
		t.Fatalf("loadTransportPolicyFromClientConfig: %v", err)
	}
	if preferred != nil || allowed != nil {
		t.Fatalf("expected nil policy for missing client.json, got preferred=%v allowed=%v", preferred, allowed)
	}
}

func TestLoadTransportPolicyFromClientConfig_EnvPath(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "policy.json")
	cfg := `{
  "hub_url": "http://localhost:8080",
  "preferred_transports": ["hysteria2", "wireguard"],
	  "allowed_transports": ["wireguard", "hysteria2"],
  "require_common_hop_transport": true
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("ARFL_CLIENT_CONFIG", cfgPath)

	preferred, allowed, err := loadTransportPolicyFromClientConfig()
	if err != nil {
		t.Fatalf("loadTransportPolicyFromClientConfig: %v", err)
	}
	if len(preferred) != 2 || preferred[0] != types.TransportHysteria2 || preferred[1] != types.TransportWireGuard {
		t.Fatalf("unexpected preferred transports: %v", preferred)
	}
	if len(allowed) != 2 || allowed[0] != types.TransportWireGuard || allowed[1] != types.TransportHysteria2 {
		t.Fatalf("unexpected allowed transports: %v", allowed)
	}
}

func TestLoadTransportPolicyFromClientConfig_InvalidTransport(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "bad-policy.json")
	cfg := `{
  "preferred_transports": ["wireguard", "totallybad"],
  "require_common_hop_transport": true
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("ARFL_CLIENT_CONFIG", cfgPath)

	_, _, err := loadTransportPolicyFromClientConfig()
	if err == nil {
		t.Fatal("expected invalid transport error")
	}
	if !strings.Contains(err.Error(), "unsupported transport") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadTransportPolicyFromClientConfig_RejectsMixedHopMode(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mixed-hop-policy.json")
	cfg := `{
  "preferred_transports": ["wireguard"],
  "allowed_transports": ["wireguard"],
  "require_common_hop_transport": false
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("ARFL_CLIENT_CONFIG", cfgPath)

	_, _, err := loadTransportPolicyFromClientConfig()
	if err == nil {
		t.Fatal("expected mixed-hop rejection error")
	}
	if !strings.Contains(err.Error(), "mixed-hop adapters are not implemented") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "require_common_hop_transport=true") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadTransportPolicyFromClientConfig_AllowsMissingCommonHopFlag(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "policy-without-flag.json")
	cfg := `{
  "preferred_transports": ["wireguard"],
  "allowed_transports": ["wireguard"]
}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("ARFL_CLIENT_CONFIG", cfgPath)

	preferred, allowed, err := loadTransportPolicyFromClientConfig()
	if err != nil {
		t.Fatalf("expected missing require_common_hop_transport to be accepted, got: %v", err)
	}
	if len(preferred) != 1 || preferred[0] != types.TransportWireGuard {
		t.Fatalf("unexpected preferred transports: %v", preferred)
	}
	if len(allowed) != 1 || allowed[0] != types.TransportWireGuard {
		t.Fatalf("unexpected allowed transports: %v", allowed)
	}
}
