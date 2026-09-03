//go:build linux

package wg

import (
	"strconv"
	"strings"
)

// Linux has WireGuard in the kernel, so interfaces are managed with iproute2.

func (m *WgctrlManager) createOSInterface(cfg InterfaceConfig) error {
	name := cfg.Name

	if err := run("ip", "link", "add", name, "type", "wireguard"); err != nil {
		// A leftover interface from a crashed session would otherwise block
		// every subsequent connection until the user rebooted.
		if strings.Contains(err.Error(), "File exists") {
			_ = run("ip", "link", "delete", name)
			return run("ip", "link", "add", name, "type", "wireguard")
		}
		return err
	}
	return nil
}

func (m *WgctrlManager) deleteOSInterface(name string) error {
	return run("ip", "link", "delete", name)
}

func (m *WgctrlManager) setAddress(name, address string, mtu int) error {
	if err := run("ip", "addr", "add", address, "dev", name); err != nil {
		return err
	}
	return run("ip", "link", "set", name, "mtu", strconv.Itoa(mtu))
}

func (m *WgctrlManager) bringUp(name string) error {
	return run("ip", "link", "set", name, "up")
}
