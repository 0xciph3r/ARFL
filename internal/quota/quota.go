package quota

// Enforcer manages bandwidth quota enforcement at the kernel level.
// The kernel enforces hard cutoffs between polling intervals.
// The Go daemon uses wgctrl byte counters for billing accuracy.
type Enforcer interface {
	// Init sets up the quota enforcement rules (e.g. nftables table/chain).
	Init() error

	// SetQuota sets a bandwidth quota slab for a client tunnel IP.
	// bytes is the slab size (typically 256 MB = 268435456).
	SetQuota(tunnelIP string, bytes int64) error

	// RefreshQuota replaces the current quota slab for a tunnel IP.
	RefreshQuota(tunnelIP string, bytes int64) error

	// RemoveQuota removes the quota for a tunnel IP (peer is being disconnected).
	RemoveQuota(tunnelIP string) error

	// Close tears down quota enforcement rules.
	Close() error
}
