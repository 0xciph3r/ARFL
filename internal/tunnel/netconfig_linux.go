//go:build linux

package tunnel

import (
	"fmt"
	"os"
	"strings"
)

// resolvBackup holds the original resolv.conf while the tunnel owns DNS.
const resolvBackup = "/etc/resolv.conf.arfl.bak"

// linuxConfigurator drives iproute2 and /etc/resolv.conf. It requires root.
type linuxConfigurator struct{}

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
