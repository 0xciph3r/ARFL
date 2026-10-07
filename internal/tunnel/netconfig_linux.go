//go:build linux

package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// resolvBackup holds the original resolv.conf while the tunnel owns DNS.
const resolvBackup = "/etc/resolv.conf.arfl.bak"

// linuxConfigurator drives iproute2 and /etc/resolv.conf. It requires root.
type linuxConfigurator struct {
	ipv6Blocked bool
	ipv6RuleID  string
}

func newNetConfigurator() (netConfigurator, error) {
	return &linuxConfigurator{}, nil
}

func (c *linuxConfigurator) DefaultRoute() (string, string, error) {
	out, err := output("ip", "route", "show", "default")
	if err != nil {
		return "", "", err
	}

	fields := strings.Fields(out)
	var gateway, iface string
	for i, f := range fields {
		switch f {
		case "via":
			if i+1 < len(fields) {
				gateway = fields[i+1]
			}
		case "dev":
			if i+1 < len(fields) {
				iface = fields[i+1]
			}
		}
	}
	if iface == "" {
		return "", "", fmt.Errorf("no default route found: %q", strings.TrimSpace(out))
	}
	return gateway, iface, nil
}

func (c *linuxConfigurator) AddRoute(cidr, gateway, iface string) error {
	args := []string{"route", "replace", cidr}
	if gateway != "" {
		args = append(args, "via", gateway)
	}
	if iface != "" {
		args = append(args, "dev", iface)
	}
	return run("ip", args...)
}

func (c *linuxConfigurator) DeleteRoute(cidr, gateway, iface string) error {
	if err := run("ip", "route", "del", cidr); err != nil {
		// A route that is already gone is the desired end state, not a failure.
		if strings.Contains(err.Error(), "No such process") {
			return nil
		}
		return err
	}
	return nil
}

func (c *linuxConfigurator) SetDNS(resolver string) error {
	current, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return fmt.Errorf("read resolv.conf: %w", err)
	}

	// Never overwrite an existing backup: a second SetDNS would otherwise save
	// the tunnel's own resolver as the "original" and RestoreDNS could never
	// recover the user's real configuration.
	if _, err := os.Stat(resolvBackup); os.IsNotExist(err) {
		if err := os.WriteFile(resolvBackup, current, 0o644); err != nil {
			return fmt.Errorf("back up resolv.conf: %w", err)
		}
	}

	content := fmt.Sprintf("# Managed by ARFL while the tunnel is up.\nnameserver %s\n", resolver)
	if err := os.WriteFile("/etc/resolv.conf", []byte(content), 0o644); err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	return nil
}

func (c *linuxConfigurator) RestoreDNS() error {
	backup, err := os.ReadFile(resolvBackup)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read resolv.conf backup: %w", err)
	}

	if err := os.WriteFile("/etc/resolv.conf", backup, 0o644); err != nil {
		return fmt.Errorf("restore resolv.conf: %w", err)
	}
	return os.Remove(resolvBackup)
}

func (c *linuxConfigurator) DisableIPv6() error {
	if c.ipv6Blocked {
		return nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("identify IPv6 rule: %w", err)
	}
	c.ipv6RuleID = "arfl-" + hex.EncodeToString(nonce[:])
	// OUTPUT is consulted for locally generated traffic on every interface,
	// including adapters brought up after the tunnel started. Insert first
	// so earlier user rules cannot ACCEPT around this rule.
	if err := run("ip6tables", "-w", "-I", "OUTPUT", "1", "-m", "comment", "--comment", c.ipv6RuleID, "-j", "REJECT"); err != nil {
		return fmt.Errorf("block outbound IPv6: %w", err)
	}
	c.ipv6Blocked = true
	out, err := output("ip6tables", "-w", "-S", "OUTPUT")
	if err != nil {
		return fmt.Errorf("verify outbound IPv6 block: %w", err)
	}
	if !ipv6RuleFirst(out, c.ipv6RuleID) {
		return fmt.Errorf("outbound IPv6 block is not first in OUTPUT")
	}
	return nil
}

func ipv6RuleFirst(rules, id string) bool {
	for _, line := range strings.Split(rules, "\n") {
		if strings.HasPrefix(line, "-A OUTPUT ") {
			return strings.Contains(line, "--comment "+id+" ") && strings.HasSuffix(line, " -j REJECT")
		}
	}
	return false
}

func (c *linuxConfigurator) RestoreIPv6() error {
	if !c.ipv6Blocked {
		return nil
	}
	if err := run("ip6tables", "-w", "-D", "OUTPUT", "-m", "comment", "--comment", c.ipv6RuleID, "-j", "REJECT"); err != nil {
		return fmt.Errorf("remove outbound IPv6 block: %w", err)
	}
	c.ipv6Blocked = false
	return nil
}
