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

// TestResolveCommitIdentity pins the per-field order (environment, fallback,
// default), the cleaning of every candidate before its emptiness test, and
// that the fallback runs only when the environment leaves a field empty.
func TestResolveCommitIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, envName, envEmail string
		fbName, fbEmail         string
		wantName, wantEmail     string
		wantCalls               int
	}{
		{name: "complete env skips the fallback", envName: "Env", envEmail: "env@example.com",
			fbName: "Fb", fbEmail: "fb@example.com", wantName: "Env", wantEmail: "env@example.com"},
		{name: "env name, fallback email", envName: "Env",
			fbName: "Fb", fbEmail: "fb@example.com", wantName: "Env", wantEmail: "fb@example.com", wantCalls: 1},
		{name: "env email, fallback name", envEmail: "env@example.com",
			fbName: "Fb", fbEmail: "fb@example.com", wantName: "Fb", wantEmail: "env@example.com", wantCalls: 1},
		{name: "fallback without email keeps the default email",
			fbName: "Fb", wantName: "Fb", wantEmail: "beads@local", wantCalls: 1},
		{name: "unicode fallback name",
			fbName: "  Zoë  Ñandú ", fbEmail: "zoe@example.com", wantName: "Zoë Ñandú", wantEmail: "zoe@example.com", wantCalls: 1},
		{name: "env empty once cleaned falls through to the fallback", envName: "<>()", envEmail: "env@example.com",
			fbName: "Fb", fbEmail: "fb@example.com", wantName: "Fb", wantEmail: "env@example.com", wantCalls: 1},
		{name: "fallback empty once cleaned falls through to the default",
			fbName: " ( ) ", fbEmail: "<>", wantName: "beads", wantEmail: "beads@local", wantCalls: 1},
		{name: "fallback values are cleaned",
			fbName: "Alice (bot)\n<x>", fbEmail: "<fb@example.com>", wantName: "Alice bot x", wantEmail: "fb@example.com", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_AUTHOR_NAME", tc.envName)
			t.Setenv("GIT_AUTHOR_EMAIL", tc.envEmail)
			calls := 0
			name, email := resolveCommitIdentity(func() (string, string) {
				calls++
				return tc.fbName, tc.fbEmail
			})
			if name != tc.wantName || email != tc.wantEmail {
				t.Errorf("resolveCommitIdentity = %q <%s>, want %q <%s>", name, email, tc.wantName, tc.wantEmail)
			}
			if calls != tc.wantCalls {
				t.Errorf("fallback called %d times, want %d", calls, tc.wantCalls)
			}
		})
	}

	t.Run("nil fallback uses the defaults", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_NAME", "")
		t.Setenv("GIT_AUTHOR_EMAIL", "")
		if name, email := resolveCommitIdentity(nil); name != "beads" || email != "beads@local" {
			t.Errorf("resolveCommitIdentity(nil) = %q <%s>, want beads <beads@local>", name, email)
		}
	})
}
