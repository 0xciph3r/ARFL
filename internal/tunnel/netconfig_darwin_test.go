//go:build darwin

package tunnel

import "testing"

func TestPFAnchorMustPrecedeQuickPass(t *testing.T) {
	for _, tc := range []struct {
		rules string
		want  bool
	}{
		{`anchor "com.apple/*" all`, true},
		{"pass out quick inet6 all\n" + `anchor "com.apple/*" all`, false},
		{`anchor "custom/*" all`, false},
	} {
		if got := pfAnchorUsable(tc.rules); got != tc.want {
			t.Errorf("pfAnchorUsable(%q) = %t, want %t", tc.rules, got, tc.want)
		}
	}
}
