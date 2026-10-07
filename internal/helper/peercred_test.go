//go:build darwin || linux

package helper

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUIDReportsTheConnectingUser(t *testing.T) {
	dir, err := os.MkdirTemp("", "arflp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := net.Listen("unix", filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		c, err := net.Dial("unix", filepath.Join(dir, "p.sock"))
		if err == nil {
			defer c.Close()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	uid, err := peerUID(conn)
	if err != nil {
		t.Fatalf("peerUID: %v", err)
	}
	if uid != os.Getuid() {
		t.Fatalf("peer uid %d, want %d", uid, os.Getuid())
	}
}
