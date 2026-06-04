//go:build !linux

package quota

// NewEnforcer returns the platform-appropriate quota enforcer.
// On non-Linux platforms, this returns a no-op enforcer that logs actions.
func NewEnforcer(iface string) Enforcer {
	return NewNoopEnforcer()
}
