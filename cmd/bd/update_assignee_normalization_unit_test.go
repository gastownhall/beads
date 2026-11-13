package main

import "testing"

// TestNormalizeAssigneeInput pins the exact policy boundary of the
// hidden-empty assignee normalization (bd-wkcfuz): a bare "-" and
// whitespace-only text (any Unicode space, including NBSP) clear; every
// other byte sequence — including a padded dash, "--" and "-x" — is a name
// and passes through unchanged.
func TestNormalizeAssigneeInput(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Clear spellings.
		{"", ""},
		{"-", ""},
		{" ", ""},
		{"  ", ""},
		{"\t", ""},
		{"\n", ""},
		{"\r\n", ""},
		{" \t\r\n ", ""},
		{" ", ""}, // NBSP: unicode.IsSpace, like strings.TrimSpace
		{"　", ""}, // ideographic space
		// Names and near-misses stay byte-for-byte.
		{"alice", "alice"},
		{" alice ", " alice "},
		{"a-b", "a-b"},
		{"-x", "-x"},
		{"x-", "x-"},
		{"--", "--"},
		{" - ", " - "},
		{"_", "_"},
		{".", "."},
		{"none", "none"},
		{"null", "null"},
		{"0", "0"},
		{"alice ", "alice "}, // NBSP inside a name is content
	}
	for _, tt := range tests {
		if got := normalizeAssigneeInput(tt.in); got != tt.want {
			t.Errorf("normalizeAssigneeInput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
