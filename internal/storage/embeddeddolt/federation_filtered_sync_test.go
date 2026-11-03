//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// configureFederationExcludeTypes sets federation.exclude_types for one test,
// ignoring the repository's tracked config so only these values apply.
func configureFederationExcludeTypes(t *testing.T, excludeTypes ...string) {
	t.Helper()
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	config.Set("federation.exclude_types", excludeTypes)
}

// countAt counts rows of query evaluated against ref with AS OF.
func countAt(t *testing.T, store *EmbeddedDoltStore, ref, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), fmt.Sprintf(query, "AS OF '"+ref+"'"), args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count at %s: %v", ref, err)
	}
	return n
}

// filteredSyncFixture is a store whose peer already holds an excluded-type
// issue, plus a new local excluded issue with a label, comment and dependency
// and a leaked ephemeral row in the committed issues table.
type filteredSyncFixture struct {
	store                           *EmbeddedDoltStore
	peer                            string
	task, peerChore, newChore, wisp string
}

func newFilteredSyncFixture(t *testing.T) *filteredSyncFixture {
	t.Helper()
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	f := &filteredSyncFixture{
		store:     store,
		peer:      "peer",
		task:      "fedauth-task",
		peerChore: "fedauth-peerchore",
		newChore:  "fedauth-newchore",
		wisp:      "fedauth-leak",
	}
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: f.peer, RemoteURL: "file://" + filepath.Join(t.TempDir(), "peer"),
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}

	// The peer already holds an excluded-type issue.
	mustCreate(t, store, &types.Issue{ID: f.peerChore, Title: "held by peer", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
	if err := store.PushTo(ctx, f.peer); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	mustCreate(t, store, &types.Issue{ID: f.task, Title: "shared task", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2})
	mustCreate(t, store, &types.Issue{ID: f.newChore, Title: "local chore", IssueType: types.TypeChore, Status: types.StatusOpen, Priority: 2})
	if err := store.AddLabel(ctx, f.newChore, "local-label", "test"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, f.newChore, "test", "local comment"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: f.task, DependsOnID: f.newChore, Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	// An ephemeral row that leaked into the committed issues table.
	if err := store.withConn(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO issues
			(id, title, description, design, acceptance_criteria, notes, issue_type, status, priority, ephemeral)
			VALUES (?, 'leaked wisp', '', '', '', '', 'task', 'open', 2, 1)`, f.wisp)
		return err
	}); err != nil {
		t.Fatalf("insert ephemeral row: %v", err)
	}
	if _, err := store.CommitPending(ctx, "test"); err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	return f
}

func mustCreate(t *testing.T, store *EmbeddedDoltStore, issue *types.Issue) {
	t.Helper()
	if err := store.CreateIssue(t.Context(), issue, "test"); err != nil {
		t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
	}
}

// peerTip refreshes and returns the remote-tracking ref of the peer's branch.
func (f *filteredSyncFixture) peerTip(t *testing.T) string {
	t.Helper()
	if err := f.store.Fetch(t.Context(), f.peer); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return "remotes/" + f.peer + "/main"
}

func (f *filteredSyncFixture) issueCount(t *testing.T, ref, id string) int {
	return countAt(t, f.store, ref, "SELECT COUNT(*) FROM issues %s WHERE id = ?", id)
}

func (f *filteredSyncFixture) childRows(t *testing.T, ref, id string) int {
	return countAt(t, f.store, ref, "SELECT COUNT(*) FROM labels %s WHERE issue_id = ?", id) +
		countAt(t, f.store, ref, "SELECT COUNT(*) FROM comments %s WHERE issue_id = ?", id) +
		countAt(t, f.store, ref, "SELECT COUNT(*) FROM dependencies %s WHERE issue_id = ? OR depends_on_issue_id = ?", id, id)
}

// A filtered sync withholds the new local excluded issue and its rows from the
// peer while leaving the excluded issue the peer already holds in place.
func TestFilteredFederationSyncExcludesTypes(t *testing.T) {
	ctx := context.Background()
	f := newFilteredSyncFixture(t)
	configureFederationExcludeTypes(t, "chore", "wisp")

	res, err := f.store.Sync(ctx, f.peer, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.PushError != nil || !res.Pushed {
		t.Fatalf("Sync pushed=%v push error=%v, want a publication", res.Pushed, res.PushError)
	}

	tip := f.peerTip(t)
	if f.issueCount(t, tip, f.task) != 1 {
		t.Fatal("peer tip lacks the shared task; the publication did not land")
	}
	if n := f.issueCount(t, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds the new excluded issue (%d rows)", n)
	}
	if n := f.childRows(t, tip, f.newChore); n != 0 {
		t.Errorf("peer tip holds %d label/comment/dependency rows of the new excluded issue", n)
	}
	if n := f.issueCount(t, tip, f.wisp); n != 0 {
		t.Errorf("peer tip holds the leaked ephemeral row (%d rows)", n)
	}
	if n := f.issueCount(t, tip, f.peerChore); n != 1 {
		t.Errorf("peer tip lost the excluded issue it already held (%d rows)", n)
	}

	if f.issueCount(t, "main", f.newChore) != 1 || f.childRows(t, "main", f.newChore) != 3 || f.issueCount(t, "main", f.wisp) != 1 {
		t.Error("filtered sync removed local excluded data")
	}
}
