//go:build !windows

package tunnel

import (
	"fmt"
	"os"
)

// checkPrivileges reports whether the process can modify the routing table.
//
// Route and interface changes need root on Unix. The check exists so a
// connection attempt fails before any payment is made rather than after: the
// service spends tokens at both nodes before calling Up, and tokens a node has
// accepted are burned at the hub and cannot be reclaimed.
func checkPrivileges() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("root privileges are required to configure the tunnel: run ARFL with sudo")
	}
	return nil
}
