package main

import (
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// `bd gc` is the one caller left that is not behind issueops.Sweeper, so what
// is here is a thin call into the SAME pure function
// (workapi.FilterSweepCandidates) the role applies below both front doors,
// plus gc's own warning line. One definition, two callers: gc and the role
// cannot come to disagree about what "a closed bead safe to delete" means.

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

// holdBackRetainedTypes keeps `bd gc`'s decay off the closed beads whose type
// the workspace retains (issueops.SettingKeyTypesRetained). It is the same
// partition the Sweeper role applies to a sweep without RetainIDs: decay
// deletes by id and cannot retire one, so a retained bead is never its to
// take. setting is the stored value of types.retained.
func holdBackRetainedTypes(issues []*types.Issue, setting string) ([]*types.Issue, int) {
	return workapi.PartitionSweepRetainedTypes(issues, workapi.RetainedTypesFromSetting(setting), false)
}

// retainedTypesKeptLine is the text line for the decay's kept count.
func retainedTypesKeptLine(kept int) string {
	return fmt.Sprintf("  Kept %d closed issue(s) of a retained type (types.retained) — only bd prune/purge --retain-ids deletes them", kept)
}
