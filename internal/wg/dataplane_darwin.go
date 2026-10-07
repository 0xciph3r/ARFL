//go:build darwin

package wg

// CheckDataPlane reports whether this system can create WireGuard interfaces.
// On macOS that needs the wireguard-go userspace daemon.
func CheckDataPlane() error {
	_, err := WireGuardGoPath()
	return err
}
