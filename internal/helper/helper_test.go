package helper

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
)

type fakeBackend struct {
	ups   []app.TunnelConfig
	downs int
	reset int
	ipv6  int
}

func (f *fakeBackend) PublicKey() (string, error) { return "client-key", nil }
func (f *fakeBackend) Preflight() error           { return nil }
func (f *fakeBackend) ValidateEndpoints(entry, exit string) error {
	if entry == exit {
		return errors.New("entry and exit both resolve to one host")
	}
	return nil
}
func (f *fakeBackend) Up(_ context.Context, cfg app.TunnelConfig) error {
	f.ups = append(f.ups, cfg)
	return nil
}
func (f *fakeBackend) UpOuter(ctx context.Context, cfg app.TunnelConfig) error { return f.Up(ctx, cfg) }
func (f *fakeBackend) UpInner(ctx context.Context, cfg app.TunnelConfig) error { return f.Up(ctx, cfg) }
func (f *fakeBackend) Down(context.Context) error                              { f.downs++; return nil }
func (f *fakeBackend) PrepareHopKeys() (string, string, error)                 { return "entry-pub", "exit-pub", nil }
func (f *fakeBackend) ResetHopKeys()                                           { f.reset++ }
func (f *fakeBackend) Usage() (tunnel.Usage, bool, error) {
	return tunnel.Usage{RxBytes: 10, TxBytes: 5, ExitHandshake: time.Unix(100, 0)}, true, nil
}
func (f *fakeBackend) DisableIPv6() error { f.ipv6++; return nil }

// startServer serves backend on a temporary socket. peer stands in for the
// kernel's report of who connected.
func startServer(t *testing.T, backend Backend, allowed, peer int) *Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "arflh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	srv := NewServer(backend, allowed)
	srv.peerUID = func(net.Conn) (int, error) { return peer, nil }
	go srv.Serve(l)
	return NewClient(sock)
}

func TestClientDrivesTheBackendAcrossTheSocket(t *testing.T) {
	fb := &fakeBackend{}
	c := startServer(t, fb, 501, 501)

	if v, err := c.Ping(); err != nil || v != Version {
		t.Fatalf("ping: v=%d err=%v", v, err)
	}
	if key, err := c.PublicKey(); err != nil || key != "client-key" {
		t.Fatalf("public key: %q %v", key, err)
	}
	if e, x, err := c.PrepareHopKeys(); err != nil || e != "entry-pub" || x != "exit-pub" {
		t.Fatalf("hop keys: %q %q %v", e, x, err)
	}
	cfg := app.TunnelConfig{Entry: app.HopConfig{NodeID: "n1", Endpoint: "203.0.113.1:51820"}}
	if err := c.UpOuter(context.Background(), cfg); err != nil {
		t.Fatalf("up outer: %v", err)
	}
	if len(fb.ups) != 1 || fb.ups[0].Entry.NodeID != "n1" {
		t.Fatalf("config did not arrive intact: %+v", fb.ups)
	}
	u, err := c.Usage()
	if err != nil || !u.Active || u.RxBytes != 10 || !u.ExitHandshake.Equal(time.Unix(100, 0)) {
		t.Fatalf("usage: %+v %v", u, err)
	}
	if err := c.DisableIPv6(); err != nil || fb.ipv6 != 1 {
		t.Fatalf("disable ipv6: %v (%d)", err, fb.ipv6)
	}
	c.ResetHopKeys()
	if err := c.Down(context.Background()); err != nil || fb.downs != 1 || fb.reset != 1 {
		t.Fatalf("down/reset: %v downs=%d reset=%d", err, fb.downs, fb.reset)
	}
}

func TestBackendErrorsReachTheCaller(t *testing.T) {
	c := startServer(t, &fakeBackend{}, 501, 501)
	err := c.ValidateEndpoints("same:1", "same:1")
	if err == nil || !strings.Contains(err.Error(), "one host") {
		t.Fatalf("got %v, want the backend's error", err)
	}
}

// The helper changes routes and DNS as root, so only its owner may use it.
func TestOtherUsersAreRefused(t *testing.T) {
	fb := &fakeBackend{}
	c := startServer(t, fb, 501, 502)
	if err := c.Up(context.Background(), app.TunnelConfig{}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("got %v, want a refusal", err)
	}
	if len(fb.ups) != 0 {
		t.Fatal("a refused caller reached the tunnel")
	}
}

func TestRootIsAlwaysAllowed(t *testing.T) {
	c := startServer(t, &fakeBackend{}, 501, 0)
	if _, err := c.Ping(); err != nil {
		t.Fatalf("root refused: %v", err)
	}
}

func TestMissingHelperIsReportedAsNotRunning(t *testing.T) {
	c := NewClient(filepath.Join(os.TempDir(), "no-arfl-helper.sock"))
	if _, err := c.Ping(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("got %v, want ErrNotRunning", err)
	}
}

func TestShellJoinQuotesSafely(t *testing.T) {
	got := shellJoin([]string{"/Applications/AR FL.app/x", "it's"})
	if got != `'/Applications/AR FL.app/x' 'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}
