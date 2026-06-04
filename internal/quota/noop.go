//go:build !linux

package quota

import "log"

// NoopEnforcer is a no-op quota enforcer for non-Linux platforms (dev/testing).
// On macOS and other platforms, quota enforcement is skipped.
type NoopEnforcer struct{}

func NewNoopEnforcer() *NoopEnforcer {
	return &NoopEnforcer{}
}

func (e *NoopEnforcer) Init() error {
	log.Println("[quota] noop enforcer: nftables not available on this platform")
	return nil
}

func (e *NoopEnforcer) SetQuota(tunnelIP string, bytes int64) error {
	log.Printf("[quota] noop: would set %d byte quota for %s\n", bytes, tunnelIP)
	return nil
}

func (e *NoopEnforcer) RefreshQuota(tunnelIP string, bytes int64) error {
	log.Printf("[quota] noop: would refresh %d byte quota for %s\n", bytes, tunnelIP)
	return nil
}

func (e *NoopEnforcer) RemoveQuota(tunnelIP string) error {
	log.Printf("[quota] noop: would remove quota for %s\n", tunnelIP)
	return nil
}

func (e *NoopEnforcer) Close() error {
	return nil
}
