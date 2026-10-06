package tunnel

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Usage is the traffic carried by the inner tunnel since it came up.
type Usage struct {
	RxBytes int64 `json:"rx_bytes"`
	TxBytes int64 `json:"tx_bytes"`
	// EntryHandshake and ExitHandshake are the latest WireGuard handshakes
	// with each node. Peers re-handshake about every two minutes while
	// traffic flows, so an old one means the node stopped answering.
	EntryHandshake time.Time `json:"entry_handshake"`
	ExitHandshake  time.Time `json:"exit_handshake"`
}

// Usage reports bytes through the inner (exit) tunnel. Every byte the user
// sends or receives crosses it exactly once, so it is the honest measure of
// what the session has used. ok is false when no tunnel is up.
func (t *Tunnel) Usage() (Usage, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil || !t.active.innerReady {
		return Usage{}, false, nil
	}
	stats, err := t.wg.GetPeerStats(InnerInterface)
	if err != nil {
		return Usage{}, true, fmt.Errorf("read tunnel counters: %w", err)
	}
	var u Usage
	for _, s := range stats {
		u.RxBytes += s.ReceiveBytes
		u.TxBytes += s.TransmitBytes
		if s.LastHandshake.After(u.ExitHandshake) {
			u.ExitHandshake = s.LastHandshake
		}
	}
	if outer, err := t.wg.GetPeerStats(OuterInterface); err == nil {
		for _, s := range outer {
			if s.LastHandshake.After(u.EntryHandshake) {
				u.EntryHandshake = s.LastHandshake
			}
		}
	}
	return u, true, nil
}

// DisableIPv6 turns IPv6 off for the rest of the session. Teardown restores
// it, so the change never outlives the tunnel.
func (t *Tunnel) DisableIPv6() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		return fmt.Errorf("connect first: IPv6 is only turned off while the tunnel is up")
	}
	if t.active.ipv6Changed {
		return nil
	}
	if err := t.net.DisableIPv6(); err != nil {
		return err
	}
	t.active.ipv6Changed = true
	return nil
}

// IPv6Exposed reports whether a physical adapter has a global IPv6 address.
// The tunnel carries IPv4 only, so such an address lets traffic bypass it.
func IPv6Exposed() (bool, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false, fmt.Errorf("list network interfaces: %w", err)
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || isTunnelInterface(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() != nil {
				continue
			}
			if ipnet.IP.IsGlobalUnicast() && !ipnet.IP.IsPrivate() {
				return true, nil
			}
		}
	}
	return false, nil
}

// isTunnelInterface skips virtual adapters (ours and other VPNs') whose IPv6
// addresses do not reach the internet directly.
func isTunnelInterface(name string) bool {
	for _, prefix := range []string{"utun", "arfl", "wg", "tun", "tap", "awdl", "llw", "bridge", "docker", "veth"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
