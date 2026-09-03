package wg

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// WgctrlManager implements Manager using the wgctrl-go library for
// configuration and OS commands for interface creation.
type WgctrlManager struct {
	client *wgctrl.Client

	// osNames maps the caller's logical interface name to the name the OS
	// actually assigned.
	//
	// They differ on macOS, where the utun driver refuses arbitrary names and
	// the kernel picks utunN. Every OS-level call — wgctrl, ifconfig, route —
	// must use the real name, so the mapping is resolved centrally here rather
	// than leaking the platform difference to callers.
	mu      sync.Mutex
	osNames map[string]string
}

// NewManager creates a new WgctrlManager.
func NewManager() (*WgctrlManager, error) {
	client, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("create wgctrl client: %w", err)
	}
	return &WgctrlManager{client: client, osNames: map[string]string{}}, nil
}

// InterfaceName returns the OS-level name for a logical interface.
//
// Callers that run their own network commands — adding routes, for example —
// must use this rather than the name they passed to CreateInterface.
func (m *WgctrlManager) InterfaceName(logical string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if real, ok := m.osNames[logical]; ok {
		return real
	}
	return logical
}

func (m *WgctrlManager) setOSName(logical, real string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.osNames[logical] = real
}

func (m *WgctrlManager) forgetOSName(logical string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.osNames, logical)
}

func (m *WgctrlManager) CreateInterface(cfg InterfaceConfig) error {
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	cfg.MTU = mtu

	// The full config is passed through because Windows installs a tunnel
	// service that must be given the key and address up front, whereas Linux
	// and macOS create a bare interface and configure it afterwards.
	//
	// The OS may not honour the requested name, so everything below uses the
	// name it reports back.
	osName, err := m.createOSInterface(cfg)
	if err != nil {
		return fmt.Errorf("create OS interface %s: %w", cfg.Name, err)
	}
	m.setOSName(cfg.Name, osName)

	privKey, err := ParseKey(cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}

	port := cfg.ListenPort
	wgCfg := wgtypes.Config{
		PrivateKey: &privKey,
	}
	if port > 0 {
		wgCfg.ListenPort = &port
	}

	if err := m.client.ConfigureDevice(osName, wgCfg); err != nil {
		return fmt.Errorf("configure device %s: %w", osName, err)
	}

	if err := m.setAddress(osName, cfg.Address, mtu); err != nil {
		return fmt.Errorf("set address on %s: %w", osName, err)
	}

	if err := m.bringUp(osName); err != nil {
		return fmt.Errorf("bring up %s: %w", osName, err)
	}

	return nil
}

func (m *WgctrlManager) DeleteInterface(name string) error {
	err := m.deleteOSInterface(m.InterfaceName(name))
	// Drop the mapping either way: a stale entry would make a later attempt to
	// recreate the interface talk to a device that no longer exists.
	m.forgetOSName(name)
	return err
}

func (m *WgctrlManager) AddPeer(iface string, peer PeerConfig) error {
	pubKey, err := ParseKey(peer.PublicKey)
	if err != nil {
		return fmt.Errorf("parse peer public key: %w", err)
	}

	peerCfg := wgtypes.PeerConfig{
		PublicKey:         pubKey,
		ReplaceAllowedIPs: true,
	}

	if peer.Endpoint != "" {
		addr, err := net.ResolveUDPAddr("udp", peer.Endpoint)
		if err != nil {
			return fmt.Errorf("resolve endpoint %s: %w", peer.Endpoint, err)
		}
		peerCfg.Endpoint = addr
	}

	for _, cidr := range peer.AllowedIPs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("parse allowed IP %s: %w", cidr, err)
		}
		peerCfg.AllowedIPs = append(peerCfg.AllowedIPs, *ipNet)
	}

	if peer.Keepalive > 0 {
		dur := time.Duration(peer.Keepalive) * time.Second
		peerCfg.PersistentKeepaliveInterval = &dur
	}

	return m.client.ConfigureDevice(m.InterfaceName(iface), wgtypes.Config{
		Peers: []wgtypes.PeerConfig{peerCfg},
	})
}

func (m *WgctrlManager) RemovePeer(iface string, pubkey string) error {
	key, err := ParseKey(pubkey)
	if err != nil {
		return fmt.Errorf("parse peer public key: %w", err)
	}

	return m.client.ConfigureDevice(m.InterfaceName(iface), wgtypes.Config{
		Peers: []wgtypes.PeerConfig{{
			PublicKey: key,
			Remove:    true,
		}},
	})
}

func (m *WgctrlManager) GetPeerStats(iface string) ([]PeerStats, error) {
	dev, err := m.client.Device(m.InterfaceName(iface))
	if err != nil {
		return nil, fmt.Errorf("get device %s: %w", iface, err)
	}

	stats := make([]PeerStats, 0, len(dev.Peers))
	for _, peer := range dev.Peers {
		ps := PeerStats{
			// Must match the encoding used by AddPeer and GenerateKeyPair,
			// otherwise callers cannot correlate stats with the peer they added.
			PublicKey:     peer.PublicKey.String(),
			ReceiveBytes:  peer.ReceiveBytes,
			TransmitBytes: peer.TransmitBytes,
			TotalBytes:    peer.ReceiveBytes + peer.TransmitBytes,
			LastHandshake: peer.LastHandshakeTime,
		}
		if peer.Endpoint != nil {
			ps.Endpoint = *peer.Endpoint
		}
		for _, aip := range peer.AllowedIPs {
			ps.AllowedIPs = append(ps.AllowedIPs, aip)
		}
		stats = append(stats, ps)
	}
	return stats, nil
}

func (m *WgctrlManager) Close() error {
	return m.client.Close()
}

// --- Helpers ---

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
