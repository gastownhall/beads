package schema

import "testing"

// TestLatestVersionIncludesMigration0068 is the twin of
// TestLatestVersionIncludesMigration0067, naming the slot the opt-in label
// vocabulary claims. Same shape and same purpose: a hardcoded literal, so the
// NEXT migration to claim a slot fails here and has to say why its number is
// what it is, rather than silently inheriting whatever LatestVersion() drifted
// to.
//
// This registry originally landed on 0067 and was renumbered to 0068 when
// #6304 took 0067 for versioned beads on main (2bb1e20de). The collision was
// caught by schema's own duplicate-version panic, not by a review, which is
// exactly why both pins are literals.
func TestLatestVersionIncludesMigration0068(t *testing.T) {
	const want = 68
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (label_definitions migration slot claimed by the opt-in label vocabulary)", got, want)
	}
}
