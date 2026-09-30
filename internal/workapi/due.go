package workapi

import "github.com/steveyegge/beads/internal/types"

// BuildDueBackfillFilter selects the rows `bd due backfill` reads: ALL of
// them, deliberately.
//
// Every other read in this package narrows, because a viewer that leaks
// gates, wisps, templates and closed beads is a bug. The backfill is the
// opposite operation — it is an audit of what the database contains, and a
// bead it cannot see is a bead that silently keeps no deadline. Narrowing
// here would hide exactly the rows the audit exists to find, and would do it
// invisibly: the report would simply say "0 candidates".
//
// So the selection is unrestricted and the EXCLUSIONS live in Go, next to the
// exemption rule they have to agree with (issueops.DueRequiredExempt, plus
// the closed and already-dated skips in planDueBackfill). That is the same
// trade BuildSweepCandidateFilter records for glob matching: when a
// disagreement between the SQL predicate and the Go predicate would decide
// which rows get written, there is only allowed to be one predicate.
func BuildDueBackfillFilter() types.IssueFilter {
	return types.IssueFilter{}
}
