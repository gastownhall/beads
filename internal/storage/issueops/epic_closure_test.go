package issueops

import "testing"

func TestIsNonCompletingClose(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason string
		want   bool
	}{
		{"", false},
		{"   ", false},
		{"done", false},
		{"completed as planned", false},
		{"fixed", false},
		{"duplicate of bd-1", true},
		{"Duplicate", true},
		{"closed as dup of x", true},
		{"wontfix", true},
		{"won't fix — out of scope", true},
		{"wont fix", true},
		{"superseded by bd-9", true},
		{"obsolete", true},
		{"not planned", true},
		// Failure closes (rejected/canceled) still completed their lifecycle
		// for eligibility purposes — only redirect/abandon keywords qualify.
		{"failed CI", false},
		{"canceled", false},
	}
	for _, tc := range cases {
		got := IsNonCompletingClose(tc.reason)
		if got != tc.want {
			t.Errorf("IsNonCompletingClose(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}
