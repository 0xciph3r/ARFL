//go:build linux

package quota

import (
	"fmt"
	"os/exec"
	"strings"
)

// NftablesEnforcer enforces bandwidth quotas using nftables on Linux.
// It manages a dynamic set of per-client-IP quota elements in the kernel,
// providing wire-speed enforcement independent of userspace polling.
type NftablesEnforcer struct {
	iface string // WireGuard interface to enforce on (e.g. "wg-exit")
}

// NewNftablesEnforcer creates a new enforcer for the given WireGuard interface.
func NewNftablesEnforcer(iface string) *NftablesEnforcer {
	return &NftablesEnforcer{iface: iface}
}

func (e *NftablesEnforcer) Init() error {
	// Create the arfl table and quota chain
	script := fmt.Sprintf(`
table inet arfl {
	set quotas {
		type ipv4_addr
		flags dynamic,timeout
		timeout 30m
	}

	chain forward {
		type filter hook forward priority 0; policy accept;
		iifname "%s" ip saddr @quotas counter drop comment "arfl: over quota"
	}
}
`, e.iface)

	return nftRun(script)
}

func (e *NftablesEnforcer) SetQuota(tunnelIP string, bytes int64) error {
	cmd := fmt.Sprintf(
		"add element inet arfl quotas { %s : quota over %d bytes }",
		tunnelIP, bytes,
	)
	return nftCmd(cmd)
}

func (e *NftablesEnforcer) RefreshQuota(tunnelIP string, bytes int64) error {
	// Delete existing, then re-add with fresh quota
	_ = nftCmd(fmt.Sprintf("delete element inet arfl quotas { %s }", tunnelIP))
	return e.SetQuota(tunnelIP, bytes)
}

func (e *NftablesEnforcer) RemoveQuota(tunnelIP string) error {
	return nftCmd(fmt.Sprintf("delete element inet arfl quotas { %s }", tunnelIP))
}

func (e *NftablesEnforcer) Close() error {
	return nftCmd("delete table inet arfl")
}

func nftRun(script string) error {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func nftCmd(rule string) error {
	cmd := exec.Command("nft", rule)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %s: %w: %s", rule, err, strings.TrimSpace(string(out)))
	}
	return nil
}
