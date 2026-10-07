package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/wg"
)

func TestIPv6PreflightProbesAndRestoresBeforeConnection(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)
	if err := tun.preflightIPv6Locked(); err != nil {
		t.Fatalf("preflight IPv6 probe: %v", err)
	}
	if fnet.ipv6Off != 1 || fnet.ipv6Restore != 1 || tun.active != nil || tun.pendingCleanup != nil {
		t.Fatalf("preflight left changes: installs=%d restores=%d active=%v pending=%v",
			fnet.ipv6Off, fnet.ipv6Restore, tun.active, tun.pendingCleanup)
	}
	if len(fwg.created) != 0 || len(fnet.added) != 0 {
		t.Fatal("preflight must not create interfaces or routes")
	}
	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("connect after probe: %v", err)
	}
	if fnet.ipv6Off != 2 {
		t.Fatalf("connection must install a fresh block, got %d installs", fnet.ipv6Off)
	}
	if err := tun.Down(context.Background()); err != nil || fnet.ipv6Restore != 2 {
		t.Fatalf("disconnect after probe: %v (restores=%d)", err, fnet.ipv6Restore)
	}
}

func TestIPv6PreflightFailureRollsBackWithoutConnecting(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fnet.ipv6Err = errors.New("firewall policy refused")
	tun := newReadyTunnel(t, fwg, fnet)
	if err := tun.preflightIPv6Locked(); err == nil || !strings.Contains(err.Error(), "firewall policy refused") {
		t.Fatalf("preflight must reject missing capability: %v", err)
	}
	if fnet.ipv6Restore != 1 || tun.pendingCleanup != nil || tun.active != nil {
		t.Fatalf("failed preflight must undo partial install: restores=%d pending=%v", fnet.ipv6Restore, tun.pendingCleanup)
	}
	if len(fnet.added) != 0 || len(fwg.created) != 0 {
		t.Fatal("failed preflight must not bring up the tunnel")
	}
}

func TestIPv6PreflightFailedCleanupIsVisibleAndRetryable(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fnet.ipv6RestoreErr = errors.New("rule locked")
	tun := newReadyTunnel(t, fwg, fnet)
	if err := tun.preflightIPv6Locked(); err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("preflight must report failed cleanup: %v", err)
	}
	if tun.active != nil || tun.pendingCleanup == nil {
		t.Fatal("failed cleanup must not look connected, but remain retryable")
	}
	if err := tun.Up(context.Background(), validConfig()); err == nil || !strings.Contains(err.Error(), "teardown incomplete") {
		t.Fatalf("must reject new session before cleanup: %v", err)
	}
	if fnet.ipv6Off != 1 {
		t.Fatal("blocked retry reinstalled IPv6 protection")
	}
	fnet.ipv6RestoreErr = nil
	if err := tun.preflightIPv6Locked(); err != nil || tun.pendingCleanup != nil {
		t.Fatalf("retry preflight cleanup and probe: %v (pending=%v)", err, tun.pendingCleanup)
	}
	if fnet.ipv6Off != 2 || fnet.ipv6Restore != 3 {
		t.Fatalf("retry must clean old block, probe, then clean new block: installs=%d restores=%d",
			fnet.ipv6Off, fnet.ipv6Restore)
	}
}

func TestUsageReadsTheInnerTunnelOnly(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if _, ok, _ := tun.Usage(); ok {
		t.Fatal("usage reported before the tunnel is up")
	}
	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	fwg.stats = map[string][]wg.PeerStats{
		InnerInterface: {{ReceiveBytes: 3_000, TransmitBytes: 500}},
		OuterInterface: {{ReceiveBytes: 9_999, TransmitBytes: 9_999}},
	}
	u, ok, err := tun.Usage()
	if err != nil || !ok {
		t.Fatalf("usage: ok=%v err=%v", ok, err)
	}
	if u.RxBytes != 3_000 || u.TxBytes != 500 {
		t.Fatalf("got %+v, want only the inner tunnel's counters", u)
	}
}

// IPv6 is protected automatically, including the staged outer-only interval.
func TestIPv6ProtectionIsUndoneOnDown(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.DisableIPv6(); err == nil {
		t.Fatal("manual IPv6 disable must be refused while disconnected")
	}
	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.DisableIPv6(); err == nil {
		t.Fatal("manual IPv6 disable must not report success while connected")
	}
	if fnet.ipv6Off != 1 {
		t.Fatalf("IPv6 block installed %d times, want 1", fnet.ipv6Off)
	}
	if err := tun.Down(context.Background()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if fnet.ipv6Restore != 1 {
		t.Fatalf("IPv6 restored %d times, want 1", fnet.ipv6Restore)
	}
}

func TestTunnelInterfacesAreNotTreatedAsLeaks(t *testing.T) {
	for _, name := range []string{"utun4", "arfl-inner", "wg0", "awdl0"} {
		if !isTunnelInterface(name) {
			t.Errorf("%s should be skipped", name)
		}
	}
	for _, name := range []string{"en0", "eth0", "Wi-Fi"} {
		if isTunnelInterface(name) {
			t.Errorf("%s should be checked", name)
		}
	}
}
