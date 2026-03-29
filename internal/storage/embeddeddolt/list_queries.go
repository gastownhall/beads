package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssuesInTx(ctx, tx, query, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	issueFilter := issueops.WispFilterToIssueFilter(filter)
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.SearchIssuesInTx(ctx, tx, "", issueFilter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	var result map[string][]string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetLabelsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error) {
	var result map[string]int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetCommentCountsInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllDependencyRecordsInTx(ctx, tx)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	var result map[string][]*types.Dependency
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetDependencyCounts(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	var result map[string]*types.DependencyCounts
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetDependencyCountsInTx(ctx, tx, issueIDs)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetBlockingInfoForIssues(ctx context.Context, issueIDs []string) (
	blockedByMap map[string][]string,
	blocksMap map[string][]string,
	parentMap map[string]string,
	err error,
) {
	err = s.withConn(ctx, false, func(tx *sql.Tx) error {
		var txErr error
		blockedByMap, blocksMap, parentMap, txErr = issueops.GetBlockingInfoForIssuesInTx(ctx, tx, issueIDs)
		return txErr
	})
	return
}
