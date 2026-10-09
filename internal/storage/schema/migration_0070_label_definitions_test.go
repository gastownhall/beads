package schema

import "testing"

// TestLatestVersionIncludesMigration0070 pins the slot the opt-in label
// vocabulary claims. It supersedes 0069's own version of this test the same
// way 0069's superseded 0068's: the pin moves to whichever migration is the
// current top. Same shape and same purpose: a hardcoded literal, so the NEXT
// migration to claim a slot fails here and has to say why its number is what
// it is, rather than silently inheriting whatever LatestVersion() drifted to.
//
// This registry originally landed on 0067 and was renumbered to 0068 when
// #6304 took 0067 for versioned beads on main (2bb1e20de), then to 0069 when
// #6650 took 0068 for attribution_status (da6263374), then to 0070 when #6675
// took 0069 for the issue_versions DATETIME(6) widening (73702cc73). The
// first collision was
// caught by schema's own duplicate-version panic, not by a review, which is
// exactly why the pin is a literal.
func TestLatestVersionIncludesMigration0070(t *testing.T) {
	const want = 70
	if got := LatestVersion(); got != want {
		t.Fatalf("LatestVersion() = %d, want %d (label_definitions migration slot claimed by the opt-in label vocabulary)", got, want)
	}
}
