package domain

import (
	"context"
	"slices"
	"testing"
	"time"
)

// retireOrderIssueRepo records the order of the issue-repository calls a
// retaining delete makes. The embedded interface is nil on purpose: a call
// this test did not intend panics rather than passing quietly.
type retireOrderIssueRepo struct {
	IssueSQLRepository
	calls       *[]string
	coordinated []string
}

func (r *retireOrderIssueRepo) PartitionWispIDs(_ context.Context, ids []string) ([]string, []string, error) {
	return nil, ids, nil
}

func (r *retireOrderIssueRepo) CoordinateCreates(_ context.Context, ids []string) error {
	*r.calls = append(*r.calls, "coordinate")
	r.coordinated = append(r.coordinated, ids...)
	return nil
}

func (r *retireOrderIssueRepo) AffectedByDeletion(context.Context, []string, []string) ([]string, []string, error) {
	return nil, nil, nil
}

func (r *retireOrderIssueRepo) DeleteByIDs(_ context.Context, ids []string, _ IssueTableOpts, _ string) (int, error) {
	if len(ids) > 0 {
		*r.calls = append(*r.calls, "delete")
	}
	return len(ids), nil
}

func (r *retireOrderIssueRepo) RetireIDs(context.Context, map[string]time.Time, IssueTableOpts) error {
	*r.calls = append(*r.calls, "retire")
	return nil
}

func (r *retireOrderIssueRepo) RecomputeIsBlockedAfterDelete(context.Context, []string, []string, []string) error {
	return nil
}

type retireOrderDepRepo struct{ DependencySQLRepository }

func (retireOrderDepRepo) ListByIssueIDs(context.Context, []string, DepListOpts) (DepBulkResult, error) {
	return DepBulkResult{}, nil
}

func (retireOrderDepRepo) DeleteAllForIDs(context.Context, []string, DepInsertOpts, string) (int, error) {
	return 0, nil
}

type retireOrderLabelRepo struct{ LabelSQLRepository }

func (retireOrderLabelRepo) CountAllForIDs(context.Context, []string, LabelOpts) (int, error) {
	return 0, nil
}

func (retireOrderLabelRepo) DeleteAllForIDs(context.Context, []string, LabelOpts) (int, error) {
	return 0, nil
}

type retireOrderEventsRepo struct{ EventsSQLRepository }

func (retireOrderEventsRepo) CountAllForIDs(context.Context, []string, RecordEventOpts) (int, error) {
	return 0, nil
}

func (retireOrderEventsRepo) DeleteAllForIDs(context.Context, []string, RecordEventOpts) (int, error) {
	return 0, nil
}

// TestRetainingDeleteCoordinatesBeforeItDeletes pins the unit-of-work half of
// the create/sweep lock (issueops.CoordinateIDCreatesInTx): a delete that
// retires what it removes coordinates every id BEFORE the first row goes, and
// a plain delete and a dry run coordinate nothing.
func TestRetainingDeleteCoordinatesBeforeItDeletes(t *testing.T) {
	closedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		retire    map[string]time.Time
		dryRun    bool
		wantCalls []string
	}{
		{"retaining", map[string]time.Time{"bd-1": closedAt, "bd-2": closedAt}, false, []string{"coordinate", "delete", "retire"}},
		{"plain", nil, false, []string{"delete"}},
		{"dry run", map[string]time.Time{"bd-1": closedAt, "bd-2": closedAt}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			repo := &retireOrderIssueRepo{calls: &calls}
			u := &issueUseCaseImpl{
				issueRepo:  repo,
				depRepo:    retireOrderDepRepo{},
				labelRepo:  retireOrderLabelRepo{},
				eventsRepo: retireOrderEventsRepo{},
			}
			if _, err := u.deleteMany(t.Context(), DeleteIssuesParams{
				IDs:    []string{"bd-1", "bd-2"},
				Force:  true,
				DryRun: tc.dryRun,
				Retire: tc.retire,
			}, "actor"); err != nil {
				t.Fatalf("deleteMany() = %v", err)
			}
			if !slices.Equal(calls, tc.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tc.wantCalls)
			}
			if tc.retire != nil && !tc.dryRun && !slices.Equal(repo.coordinated, []string{"bd-1", "bd-2"}) {
				t.Fatalf("coordinated %v, want every id the delete retires", repo.coordinated)
			}
		})
	}
}
