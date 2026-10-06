package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Open at login is a per-user startup entry pointing at this executable.
const autostartID = "io.arfl.desktop"

func autostartPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", autostartID+".plist"), nil
	case "linux":
		return filepath.Join(home, ".config", "autostart", "arfl.desktop"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "ARFL.cmd"), nil
	}
	return "", fmt.Errorf("open at login is not supported on %s", runtime.GOOS)
}

func autostartContent(exe string) string {
	switch runtime.GOOS {
	case "darwin":
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + autostartID + `</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(exe) + `</string></array>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
`
	case "linux":
		return "[Desktop Entry]\nType=Application\nName=ARFL\nExec=\"" + exe + "\"\nX-GNOME-Autostart-enabled=true\n"
	default:
		return "@start \"\" \"" + exe + "\"\r\n"
	}
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// OpenAtLogin reports whether ARFL starts when the user signs in.
func (b *Bridge) OpenAtLogin() bool {
	path, err := autostartPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// SetOpenAtLogin adds or removes the startup entry.
func (b *Bridge) SetOpenAtLogin(on bool) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove startup entry: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the ARFL executable: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create startup folder: %w", err)
	}
	return os.WriteFile(path, []byte(autostartContent(exe)), 0o644)
}
