//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// WakeExpiredDefersAdvisory preserves the backend wake contract for decorators.
// The name is historical — the defer wake was the only scheduled sweep when it
// was introduced; it now runs the full pass (defer wake + due trigger).
func (s *EmbeddedDoltStore) WakeExpiredDefersAdvisory(ctx context.Context) {
	s.runScheduledSweeps(ctx)
}

var _ storage.ExpiredDeferWaker = (*EmbeddedDoltStore)(nil)

// runScheduledSweeps runs the lazy time-based sweeps — defer wake and due
// trigger (issueops.RunScheduledSweepsInTx) — in one write transaction before
// a ready-work read. Advisory by contract:
// a ready listing must never fail because the sweep could not run — strict
// --readonly stores skip it silently (the one mode that reaches ErrReadOnly,
// mirroring the tip-metadata write's tolerance), anything else warns. It runs
// OUTSIDE the read connections below because withConn(ctx, false, …) always
// rolls back.
//
// The body lives on RunScheduledSweeps (due_sweep.go), which `bd due sweep`
// and the external clock call directly; this is that same pass with its
// result discarded and its error reduced to a warning.
func (s *EmbeddedDoltStore) runScheduledSweeps(ctx context.Context) {
	if _, err := s.RunScheduledSweeps(ctx); err != nil && !errors.Is(err, ErrReadOnly) {
		fmt.Fprintf(os.Stderr, "warning: scheduled sweep skipped: %v\n", err)
	}
}

func (s *EmbeddedDoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	s.runScheduledSweeps(ctx)
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	s.runScheduledSweeps(ctx)
	var result []*types.IssueWithCounts
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkWithCountsInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

// CountReadyWork returns the total ready-work count for filter. It is identical
// to len(GetReadyWorkWithCounts(filter with Limit=0)) but sizes the total with
// cheap indexed COUNT(*)s instead of re-running the counts mega-query. Backs the
// storage.ReadyWorkCounter capability.
func (s *EmbeddedDoltStore) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	s.runScheduledSweeps(ctx)
	var n int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		n, err = issueops.CountReadyWorkInTx(ctx, tx, filter)
		return err
	})
	return n, err
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
