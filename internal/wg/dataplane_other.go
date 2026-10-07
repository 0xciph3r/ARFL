//go:build !darwin

package wg

// CheckDataPlane reports whether this system can create WireGuard interfaces.
// Linux uses the kernel module and Windows the wireguard-nt driver, both of
// which surface their own errors at creation; only macOS needs a separate
// userspace binary that can be checked up front.
func CheckDataPlane() error { return nil }
