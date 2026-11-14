package main

import (
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// `bd gc`'s decay selects closed beads outside issueops.Sweeper, so what is
// here is a thin call into the SAME pure function
// (workapi.FilterSweepCandidates) the role applies below both front doors,
// plus gc's own warning line. One definition, two callers: gc and the role
// cannot come to disagree about what "a closed bead safe to delete" means.
//
// The other retention doors outside the role — `bd admin cleanup` and the
// stale-closed-issues doctor fix — select by their own rules and delete with
// no tombstone (cleanup through the Deleter role, the doctor fix row by row),
// so each holds back the beads of a retained type: cleanup through
// holdBackRetainedTypes, the doctor fix, in its own package, through the
// workapi partition itself. Each reads types.retained with its selection, on
// one snapshot (storage.SearchIssuesWithConfig). `bd mol wisp gc` does not
// consult the setting; SweepRequest.RetainIDs says why.

type closedDeletionCandidateStats = issueops.SweepSkips

// filterClosedDeletionCandidates keeps the closed, unpinned, old-enough
// candidates and reports what it held back. `bd gc` matches no glob, so it
// passes the empty pattern, which admits everything.
func filterClosedDeletionCandidates(issues []*types.Issue, cutoff *time.Time) ([]*types.Issue, closedDeletionCandidateStats) {
	return workapi.FilterSweepCandidates(issues, "", cutoff)
}

func warnClosedDeletionSafetySkips(stats closedDeletionCandidateStats) {
	if workapi.SweepDefenseSkips(stats) == 0 {
		return
	}
	WarnError("skipped %d deletion candidate(s) after closed_at safety recheck (nil=%d, non_closed=%d, missing_closed_at=%d, too_recent=%d)",
		workapi.SweepDefenseSkips(stats),
		stats.Unreadable,
		stats.NotClosed,
		stats.UnknownClosedAt,
		stats.ClosedAtOrAfterCutoff,
	)
}

// holdBackRetainedTypes keeps a retention door that cannot retire an id off
// the beads whose type the workspace retains (issueops.SettingKeyTypesRetained):
// `bd gc`'s decay and `bd admin cleanup`. It is the same
// partition the Sweeper role applies to a sweep without RetainIDs: those
// commands delete by id and write no tombstone, so a retained bead is never
// theirs to take. setting is the stored value of types.retained, read from the
// workspace database with the selection; a caller that cannot read it must
// fail rather than pass "".
func holdBackRetainedTypes(issues []*types.Issue, setting string) ([]*types.Issue, int) {
	return workapi.PartitionSweepRetainedTypes(issues, workapi.RetainedTypesFromSetting(setting))
}

// retainedTypesKeptLine is the text line for a bulk delete's kept count; noun
// names what was kept ("closed issue(s)").
func retainedTypesKeptLine(kept int, noun string) string {
	return fmt.Sprintf("  Kept %d %s of a retained type (types.retained) — only bd prune/purge --retain-ids deletes them", kept, noun)
}

// warnIgnoredYAMLRetainedTypes prints config.RetainedTypesYAMLWarning to
// stderr, if there is one. That layer is never read for the setting — every
// sweep and bulk delete reads the workspace database, so a shared workspace
// has one rule — so every bulk delete that honors types.retained calls it
// before it selects (the doctor fix through its own copy of this call).
func warnIgnoredYAMLRetainedTypes() {
	if msg := config.RetainedTypesYAMLWarning(); msg != "" {
		WarnError("%s", msg)
	}
}

// retentionReport is the --json member a bulk delete outside the Sweeper role
// adds when it held retained beads back: retention_required, the name the
// sweeps use. It is nil when nothing was kept, so a workspace without the
// setting sees its old shape.
func retentionReport(kept int) map[string]interface{} {
	if kept == 0 {
		return nil
	}
	return map[string]interface{}{"retention_required": kept}
}
