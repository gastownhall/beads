package dolt

import (
	"github.com/steveyegge/beads/internal/storage/schema"
)

// doltMergeSucceededMessage is the message DOLT_PULL and DOLT_MERGE put in
// their `message` column when they actually merged something. Every other
// message either names a reason nothing merged or is absent.
//
// Measured on Dolt 2.1.8, all four outcomes of CALL DOLT_PULL('origin','main'):
//
//	fast_forward  conflicts  message
//	1             0          merge successful                    (fast-forward)
//	0             0          merge successful                    (three-way)
//	0             0          Everything up-to-date               (no-op, level)
//	0             0          cannot fast forward from a to b. a is ahead of b already
//
// Two things follow, and both are why this constant exists rather than a
// fast_forward check. fast_forward is 0 for a genuine three-way merge AND for
// both no-ops, so it does not answer "did anything merge"; it distinguishes the
// KIND of merge, which is what its name says and what dolt uses it for. And the
// no-op has more than one spelling — doltdb.ErrUpToDate and doltdb.ErrIsAhead
// are both surfaced as the message with a nil error — so enumerating the
// negatives is a losing game. The single positive is the stable thing to match:
// upstream returns this exact literal from all three of its merged paths
// (dprocedures/dolt_merge.go, dolt_pull.go) and pins it in hundreds of its own
// enginetest expectations, so it cannot drift without breaking dolt's suite.
const doltMergeSucceededMessage = "merge successful"

// pullReport is what the engine itself said a pull did. It is the in-band
// answer to "did anything actually arrive", which an error alone cannot give:
// a pull that merged 400 commits and a pull that found the remote unchanged
// both return nil.
//
// This is the TRANSPORT's own report and nothing more. It is not proof the
// merge landed where the caller reads — a merge that succeeded on the wrong
// branch reports "merge successful" quite truthfully — and it exists only on
// the SQL routes, because `dolt pull` as a subprocess exits 0 either way and
// says nothing structured. Reported is false in both of those cases and in
// every other case where no row came back; a caller that treats !Reported as
// "nothing merged" has misread it.
type pullReport struct {
	// Reported is true when a transport handed back an engine row at all.
	// When it is false every other field is meaningless, not merely zero.
	Reported bool

	// Merged is true when the engine says commits were merged.
	Merged bool

	// Conflicted is dolt's conflicts column, which despite the plural name is
	// a 0/1 FLAG (dprocedures: noConflictsOrViolations=0,
	// hasConflictsOrViolations=1), not a count. It covers constraint
	// violations as well as conflicts. Use GetConflicts for the actual set.
	Conflicted bool

	// Message is the engine's own words, for error text and logs. Not a
	// control-flow input beyond Merged above.
	Message string
}

// parseMergeReport reads the row CALL DOLT_PULL or CALL DOLT_MERGE returned.
// Both carry `conflicts` and `message`; DOLT_MERGE additionally carries `hash`,
// which this ignores. A nil row (no rows returned) reports nothing.
func parseMergeReport(row schema.CallRow) pullReport {
	if row == nil {
		return pullReport{}
	}
	message, _ := row.Str("message")
	conflicts, _ := row.Int("conflicts")
	return pullReport{
		Reported:   true,
		Merged:     message == doltMergeSucceededMessage,
		Conflicted: conflicts != 0,
		Message:    message,
	}
}
