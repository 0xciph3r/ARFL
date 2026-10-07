package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Radi-Labs/ARFL/internal/app"
	"github.com/Radi-Labs/ARFL/internal/helper"
	"github.com/Radi-Labs/ARFL/internal/tunnel"
)

// tunnelHandle is the tunnel the bridge drives: in this process when ARFL
// runs as root, otherwise through the privileged helper.
type tunnelHandle interface {
	app.Tunnel
	usage() (helper.UsageResult, error)
	DisableIPv6() error
	Close() error
}

// localTunnel runs the tunnel in this process. Embedding keeps the staged,
// endpoint and per-hop key methods visible to app.Service.
type localTunnel struct{ *tunnel.Tunnel }

func (l localTunnel) usage() (helper.UsageResult, error) {
	u, active, err := l.Usage()
	return helper.UsageResult{Active: active, RxBytes: u.RxBytes, TxBytes: u.TxBytes, EntryHandshake: u.EntryHandshake, ExitHandshake: u.ExitHandshake}, err
}

// helperTunnel drives the tunnel inside the privileged helper.
type helperTunnel struct{ *helper.Client }

func (h helperTunnel) usage() (helper.UsageResult, error) { return h.Usage() }

const (
	needsSetupMsg = "Set up the tunnel to connect. ARFL needs your administrator approval once to change network settings."
	adminOnlyMsg  = "Run ARFL as administrator to connect. A background helper is not available on this system yet."
)

// helperSupported reports whether this platform has the background helper.
func helperSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

// chooseTunnel picks how the tunnel runs. needsSetup is true when installing
// (or updating) the helper would make Connect available.
func chooseTunnel() (t tunnelHandle, reason string, needsSetup bool) {
	if helperSupported() {
		c := helper.NewClient(helper.SocketPath)
		if v, err := c.Ping(); err == nil {
			if v >= helper.Version {
				return helperTunnel{c}, "", false
			}
			return nil, "The tunnel helper is out of date. Set it up again to connect.", true
		}
	}

	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		tun, err := tunnel.New()
		if err == nil {
			if err = tun.Preflight(); err == nil {
				return localTunnel{tun}, "", false
			}
			tun.Close()
		}
		if runtime.GOOS == "windows" {
			return nil, adminOnlyMsg, false
		}
		return nil, err.Error(), false
	}

	if helperSupported() {
		return nil, needsSetupMsg, true
	}
	return nil, adminOnlyMsg, false
}

// InstallHelper shows the system's administrator prompt, installs the helper
// and switches the open wallet over to it.
func (b *Bridge) InstallHelper() error {
	if !helperSupported() {
		return errors.New(adminOnlyMsg)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the ARFL program: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	cmd := helper.ElevatedCommand([]string{exe, "helper", "install", "-uid", strconv.Itoa(os.Getuid())})
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := string(out)
		if strings.Contains(msg, "User canceled") || strings.Contains(msg, "(-128)") || strings.Contains(msg, "dismissed") {
			return errors.New("setup was cancelled")
		}
		return fmt.Errorf("install the helper: %v: %s", err, out)
	}

	c := helper.NewClient(helper.SocketPath)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v, err := c.Ping(); err == nil && v >= helper.Version {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the helper was installed but did not start; try again")
		}
		time.Sleep(300 * time.Millisecond)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tun != nil {
		_ = b.tun.Close()
	}
	b.tun, b.tunErr, b.needsSetup = helperTunnel{c}, "", false
	if b.svc != nil {
		return b.svc.SetTunnel(b.tun)
	}
	return nil
}

// runHelperCommand handles "arfl-desktop helper ...". The same binary is the
// helper so there is nothing extra to build or ship.
func runHelperCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: arfl-desktop helper run|install -uid N|uninstall")
	}
	switch args[0] {
	case "run":
		return helper.Run(helper.ConfigPath)
	case "install":
		uid := -1
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "-uid" {
				uid, _ = strconv.Atoi(args[i+1])
			}
		}
		if uid < 0 {
			return errors.New("install needs -uid <user id>")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return helper.Install(exe, uid)
	case "uninstall":
		return helper.Uninstall()
	}
	return fmt.Errorf("unknown helper command %q", args[0])
}
