//go:build darwin

package helper

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	binPath    = "/Library/PrivilegedHelperTools/" + Label
	plistPath  = "/Library/LaunchDaemons/" + Label + ".plist"
	configDir  = "/Library/Application Support/ARFL"
	ConfigPath = configDir + "/helper.json"
)

func plist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + Label + `</string>
  <key>ProgramArguments</key>
  <array><string>` + binPath + `</string><string>helper</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/var/log/` + Label + `.log</string>
</dict>
</plist>
`
}

// Install copies exe into place, records the allowed user and starts the
// launchd daemon. It must run as root.
func Install(exe string, uid int) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("installing the helper needs administrator rights")
	}
	_ = exec.Command("launchctl", "bootout", "system/"+Label).Run()

	if err := copyFile(exe, binPath, 0o755); err != nil {
		return err
	}
	if err := writeConfig(configDir, ConfigPath, uid); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(plist()), 0o644); err != nil {
		return fmt.Errorf("write launch daemon: %w", err)
	}
	if out, err := exec.Command("launchctl", "bootstrap", "system", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("start helper: %v: %s", err, out)
	}
	return nil
}

// Uninstall stops the daemon and removes its files. It must run as root.
func Uninstall() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("removing the helper needs administrator rights")
	}
	_ = exec.Command("launchctl", "bootout", "system/"+Label).Run()
	for _, p := range []string{plistPath, binPath, ConfigPath, SocketPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// ElevatedCommand runs args as root after the system's administrator prompt.
func ElevatedCommand(args []string) *exec.Cmd {
	script := "do shell script " + appleScriptQuote(shellJoin(args)) + " with administrator privileges with prompt \"ARFL needs to install a small helper so the tunnel can change your network settings.\""
	return exec.Command("osascript", "-e", script)
}

func appleScriptQuote(s string) string {
	out := []byte{'"'}
	for _, r := range []byte(s) {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(append(out, '"'))
}
