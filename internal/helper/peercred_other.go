//go:build !darwin && !linux

package helper

import (
	"errors"
	"net"
)

// The helper is not yet available on this platform; the desktop app runs the
// tunnel in-process when started with administrator rights instead.
func peerUID(net.Conn) (int, error) {
	return -1, errors.New("the ARFL helper is not supported on this platform yet")
}
