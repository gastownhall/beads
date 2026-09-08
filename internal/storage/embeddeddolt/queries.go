//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	var result []*types.IssueWithCounts
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkWithCountsInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetMoleculeProgress(ctx context.Context, moleculeID string) (*types.MoleculeProgressStats, error) {
	var result *types.MoleculeProgressStats
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetMoleculeProgressInTx(ctx, tx, moleculeID)
		return err
	})
	return result, err
}
