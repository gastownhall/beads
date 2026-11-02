//go:build cgo

package embeddeddolt

import "testing"

func TestCleanCommitIdentityField(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Alice Author", "Alice Author"},
		{"  Zoë  Ñandú ", "Zoë Ñandú"},
		{"Alice (bot)", "Alice bot"},
		{"Alice (bot)\n<x>", "Alice bot x"},
		{"a<b>c\t\r\nd", "abc d"},
		{"alice@example.com", "alice@example.com"},
		{"<alice@example.com>", "alice@example.com"},
		{"", ""},
		{" <()> \n", ""},
	} {
		if got := cleanCommitIdentityField(tc.in); got != tc.want {
			t.Errorf("cleanCommitIdentityField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
