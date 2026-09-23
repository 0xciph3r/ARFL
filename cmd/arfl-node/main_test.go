package main

import (
	"testing"

	"github.com/Radi-Labs/ARFL/internal/config"
	"github.com/Radi-Labs/ARFL/pkg/types"
)

func TestBuildTransportCapabilities_DefaultsToWireGuard(t *testing.T) {
	cfg := &config.NodeConfig{
		Endpoint:    "203.0.113.10:51820",
		ConnectAddr: "0.0.0.0:9091",
	}

	got := buildTransportCapabilities(cfg, derivePublicConnectURL(cfg.Endpoint, cfg.ConnectAddr))
	if len(got) != 1 {
		t.Fatalf("expected 1 transport capability, got %d", len(got))
	}
	if got[0].Transport != types.TransportWireGuard {
		t.Fatalf("expected wireguard transport, got %q", got[0].Transport)
	}
	if got[0].Endpoint != cfg.Endpoint {
		t.Fatalf("expected endpoint %q, got %q", cfg.Endpoint, got[0].Endpoint)
	}
	if got[0].ConnectURL != "http://203.0.113.10:9091" {
		t.Fatalf("unexpected connect url %q", got[0].ConnectURL)
	}
}

func TestBuildTransportCapabilities_UsesEnabledAndMaps(t *testing.T) {
	cfg := &config.NodeConfig{
		Endpoint:          "198.51.100.7:51820",
		ConnectAddr:       "0.0.0.0:9091",
		EnabledTransports: []string{"hysteria2", "wireguard", "invalid", "amneziawg"},
		TransportEndpoints: map[string]string{
			"hysteria2": "198.51.100.7:443",
			"amneziawg": "198.51.100.7:51830",
		},
		TransportConnectAddr: map[string]string{
			"hysteria2": "0.0.0.0:9092",
			"amneziawg": "0.0.0.0:9093",
		},
	}

	got := buildTransportCapabilities(cfg, derivePublicConnectURL(cfg.Endpoint, cfg.ConnectAddr))
	if len(got) != 3 {
		t.Fatalf("expected 3 capabilities (invalid ignored), got %d", len(got))
	}

	if got[0].Transport != types.TransportHysteria2 || got[0].Endpoint != "198.51.100.7:443" || got[0].ConnectURL != "http://198.51.100.7:9091" {
		t.Fatalf("unexpected hysteria2 capability: %+v", got[0])
	}
	if got[1].Transport != types.TransportWireGuard || got[1].Endpoint != "198.51.100.7:51820" || got[1].ConnectURL != "http://198.51.100.7:9091" {
		t.Fatalf("unexpected wireguard capability: %+v", got[1])
	}
	if got[2].Transport != types.TransportAmneziaWG || got[2].Endpoint != "198.51.100.7:51830" || got[2].ConnectURL != "http://198.51.100.7:9091" {
		t.Fatalf("unexpected amneziawg capability: %+v", got[2])
	}
}

func TestBuildTransportCapabilities_SkipsIncompleteAndDedupes(t *testing.T) {
	cfg := &config.NodeConfig{
		Endpoint:          "198.51.100.7:51820",
		ConnectAddr:       "0.0.0.0:9091",
		EnabledTransports: []string{"hysteria2", "hysteria2", "amneziawg", "wireguard"},
		TransportEndpoints: map[string]string{
			"wireguard": "198.51.100.7:51820",
		},
		TransportConnectAddr: map[string]string{
			"wireguard": "0.0.0.0:9091",
		},
	}

	got := buildTransportCapabilities(cfg, derivePublicConnectURL(cfg.Endpoint, cfg.ConnectAddr))
	if len(got) != 1 {
		t.Fatalf("expected only complete wireguard capability, got %d", len(got))
	}
	if got[0].Transport != types.TransportWireGuard {
		t.Fatalf("expected wireguard capability, got %+v", got[0])
	}
}

func TestBuildTransportCapabilities_CanonicalMapLookup(t *testing.T) {
	cfg := &config.NodeConfig{
		Endpoint:          "198.51.100.7:51820",
		ConnectAddr:       "0.0.0.0:9091",
		EnabledTransports: []string{" Hysteria2 "},
		TransportEndpoints: map[string]string{
			"hysteria2": "198.51.100.7:443",
		},
		TransportConnectAddr: map[string]string{
			"hysteria2": "0.0.0.0:9092",
		},
	}

	got := buildTransportCapabilities(cfg, derivePublicConnectURL(cfg.Endpoint, cfg.ConnectAddr))
	if len(got) != 1 {
		t.Fatalf("expected one hysteria2 capability, got %d", len(got))
	}
	if got[0].Transport != types.TransportHysteria2 || got[0].ConnectURL != "http://198.51.100.7:9091" {
		t.Fatalf("unexpected capability: %+v", got[0])
	}
}

func TestPreferredTransport(t *testing.T) {
	if got := preferredTransport(nil); got != types.TransportWireGuard {
		t.Fatalf("expected default wireguard, got %q", got)
	}
	if got := preferredTransport([]types.TransportCapability{{Transport: types.TransportHysteria2}}); got != types.TransportHysteria2 {
		t.Fatalf("expected hysteria2, got %q", got)
	}
}

func TestDerivePublicConnectURL_IPv6(t *testing.T) {
	got := derivePublicConnectURL("[2001:db8::1]:51820", "[::]:9091")
	if got != "http://[2001:db8::1]:9091" {
		t.Fatalf("unexpected connect URL for IPv6: %q", got)
	}
}
