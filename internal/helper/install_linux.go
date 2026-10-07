//go:build linux

package helper

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	binPath    = "/usr/local/libexec/" + Label
	unitPath   = "/etc/systemd/system/arfl-helper.service"
	configDir  = "/etc/arfl"
	ConfigPath = configDir + "/helper.json"
)

func unit() string {
	return `[Unit]
Description=ARFL tunnel helper
After=network-online.target

[Service]
ExecStart=` + binPath + ` helper run
Restart=always

[Install]
WantedBy=multi-user.target
`
}

// Install copies exe into place, records the allowed user and starts the
// systemd service. It must run as root.
func Install(exe string, uid int) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("installing the helper needs administrator rights")
	}
	_ = exec.Command("systemctl", "stop", "arfl-helper").Run()
	if err := os.MkdirAll("/usr/local/libexec", 0o755); err != nil {
		return err
	}
	if err := copyFile(exe, binPath, 0o755); err != nil {
		return err
	}
	if err := writeConfig(configDir, ConfigPath, uid); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit()), 0o644); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "arfl-helper"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %v: %s", args, err, out)
		}
	}
	return nil
}

// Uninstall stops the service and removes its files. It must run as root.
func Uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("removing the helper needs administrator rights")
	}
	_ = exec.Command("systemctl", "disable", "--now", "arfl-helper").Run()
	for _, p := range []string{unitPath, binPath, ConfigPath, SocketPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

// ElevatedCommand runs args as root after the desktop's administrator prompt.
func ElevatedCommand(args []string) *exec.Cmd {
	return exec.Command("pkexec", args...)
}
