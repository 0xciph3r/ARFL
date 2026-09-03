// Package tunnel establishes the nested two-hop WireGuard tunnel that carries
// ARFL traffic.
//
// Two interfaces are created. The outer tunnel reaches the entry node
// directly. The inner tunnel targets the exit node, but its packets are routed
// through the outer tunnel, so the exit node only ever sees traffic arriving
// from the entry node and never learns the client's real address.
//
// Everything platform-specific lives behind the netConfigurator interface:
// creating the interface, moving routes and pointing DNS. Those operations
// need root on Unix and an elevated service on Windows, which is why this
// package is kept separate from the UI — the desktop app talks to it through
// internal/app.Tunnel rather than performing privileged work itself.
package tunnel

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/wg"
	"github.com/Radi-Labs/ARFL/pkg/protocol"
)

// Interface names for the two hops.
const (
	OuterInterface = "arfl-outer"
	InnerInterface = "arfl-inner"

	// keepaliveSeconds keeps NAT bindings alive. Residential node operators
	// are usually behind NAT, so without this the tunnel silently dies once
	// the mapping expires.
	keepaliveSeconds = 25

	// innerMTUOverhead accounts for the second WireGuard encapsulation. Packets
	// exceeding it fragment inside the outer tunnel and stall the connection.
	innerMTUOverhead = 80
)

// netConfigurator performs the privileged, OS-specific parts of bring-up.
type netConfigurator interface {
	// DefaultRoute reports the gateway and interface currently carrying
	// traffic, captured before any routes are changed.
	DefaultRoute() (gateway string, iface string, err error)
	// AddRoute directs cidr via gateway, or out of iface when gateway is empty.
	AddRoute(cidr, gateway, iface string) error
	// DeleteRoute removes a route previously added.
	DeleteRoute(cidr, gateway, iface string) error
	// SetDNS points the system resolver at the tunnel's DNS server.
	SetDNS(resolver string) error
	// RestoreDNS puts the previous resolver back.
	RestoreDNS() error
}

// WireGuard is the subset of wg.Manager the tunnel needs. Narrowing it keeps
// the package testable without a real kernel interface.
type WireGuard interface {
	CreateInterface(cfg wg.InterfaceConfig) error
	DeleteInterface(name string) error
	AddPeer(iface string, peer wg.PeerConfig) error
	// InterfaceName maps a logical interface name to the name the OS assigned.
	//
	// They differ on macOS, where the utun driver refuses arbitrary names and
	// the kernel picks utunN. Routes are added with our own commands rather
	// than through the manager, so they must be given the real name or they
	// reference an interface that does not exist.
	InterfaceName(logical string) string
	Close() error
}

// Tunnel is a platform-independent implementation of app.Tunnel.
type Tunnel struct {
	mu sync.Mutex

	wg  WireGuard
	net netConfigurator

	keys *wg.KeyPair

	// active records what was configured so teardown reverses exactly those
	// changes. A partial bring-up must not leave routes or interfaces behind.
	active *activeState
}

// activeState is the set of changes made to the system for one session.
type activeState struct {
	routes     []route
	dnsChanged bool
	interfaces []string
}

type route struct {
	cidr    string
	gateway string
	iface   string
}

// New returns a Tunnel using the host's WireGuard and network stack.
func New() (*Tunnel, error) {
	mgr, err := wg.NewManager()
	if err != nil {
		return nil, fmt.Errorf("create WireGuard manager: %w", err)
	}

	cfg, err := newNetConfigurator()
	if err != nil {
		mgr.Close()
		return nil, err
	}

	return &Tunnel{wg: mgr, net: cfg}, nil
}

// newTunnel builds a Tunnel from injected dependencies, for tests.
func newTunnel(w WireGuard, n netConfigurator) *Tunnel {
	return &Tunnel{wg: w, net: n}
}

// PublicKey returns the client's WireGuard public key, generating a keypair on
// first use.
//
// The key is deliberately ephemeral: persisting it across sessions would give
// nodes a stable identifier that links every session back to one client,
// undoing the unlinkability the blind-signed tokens provide.
func (t *Tunnel) PublicKey() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.keys == nil {
		kp, err := wg.GenerateKeyPair()
		if err != nil {
			return "", fmt.Errorf("generate WireGuard keypair: %w", err)
		}
		t.keys = kp
	}
	return t.keys.PublicKey, nil
}

// Preflight reports whether the tunnel could be brought up right now, without
// making any change.
//
// app.Service pays both nodes before calling Up, so a failure discovered
// during bring-up costs the user real sats. This is checked first so an
// unprivileged process is refused before any payment.
func (t *Tunnel) Preflight() error {
	return checkPrivileges()
}

// Up establishes the nested tunnel.
func (t *Tunnel) Up(ctx context.Context, cfg app.TunnelConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.active != nil {
		return fmt.Errorf("tunnel is already up")
	}
	if t.keys == nil {
		return fmt.Errorf("no WireGuard keypair: call PublicKey before connecting")
	}
	if err := validate(cfg); err != nil {
		return err
	}

	entryIP, err := hostOf(cfg.Entry.Endpoint)
	if err != nil {
		return fmt.Errorf("entry endpoint: %w", err)
	}
	exitIP, err := hostOf(cfg.Exit.Endpoint)
	if err != nil {
		return fmt.Errorf("exit endpoint: %w", err)
	}

	// Capture the current default route first: once the tunnel takes over the
	// routing table there is no way to rediscover where traffic used to go.
	gateway, iface, err := t.net.DefaultRoute()
	if err != nil {
		return fmt.Errorf("determine default route: %w", err)
	}

	state := &activeState{}
	if err := t.bringUp(cfg, entryIP, exitIP, gateway, iface, state); err != nil {
		// Roll back so a failed attempt does not strand the machine with a
		// half-configured routing table and no internet.
		t.teardown(state)
		return err
	}

	t.active = state
	return nil
}

func (t *Tunnel) bringUp(
	cfg app.TunnelConfig,
	entryIP, exitIP, gateway, iface string,
	state *activeState,
) error {
	// Outer tunnel: client → entry node.
	if err := t.createInterface(wg.InterfaceConfig{
		Name:       OuterInterface,
		PrivateKey: t.keys.PrivateKey,
		Address:    cfg.Entry.TunnelIP,
		MTU:        protocol.TunnelMTU,
	}, state); err != nil {
		return err
	}

	// AllowedIPs includes the exit node's address so inner-tunnel packets are
	// carried by the outer tunnel instead of leaking onto the local network.
	if err := t.wg.AddPeer(OuterInterface, wg.PeerConfig{
		PublicKey:  cfg.Entry.NodeWGPubkey,
		Endpoint:   cfg.Entry.Endpoint,
		AllowedIPs: []string{protocol.OuterTunnelSubnet, exitIP + "/32"},
		Keepalive:  keepaliveSeconds,
	}); err != nil {
		return fmt.Errorf("add entry peer: %w", err)
	}

	// Pin the entry node to the real gateway. Without this the default route
	// below would send the tunnel's own packets into the tunnel.
	if err := t.addRoute(route{cidr: entryIP + "/32", gateway: gateway, iface: iface}, state); err != nil {
		return err
	}
	// Routes must name the interface the OS actually created, which is not the
	// logical name on macOS.
	outerOS := t.wg.InterfaceName(OuterInterface)
	if err := t.addRoute(route{cidr: exitIP + "/32", iface: outerOS}, state); err != nil {
		return err
	}

	// Inner tunnel: client → exit node, carried inside the outer tunnel.
	if err := t.createInterface(wg.InterfaceConfig{
		Name:       InnerInterface,
		PrivateKey: t.keys.PrivateKey,
		Address:    cfg.Exit.TunnelIP,
		MTU:        protocol.TunnelMTU - innerMTUOverhead,
	}, state); err != nil {
		return err
	}

	if err := t.wg.AddPeer(InnerInterface, wg.PeerConfig{
		PublicKey:  cfg.Exit.NodeWGPubkey,
		Endpoint:   cfg.Exit.Endpoint,
		AllowedIPs: []string{"0.0.0.0/0"},
		Keepalive:  keepaliveSeconds,
	}); err != nil {
		return fmt.Errorf("add exit peer: %w", err)
	}

	// Two half-routes rather than 0.0.0.0/0: they outrank the existing default
	// route on longest-prefix match without deleting it, so teardown restores
	// connectivity by removing these alone.
	innerOS := t.wg.InterfaceName(InnerInterface)
	for _, half := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := t.addRoute(route{cidr: half, iface: innerOS}, state); err != nil {
			return err
		}
	}

	// DNS last: a resolver pointing into a tunnel that failed to come up would
	// break name resolution entirely.
	if err := t.net.SetDNS(protocol.DNSResolver); err != nil {
		return fmt.Errorf("set DNS: %w", err)
	}
	state.dnsChanged = true

	return nil
}

// Down tears the tunnel down and restores the previous network state.
func (t *Tunnel) Down(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.active == nil {
		return nil
	}

	err := t.teardown(t.active)
	t.active = nil
	// The keypair is dropped so the next session presents a fresh identity.
	t.keys = nil
	return err
}

// teardown reverses state, continuing past failures.
//
// Stopping at the first error would leave the machine with no working default
// route, so every step is attempted and the problems are reported together.
func (t *Tunnel) teardown(state *activeState) error {
	var problems []error

	if state.dnsChanged {
		if err := t.net.RestoreDNS(); err != nil {
			problems = append(problems, fmt.Errorf("restore DNS: %w", err))
		}
	}

	// Reverse order so the default route is handed back before the interface
	// carrying it disappears.
	for i := len(state.routes) - 1; i >= 0; i-- {
		r := state.routes[i]
		if err := t.net.DeleteRoute(r.cidr, r.gateway, r.iface); err != nil {
			problems = append(problems, fmt.Errorf("delete route %s: %w", r.cidr, err))
		}
	}

	for i := len(state.interfaces) - 1; i >= 0; i-- {
		if err := t.wg.DeleteInterface(state.interfaces[i]); err != nil {
			problems = append(problems, fmt.Errorf("delete interface %s: %w", state.interfaces[i], err))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("tunnel teardown incomplete: %w", joinErrors(problems))
}

// Close releases the tunnel, tearing down any active session first.
func (t *Tunnel) Close() error {
	if err := t.Down(context.Background()); err != nil {
		t.wg.Close()
		return err
	}
	return t.wg.Close()
}

func (t *Tunnel) createInterface(cfg wg.InterfaceConfig, state *activeState) error {
	if err := t.wg.CreateInterface(cfg); err != nil {
		return fmt.Errorf("create interface %s: %w", cfg.Name, err)
	}
	state.interfaces = append(state.interfaces, cfg.Name)
	return nil
}

func (t *Tunnel) addRoute(r route, state *activeState) error {
	if err := t.net.AddRoute(r.cidr, r.gateway, r.iface); err != nil {
		return fmt.Errorf("add route %s: %w", r.cidr, err)
	}
	state.routes = append(state.routes, r)
	return nil
}

// validate rejects hop configuration that a malicious or buggy hub could use
// to weaken the tunnel.
//
// Every field here is attacker-controlled: the hub supplies the node list and
// the nodes supply their own endpoints and tunnel addresses. Trusting them
// unchecked would let a hub silently collapse the two-hop guarantee or hijack
// the client's routing table.
func validate(cfg app.TunnelConfig) error {
	for _, hop := range []struct {
		name string
		cfg  app.HopConfig
	}{{"entry", cfg.Entry}, {"exit", cfg.Exit}} {
		if hop.cfg.Endpoint == "" {
			return fmt.Errorf("%s hop is missing an endpoint", hop.name)
		}
		if hop.cfg.NodeWGPubkey == "" {
			return fmt.Errorf("%s hop is missing a WireGuard public key", hop.name)
		}
		if hop.cfg.TunnelIP == "" {
			return fmt.Errorf("%s hop is missing a tunnel IP", hop.name)
		}
		if err := validateTunnelIP(hop.name, hop.cfg.TunnelIP); err != nil {
			return err
		}
	}

	// Distinct endpoints alone are not enough: one operator can run both nodes
	// on different addresses. A shared key means a single party observes the
	// client's real address and its destination, which is exactly what the
	// second hop exists to prevent.
	if cfg.Entry.NodeWGPubkey == cfg.Exit.NodeWGPubkey {
		return fmt.Errorf("entry and exit share a WireGuard key: both hops would be the same operator")
	}
	if cfg.Entry.Endpoint == cfg.Exit.Endpoint {
		return fmt.Errorf("entry and exit endpoints are identical: a two-hop tunnel needs distinct nodes")
	}

	return nil
}

// validateTunnelIP rejects tunnel addresses that would capture more traffic
// than the single client address they are meant to represent.
func validateTunnelIP(hop, value string) error {
	ip, ipNet, err := net.ParseCIDR(value)
	if err != nil {
		return fmt.Errorf("%s hop tunnel IP %q is not valid CIDR: %w", hop, value, err)
	}

	if ip.To4() == nil {
		return fmt.Errorf("%s hop tunnel IP %q is not IPv4", hop, value)
	}

	// A hub offering "0.0.0.0/0" or similar would have the interface claim the
	// entire address space and blackhole the machine's traffic.
	if ones, _ := ipNet.Mask.Size(); ones < 8 {
		return fmt.Errorf("%s hop tunnel IP %q covers too broad a range", hop, value)
	}

	// Tunnel addresses are private by construction. A public address here would
	// route real internet traffic onto the interface.
	if !ip.IsPrivate() {
		return fmt.Errorf("%s hop tunnel IP %q is not a private address", hop, value)
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("%s hop tunnel IP %q is a reserved address", hop, value)
	}

	return nil
}

// hostOf extracts the IP from a host:port endpoint, resolving a hostname if
// needed. Routing rules require a literal address.
//
// Addresses that are not routable on the public internet are rejected. A
// hostile hub could otherwise publish an endpoint resolving to loopback or the
// user's LAN, and the pinning route added for it would redirect local traffic
// or point the tunnel at a machine on the victim's own network.
func hostOf(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}
	if port == "" {
		return "", fmt.Errorf("endpoint %q has no port", endpoint)
	}

	if ip := net.ParseIP(host); ip != nil {
		if err := checkRoutable(ip); err != nil {
			return "", fmt.Errorf("endpoint %q: %w", endpoint, err)
		}
		return ip.String(), nil
	}

	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() == nil {
			continue
		}
		if err := checkRoutable(ip); err != nil {
			return "", fmt.Errorf("endpoint %q resolved to %s: %w", endpoint, ip, err)
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("no usable IPv4 address for %q", host)
}

// checkRoutable rejects addresses a node endpoint must never resolve to.
func checkRoutable(ip net.IP) error {
	switch {
	case ip.To4() == nil:
		return fmt.Errorf("address %s is not IPv4", ip)
	case ip.IsLoopback():
		return fmt.Errorf("address %s is loopback", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("address %s is unspecified", ip)
	case ip.IsMulticast(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("address %s is reserved", ip)
	case ip.IsPrivate():
		// A node on the user's own LAN cannot provide the network-level
		// separation the two-hop design depends on.
		return fmt.Errorf("address %s is a private address", ip)
	}
	return nil
}

func joinErrors(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	msg := ""
	for i, err := range errs {
		if i > 0 {
			msg += "; "
		}
		msg += err.Error()
	}
	return fmt.Errorf("%s", msg)
}
