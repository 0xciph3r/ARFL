//go:build windows

package tunnel

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// checkPrivileges reports whether the process is elevated.
//
// Route changes and installing the WireGuard tunnel service both require
// administrator rights. The check runs before any payment so a non-elevated
// user is told to restart the app as administrator instead of paying two nodes
// for a session that cannot be established.
func checkPrivileges() error {
	// Membership in the Administrators group is not enough: without elevation
	// the SID is present but disabled, and the route commands still fail.
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return fmt.Errorf("determine administrator status: %w", err)
	}
	defer windows.FreeSid(sid)

	member, err := windows.Token(0).IsMember(sid)
	if err != nil {
		return fmt.Errorf("determine administrator status: %w", err)
	}
	if !member {
		return fmt.Errorf("administrator privileges are required to configure the tunnel: restart ARFL as administrator")
	}
	return nil
}
