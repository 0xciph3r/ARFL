package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/wg"
)

// fakeWG records interface and peer operations instead of touching the kernel.
type fakeWG struct {
	created  []wg.InterfaceConfig
	deleted  []string
	peers    map[string][]wg.PeerConfig
	failOn   string
	closeErr error
	// osNames simulates a platform that renames interfaces, as macOS does.
	osNames map[string]string
}

func newFakeWG() *fakeWG {
	return &fakeWG{peers: map[string][]wg.PeerConfig{}, osNames: map[string]string{}}
}

func (f *fakeWG) InterfaceName(logical string) string {
	if real, ok := f.osNames[logical]; ok {
		return real
	}
	return logical
}

func (f *fakeWG) CreateInterface(cfg wg.InterfaceConfig) error {
	if f.failOn == "create:"+cfg.Name {
		return errors.New("interface creation refused")
	}
	f.created = append(f.created, cfg)
	return nil
}

func (f *fakeWG) DeleteInterface(name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeWG) AddPeer(iface string, peer wg.PeerConfig) error {
	if f.failOn == "peer:"+iface {
		return errors.New("peer rejected")
	}
	f.peers[iface] = append(f.peers[iface], peer)
	return nil
}

func (f *fakeWG) Close() error { return f.closeErr }

// fakeNet records routing and DNS changes.
type fakeNet struct {
	gateway    string
	iface      string
	routeErr   error
	added      []route
	deleted    []route
	dnsSet     int
	dnsRestore int
	setDNSErr  error
	failRoute  string
}

func newFakeNet() *fakeNet {
	return &fakeNet{gateway: "192.168.1.1", iface: "en0"}
}

func (f *fakeNet) DefaultRoute() (string, string, error) {
	if f.routeErr != nil {
		return "", "", f.routeErr
	}
	return f.gateway, f.iface, nil
}

func (f *fakeNet) AddRoute(cidr, gateway, iface string) error {
	if f.failRoute == cidr {
		return errors.New("route refused")
	}
	f.added = append(f.added, route{cidr: cidr, gateway: gateway, iface: iface})
	return nil
}

func (f *fakeNet) DeleteRoute(cidr, gateway, iface string) error {
	f.deleted = append(f.deleted, route{cidr: cidr, gateway: gateway, iface: iface})
	return nil
}

func (f *fakeNet) SetDNS(resolver string) error {
	if f.setDNSErr != nil {
		return f.setDNSErr
	}
	f.dnsSet++
	return nil
}

func (f *fakeNet) RestoreDNS() error {
	f.dnsRestore++
	return nil
}

func validConfig() app.TunnelConfig {
	return app.TunnelConfig{
		ClientKey: "client-key",
		Entry: app.HopConfig{
			NodeID:       "entry-1",
			Endpoint:     "203.0.113.10:51820",
			NodeWGPubkey: "entry-pubkey",
			TunnelIP:     "10.100.0.2/32",
		},
		Exit: app.HopConfig{
			NodeID:       "exit-1",
			Endpoint:     "198.51.100.20:51821",
			NodeWGPubkey: "exit-pubkey",
			TunnelIP:     "10.200.0.2/32",
		},
	}
}

// newReadyTunnel returns a tunnel with a keypair already generated.
func newReadyTunnel(t *testing.T, w WireGuard, n netConfigurator) *Tunnel {
	t.Helper()
	tun := newTunnel(w, n)
	if _, err := tun.PublicKey(); err != nil {
		t.Fatalf("public key: %v", err)
	}
	return tun
}

func TestUpCreatesBothHops(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	if len(fwg.created) != 2 {
		t.Fatalf("created %d interfaces, want 2", len(fwg.created))
	}
	outer, inner := fwg.created[0], fwg.created[1]
	if outer.Name != OuterInterface || inner.Name != InnerInterface {
		t.Fatalf("interfaces = %q, %q", outer.Name, inner.Name)
	}

	// The inner tunnel is encapsulated inside the outer one, so its MTU must be
	// smaller or packets fragment and the connection stalls.
	if inner.MTU >= outer.MTU {
		t.Errorf("inner MTU %d is not below outer MTU %d", inner.MTU, outer.MTU)
	}

	if got := len(fwg.peers[OuterInterface]); got != 1 {
		t.Fatalf("outer peers = %d, want 1", got)
	}
	if got := len(fwg.peers[InnerInterface]); got != 1 {
		t.Fatalf("inner peers = %d, want 1", got)
	}
	if fnet.dnsSet != 1 {
		t.Errorf("DNS set %d times, want 1", fnet.dnsSet)
	}
}

// The exit node's address must be reachable through the outer tunnel,
// otherwise inner-tunnel packets leak onto the local network in the clear.
func TestExitNodeIsRoutedThroughOuterTunnel(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	entryPeer := fwg.peers[OuterInterface][0]
	var allowsExit bool
	for _, cidr := range entryPeer.AllowedIPs {
		if cidr == "198.51.100.20/32" {
			allowsExit = true
		}
	}
	if !allowsExit {
		t.Errorf("entry peer AllowedIPs %v does not carry the exit node", entryPeer.AllowedIPs)
	}

	var exitRoute *route
	for i := range fnet.added {
		if fnet.added[i].cidr == "198.51.100.20/32" {
			exitRoute = &fnet.added[i]
		}
	}
	if exitRoute == nil {
		t.Fatal("no route pinning the exit node to the outer tunnel")
	}
	if exitRoute.iface != OuterInterface {
		t.Errorf("exit route uses %q, want %q", exitRoute.iface, OuterInterface)
	}
}

// The entry node must stay reachable via the real gateway. Routing it into the
// tunnel would send the tunnel's own packets through itself and deadlock.
func TestEntryNodeStaysOnTheRealGateway(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	var entryRoute *route
	for i := range fnet.added {
		if fnet.added[i].cidr == "203.0.113.10/32" {
			entryRoute = &fnet.added[i]
		}
	}
	if entryRoute == nil {
		t.Fatal("no route pinning the entry node to the physical gateway")
	}
	if entryRoute.gateway != fnet.gateway || entryRoute.iface != fnet.iface {
		t.Errorf("entry route = via %q dev %q, want via %q dev %q",
			entryRoute.gateway, entryRoute.iface, fnet.gateway, fnet.iface)
	}
}

// Half-routes beat the existing default route on longest-prefix match without
// deleting it, so teardown restores connectivity by removing them alone.
func TestDefaultTrafficUsesHalfRoutes(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	wanted := map[string]bool{"0.0.0.0/1": false, "128.0.0.0/1": false}
	for _, r := range fnet.added {
		if _, ok := wanted[r.cidr]; ok {
			wanted[r.cidr] = true
			if r.iface != InnerInterface {
				t.Errorf("half route %s uses %q, want %q", r.cidr, r.iface, InnerInterface)
			}
		}
		if r.cidr == "0.0.0.0/0" {
			t.Error("default route was replaced outright; teardown could not restore it")
		}
	}
	for cidr, found := range wanted {
		if !found {
			t.Errorf("missing half route %s", cidr)
		}
	}
}

func TestDownReversesEverything(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)
	ctx := context.Background()

	if err := tun.Up(ctx, validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.Down(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}

	if len(fnet.deleted) != len(fnet.added) {
		t.Errorf("deleted %d routes but added %d", len(fnet.deleted), len(fnet.added))
	}
	if len(fwg.deleted) != 2 {
		t.Errorf("deleted %d interfaces, want 2", len(fwg.deleted))
	}
	if fnet.dnsRestore != 1 {
		t.Errorf("DNS restored %d times, want 1", fnet.dnsRestore)
	}
}

// A failure partway through bring-up must not leave the machine with a
// half-configured routing table and no working internet.
func TestFailedBringUpRollsBack(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fwg.failOn = "peer:" + InnerInterface
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected bring-up to fail")
	}

	if len(fnet.deleted) != len(fnet.added) {
		t.Errorf("rolled back %d routes but added %d", len(fnet.deleted), len(fnet.added))
	}
	if len(fwg.deleted) != len(fwg.created) {
		t.Errorf("rolled back %d interfaces but created %d", len(fwg.deleted), len(fwg.created))
	}
	// DNS was never changed, so it must not have been "restored" either.
	if fnet.dnsRestore != 0 {
		t.Errorf("DNS restored %d times despite never being set", fnet.dnsRestore)
	}
	if tun.active != nil {
		t.Error("tunnel still reports an active session after a failed bring-up")
	}
}

// DNS is set last, so a failure there must still unwind the routes.
func TestDNSFailureRollsBackRoutes(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fnet.setDNSErr = errors.New("resolver locked")
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected bring-up to fail")
	}
	if len(fnet.deleted) != len(fnet.added) {
		t.Errorf("rolled back %d routes but added %d", len(fnet.deleted), len(fnet.added))
	}
	if len(fwg.deleted) != 2 {
		t.Errorf("rolled back %d interfaces, want 2", len(fwg.deleted))
	}
}

func TestUpTwiceIsRejected(t *testing.T) {
	tun := newReadyTunnel(t, newFakeWG(), newFakeNet())
	ctx := context.Background()

	if err := tun.Up(ctx, validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.Up(ctx, validConfig()); err == nil {
		t.Fatal("expected the second bring-up to be rejected")
	}
}

func TestDownWhenAlreadyDownIsNoOp(t *testing.T) {
	tun := newReadyTunnel(t, newFakeWG(), newFakeNet())
	if err := tun.Down(context.Background()); err != nil {
		t.Fatalf("down on an idle tunnel: %v", err)
	}
}

// A stable key across sessions would give nodes an identifier linking every
// session to one client, undoing the unlinkability of the blind-signed tokens.
func TestKeypairIsFreshAfterDisconnect(t *testing.T) {
	tun := newReadyTunnel(t, newFakeWG(), newFakeNet())
	ctx := context.Background()

	first, err := tun.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	// Repeat calls within one session must be stable, or the key handed to the
	// nodes would not match the one the tunnel actually uses.
	again, _ := tun.PublicKey()
	if first != again {
		t.Error("public key changed within a single session")
	}

	if err := tun.Up(ctx, validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.Down(ctx); err != nil {
		t.Fatalf("down: %v", err)
	}

	second, err := tun.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if first == second {
		t.Error("the same WireGuard key was reused for a new session")
	}
}

func TestUpWithoutKeypairIsRejected(t *testing.T) {
	tun := newTunnel(newFakeWG(), newFakeNet())
	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected bring-up without a keypair to fail")
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	cases := map[string]func(*app.TunnelConfig){
		"missing entry endpoint":  func(c *app.TunnelConfig) { c.Entry.Endpoint = "" },
		"missing exit pubkey":     func(c *app.TunnelConfig) { c.Exit.NodeWGPubkey = "" },
		"missing entry tunnel IP": func(c *app.TunnelConfig) { c.Entry.TunnelIP = "" },
		"tunnel IP without CIDR":  func(c *app.TunnelConfig) { c.Exit.TunnelIP = "10.200.0.2" },
		"identical endpoints": func(c *app.TunnelConfig) {
			c.Exit.Endpoint = c.Entry.Endpoint
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fwg, fnet := newFakeWG(), newFakeNet()
			tun := newReadyTunnel(t, fwg, fnet)

			cfg := validConfig()
			mutate(&cfg)

			if err := tun.Up(context.Background(), cfg); err == nil {
				t.Fatal("expected the config to be rejected")
			}
			if len(fwg.created) != 0 {
				t.Error("an interface was created despite invalid config")
			}
		})
	}
}

// Losing the default route before it is recorded would make teardown unable to
// restore connectivity, so bring-up must stop rather than proceed blindly.
func TestMissingDefaultRouteAbortsBringUp(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fnet.routeErr = errors.New("no default route")
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected bring-up to fail without a default route")
	}
	if len(fwg.created) != 0 {
		t.Error("an interface was created before the default route was known")
	}
}

func TestHostOfResolvesLiteralAddresses(t *testing.T) {
	host, err := hostOf("203.0.113.10:51820")
	if err != nil {
		t.Fatalf("hostOf: %v", err)
	}
	if host != "203.0.113.10" {
		t.Errorf("host = %q, want 203.0.113.10", host)
	}

	if _, err := hostOf("no-port"); err == nil {
		t.Error("expected an endpoint without a port to fail")
	}
}

// Teardown must attempt every step: stopping at the first failure could leave
// the machine with no working default route.
func TestTeardownContinuesPastFailures(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)
	ctx := context.Background()

	if err := tun.Up(ctx, validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	failing := &failingNet{fakeNet: fnet}
	tun.net = failing

	err := tun.Down(ctx)
	if err == nil {
		t.Fatal("expected teardown to report the failures")
	}
	if !strings.Contains(err.Error(), "teardown incomplete") {
		t.Errorf("error = %v, want a teardown-incomplete report", err)
	}
	if len(fwg.deleted) != 2 {
		t.Errorf("deleted %d interfaces, want 2 despite route failures", len(fwg.deleted))
	}
	if tun.active != nil {
		t.Error("session must be cleared even when teardown fails")
	}
}

type failingNet struct {
	*fakeNet
}

func (f *failingNet) DeleteRoute(cidr, gateway, iface string) error {
	return errors.New("route deletion refused")
}

// On macOS the utun driver refuses arbitrary interface names, so the kernel
// picks utunN and the tunnel's logical names never exist at the OS level.
// Routes naming the logical interface would silently target nothing, leaving
// traffic outside the tunnel.
func TestRoutesUseTheNameTheOSAssigned(t *testing.T) {
	fwg, fnet := newFakeWG(), newFakeNet()
	fwg.osNames[OuterInterface] = "utun4"
	fwg.osNames[InnerInterface] = "utun5"
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	for _, r := range fnet.added {
		if r.iface == OuterInterface || r.iface == InnerInterface {
			t.Fatalf("route %s uses the logical name %q instead of the assigned one", r.cidr, r.iface)
		}
	}

	// The exit node must be reachable over the outer interface, and default
	// traffic over the inner one.
	byCIDR := map[string]string{}
	for _, r := range fnet.added {
		byCIDR[r.cidr] = r.iface
	}
	if got := byCIDR["198.51.100.20/32"]; got != "utun4" {
		t.Errorf("exit route iface = %q, want utun4", got)
	}
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if got := byCIDR[half]; got != "utun5" {
			t.Errorf("half route %s iface = %q, want utun5", half, got)
		}
	}
}
