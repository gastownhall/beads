//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// GH#7890, AC-7: a filtered push must fail closed. A DELETE failure on the
// staging branch may not degrade into an unfiltered publish (the exact leak
// the filter exists to prevent), a push failure after the local compensation
// may not lose local data, and both paths must leave no temporary branches
// behind — the store stays usable for the next, successful attempt.
//
// These tests drive the embedded store's filteredPushToPeer directly over
// the vcDeleteExcluded / vcPush seams (the same seams the store's other
// tests use), because the failures they inject sit below the SQL layer the
// driver would otherwise never produce on demand.

// newFilteredPushFailureEnv returns a store holding one shareable task and one
// private chore (the excluded type), committed, with a file:// peer remote
// that has received the unfiltered initial push.
func newFilteredPushFailureEnv(t *testing.T) (*EmbeddedDoltStore, *types.Issue, *types.Issue) {
	t.Helper()
	ctx := t.Context()
	store := newPeerAuthTestStore(t)

	// CreateIssue refuses to run without the database's issue_prefix config
	// row (the same gate 'bd init --prefix' satisfies in production).
	if err := store.SetConfig(ctx, "issue_prefix", "fedfail"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("commit init config: %v", err)
	}

	task := &types.Issue{
		ID:        "fedfail-task",
		Title:     "Shareable task",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	chore := &types.Issue{
		ID:        "fedfail-chore",
		Title:     "Private chore",
		IssueType: types.TypeChore,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	for _, issue := range []*types.Issue{task, chore} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.Commit(ctx, "create test issues"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	if err := store.AddRemote(ctx, "peer", "file://"+remoteDir); err != nil {
		t.Fatalf("add peer remote: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("initial push: %v", err)
	}
	return store, task, chore
}

// assertFilteredPushFailureAftermath pins the shared post-failure contract:
// both local rows survive, no staging or keep branch is left behind, and the
// store still accepts ordinary writes.
func assertFilteredPushFailureAftermath(t *testing.T, store *EmbeddedDoltStore, task, chore *types.Issue) {
	t.Helper()
	ctx := t.Context()

	if err := assertIssueRowExists(ctx, store, task.ID); err != nil {
		t.Errorf("task row lost: %v", err)
	}
	if err := assertIssueRowExists(ctx, store, chore.ID); err != nil {
		t.Errorf("excluded chore row lost (local data must survive a failed filtered push): %v", err)
	}

	branches, err := store.ListBranches(ctx)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, branch := range branches {
		if strings.HasPrefix(branch, versioncontrolops.FederationStagingBranchPrefix) ||
			strings.HasPrefix(branch, versioncontrolops.FederationKeepBranchPrefix) {
			t.Errorf("temporary branch %q outlived the failed filtered push", branch)
		}
	}
	// The store must remain usable: the pinned session the staging steps ran
	// on is shared with later operations, so a failure that left it checked
	// out on the (deleted) staging branch would wedge ordinary writes.
	after := &types.Issue{
		ID:        "fedfail-after",
		Title:     "Written after the failure",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateIssue(ctx, after, "test"); err != nil {
		t.Fatalf("create issue after failed filtered push: %v", err)
	}
	if err := store.Commit(ctx, "write after failed filtered push"); err != nil {
		t.Fatalf("commit after failed filtered push: %v", err)
	}
	if err := assertIssueRowExists(ctx, store, after.ID); err != nil {
		t.Errorf("post-failure write did not land: %v", err)
	}
}

// assertIssueRowExists reports whether the issues row with id exists.
func assertIssueRowExists(ctx context.Context, store *EmbeddedDoltStore, id string) error {
	var n int
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&n)
	}); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("issue %s row count = %d, want 1", id, n)
	}
	return nil
}

// countLocalRevertCommits counts the DOLT_REVERT commits on the current
// branch — the visible half of the local compensation.
func countLocalRevertCommits(ctx context.Context, store *EmbeddedDoltStore) (int, error) {
	var n int
	err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'Revert %'").Scan(&n)
	})
	return n, err
}

// TestEmbeddedFilteredPushDeleteFailureFailsClosed pins the core AC-7
// guarantee on the embedded backend: a DELETE failure on the staging branch
// aborts the push with that error instead of publishing the unfiltered tip.
func TestEmbeddedFilteredPushDeleteFailureFailsClosed(t *testing.T) {
	store, task, chore := newFilteredPushFailureEnv(t)
	ctx := t.Context()

	deleteErr := errors.New("Error 1205: lock wait timeout exceeded")
	prevDelete := vcDeleteExcluded
	vcDeleteExcluded = func(context.Context, versioncontrolops.DBConn, []string) (bool, error) {
		return false, deleteErr
	}
	t.Cleanup(func() { vcDeleteExcluded = prevDelete })

	// The push must never be attempted, let alone fall back to unfiltered.
	pushAttempts := 0
	prevPush := vcPush
	vcPush = func(context.Context, versioncontrolops.DBConn, string, string, string) error {
		pushAttempts++
		return errors.New("push must not run after a delete failure")
	}
	t.Cleanup(func() { vcPush = prevPush })

	err := store.filteredPushToPeer(ctx, "peer", []string{"chore"})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("filteredPushToPeer error = %v, want the DELETE failure wrapped (fail-closed, no unfiltered fallback)", err)
	}
	if pushAttempts != 0 {
		t.Errorf("push attempted %d times after the DELETE failure, want 0", pushAttempts)
	}
	assertFilteredPushFailureAftermath(t, store, task, chore)
}

// TestEmbeddedFilteredPushPushFailurePreservesLocalData pins the AC-7
// push-failure half: the local compensation (keep branch + revert merge)
// already landed, the push fails with its own error, and the excluded local
// row survives for the next, successful sync.
func TestEmbeddedFilteredPushPushFailurePreservesLocalData(t *testing.T) {
	store, task, chore := newFilteredPushFailureEnv(t)
	ctx := t.Context()

	pushErr := errors.New("Error 1227: access denied to remote")
	prevPush := vcPush
	vcPush = func(context.Context, versioncontrolops.DBConn, string, string, string) error {
		return pushErr
	}
	t.Cleanup(func() { vcPush = prevPush })

	err := store.filteredPushToPeer(ctx, "peer", []string{"chore"})
	if !errors.Is(err, pushErr) {
		t.Fatalf("filteredPushToPeer error = %v, want the push failure wrapped", err)
	}

	// The compensation must have run before the push attempt: the revert of
	// the filtered commit is already on the local branch, so the next sync's
	// merge cannot fast-forward the peer's (never published) filtered tip
	// over the excluded rows.
	reverts, err := countLocalRevertCommits(ctx, store)
	if err != nil {
		t.Fatalf("count revert commits: %v", err)
	}
	if reverts == 0 {
		t.Error("no revert commit on the local branch, want the compensation recorded before publication")
	}
	assertFilteredPushFailureAftermath(t, store, task, chore)
}
