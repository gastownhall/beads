package issueops

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
)

// NewRevision mints a fresh optimistic-concurrency token for a bead's `revision`
// cell. It is a uniformly-random positive int64 (the sign bit is masked, so the
// value is in [1, 2^63-1]) and it is OPAQUE: callers must compare it only for
// equality — it carries no ordering and must never be sorted, incremented, or
// compared with `<`/`>`.
//
// Every issues/wisps row write stamps a fresh NewRevision() so that (a) the cell
// always changes — Dolt reports changed rows, not matched rows, so a guard that
// left the cell untouched would look like a false conflict — and (b) two
// concurrent disjoint-column writers each stamp a distinct value, turning what
// would be a silent cell-wise merge into a detectable revision-cell conflict that
// the CAS guard (and the merge resolver) can act on.
//
// It never returns 0: 0 is reserved as the column DEFAULT / "unwritten" sentinel
// and as the assert-pristine precondition value.
//
// A guarded write should additionally reroll if the fresh draw equals the
// expected token (a ~2^-63 self-collision) so the cell is guaranteed to change;
// the two-writer peer-collision case rests on that same negligible improbability
// and is not otherwise guardable.
func NewRevision() int64 {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			// crypto/rand cannot realistically fail on a supported platform;
			// treat it as fatal, mirroring issueops.NewEventID's uuid.Must.
			panic(fmt.Errorf("beads: crypto/rand failed minting a revision token: %w", err))
		}
		v := int64(binary.BigEndian.Uint64(b[:]) & math.MaxInt64)
		if v != 0 {
			return v
		}
	}
}
