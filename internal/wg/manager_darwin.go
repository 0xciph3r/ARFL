//go:build darwin

package wg

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// macOS has no kernel WireGuard, so interfaces are backed by the wireguard-go
// userspace daemon. It creates a utun device and exposes a control socket that
// wgctrl then configures.
//
// The utun driver refuses arbitrary interface names: wireguard-go accepts only
// "utun" or "utunN". Passing a name like "arfl-outer" fails outright, so the
// kernel is asked to allocate one and the name it picked is read back from
// WG_TUN_NAME_FILE. Everything afterwards — wgctrl, ifconfig, routes — must use
// that real name, which is why createOSInterface returns it.

// wireguardSocketDir is where wireguard-go publishes its control sockets.
const wireguardSocketDir = "/var/run/wireguard"

// tunNameTimeout bounds the wait for the name file. wireguard-go daemonises,
// so the command returns before the child has created the device and recorded
// its name.
const tunNameTimeout = 10 * time.Second

// tunNameTimeoutForTest is the timeout actually used, so tests can shorten it.
var tunNameTimeoutForTest = tunNameTimeout

func (m *WgctrlManager) createOSInterface(cfg InterfaceConfig) (string, error) {
	if err := os.MkdirAll(wireguardSocketDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", wireguardSocketDir, err)
	}

	nameFile := filepath.Join(wireguardSocketDir, cfg.Name+".name")
	// wireguard-go writes the file 0400, so a stale one from a previous run
	// would both be hard to overwrite and could be mistaken for this run's
	// result.
	if err := os.Remove(nameFile); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove stale name file %s: %w", nameFile, err)
	}

	cmd := exec.Command("wireguard-go", "utun")
	cmd.Env = append(os.Environ(), "WG_TUN_NAME_FILE="+nameFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("start wireguard-go (is it installed?): %w: %s",
			err, strings.TrimSpace(string(out)))
	}

	name, err := awaitTunName(nameFile)
	if err != nil {
		return "", err
	}
	// The name file is per-logical-name and only used to hand the real name
	// back, so it is removed once read.
	_ = os.Remove(nameFile)
	return name, nil
}

// awaitTunName polls for the name file wireguard-go writes once the kernel has
// assigned a utun device.
func awaitTunName(path string) (string, error) {
	deadline := time.Now().Add(tunNameTimeoutForTest)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			// The file holds the name with a trailing newline.
			if name := strings.TrimSpace(string(data)); name != "" {
				return name, nil
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("read tun name file %s: %w", path, err)
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("wireguard-go did not report an interface name within %s", tunNameTimeoutForTest)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *WgctrlManager) deleteOSInterface(name string) error {
	// Removing the control socket stops the wireguard-go process holding the
	// utun device open. name is already the real utunN name.
	sock := filepath.Join(wireguardSocketDir, name+".sock")
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
