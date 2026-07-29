package main

import "testing"

// TestSanitizeDBName lives in an untagged file so both cgo and no-cgo builds
// compile and verify the shared helper.
func TestSanitizeDBName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-project", "my_project"},
		{"jtbot-core", "jtbot_core"},
		{"no-hyphens-here", "no_hyphens_here"},
		{"dots.and-hyphens", "dots_and_hyphens"},
		{"already_clean", "already_clean"},
		{"beads", "beads"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeDBName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeDBName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
