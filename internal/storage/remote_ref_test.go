package storage

import "testing"

// An unset ref and Dolt's default name the same ref; any other value is
// compared verbatim, so a branch and a ref outside refs/heads/ are distinct
// from each other and from the default.
func TestRemoteRefsMatch(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"", DefaultGitDataRef, true},
		{DefaultGitDataRef, "", true},
		{" refs/dolt/data ", "", true},
		{"refs/heads/issue-data", "refs/heads/issue-data", true},
		{"refs/dolt/units/team-12542", "refs/dolt/units/team-12542", true},
		{"refs/heads/issue-data", "", false},
		{"refs/dolt/units/team-12542", "", false},
		{"refs/heads/issue-data", "refs/dolt/units/team-12542", false},
		{"refs/heads/issue-data", "refs/heads/issue-data-2", false},
	}
	for _, tt := range tests {
		if got := RemoteRefsMatch(tt.a, tt.b); got != tt.want {
			t.Errorf("RemoteRefsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestEffectiveGitDataRef(t *testing.T) {
	if got := EffectiveGitDataRef(""); got != DefaultGitDataRef {
		t.Errorf("EffectiveGitDataRef(\"\") = %q, want %q", got, DefaultGitDataRef)
	}
	if got := EffectiveGitDataRef("  refs/dolt/units/k  "); got != "refs/dolt/units/k" {
		t.Errorf("EffectiveGitDataRef trims but does not rewrite: got %q", got)
	}
}
