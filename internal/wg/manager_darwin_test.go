//go:build darwin

package wg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wireguard-go writes the chosen interface name with a trailing newline. Using
// the raw contents would produce a name like "utun4\n", which every later
// ifconfig, route and wgctrl call would reject.
func TestAwaitTunNameTrimsTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arfl-outer.name")
	if err := os.WriteFile(path, []byte("utun7\n"), 0o400); err != nil {
		t.Fatalf("write name file: %v", err)
	}

	name, err := awaitTunName(path)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if name != "utun7" {
		t.Fatalf("name = %q, want %q", name, "utun7")
	}
}

// wireguard-go daemonises, so the command returns before the child has created
// the device and recorded its name. The wait must tolerate the file appearing
// late rather than failing immediately.
func TestAwaitTunNameWaitsForALateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arfl-outer.name")

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(path, []byte("utun9\n"), 0o400)
	}()

	name, err := awaitTunName(path)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if name != "utun9" {
		t.Fatalf("name = %q, want %q", name, "utun9")
	}
}

// An empty file must not be mistaken for a result: wireguard-go creates it and
// writes to it, so a zero-length read is a partial write, not a name.
func TestAwaitTunNameIgnoresAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arfl-outer.name")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write name file: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(path, []byte("utun3\n"), 0o600)
	}()

	name, err := awaitTunName(path)
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if name != "utun3" {
		t.Fatalf("name = %q, want %q", name, "utun3")
	}
}

// A name that never arrives must fail rather than hang, so the caller can roll
// back instead of leaving a half-created interface.
func TestAwaitTunNameTimesOut(t *testing.T) {
	// Shorten the wait so the test does not take the full production timeout.
	orig := tunNameTimeoutForTest
	tunNameTimeoutForTest = 200 * time.Millisecond
	defer func() { tunNameTimeoutForTest = orig }()

	path := filepath.Join(t.TempDir(), "missing.name")
	if _, err := awaitTunName(path); err == nil {
		t.Fatal("expected a timeout when the name file never appears")
	}
}
