package main

import (
	"fmt"
	"strings"
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
// The other bulk deletes outside the role — `bd admin cleanup`, `bd mol wisp
// gc` and the stale-closed-issues doctor fix — select by their own rules, but
// all of them delete through the Deleter role, which writes no tombstone, so
// each holds back the beads of a retained type through holdBackRetainedTypes
// (the doctor fix, in its own package, through the workapi partition itself).

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

// holdBackRetainedTypes keeps a bulk delete that cannot retire an id off the
// beads whose type the workspace retains (issueops.SettingKeyTypesRetained):
// `bd gc`'s decay, `bd admin cleanup` and `bd mol wisp gc`. It is the same
// partition the Sweeper role applies to a sweep without RetainIDs: those
// commands delete by id through the Deleter role and write no tombstone, so a
// retained bead is never theirs to take. setting is the stored value of
// types.retained, read from the workspace database; a caller that cannot read
// it must fail rather than pass "".
func holdBackRetainedTypes(issues []*types.Issue, setting string) ([]*types.Issue, int) {
	return workapi.PartitionSweepRetainedTypes(issues, workapi.RetainedTypesFromSetting(setting))
}

// retainedTypesKeptLine is the text line for a bulk delete's kept count; noun
// names what was kept ("closed issue(s)", "wisp(s)").
func retainedTypesKeptLine(kept int, noun string) string {
	return fmt.Sprintf("  Kept %d %s of a retained type (types.retained) — only bd prune/purge --retain-ids deletes them", kept, noun)
}

// ignoredYAMLRetainedTypesWarning is the warning for a types.retained key in
// config.yaml (or BD_TYPES_RETAINED), or "" when there is none. That layer is
// never read for the setting — every sweep and bulk delete reads the
// workspace database, so a shared workspace has one rule — and a key there
// would otherwise look like protection while protecting nothing.
func ignoredYAMLRetainedTypesWarning() string {
	names := config.GetRetainedTypesFromYAML()
	if len(names) == 0 {
		return ""
	}
	list := strings.Join(names, ",")
	return fmt.Sprintf("%s in config.yaml (%s) is ignored: it is read from the workspace database only; set it with `bd config set %s %s`",
		issueops.SettingKeyTypesRetained, list, issueops.SettingKeyTypesRetained, list)
}

// warnIgnoredYAMLRetainedTypes prints ignoredYAMLRetainedTypesWarning to
// stderr, if there is one. Every bulk delete that honors types.retained calls
// it before it selects.
func warnIgnoredYAMLRetainedTypes() {
	if msg := ignoredYAMLRetainedTypesWarning(); msg != "" {
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
