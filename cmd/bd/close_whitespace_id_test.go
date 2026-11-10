package main

import "testing"

// TestCloseWhitespaceIDHint pins when close's not-found error carries the
// separate-argument hint (GH#5152): only for an argument holding whitespace.
func TestCloseWhitespaceIDHint(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"bd-a bd-b", "bd-a\tbd-b", "bd-a\nbd-b", " bd-a", "bd-a bd-b"} {
		if got := closeWhitespaceIDHint(id); got != " (pass each ID as its own argument; in zsh use an array or ${=IDS})" {
			t.Errorf("closeWhitespaceIDHint(%q) = %q, want the separate-argument hint", id, got)
		}
	}
	for _, id := range []string{"bd-a", "bd-a,bd-b", "", "bd-wisp-x1y"} {
		if got := closeWhitespaceIDHint(id); got != "" {
			t.Errorf("closeWhitespaceIDHint(%q) = %q, want no hint", id, got)
		}
	}
}
