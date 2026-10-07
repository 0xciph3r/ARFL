//go:build windows

package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// windowsConfigurator drives netsh and PowerShell. It requires an elevated
// process.
//
// Windows keys most network configuration by interface index rather than name,
// so the default route is discovered through PowerShell and the index is
// carried alongside the friendly name for later calls.
type windowsConfigurator struct {
	// dnsBackup maps interface alias to the resolvers configured before the
	// tunnel took over, so teardown restores each adapter exactly.
	dnsBackup map[string][]string
	ipv6Rule  string
}

func newNetConfigurator() (netConfigurator, error) {
	return &windowsConfigurator{dnsBackup: map[string][]string{}}, nil
}

func (c *windowsConfigurator) DefaultRoute() (string, string, error) {
	// Lowest-metric default route is the one currently carrying traffic.
	const script = `Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction Stop |
		Sort-Object -Property RouteMetric |
		Select-Object -First 1 -Property NextHop,InterfaceAlias |
		ConvertTo-Json -Compress`

	out, err := powershell(script)
	if err != nil {
		return "", "", fmt.Errorf("query default route: %w", err)
	}

	var parsed struct {
		NextHop        string `json:"NextHop"`
		InterfaceAlias string `json:"InterfaceAlias"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		return "", "", fmt.Errorf("parse default route %q: %w", strings.TrimSpace(out), err)
	}
	if parsed.InterfaceAlias == "" {
		return "", "", fmt.Errorf("no default route found")
	}

	// An unset next hop is reported as 0.0.0.0, which means "on-link".
	gateway := parsed.NextHop
	if gateway == "0.0.0.0" {
		gateway = ""
	}
	return gateway, parsed.InterfaceAlias, nil
}

func (c *windowsConfigurator) AddRoute(cidr, gateway, iface string) error {
	// Replace rather than add, so a leftover route from a crashed session does
	// not fail the bring-up.
	_ = c.DeleteRoute(cidr, gateway, iface)

	nextHop := gateway
	if nextHop == "" {
		nextHop = "0.0.0.0"
	}

	script := fmt.Sprintf(
		`New-NetRoute -DestinationPrefix '%s' -InterfaceAlias '%s' -NextHop '%s' `+
			`-PolicyStore ActiveStore -ErrorAction Stop | Out-Null`,
		cidr, iface, nextHop,
	)
	if _, err := powershell(script); err != nil {
		return fmt.Errorf("add route %s: %w", cidr, err)
	}
	return nil
}

func (c *windowsConfigurator) DeleteRoute(cidr, gateway, iface string) error {
	script := fmt.Sprintf(
		`Remove-NetRoute -DestinationPrefix '%s' -InterfaceAlias '%s' `+
			`-Confirm:$false -ErrorAction SilentlyContinue`,
		cidr, iface,
	)
	if _, err := powershell(script); err != nil {
		return fmt.Errorf("delete route %s: %w", cidr, err)
	}
	return nil
}

func (c *windowsConfigurator) SetDNS(resolver string) error {
	aliases, err := activeAdapters()
	if err != nil {
		return err
	}

	for _, alias := range aliases {
		current, err := currentDNS(alias)
		if err != nil {
			continue
		}
		script := fmt.Sprintf(
			`Set-DnsClientServerAddress -InterfaceAlias '%s' -ServerAddresses '%s' -ErrorAction Stop`,
			alias, resolver,
		)
		if _, err := powershell(script); err != nil {
			return fmt.Errorf("set DNS on %q: %w", alias, err)
		}
		c.dnsBackup[alias] = current
	}

	if len(c.dnsBackup) == 0 {
		return fmt.Errorf("no adapter accepted a DNS change")
	}
	return nil
}

func (c *windowsConfigurator) RestoreDNS() error {
	var problems []error

	for alias, previous := range c.dnsBackup {
		var script string
		if len(previous) == 0 {
			// ResetServerAddresses returns the adapter to DHCP-provided DNS.
			script = fmt.Sprintf(
				`Set-DnsClientServerAddress -InterfaceAlias '%s' -ResetServerAddresses -ErrorAction Stop`,
				alias,
			)
		} else {
			quoted := make([]string, len(previous))
			for i, server := range previous {
				quoted[i] = "'" + server + "'"
			}
			script = fmt.Sprintf(
				`Set-DnsClientServerAddress -InterfaceAlias '%s' -ServerAddresses %s -ErrorAction Stop`,
				alias, strings.Join(quoted, ","),
			)
		}
		if _, err := powershell(script); err != nil {
			problems = append(problems, fmt.Errorf("restore DNS on %q: %w", alias, err))
		}
	}

	c.dnsBackup = map[string][]string{}
	if len(problems) > 0 {
		return joinErrors(problems)
	}
	return nil
}

// activeAdapters lists connected physical adapters, excluding the tunnel's own
// interfaces so the resolver is not written to the adapter being torn down.
func activeAdapters() ([]string, error) {
	script := fmt.Sprintf(
		`Get-NetAdapter -Physical | Where-Object { $_.Status -eq 'Up' -and `+
			`$_.Name -ne '%s' -and $_.Name -ne '%s' } | `+
			`Select-Object -ExpandProperty Name`,
		OuterInterface, InnerInterface,
	)

	out, err := powershell(script)
	if err != nil {
		return nil, fmt.Errorf("list adapters: %w", err)
	}

	var aliases []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if alias := strings.TrimSpace(line); alias != "" {
			aliases = append(aliases, alias)
		}
	}
	if len(aliases) == 0 {
		return nil, fmt.Errorf("no active physical adapters found")
	}
	return aliases, nil
}

// currentDNS returns the statically configured resolvers for an adapter.
func currentDNS(alias string) ([]string, error) {
	script := fmt.Sprintf(
		`Get-DnsClientServerAddress -InterfaceAlias '%s' -AddressFamily IPv4 -ErrorAction Stop | `+
			`Select-Object -ExpandProperty ServerAddresses`,
		alias,
	)

	out, err := powershell(script)
	if err != nil {
		return nil, err
	}

	var servers []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			servers = append(servers, s)
		}
	}
	return servers, nil
}

func powershell(script string) (string, error) {
	return output("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

func (c *windowsConfigurator) DisableIPv6() error {
	if c.ipv6Rule != "" {
		return nil
	}
	// A block rule has no effect when the firewall is disabled, and local
	// rules can be ignored by domain policy. Reject either configuration.
	_, err := powershell(`$profiles = @(Get-NetFirewallProfile -ErrorAction Stop);
		if ($profiles.Count -ne 3 -or @($profiles | Where-Object {
			-not $_.Enabled -or $_.AllowLocalFirewallRules -eq $false
		}).Count -ne 0) { throw 'Windows firewall must accept local rules on every profile' }`)
	if err != nil {
		return fmt.Errorf("cannot secure IPv6: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("identify IPv6 rule: %w", err)
	}
	name := "ARFL-IPv6-" + hex.EncodeToString(nonce[:])
	if _, err := powershell(fmt.Sprintf(
		`New-NetFirewallRule -Name '%s' -DisplayName 'ARFL outbound IPv6 block' -Direction Outbound -Action Block -RemoteAddress '::/0' -Profile Any -PolicyStore PersistentStore -ErrorAction Stop | Out-Null`,
		name)); err != nil {
		return fmt.Errorf("block outbound IPv6: %w", err)
	}
	c.ipv6Rule = name
	// The ActiveStore check catches policies that silently exclude local
	// rules, or a firewall service that accepted but did not apply the rule.
	if _, err := powershell(fmt.Sprintf(
		`$rule = Get-NetFirewallRule -Name '%s' -PolicyStore ActiveStore -ErrorAction Stop;
		if ($rule.Enabled -ne 'True' -or $rule.Action -ne 'Block' -or
		    (Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $rule -ErrorAction Stop).RemoteAddress -notcontains '::/0') {
		    throw 'IPv6 block is not active'
		}`, name)); err != nil {
		return fmt.Errorf("verify outbound IPv6 block: %w", err)
	}
	return nil
}

func (c *windowsConfigurator) RestoreIPv6() error {
	if c.ipv6Rule == "" {
		return nil
	}
	if _, err := powershell(fmt.Sprintf(
		`Remove-NetFirewallRule -Name '%s' -PolicyStore PersistentStore -ErrorAction Stop`, c.ipv6Rule)); err != nil {
		return fmt.Errorf("remove IPv6 block: %w", err)
	}
	c.ipv6Rule = ""
	return nil
}
