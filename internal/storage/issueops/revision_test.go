package issueops

import "testing"

// NewRevision is the optimistic-concurrency token minted for a bead's revision
// cell. Its load-bearing properties: it is never zero (0 is reserved as the
// DEFAULT / "unwritten" sentinel and as the assert-pristine precondition), it is
// positive (the sign bit is masked), and collisions across draws are negligible
// so a fresh draw reliably changes the cell and disambiguates a real guard miss.

func TestNewRevisionIsNonZeroPositive(t *testing.T) {
	for i := 0; i < 100_000; i++ {
		if r := NewRevision(); r <= 0 {
			t.Fatalf("NewRevision() = %d; want a positive non-zero int64 "+
				"(sign bit masked, 0 reserved as the unwritten/assert-pristine sentinel)", r)
		}
	}
}

func TestNewRevisionIsUnique(t *testing.T) {
	const n = 200_000
	seen := make(map[int64]struct{}, n)
	for i := 0; i < n; i++ {
		r := NewRevision()
		if _, dup := seen[r]; dup {
			t.Fatalf("NewRevision() produced duplicate %d within %d draws; "+
				"the nonce space must make collisions negligible so every write changes the cell", r, n)
		}
		seen[r] = struct{}{}
	}
}
