//go:build darwin

package wg

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// macOS has no kernel WireGuard, so interfaces are backed by the wireguard-go
// userspace daemon. It creates a utun device and exposes a control socket that
// wgctrl then configures.

// wireguardSocketDir is where wireguard-go publishes its control sockets.
const wireguardSocketDir = "/var/run/wireguard"

func (m *WgctrlManager) createOSInterface(cfg InterfaceConfig) error {
	name := cfg.Name

	// WG_TUN_NAME_FILE makes wireguard-go report which utun device it picked;
	// without it the caller cannot know the real interface name.
	if err := run("wireguard-go", name); err != nil {
		return fmt.Errorf("start wireguard-go (is it installed?): %w", err)
	}
	return nil
}

func (m *WgctrlManager) deleteOSInterface(name string) error {
	// Removing the control socket stops the wireguard-go process holding the
	// utun device open.
	sock := wireguardSocketDir + "/" + name + ".sock"
	if err := os.Remove(sock); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove wireguard socket %s: %w", sock, err)
	}
	return nil
}

func (m *WgctrlManager) setAddress(name, address string, mtu int) error {
	ip, ipNet, err := net.ParseCIDR(address)
	if err != nil {
		return fmt.Errorf("parse address %s: %w", address, err)
	}

	// ifconfig on macOS wants a point-to-point pair; the tunnel address is used
	// for both ends because the peer address is not known here.
	mask := net.IP(ipNet.Mask).String()
	if err := run("ifconfig", name, "inet", ip.String(), ip.String(), "netmask", mask); err != nil {
		return err
	}
	return run("ifconfig", name, "mtu", strconv.Itoa(mtu))
}

func (m *WgctrlManager) bringUp(name string) error {
	// wireguard-go brings the utun device up as part of creating it.
	return nil
}
