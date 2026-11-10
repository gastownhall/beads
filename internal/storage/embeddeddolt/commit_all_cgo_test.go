//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCommitAllOutOfBandChangeAndHonestNoOp covers the embedded half of the
// explicit-commit fix. Embedded Commit already stages everything via
// DOLT_COMMIT('-Am'), so the staging gap is server-mode-only; what CommitAll
// adds here is the honest report: (true, nil) exactly when a commit was
// created, (false, nil) when there was nothing to commit — so `bd vc commit`
// can print "Nothing to commit" instead of "Created commit <existing HEAD>".
func TestCommitAllOutOfBandChangeAndHonestNoOp(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "cat")

	// Flush anything setup left uncommitted, then pin the honest no-op.
	if _, err := te.store.CommitAll(ctx, "test: flush baseline"); err != nil {
		t.Fatalf("baseline CommitAll: %v", err)
	}
	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	committed, err := te.store.CommitAll(ctx, "test: nothing to commit")
	if err != nil {
		t.Fatalf("CommitAll on clean working set: %v", err)
	}
	if committed {
		t.Fatalf("CommitAll reported a commit on a clean working set")
	}
	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("CommitAll reported committed=false but HEAD advanced: %s -> %s", headBefore, headAfter)
	}

	// Out-of-band write: a config row inserted through a raw SQL connection,
	// bypassing the store's transaction tracking entirely.
	te.exec(t, ctx, "INSERT INTO config (`key`, value) VALUES ('test.oob-commit-all', 'v1')")

	committed, err = te.store.CommitAll(ctx, "test: sweep out-of-band config")
	if err != nil {
		t.Fatalf("CommitAll with out-of-band config change: %v", err)
	}
	if !committed {
		t.Fatalf("CommitAll reported nothing to commit while config was dirty")
	}
	var dirty int
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'config'", nil, &dirty)
	if dirty != 0 {
		t.Fatalf("CommitAll left config dirty (%d dolt_status rows)", dirty)
	}
	headSwept, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headSwept == headAfter {
		t.Fatalf("CommitAll reported committed=true but HEAD did not advance from %s", headAfter)
	}
}

// seedIgnoredIssue creates one issue, commits it, then registers an operator
// dolt_ignore pattern for the tracked issues table and commits the pattern,
// so the next issues write lands in the working set but DOLT_COMMIT('-Am')
// cannot stage it (GH#5433). It returns the issue id.
func seedIgnoredIssue(t *testing.T, ctx context.Context, te *testEnv) string {
	t.Helper()
	issue := &types.Issue{Title: "before", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := te.store.CommitAll(ctx, "test: commit issue"); err != nil {
		t.Fatalf("CommitAll after CreateIssue: %v", err)
	}
	te.exec(t, ctx, "INSERT INTO dolt_ignore VALUES ('issues', true)")
	if _, err := te.store.CommitAll(ctx, "test: commit ignore pattern"); err != nil {
		t.Fatalf("CommitAll after dolt_ignore insert: %v", err)
	}
	te.exec(t, ctx, "UPDATE issues SET title = 'after' WHERE id = ?", issue.ID)
	var dirty int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'", nil, &dirty)
	if dirty == 0 {
		t.Fatal("fixture: modified ignored issues table is not listed in dolt_status")
	}
	return issue.ID
}

// assertIssuesLeftInWorkingSet checks the residual issues change is still
// saved in the working set and absent from HEAD.
func assertIssuesLeftInWorkingSet(t *testing.T, ctx context.Context, te *testEnv, id string) {
	t.Helper()
	var working, head string
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{id}, &working)
	te.queryScalar(t, ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", []any{id}, &head)
	if working != "after" || head != "before" {
		t.Fatalf("issue title working=%q HEAD=%q, want working=after HEAD=before", working, head)
	}
}

func requireUncommittedChanges(t *testing.T, err error, table string, committed bool) *storage.UncommittedChangesError {
	t.Helper()
	var residual *storage.UncommittedChangesError
	if !errors.As(err, &residual) {
		t.Fatalf("CommitAll error = %v, want *storage.UncommittedChangesError", err)
	}
	if !slices.Contains(residual.Tables, table) {
		t.Fatalf("UncommittedChangesError.Tables = %v, want %q", residual.Tables, table)
	}
	if residual.Committed != committed {
		t.Fatalf("UncommittedChangesError.Committed = %v, want %v", residual.Committed, committed)
	}
	if errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("CommitAll error = %v, want a verification finding, not ErrCommitIndeterminate", err)
	}
	return residual
}

// TestCommitAllReportsTrackedTableSkippedByIgnorePattern pins GH#5433's
// false-clean report: a tracked table covered by an ignore pattern stays dirty
// after DOLT_COMMIT('-Am') answers "nothing to commit", and CommitAll must
// say so instead of returning (false, nil).
func TestCommitAllReportsTrackedTableSkippedByIgnorePattern(t *testing.T) {
	ctx := t.Context()
	te := newTestEnv(t, "ig")
	id := seedIgnoredIssue(t, ctx, te)
	headBefore, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}

	committed, err := te.store.CommitAll(ctx, "test: commit ignored issues change")
	if committed {
		t.Fatal("CommitAll committed = true, want false: nothing was committable")
	}
	residual := requireUncommittedChanges(t, err, "issues", false)
	if !slices.Contains(residual.IgnorePatterns, "issues") {
		t.Errorf("UncommittedChangesError.IgnorePatterns = %v, want the operator pattern %q", residual.IgnorePatterns, "issues")
	}
	if !strings.Contains(err.Error(), "tracked changes remain in issues") {
		t.Errorf("CommitAll error = %q, want it to name issues", err)
	}

	headAfter, err := te.store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("HEAD moved %s -> %s on a refused commit", headBefore, headAfter)
	}
	assertIssuesLeftInWorkingSet(t, ctx, te, id)
}
