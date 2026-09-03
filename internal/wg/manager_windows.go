//go:build windows

package wg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Windows has no kernel WireGuard and no ip(8). Interfaces are created by
// asking the WireGuard for Windows service manager to install a tunnel
// service, which loads the WireGuardNT driver and creates the adapter. wgctrl
// then configures peers over the driver's named pipe exactly as on Linux.
//
// This is the documented embedding path for third-party applications; see
// https://git.zx2c4.com/wireguard-windows/about/docs/enterprise.md.

// interfaceReadyTimeout bounds the wait for the service to create the adapter.
// Installation is asynchronous, so configuring it immediately would race.
const interfaceReadyTimeout = 15 * time.Second

func (m *WgctrlManager) createOSInterface(cfg InterfaceConfig) (string, error) {
	exe, err := wireguardExe()
	if err != nil {
		return "", err
	}

	confPath, err := writeTunnelConfig(cfg)
	if err != nil {
		return "", err
	}

	// The config holds the tunnel's private key and the service reads it from
	// this path for the tunnel's lifetime, so it can only be removed once the
	// tunnel is gone. Any failure below means CreateInterface never succeeds
	// and DeleteInterface will not be called, so every early return has to
	// clean up itself or the key is left on disk indefinitely.
	cleanup := func() {
		_ = run(exe, "/uninstalltunnelservice", cfg.Name)
		_ = os.Remove(confPath)
	}

	// A leftover service from a crashed session owns the adapter name and
	// would make installation fail, so clear it first.
	_ = run(exe, "/uninstalltunnelservice", cfg.Name)

	if err := run(exe, "/installtunnelservice", confPath); err != nil {
		// The service may have been partially registered before failing, so
		// tear it down rather than assuming nothing was created.
		cleanup()
		return "", fmt.Errorf("install tunnel service (is WireGuard for Windows installed?): %w", err)
	}

	if err := m.awaitInterface(cfg.Name); err != nil {
		// The service installed but never produced an adapter. Leaving it
		// registered would block the next attempt, which reuses this name.
		cleanup()
		return "", err
	}

	// The tunnel service uses the requested name for the adapter.
	return cfg.Name, nil
}

func (m *WgctrlManager) deleteOSInterface(name string) error {
	exe, err := wireguardExe()
	if err != nil {
		return err
	}

	if err := run(exe, "/uninstalltunnelservice", name); err != nil {
		// An already-removed tunnel is the desired end state.
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return nil
		}
		return err
	}

	// The config holds the private key, so it must not outlive the tunnel.
	if path, perr := tunnelConfigPath(name); perr == nil {
		_ = os.Remove(path)
	}
	return nil
}

// setAddress is a no-op on Windows: the address and MTU are declared in the
// tunnel config and applied by the service when the adapter is created.
func (m *WgctrlManager) setAddress(name, address string, mtu int) error {
	return nil
}

// bringUp is a no-op on Windows: installing the tunnel service starts it.
func (m *WgctrlManager) bringUp(name string) error {
	return nil
}

// awaitInterface waits for the adapter to appear, since service installation
// returns before the driver has finished creating it.
func (m *WgctrlManager) awaitInterface(name string) error {
	deadline := time.Now().Add(interfaceReadyTimeout)
	for {
		if _, err := m.client.Device(name); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("interface %s did not appear within %s", name, interfaceReadyTimeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// writeTunnelConfig writes the minimal config the service needs. Peers are
// added afterwards over wgctrl, so only the interface section is written.
func writeTunnelConfig(cfg InterfaceConfig) (string, error) {
	path, err := tunnelConfigPath(cfg.Name)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create config directory: %w", err)
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", cfg.PrivateKey)
	if cfg.Address != "" {
		fmt.Fprintf(&b, "Address = %s\n", cfg.Address)
	}
	if cfg.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", cfg.ListenPort)
	}
	if cfg.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", cfg.MTU)
	}

	// 0600: the file contains the tunnel's private key.
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", fmt.Errorf("write tunnel config: %w", err)
	}
	return path, nil
}

// tunnelConfigPath returns a per-user path for a tunnel's configuration.
func tunnelConfigPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config directory: %w", err)
	}
	return filepath.Join(dir, "ARFL", "tunnels", name+".conf"), nil
}

// wireguardExe locates the WireGuard for Windows executable.
func wireguardExe() (string, error) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "WireGuard", "wireguard.exe"),
		filepath.Join(os.Getenv("ProgramW6432"), "WireGuard", "wireguard.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "WireGuard", "wireguard.exe"),
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("wireguard.exe not found: install WireGuard for Windows from https://www.wireguard.com/install/")
}
