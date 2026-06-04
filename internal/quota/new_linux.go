//go:build linux

package quota

// NewEnforcer returns the platform-appropriate quota enforcer.
// On Linux, this returns a real nftables enforcer that manages kernel quotas.
func NewEnforcer(iface string) Enforcer {
	return NewNftablesEnforcer(iface)
}
