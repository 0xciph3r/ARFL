package routing

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EnableForwarding turns on IP forwarding in the Linux kernel.
//
// Why this is needed: by default, the kernel drops packets that aren't
// addressed to this machine. An ARFL node is a ROUTER — it receives
// packets on the WireGuard interface and forwards them out to the internet
// (or to the next hop). Without this, every packet would be silently dropped.
//
// This is the equivalent of:
//   sysctl -w net.ipv4.ip_forward=1
func EnableForwarding() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

// SetupNAT configures iptables to masquerade traffic from a WireGuard
// interface going out to the internet.
//
// Why this is needed: packets from clients have private tunnel IPs
// (e.g. 10.100.0.2). The internet doesn't know how to route to those.
// NAT (Network Address Translation) rewrites the source IP to the
// node's public IP so replies come back to the right place.
//
// MASQUERADE means "use whatever IP is on the outgoing interface" —
// this works even if the node's public IP changes (DHCP, cloud).
func SetupNAT(wgIface, outIface string) error {
	commands := [][]string{
		// Masquerade: rewrite source IP on outbound traffic
		{"iptables", "-t", "nat", "-A", "POSTROUTING",
			"-s", subnetForIface(wgIface), "-o", outIface, "-j", "MASQUERADE"},

		// Allow forwarding from WireGuard to internet
		{"iptables", "-A", "FORWARD",
			"-i", wgIface, "-o", outIface, "-j", "ACCEPT"},

		// Allow return traffic (established connections) back through
		{"iptables", "-A", "FORWARD",
			"-i", outIface, "-o", wgIface,
			"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	for _, cmd := range commands {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return fmt.Errorf("setup NAT rule: %w", err)
		}
	}
	return nil
}

// CleanupNAT removes the iptables rules added by SetupNAT.
// Called on graceful shutdown so the node doesn't leave stale firewall rules.
func CleanupNAT(wgIface, outIface string) error {
	commands := [][]string{
		{"iptables", "-t", "nat", "-D", "POSTROUTING",
			"-s", subnetForIface(wgIface), "-o", outIface, "-j", "MASQUERADE"},
		{"iptables", "-D", "FORWARD",
			"-i", wgIface, "-o", outIface, "-j", "ACCEPT"},
		{"iptables", "-D", "FORWARD",
			"-i", outIface, "-o", wgIface,
			"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}

	var firstErr error
	for _, cmd := range commands {
		if err := run(cmd[0], cmd[1:]...); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// subnetForIface returns the expected subnet for an ARFL WireGuard interface.
// Entry nodes use 10.100.0.0/16, exit nodes use 10.200.0.0/16.
func subnetForIface(iface string) string {
	if strings.Contains(iface, "exit") || strings.Contains(iface, "inner") {
		return "10.200.0.0/16"
	}
	return "10.100.0.0/16"
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
