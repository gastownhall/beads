package main

import "testing"

// TestNormalizeAssigneeInput pins the bd-wkcfuz policy boundary for the
// update/assign/batch assignee ingests: whitespace-only text and a "-" with
// only whitespace around it are hidden spellings of "unassigned" and fold
// into the "" clear sentinel (#8203's rule, its "  - " case included); every
// other value — spaced names and other dashes included — passes through
// byte-for-byte.
func TestNormalizeAssigneeInput(t *testing.T) {
	clears := []string{"", " ", "  ", "\t", "\n", " \t\r\n ", " ", "-", " - ", "  - ", "- ", " -", "\t-\n"}
	for _, in := range clears {
		if got := normalizeAssigneeInput(in); got != "" {
			t.Errorf("normalizeAssigneeInput(%q) = %q, want cleared (\"\")", in, got)
		}
	}

	verbatim := []string{"alice", "a-b", "-x", " -x", "--", " -- ", "_", ".", "none", "null", " alice ", "Bob (Bot)", "Dana-Dash (Robot)"}
	for _, in := range verbatim {
		if got := normalizeAssigneeInput(in); got != in {
			t.Errorf("normalizeAssigneeInput(%q) = %q, want verbatim", in, got)
		}
	}
}
