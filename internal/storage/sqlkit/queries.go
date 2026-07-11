package sqlkit

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// SearchIssues finds issues matching query and filters.
// Delegates to issueops.SearchIssuesInTx for shared query logic.
func (s *Store) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssuesInTx(ctx, tx, query, filter)
		return err
	})
	return result, err
}

// SearchIssueIDs is the narrow-projection variant of SearchIssues, returning
// only matching ids. Delegates to issueops.SearchIssueIDsInTx (parity with the
// Dolt stores).
func (s *Store) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	var result []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssueIDsInTx(ctx, tx, query, filter)
		return err
	})
	return result, err
}

// SearchIssuesWithCounts finds issues matching query and filters, hydrating
// per-issue dependency/dependent counts.
func (s *Store) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	var result []*types.IssueWithCounts
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssuesWithCountsInTx(ctx, tx, query, filter)
		return err
	})
	return result, err
}

// GetReadyWorkWithCounts returns ready-to-work issues with dependency counts.
func (s *Store) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	var result []*types.IssueWithCounts
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkWithCountsInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

// CountReadyWork returns the total ready-work count for filter. It is identical
// to len(GetReadyWorkWithCounts(filter with Limit=0)) but sizes the total with
// cheap indexed COUNT(*)s instead of re-running the counts mega-query, so every
// SQL backend (Postgres/MySQL/SQLite) satisfies storage.ReadyWorkCounter and
// `bd ready --json` prints "Showing X of N" without the unbounded pass.
func (s *Store) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	var n int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountReadyWorkInTx(ctx, tx, filter)
		return err
	})
	return n, err
}

// GetBlockedIssues returns issues that are blocked by open dependencies.
func (s *Store) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	var result []*types.BlockedIssue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetBlockedIssuesInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

// GetStatistics returns summary statistics (counts, blocked, ready) for the workspace.
func (s *Store) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	var stats *types.Statistics
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		stats, err = issueops.GetStatisticsInTx(ctx, tx)
		return err
	})
	return stats, err
}

// GetStaleIssues returns non-ephemeral issues not updated within the filter window.
func (s *Store) GetStaleIssues(ctx context.Context, filter types.StaleFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetStaleIssuesInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

// GetEpicsEligibleForClosure returns epics whose children are all closed.
func (s *Store) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	var result []*types.EpicStatus
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetEpicsEligibleForClosureInTx(ctx, tx)
		return err
	})
	return result, err
}

// ListWisps returns ephemeral issues matching the filter.
// It always queries the wisps table (Ephemeral=true); callers do not need to
// set that flag. WispFilterToIssueFilter sets Ephemeral=true (and excludes
// closed unless IncludeClosed), and SearchIssuesInTx routes Ephemeral=true
// queries to the wisps table.
func (s *Store) ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	issueFilter := issueops.WispFilterToIssueFilter(filter)
	var result []*types.Issue
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssuesInTx(ctx, tx, "", issueFilter)
		return err
	})
	return result, err
}

// CountIssues returns the number of issues matching query and filter.
// Filter.Limit and Filter.Offset are ignored; all other fields apply.
func (s *Store) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	var n int64
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		count, err := issueops.CountIssuesInTx(ctx, tx, query, filter)
		if err != nil {
			return err
		}
		n = int64(count)
		return nil
	})
	return n, err
}

// CountIssuesByGroup returns per-group issue counts. groupBy is one of:
// status, priority, type, assignee, label.
func (s *Store) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	var result map[string]int
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.CountIssuesByGroupInTx(ctx, tx, filter, groupBy)
		return err
	})
	return result, err
}
