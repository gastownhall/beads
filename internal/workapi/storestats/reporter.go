// Package storestats holds the store-backed implementation of
// issueops.StatsReporter: one shared body that every store-shaped backend's
// StatsReporter accessor hands back.
//
// It is a package of its own for the reason internal/workapi/storecounter is —
// see that package's doc. Down here the only importers are the two Dolt store
// packages, and the cmd-bd-role-constructors depguard rule in .golangci.yml
// makes a front door importing it a lint failure rather than a review comment.
package storestats

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// New returns the summary-statistics surface backed by a store handle.
// *DoltStore and *EmbeddedDoltStore answer identically because the difference
// between them is below storage.DoltStorage, not above it.
func New(store storage.DoltStorage) (issueops.StatsReporter, error) {
	if store == nil {
		return nil, &issueops.ErrUnsupported{Op: "storestats.New", Backend: "nil"}
	}
	return &storeStatsReporter{store: store}, nil
}

type storeStatsReporter struct{ store storage.DoltStorage }

var _ issueops.StatsReporter = (*storeStatsReporter)(nil)

// Stats takes the SkipBlocked hint, because this seam is the one that has a
// cheaper path: GetStatisticsNoBlocked runs the status scan and stops, leaving
// BlockedIssues and ReadyIssues nil. That pairing is the whole of what the hint
// promises, and it is the seam's own — nothing is nilled here.
func (r *storeStatsReporter) Stats(ctx context.Context, req issueops.StatsRequest) (issueops.StatsResult, error) {
	var (
		summary *types.Statistics
		err     error
	)
	if req.SkipBlocked {
		summary, err = r.store.GetStatisticsNoBlocked(ctx)
	} else {
		summary, err = r.store.GetStatistics(ctx)
	}
	if err != nil {
		return issueops.StatsResult{}, err
	}
	if summary == nil {
		// The seam has no documented nil-with-nil-error answer, and a role
		// that dereferenced one anyway would fault inside whichever front door
		// asked. An empty workspace is zeros, so that is what an absent
		// summary becomes.
		return issueops.StatsResult{}, nil
	}
	return issueops.StatsResult{Summary: *summary}, nil
}

// AssigneeStats asks storage its two questions and folds them through the
// shared builder, so this route and the unit-of-work route cannot come to
// disagree about what one actor's summary means.
//
// The ready-work failure is reported as zero ready work rather than as an
// error, which AssigneeStats documents as this role's one number that may not
// be an answer. The alternative turns a summary that is right about five
// numbers into no summary at all, on the one query in this role slow enough to
// time out on a large graph.
func (r *storeStatsReporter) AssigneeStats(ctx context.Context, req issueops.AssigneeStatsRequest) (issueops.StatsResult, error) {
	assignee, err := workapi.ValidateStatsAssignee(req.Assignee)
	if err != nil {
		return issueops.StatsResult{}, err
	}

	issues, err := r.store.SearchIssues(ctx, "", workapi.BuildStatsAssigneeIssueFilter(assignee))
	if err != nil {
		return issueops.StatsResult{}, err
	}

	readyCount := 0
	if ready, readyErr := r.store.GetReadyWork(ctx, workapi.BuildStatsAssigneeWorkFilter(assignee)); readyErr == nil {
		readyCount = len(ready)
	}

	return issueops.StatsResult{Summary: workapi.FoldStatsAssigneeSummary(issues, readyCount)}, nil
}
