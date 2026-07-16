package flatfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: sqlkit.CreateIssuesWithFullOptions runs the whole
// batch inside one transaction (CreateIssuesInTxWithResult), so a mid-batch
// failure persists NOTHING — no issue rows, no events, no counter advance.
// The flat-file batch must match: bd import and repo-sync call
// CreateIssuesWithFullOptions directly (no RunInTransaction wrapper), so the
// batch itself must journal its write phase.

func TestBatchMidWriteIOFailureRollsBack(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Sequential IDs so the batch also writes counter.json in the plan phase.
	if err := s.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// A plain file where the second issue's comments directory must go makes
	// its comment import fail mid write phase (deterministic stand-in for
	// ENOSPC/EIO), after the first issue's file and event already landed.
	blocker := filepath.Join(s.commentsDir, "test-b1")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	batch := []*types.Issue{
		{Title: "first", Priority: 2}, // generated ID test-1 via counter
		{ID: "test-b1", Title: "second", Priority: 2,
			Comments: []*types.Comment{{Author: "alice", Text: "hi"}}},
	}
	if err := s.CreateIssues(ctx, batch, "tester"); err == nil {
		t.Fatal("mid-batch comment I/O failure returned nil; want error")
	}

	// All-or-nothing: the first issue (written before the failure) must not
	// survive, nor its created event, nor the counter advance.
	if _, err := s.GetIssue(ctx, "test-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetIssue(test-1) after failed batch = %v, want ErrNotFound (partial import persisted)", err)
	}
	if _, err := s.GetIssue(ctx, "test-b1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetIssue(test-b1) after failed batch = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(s.eventsDir, "test-1.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("events file for test-1 survived rollback (stat err = %v)", err)
	}
	if n, err := s.peekSequentialID("test"); err != nil || n != 0 {
		t.Errorf("counter after failed batch = %d (err %v), want 0 (counter advance rolled back)", n, err)
	}

	// A re-run after the obstacle is removed is a clean import: same
	// generated ID, no upsert/stale-guard path on leftovers.
	if err := os.Remove(blocker); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	retry := []*types.Issue{
		{Title: "first", Priority: 2},
		{ID: "test-b1", Title: "second", Priority: 2,
			Comments: []*types.Comment{{Author: "alice", Text: "hi"}}},
	}
	if err := s.CreateIssues(ctx, retry, "tester"); err != nil {
		t.Fatalf("re-run after rollback: %v", err)
	}
	if _, err := s.GetIssue(ctx, "test-1"); err != nil {
		t.Errorf("GetIssue(test-1) after re-run: %v", err)
	}
	comments, err := s.GetIssueComments(ctx, "test-b1")
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("comments after re-run = %d, want 1", len(comments))
	}
}

// A batch inside a RunInTransaction callback must not re-arm the journal (or
// re-take txMu): the outer transaction's journal covers its writes, and the
// outer rollback reverts them.
func TestBatchInsideTransactionRollsBackWithOuter(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	sentinel := errors.New("abort tx")
	err := s.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		if err := tx.CreateIssues(ctx, []*types.Issue{{ID: "test-t1", Title: "in tx", Priority: 2}}, "tester"); err != nil {
			return err
		}
		if _, err := s.GetIssue(ctx, "test-t1"); err != nil {
			return err // batch write must be visible inside the tx
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunInTransaction = %v, want sentinel", err)
	}
	if _, err := s.GetIssue(ctx, "test-t1"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetIssue(test-t1) after aborted tx = %v, want ErrNotFound", err)
	}
}
