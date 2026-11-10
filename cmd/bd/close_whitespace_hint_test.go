package main

import "testing"

// TestCloseWhitespaceIDHint pins the helper behind GH#5152's close remedy: an
// unresolved close argument containing whitespace gets the separate-argument
// hint, a plain single unknown id gets nothing, and the hint is a pure
// APPENDIX — the not-found error it attaches to keeps its identity.
func TestCloseWhitespaceIDHint(t *testing.T) {
	const want = " (pass each ID as its own argument; in zsh use an array or ${=IDS})"

	for _, id := range []string{"a b c", "a\tb", "a\nb", " lead", "trail "} {
		if got := closeArgWhitespaceHint(id); got != want {
			t.Errorf("closeArgWhitespaceHint(%q) = %q, want the separate-argument hint", id, got)
		}
	}
	for _, id := range []string{"", "gc-wisp-1", "no-such-id", "a_b_c"} {
		if got := closeArgWhitespaceHint(id); got != "" {
			t.Errorf("closeArgWhitespaceHint(%q) = %q, want no hint (single id keeps today's error)", id, got)
		}
	}
}
