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
	// Each client gets its own named quota object, looked up by source IP
	// through a map. The drop rule fires only once that client's quota is
	// exceeded. Deleting any existing table first clears the older set-based
	// layout, which nft rejects when merged with a map of the same name.
	script := fmt.Sprintf(`
add table inet arfl
delete table inet arfl
table inet arfl {
	map quotas {
		type ipv4_addr : quota
	}

	chain forward {
		type filter hook forward priority 0; policy accept;
		iifname "%s" quota name ip saddr map @quotas counter drop comment "arfl: over quota"
	}
}
`, e.iface)

	return nftRun(script)
}

func quotaObjectName(tunnelIP string) string {
	return "q_" + strings.ReplaceAll(tunnelIP, ".", "_")
}

func (e *NftablesEnforcer) SetQuota(tunnelIP string, bytes int64) error {
	name := quotaObjectName(tunnelIP)
	_ = e.RemoveQuota(tunnelIP)
	if err := nftCmd(fmt.Sprintf("add quota inet arfl %s { over %d bytes }", name, bytes)); err != nil {
		return err
	}
	if err := nftCmd(fmt.Sprintf("add element inet arfl quotas { %s : \"%s\" }", tunnelIP, name)); err != nil {
		_ = nftCmd(fmt.Sprintf("delete quota inet arfl %s", name))
		return err
	}
	return nil
}

func (e *NftablesEnforcer) RefreshQuota(tunnelIP string, bytes int64) error {
	return e.SetQuota(tunnelIP, bytes)
}

func (e *NftablesEnforcer) RemoveQuota(tunnelIP string) error {
	elemErr := nftCmd(fmt.Sprintf("delete element inet arfl quotas { %s }", tunnelIP))
	objErr := nftCmd(fmt.Sprintf("delete quota inet arfl %s", quotaObjectName(tunnelIP)))
	if elemErr != nil {
		return elemErr
	}
	return objErr
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
