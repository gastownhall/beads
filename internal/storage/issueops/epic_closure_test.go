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
		// Bare "dup" was dropped (GH#5026 review): redundant with
		// "duplicate"/"dupe" and only added false-positive recall.
		{"closed as dup of x", false},
		{"wontfix", true},
		{"won't fix — out of scope", true},
		{"wont fix", true},
		{"superseded by bd-9", true},
		{"obsoleted by rewrite", true},
		// Bare "obsolete" was dropped (GH#5026 review): "removed obsolete
		// migration shim" describes completed cleanup work, not a redirect.
		{"obsolete", false},
		{"not planned", true},
		// Failure closes (rejected/canceled) still completed their lifecycle
		// for eligibility purposes — only redirect/abandon keywords qualify.
		{"failed CI", false},
		{"canceled", false},
		// GH#5026 review, pinned regressions (word-boundary matching must not
		// swallow substrings of ordinary prose in either direction).
		{"removed obsolete shim", false},
		{"duplicate of ee-1", true},
		{"added dedup pass for event ingest", false},
	}
	for _, tc := range cases {
		got := IsNonCompletingClose(tc.reason)
		if got != tc.want {
			t.Errorf("IsNonCompletingClose(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}
