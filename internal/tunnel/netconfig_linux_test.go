//go:build linux

package tunnel

import "testing"

func TestIPv6FirewallRuleMustBeFirst(t *testing.T) {
	for _, tc := range []struct {
		rules string
		want  bool
	}{
		{"-P OUTPUT ACCEPT\n-A OUTPUT -m comment --comment arfl-123 -j REJECT\n", true},
		{"-A OUTPUT -j ACCEPT\n-A OUTPUT -m comment --comment arfl-123 -j REJECT\n", false},
		{"-A OUTPUT -m comment --comment arfl-other -j REJECT\n", false},
	} {
		if got := ipv6RuleFirst(tc.rules, "arfl-123"); got != tc.want {
			t.Errorf("ipv6RuleFirst(%q) = %t, want %t", tc.rules, got, tc.want)
		}
	}
}
