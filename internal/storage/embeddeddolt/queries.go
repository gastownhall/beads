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
func (s *EmbeddedDoltStore) WakeExpiredDefersAdvisory(ctx context.Context) {
	s.wakeExpiredDefers(ctx)
}

var _ storage.ExpiredDeferWaker = (*EmbeddedDoltStore)(nil)
var _ storage.DescendantSearcher = (*EmbeddedDoltStore)(nil)
var _ storage.IssuesWithConfigSearcher = (*EmbeddedDoltStore)(nil)

// wakeExpiredDefers runs the lazy defer-wake sweep (issueops.WakeExpiredDefersInTx)
// in its own write transaction before a ready-work read. Advisory by contract:
// a ready listing must never fail because the sweep could not run — strict
// --readonly stores skip it silently (the one mode that reaches ErrReadOnly,
// mirroring the tip-metadata write's tolerance), anything else warns. It runs
// OUTSIDE the read connections below because withConn(ctx, false, …) always
// rolls back.
//
// The write transaction opens only when a read transaction's one statement
// (issueops.ExpiredDefersDue) finds a defer that is due, so a ready read with
// nothing due commits nothing. The two are separate transactions, so the answer
// can be stale; see issueops.ExpiredDefersDue for why either stale answer is
// safe (a false one delays the wake to the next sweep; a true one is rechecked
// by the sweep's own predicate). A failed preflight is handled like a failed
// sweep.
func (s *EmbeddedDoltStore) wakeExpiredDefers(ctx context.Context) {
	if s.readOnly {
		// The write below would refuse with ErrReadOnly, which is skipped
		// silently; skip the preflight read that would precede it as well.
		return
	}
	var due bool
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		due, err = issueops.ExpiredDefersDue(ctx, tx)
		return err
	})
	afterDeferWakePreflight(due)
	if err == nil && due {
		err = s.wakeExpiredDefersTx(ctx)
	}
	if err != nil && !errors.Is(err, ErrReadOnly) {
		fmt.Fprintf(os.Stderr, "warning: defer-wake sweep skipped: %v\n", err)
	}
}

// afterDeferWakePreflight runs between wakeExpiredDefers' preflight and the
// write transaction it may open, with the preflight's answer. It is a no-op
// outside tests, which use it to change a row in that window.
var afterDeferWakePreflight = func(due bool) {}

func (s *EmbeddedDoltStore) wakeExpiredDefersTx(ctx context.Context) error {
	return s.runIssueOperationTxWithMessage(ctx, func(tx *sql.Tx) (issueops.ChangedTables, string, error) {
		woke, err := issueops.WakeExpiredDefersInTx(ctx, tx)
		if err != nil {
			return nil, "", err
		}
		if len(woke.Issues) == 0 {
			// Wisp-only wakes persist with the SQL commit but mint no version
			// commit: wisp tables are dolt_ignored.
			return nil, "", nil
		}
		tables := issueops.ChangedTables{}
		tables.Add("issues", "events")
		return tables, issueops.WakeDefersCommitMessage(len(woke.Issues)), nil
	})
}

func (s *EmbeddedDoltStore) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	s.wakeExpiredDefers(ctx)
	var result []*types.Issue
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	s.wakeExpiredDefers(ctx)
	var result []*types.IssueWithCounts
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetReadyWorkWithCountsInTx(ctx, tx, filter)
		return err
	})
	return result, err
}

// GetReadyWorkWithCountsAndTotal returns the ready page and the size of the
// whole ready set in one read transaction (and one defer-wake sweep), so a
// capped `bd ready` needs no separate count pass.
func (s *EmbeddedDoltStore) GetReadyWorkWithCountsAndTotal(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	s.wakeExpiredDefers(ctx)
	var result []*types.IssueWithCounts
	var total int
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, total, err = issueops.GetReadyWorkWithCountsAndTotalInTx(ctx, tx, filter)
		return err
	})
	return result, total, err
}

// CountReadyWork returns the total ready-work count for filter. It is identical
// to len(GetReadyWorkWithCounts(filter with Limit=0)) but sizes the total with
// cheap indexed COUNT(*)s instead of re-running the counts mega-query. Backs the
// storage.ReadyWorkCounter capability.
func (s *EmbeddedDoltStore) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	s.wakeExpiredDefers(ctx)
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
