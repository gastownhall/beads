package main

import "testing"

// TestNormalizeAssigneeInput pins the bd-wkcfuz policy boundary for the
// update/assign/batch assignee ingests: a bare "-" and whitespace-only text
// are hidden spellings of "unassigned" and fold into the "" clear sentinel;
// every other value — including a spaced dash and spaced names — passes
// through byte-for-byte.
func TestNormalizeAssigneeInput(t *testing.T) {
	clears := []string{"", " ", "  ", "\t", "\n", " \t\r\n ", " ", "-"}
	for _, in := range clears {
		if got := normalizeAssigneeInput(in); got != "" {
			t.Errorf("normalizeAssigneeInput(%q) = %q, want cleared (\"\")", in, got)
		}
	}

	verbatim := []string{"alice", "a-b", "-x", "--", "_", ".", "none", "null", " - ", "- ", " -", " alice ", "Bob (Bot)"}
	for _, in := range verbatim {
		if got := normalizeAssigneeInput(in); got != in {
			t.Errorf("normalizeAssigneeInput(%q) = %q, want verbatim", in, got)
		}
	}
}
