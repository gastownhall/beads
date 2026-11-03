//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// versionWispTablesForFilteredPush is the embedded twin of the server-mode
// fixture in dolt/federation_test.go: materialize the ignored schema, make
// the wisp plane versionable, and commit it. A staging branch created from
// the source tip carries only committed tables, so the tables the staged
// is_blocked recompute reads (wisps, wisp_dependencies) must be versioned for
// the fixture to exercise the recompute seam.
func versionWispTablesForFilteredPush(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	ctx := t.Context()
	err := store.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
		if _, err := schema.MigrateUp(ctx, db); err != nil {
			return fmt.Errorf("materialize ignored schema: %w", err)
		}
		if _, err := db.ExecContext(ctx,
			"DELETE FROM dolt_ignore WHERE pattern IN ('wisps', 'wisp_%')"); err != nil {
			return fmt.Errorf("make wisp tables versionable: %w", err)
		}
		if err := schema.DrainCall(ctx, db, "CALL DOLT_ADD('-A')"); err != nil {
			return fmt.Errorf("stage wisp tables: %w", err)
		}
		if err := schema.DrainCall(ctx, db,
			"CALL DOLT_COMMIT('-m', 'test: version empty wisp plane for filtered push')"); err != nil {
			return fmt.Errorf("commit wisp tables: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("version wisp tables: %v", err)
	}
}

// insertFilteredPushIssue seeds one issue row directly, the way the
// server-mode filtered-push tests do. federation.exclude_types filters custom
// issue types, so the seed uses one ("secret") outside the built-in set. All
// ids share the "ff" prefix so dependency edges stay same-prefix (a prefix
// mismatch routes the edge's target to the external column, see
// IsExternalDepTarget).
func insertFilteredPushIssue(t *testing.T, store *EmbeddedDoltStore, id, issueType string) {
	t.Helper()
	err := store.withConn(t.Context(), true, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
			VALUES (?, 'filtered push seed', '', '', '', '', 'open', 2, ?)
		`, id, issueType)
		return err
	})
	if err != nil {
		t.Fatalf("insert issue %s: %v", id, err)
	}
}

// storedIssueExists reports whether the id is present in the committed issues
// table the session's branch sees.
func storedIssueExists(t *testing.T, store *EmbeddedDoltStore, id string) bool {
	t.Helper()
	var n int
	err := store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM issues WHERE id = ?`, id).Scan(&n)
	})
	if err != nil {
		t.Fatalf("read issues for %s: %v", id, err)
	}
	return n > 0
}

// peerTip is the receiving peer's pushed state, read by cloning the remote.
type peerTip struct {
	issues       map[string]string // id -> issue_type
	dependencies [][2]string       // (issue_id, depends_on_issue_id)
}

// clonePeerTip clones the file:// remote into a fresh data dir — the only
// honest way to see what the peer actually received — and reads its issues
// and dependencies tables.
func clonePeerTip(t *testing.T, remoteURL, dbName string) peerTip {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "clone-data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create clone data dir: %v", err)
	}

	db, cleanup, err := OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		t.Fatalf("open clone engine: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, dbName); err != nil {
		t.Fatalf("clone peer tip: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("close clone engine: %v", err)
	}

	reader, readerCleanup, err := OpenSQL(ctx, dataDir, dbName, "main")
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	defer func() {
		if err := readerCleanup(); err != nil {
			t.Fatalf("close clone reader: %v", err)
		}
	}()

	tip := peerTip{issues: map[string]string{}}
	rows, err := reader.QueryContext(ctx, "SELECT id, issue_type FROM issues")
	if err != nil {
		t.Fatalf("read cloned issues: %v", err)
	}
	for rows.Next() {
		var id, issueType string
		if err := rows.Scan(&id, &issueType); err != nil {
			t.Fatalf("scan cloned issue: %v", err)
		}
		tip.issues[id] = issueType
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close cloned issues read: %v", err)
	}

	rows, err = reader.QueryContext(ctx, "SELECT issue_id, depends_on_issue_id FROM dependencies")
	if err != nil {
		t.Fatalf("read cloned dependencies: %v", err)
	}
	for rows.Next() {
		// Target NULLs exist in dependency rows the store writes for its own
		// bookkeeping; they are not issue-to-issue edges and assert nothing.
		var issueID string
		var dependsOnID sql.NullString
		if err := rows.Scan(&issueID, &dependsOnID); err != nil {
			t.Fatalf("scan cloned dependency: %v", err)
		}
		if dependsOnID.Valid {
			tip.dependencies = append(tip.dependencies, [2]string{issueID, dependsOnID.String})
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close cloned dependencies read: %v", err)
	}
	return tip
}

// federationStagingPrefix is the server-mode staging-branch prefix
// (dolt/federation.go); it is spelled out here so the assertion also compiles
// against commits that predate the embedded filtered push.
const federationStagingPrefix = "__federation_push_staging_"

// federationKeepPrefix is the keep-branch prefix (see versioncontrolops.
// retainExcludedRows), spelled out for the same reason as the staging one.
const federationKeepPrefix = "__federation_push_keep_"

// assertNoFederationStagingBranches fails when a filtered push left a staging
// or keep branch behind.
func assertNoFederationStagingBranches(t *testing.T, store *EmbeddedDoltStore) {
	t.Helper()
	var branches []string
	err := store.withDBConn(t.Context(), func(db versioncontrolops.DBConn) error {
		var err error
		branches, err = versioncontrolops.ListBranches(t.Context(), db)
		return err
	})
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	for _, b := range branches {
		for _, prefix := range []string{federationStagingPrefix, federationKeepPrefix} {
			if strings.HasPrefix(b, prefix) {
				t.Errorf("temporary branch %s was not cleaned up", b)
			}
		}
	}
}

// With federation.exclude_types configured, Sync publishes a filtered tip
// (GH#7890): a NEW local excluded-typed issue and its associated rows are
// absent from what the peer receives, while an excluded-typed issue the peer
// already holds stays in the tip — the publication filters what it sends, it
// must not destroy what the peer has. The filter is staged on a temporary
// branch, so the sender keeps its local excluded issue (AC: GH#7890).
func TestFederationSyncFiltersExcludedIssueTypesForPush(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)

	remoteDir := filepath.Join(t.TempDir(), "peer")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create peer remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	if err := store.AddRemote(ctx, "peer", remoteURL); err != nil {
		t.Fatalf("add peer remote: %v", err)
	}
	versionWispTablesForFilteredPush(t, store)

	// Phase 1: the peer's tip is established with an excluded-typed issue it
	// legitimately holds, before any exclusion was configured.
	insertFilteredPushIssue(t, store, "ff-held-secret", "secret")
	insertFilteredPushIssue(t, store, "ff-shared-task", "task")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "ff-shared-task", DependsOnID: "ff-held-secret", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("add held dependency: %v", err)
	}
	if _, err := store.commitAll(ctx, "seed peer-held issues", true); err != nil {
		t.Fatalf("commit peer-held issues: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("PushTo to establish peer tip: %v", err)
	}

	// Phase 2: a NEW local excluded-typed issue arrives, with an associated
	// dependency row.
	insertFilteredPushIssue(t, store, "ff-new-secret", "secret")
	insertFilteredPushIssue(t, store, "ff-local-task", "task")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "ff-local-task", DependsOnID: "ff-new-secret", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("add new dependency: %v", err)
	}
	if _, err := store.commitAll(ctx, "add new local issues", true); err != nil {
		t.Fatalf("commit new local issues: %v", err)
	}

	// Configure the exclusion the way cmd/bd reads it. config.Set is a no-op
	// until viper is initialized, so Initialize first — as every bd command
	// does before touching federation settings.
	prevExcludes := config.GetFederationConfig().ExcludeTypes
	if err := config.Initialize(); err != nil {
		t.Fatalf("initialize config: %v", err)
	}
	config.Set("federation.exclude_types", []string{"secret"})
	t.Cleanup(func() { config.Set("federation.exclude_types", prevExcludes) })

	// Phase 3: a full sync pushes the filtered publication.
	result, err := store.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("Sync error: %v", result.Error)
	}
	if result.PushError != nil {
		t.Fatalf("Sync push error: %v", result.PushError)
	}
	if !result.Pushed {
		t.Fatal("Sync did not report the filtered publication as pushed")
	}

	// The sender keeps its local excluded issue: the filter runs on the
	// staging branch, never on the source branch.
	if !storedIssueExists(t, store, "ff-new-secret") {
		t.Fatal("sender's local excluded issue was deleted by the filtered push")
	}
	assertNoFederationStagingBranches(t, store)

	// What the peer actually received: the new excluded issue and its
	// associated dependency are absent, the peer-held one and everything
	// non-excluded remain.
	tip := clonePeerTip(t, remoteURL, store.database)
	if _, ok := tip.issues["ff-new-secret"]; ok {
		t.Error("new local excluded issue reached the peer's tip")
	}
	if got, ok := tip.issues["ff-held-secret"]; !ok || got != "secret" {
		t.Errorf("peer-held excluded issue = %q present=%v, want it retained in the pushed tip", got, ok)
	}
	for _, id := range []string{"ff-shared-task", "ff-local-task"} {
		if _, ok := tip.issues[id]; !ok {
			t.Errorf("non-excluded issue %s missing from the pushed tip", id)
		}
	}
	for _, dep := range tip.dependencies {
		if dep[0] == "ff-local-task" && dep[1] == "ff-new-secret" {
			t.Error("dependency row associated with the new excluded issue reached the peer's tip")
		}
	}
	var heldDep bool
	for _, dep := range tip.dependencies {
		if dep[0] == "ff-shared-task" && dep[1] == "ff-held-secret" {
			heldDep = true
		}
	}
	if !heldDep {
		t.Error("dependency row associated with the peer-held excluded issue was dropped from the pushed tip")
	}
}

// After a successful filtered publication, the sender keeps its local excluded
// issue through later syncs: the filter commit is merged back into the source
// branch — inverted by a revert on the keep branch — BEFORE the staged tip is
// pushed, so the peer's own copy of the filter commit, which the next sync
// fetches and merges, deletes nothing locally. At publication time the filter
// commit is already an ancestor of the sender's source branch (AC: GH#7890).
func TestFilteredFederationSyncKeepsLocalExcludedIssues(t *testing.T) {
	ctx := context.Background()
	store := newPeerAuthTestStore(t)

	remoteDir := filepath.Join(t.TempDir(), "peer")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create peer remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir
	if err := store.AddRemote(ctx, "peer", remoteURL); err != nil {
		t.Fatalf("add peer remote: %v", err)
	}
	versionWispTablesForFilteredPush(t, store)

	insertFilteredPushIssue(t, store, "ff-held-secret", "secret")
	insertFilteredPushIssue(t, store, "ff-shared-task", "task")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "ff-shared-task", DependsOnID: "ff-held-secret", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("add held dependency: %v", err)
	}
	if _, err := store.commitAll(ctx, "seed peer-held issues", true); err != nil {
		t.Fatalf("commit peer-held issues: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("PushTo to establish peer tip: %v", err)
	}

	insertFilteredPushIssue(t, store, "ff-new-secret", "secret")
	insertFilteredPushIssue(t, store, "ff-local-task", "task")
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "ff-local-task", DependsOnID: "ff-new-secret", Type: types.DepBlocks,
	}, "test"); err != nil {
		t.Fatalf("add new dependency: %v", err)
	}
	if _, err := store.commitAll(ctx, "add new local issues", true); err != nil {
		t.Fatalf("commit new local issues: %v", err)
	}

	prevExcludes := config.GetFederationConfig().ExcludeTypes
	if err := config.Initialize(); err != nil {
		t.Fatalf("initialize config: %v", err)
	}
	config.Set("federation.exclude_types", []string{"secret"})
	t.Cleanup(func() { config.Set("federation.exclude_types", prevExcludes) })

	// First filtered sync: publishes the staged tip.
	result, err := store.Sync(ctx, "peer", "")
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if result.PushError != nil {
		t.Fatalf("first Sync push error: %v", result.PushError)
	}

	// At publication time the filter commit was already an ancestor of the
	// sender's source branch — dolt_log on the source branch reaches it.
	var filterCommits int
	err = store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM dolt_log WHERE message = ?`,
			"federation: exclude private issue types").Scan(&filterCommits)
	})
	if err != nil {
		t.Fatalf("read source branch log: %v", err)
	}
	if filterCommits == 0 {
		t.Fatal("the filter commit is not an ancestor of the sender's source branch")
	}

	// A later sync fetches the peer's copy of the filter commit and merges
	// it; the sender must retain the local excluded issue and its
	// associated dependency row.
	if _, err := store.Sync(ctx, "peer", ""); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if !storedIssueExists(t, store, "ff-new-secret") {
		t.Fatal("later sync deleted the sender's local excluded issue")
	}
	var depCount int
	err = store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?`,
			"ff-local-task", "ff-new-secret").Scan(&depCount)
	})
	if err != nil {
		t.Fatalf("read sender dependency: %v", err)
	}
	if depCount != 1 {
		t.Fatalf("sender's dependency row for the excluded issue count = %d, want 1", depCount)
	}

	// The peer's tip — read through the fetch-updated tracking ref — still
	// carries only the peer-held excluded issue.
	var peerHash string
	err = store.withConn(t.Context(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(),
			`SELECT hash FROM dolt_remote_branches WHERE name = ?`, "remotes/peer/main").Scan(&peerHash)
	})
	if err != nil {
		t.Fatalf("read peer tracking ref: %v", err)
	}
	if err := issueops.ValidateRef(peerHash); err != nil {
		t.Fatalf("peer tracking hash %q: %v", peerHash, err)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"ff-new-secret", 0},
		{"ff-held-secret", 1},
	} {
		var n int
		//nolint:gosec // G201: peerHash is read from dolt_remote_branches and validated above; AS OF requires a literal
		err = store.withConn(t.Context(), false, func(tx *sql.Tx) error {
			return tx.QueryRowContext(t.Context(), fmt.Sprintf(
				`SELECT COUNT(*) FROM issues AS OF '%s' WHERE id = ?`, peerHash), tc.id).Scan(&n)
		})
		if err != nil {
			t.Fatalf("read peer tip for %s: %v", tc.id, err)
		}
		if n != tc.want {
			t.Errorf("peer tip count for %s = %d, want %d", tc.id, n, tc.want)
		}
	}

	assertNoFederationStagingBranches(t, store)
}
