package tunnel

import (
	"context"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/wg"
)

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

// IPv6 must never stay off after the session ends.
func TestDisableIPv6IsUndoneOnDown(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.DisableIPv6(); err == nil {
		t.Fatal("expected IPv6 changes to be refused while disconnected")
	}
	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.DisableIPv6(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	_ = tun.DisableIPv6()
	if fnet.ipv6Off != 1 {
		t.Fatalf("IPv6 turned off %d times, want 1", fnet.ipv6Off)
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
