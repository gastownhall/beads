// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/divergence_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import "testing"

// skipKnownDivergence parks one conformance case on the backend that cannot
// express what the leaf contract asserts.
//
// It takes BOTH coordinates a park has, because they answer different
// questions and neither implies the other:
//
//   - row is the divergence-ledger id (encode/ledger.go). It says WHAT the wire
//     cannot carry and is the join back to the reason, the design decision and
//     the pin. It used to be spelled inside the reason prose, in parentheses,
//     which made it unfindable by anything but a reader.
//   - beadID names the bead that records the shape and owns its retirement. It
//     says WHO unparks the case, which is a different fact from why it is
//     parked: several rows retire with one bead, and one row can outlive the
//     bead that found it.
//
// The literal "KNOWN DIVERGENCE" prefix is how `grep -r "KNOWN DIVERGENCE"`
// finds every parked case in the tree. Parking at the WIRING site rather than
// inside the shared Run function is what keeps the case honest elsewhere: it
// still runs, and still passes, on every backend that agrees.
//
// THIS FILE CARRIES NO BUILD TAG, and that is deliberate. Every call site today
// is cgo-tagged, because the served composition needs the embedded engine — but
// a helper declared behind the tag is a helper the no-cgo build cannot see, so
// the first untagged park would not compile. An unused function costs nothing;
// a declaration that has to move the day someone needs it costs a merge.
func skipKnownDivergence(t *testing.T, row, beadID, reason string) {
	t.Helper()
	t.Skipf("KNOWN DIVERGENCE %s (%s): %s", row, beadID, reason)
}
