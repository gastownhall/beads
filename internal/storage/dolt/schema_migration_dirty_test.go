package dolt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

func TestSchemaMigrationDoesNotCommitPreExistingDirtyData(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "schema-dirty-label",
		Title:     "schema migration dirty label",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if err := store.SetConfig(ctx, "status.custom", "review:wip"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CommitWithConfig(ctx, "test: configure custom status"); err != nil {
		t.Fatalf("CommitWithConfig: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		t.Fatalf("clear custom_statuses: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"custom_statuses"}, "test: simulate missing custom status backfill"); err != nil {
		t.Fatalf("commit cleared custom_statuses: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		issue.ID, "dirty-before-schema",
	); err != nil {
		t.Fatalf("insert dirty label: %v", err)
	}

	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("initSchemaOnDB: %v", err)
	}

	var committedLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		issue.ID, "dirty-before-schema",
	).Scan(&committedLabelCount); err != nil {
		t.Fatalf("count committed dirty label: %v", err)
	}
	if committedLabelCount != 0 {
		t.Fatalf("dirty label was committed by schema migration, count = %d", committedLabelCount)
	}

	var workingLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?",
		issue.ID, "dirty-before-schema",
	).Scan(&workingLabelCount); err != nil {
		t.Fatalf("count working dirty label: %v", err)
	}
	if workingLabelCount != 1 {
		t.Fatalf("working label count = %d, want 1", workingLabelCount)
	}

	var dirtyLabelTables int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'labels'",
	).Scan(&dirtyLabelTables); err != nil {
		t.Fatalf("count dirty label tables: %v", err)
	}
	if dirtyLabelTables != 1 {
		t.Fatalf("dirty labels table count = %d, want 1", dirtyLabelTables)
	}

	var committedCustomStatuses int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM custom_statuses AS OF 'HEAD' WHERE name = ?",
		"review",
	).Scan(&committedCustomStatuses); err != nil {
		t.Fatalf("count committed custom statuses: %v", err)
	}
	if committedCustomStatuses != 1 {
		t.Fatalf("committed custom status count = %d, want 1", committedCustomStatuses)
	}
}

func TestSchemaMigrationDoesNotCommitPreExistingStagedData(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "schema-staged-label",
		Title:     "schema migration staged label",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if err := store.SetConfig(ctx, "status.custom", "review:wip"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CommitWithConfig(ctx, "test: configure custom status"); err != nil {
		t.Fatalf("CommitWithConfig: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		t.Fatalf("clear custom_statuses: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"custom_statuses"}, "test: simulate missing custom status backfill"); err != nil {
		t.Fatalf("commit cleared custom_statuses: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		issue.ID, "staged-before-schema",
	); err != nil {
		t.Fatalf("insert staged label: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD(?)", "labels"); err != nil {
		t.Fatalf("stage label: %v", err)
	}

	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("initSchemaOnDB: %v", err)
	}

	var committedLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		issue.ID, "staged-before-schema",
	).Scan(&committedLabelCount); err != nil {
		t.Fatalf("count committed staged label: %v", err)
	}
	if committedLabelCount != 0 {
		t.Fatalf("staged label was committed by schema migration, count = %d", committedLabelCount)
	}

	var workingLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?",
		issue.ID, "staged-before-schema",
	).Scan(&workingLabelCount); err != nil {
		t.Fatalf("count working staged label: %v", err)
	}
	if workingLabelCount != 1 {
		t.Fatalf("working staged label count = %d, want 1", workingLabelCount)
	}

	var stagedLabelTables int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE table_name = 'labels' AND staged = true",
	).Scan(&stagedLabelTables); err != nil {
		t.Fatalf("count staged label tables: %v", err)
	}
	if stagedLabelTables != 0 {
		t.Fatalf("labels remained staged after schema migration, count = %d", stagedLabelTables)
	}
}

func TestSchemaMigrationDoesNotCommitIgnoredDirtyWispTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "schema-dirty-with-wisp-label",
		Title:     "schema migration dirty label with ignored wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	if err := store.SetConfig(ctx, "status.custom", "review:wip"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CommitWithConfig(ctx, "test: configure custom status"); err != nil {
		t.Fatalf("CommitWithConfig: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		t.Fatalf("clear custom_statuses: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"custom_statuses"}, "test: simulate missing custom status backfill"); err != nil {
		t.Fatalf("commit cleared custom_statuses: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		issue.ID, "dirty-before-schema-with-wisp",
	); err != nil {
		t.Fatalf("insert dirty label: %v", err)
	}

	wisp := &types.Issue{
		ID:        "schema-dirty-ignored-wisp",
		Title:     "ignored wisp must stay local",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		NoHistory: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue no-history wisp: %v", err)
	}

	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("initSchemaOnDB: %v", err)
	}

	var committedLabelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		issue.ID, "dirty-before-schema-with-wisp",
	).Scan(&committedLabelCount); err != nil {
		t.Fatalf("count committed dirty label: %v", err)
	}
	if committedLabelCount != 0 {
		t.Fatalf("dirty label was committed by schema migration, count = %d", committedLabelCount)
	}

	var committedWispCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = ?",
		wisp.ID,
	).Scan(&committedWispCount); err != nil {
		t.Fatalf("count committed ignored wisp: %v", err)
	}
	if committedWispCount != 0 {
		t.Fatalf("ignored wisp was committed by schema migration, count = %d", committedWispCount)
	}

	var workingWispCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisps WHERE id = ?",
		wisp.ID,
	).Scan(&workingWispCount); err != nil {
		t.Fatalf("count working ignored wisp: %v", err)
	}
	if workingWispCount != 1 {
		t.Fatalf("working wisp count = %d, want 1", workingWispCount)
	}

	var committedCustomStatuses int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM custom_statuses AS OF 'HEAD' WHERE name = ?",
		"review",
	).Scan(&committedCustomStatuses); err != nil {
		t.Fatalf("count committed custom statuses: %v", err)
	}
	if committedCustomStatuses != 1 {
		t.Fatalf("committed custom status count = %d, want 1", committedCustomStatuses)
	}
}

func TestSchemaMigrationRejectsChangedPreExistingDirtyTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "status.custom", "review:wip"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.CommitWithConfig(ctx, "test: configure custom status"); err != nil {
		t.Fatalf("CommitWithConfig: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		t.Fatalf("clear custom_statuses: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"custom_statuses"}, "test: simulate missing custom status backfill"); err != nil {
		t.Fatalf("commit cleared custom_statuses: %v", err)
	}

	// Seed and commit a scratch row so the table has pre-existing committed
	// content, then dirty it with an UNCOMMITTED delete: the pass's backfill
	// (re-inserting 'review' from config) then changes a pre-existing dirty
	// table mid-pass, which the changed-signature guard must reject.
	// (Historically this test dirtied dolt_ignore and relied on the pass's
	// pattern re-seed to flip it; dolt_ignore is now pass-owned state exempt
	// from the guard, so the canary is a real user table.)
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO custom_statuses (name, category) VALUES ('scratch', 'wip')",
	); err != nil {
		t.Fatalf("seed scratch custom_statuses: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"custom_statuses"}, "test: seed scratch custom status"); err != nil {
		t.Fatalf("commit scratch custom_statuses: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM custom_statuses"); err != nil {
		t.Fatalf("dirty custom_statuses: %v", err)
	}

	_, err := rerunSchemaInitOnTestStore(ctx, store)
	if err == nil || !strings.Contains(err.Error(), "pre-existing dirty tables changed") {
		t.Fatalf("initSchemaOnDB error = %v, want changed dirty table error", err)
	}

	var committedCustomStatuses int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM custom_statuses AS OF 'HEAD' WHERE name = ?",
		"review",
	).Scan(&committedCustomStatuses); err != nil {
		t.Fatalf("count committed custom statuses: %v", err)
	}
	if committedCustomStatuses != 0 {
		t.Fatalf("custom status was committed after dirty-table drift, count = %d", committedCustomStatuses)
	}
}

const (
	schemaTrackedWispID          = "schema-tracked-wisp"
	schemaTrackedWispTitle       = "live wisp title"
	schemaTrackedWispDescription = "live wisp description"
	schemaTrackedCommentID       = "schema-tracked-wisp-comment"
	schemaTrackedCommentText     = "live wisp comment payload"
)

// setupTrackedIgnoredDirtyStore is the server-mode twin of the embedded
// gastownhall/beads#5816 fixture: wisps and wisp_comments are dolt-ignored
// but tracked at HEAD, hold live uncommitted rows, and the ignored cursor sits
// below ignored migration 0024, which touches wisp_comments. downgradeText
// makes 0024 perform real DDL on replay; stage leaves the named tables staged.
func setupTrackedIgnoredDirtyStore(t *testing.T, downgradeText bool, stage ...string) (*DoltStore, func()) {
	t.Helper()
	store, cleanup := setupTestStore(t)

	ctx, cancel := testContext(t)
	defer cancel()

	var queries []string
	if downgradeText {
		queries = append(queries, "ALTER TABLE wisp_comments MODIFY COLUMN text TEXT NOT NULL")
	}
	queries = append(queries,
		"CALL DOLT_ADD('-f', 'wisps')",
		"CALL DOLT_ADD('-f', 'wisp_comments')",
		"CALL DOLT_COMMIT('-m', 'test: track wisp tables')",
		"INSERT INTO wisps (id, title, description) VALUES ('"+schemaTrackedWispID+"', '"+schemaTrackedWispTitle+"', '"+schemaTrackedWispDescription+"')",
		"INSERT INTO wisp_comments (id, issue_id, author, text) VALUES ('"+schemaTrackedCommentID+"', '"+schemaTrackedWispID+"', 'agent', '"+schemaTrackedCommentText+"')",
	)
	for _, table := range stage {
		queries = append(queries, "CALL DOLT_ADD('-f', '"+table+"')")
	}
	queries = append(queries, "DELETE FROM ignored_schema_migrations WHERE version >= 24")
	for _, q := range queries {
		if _, err := store.db.ExecContext(ctx, q); err != nil {
			// The shared test database may already track the empty wisp
			// tables at HEAD; the AS OF 'HEAD' reads below still prove it.
			if strings.HasPrefix(q, "CALL DOLT_COMMIT") && strings.Contains(err.Error(), "nothing to commit") {
				continue
			}
			cleanup()
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	for _, table := range []string{"wisps", "wisp_comments"} {
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = ?", table,
		).Scan(&dirty); err != nil {
			cleanup()
			t.Fatalf("dolt_status %s: %v", table, err)
		}
		if dirty == 0 {
			cleanup()
			t.Fatalf("fixture: %s is not dirty in dolt_status", table)
		}
	}
	assertSchemaTrackedWispRowsAbsentAtHead(t, ctx, store)
	return store, cleanup
}

func assertSchemaTrackedWispRowsAbsentAtHead(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	for _, q := range []string{
		"SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = '" + schemaTrackedWispID + "'",
		"SELECT COUNT(*) FROM wisp_comments AS OF 'HEAD' WHERE id = '" + schemaTrackedCommentID + "'",
	} {
		var n int
		if err := store.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d, want 0 (live ignored rows must not be committed)", q, n)
		}
	}
}

// assertSchemaNoStagedTrackedWispTables fails when a wisp table delta is
// still staged, where the next commit of any kind would publish it.
func assertSchemaNoStagedTrackedWispTables(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	var staged int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE staged = true AND table_name IN ('wisps', 'wisp_comments')",
	).Scan(&staged); err != nil {
		t.Fatalf("count staged wisp tables: %v", err)
	}
	if staged != 0 {
		t.Fatalf("%d wisp table delta(s) still staged after migration, want 0", staged)
	}
}

func schemaWispCommentsTextType(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var columnType string
	if err := store.db.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments' AND COLUMN_NAME = 'text'`,
	).Scan(&columnType); err != nil {
		t.Fatalf("read wisp_comments.text type: %v", err)
	}
	return columnType
}

func schemaIgnoredCursorVersion(t *testing.T, ctx context.Context, store *DoltStore) int {
	t.Helper()
	var version int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	return version
}

// TestSchemaIgnoredTrackedDirtyMigrationOpens is the server-mode twin of
// TestEmbeddedIgnoredTrackedDirtyMigrationOpens (gastownhall/beads#5816 AC-1):
// the schema pass every writable open runs must apply pending ignored
// migrations over live rows in tracked, dolt-ignored wisp tables.
func TestSchemaIgnoredTrackedDirtyMigrationOpens(t *testing.T) {
	for _, tc := range []struct {
		name          string
		downgradeText bool
	}{
		{name: "already widened"},
		{name: "0024 performs real DDL", downgradeText: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTrackedIgnoredDirtyStore(t, tc.downgradeText)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
				t.Fatalf("schema init = %v, want the pending ignored migrations to apply over live wisp rows", err)
			}
			if got, want := schemaIgnoredCursorVersion(t, ctx, store), schema.LatestIgnoredVersion(); got != want {
				t.Fatalf("ignored cursor = %d, want %d", got, want)
			}
			if columnType := schemaWispCommentsTextType(t, ctx, store); columnType != "longtext" {
				t.Fatalf("wisp_comments.text = %q, want longtext after 0024", columnType)
			}
		})
	}
}

func schemaTrackedWispRowSnapshot(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var wisp, comment string
	if err := store.db.QueryRowContext(ctx,
		"SELECT CONCAT_WS('|', id, title, description, CAST(created_at AS CHAR)) FROM wisps WHERE id = ?", schemaTrackedWispID,
	).Scan(&wisp); err != nil {
		t.Fatalf("read wisp %s: %v", schemaTrackedWispID, err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT CONCAT_WS('|', id, issue_id, author, text, CAST(created_at AS CHAR)) FROM wisp_comments WHERE id = ?", schemaTrackedCommentID,
	).Scan(&comment); err != nil {
		t.Fatalf("read wisp comment %s: %v", schemaTrackedCommentID, err)
	}
	return wisp + "\n" + comment
}

// TestSchemaIgnoredTrackedDirtyMigrationPreservesRows is the server-mode twin
// of TestEmbeddedIgnoredTrackedDirtyMigrationPreservesRows
// (gastownhall/beads#5816 AC-2).
func TestSchemaIgnoredTrackedDirtyMigrationPreservesRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage []string
	}{
		{name: "unstaged"},
		{name: "pre-staged wisps", stage: []string{"wisps"}},
		{name: "pre-staged wisp_comments", stage: []string{"wisp_comments"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTrackedIgnoredDirtyStore(t, true, tc.stage...)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			before := schemaTrackedWispRowSnapshot(t, ctx, store)
			if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
				t.Fatalf("schema init: %v", err)
			}
			if after := schemaTrackedWispRowSnapshot(t, ctx, store); after != before {
				t.Fatalf("wisp rows after migration =\n%s\nwant unchanged\n%s", after, before)
			}
			if columnType := schemaWispCommentsTextType(t, ctx, store); columnType != "longtext" {
				t.Fatalf("wisp_comments.text = %q, want longtext after 0024", columnType)
			}
			assertSchemaTrackedWispRowsAbsentAtHead(t, ctx, store)
			assertSchemaNoStagedTrackedWispTables(t, ctx, store)
		})
	}
}

const (
	schemaCommittableParentID     = "schema-committable-parent"
	schemaCommittableWispID       = "schema-committable-unrelated-wisp"
	schemaCommittableDirtyIssueID = "schema-committable-dirty-issue"
)

// setupCommittableIgnoredDirtyStore is the server-mode twin of the embedded
// committable fixture: a durable parent issue is committed, child_counters
// holds an uncommitted row for it, an unrelated wisp is dirty, and the ignored
// cursor sits at 1 so pending ignored migration 0002 touches child_counters.
// rewindMain also leaves the latest main migration (which does not touch
// child_counters) pending.
func setupCommittableIgnoredDirtyStore(t *testing.T, rewindMain bool) (*DoltStore, func()) {
	t.Helper()
	store, cleanup := setupTestStore(t)

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        schemaCommittableParentID,
		Title:     "durable parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		cleanup()
		t.Fatalf("CreateIssue(parent): %v", err)
	}
	if _, err := store.CommitAll(ctx, "test: durable parent"); err != nil {
		cleanup()
		t.Fatalf("CommitAll: %v", err)
	}
	queries := []string{
		"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + schemaCommittableParentID + "', 7) " +
			"ON DUPLICATE KEY UPDATE last_child = 7",
		"INSERT INTO wisps (id, title) VALUES ('" + schemaCommittableWispID + "', 'unrelated wisp')",
		"DELETE FROM ignored_schema_migrations WHERE version > 1",
	}
	if rewindMain {
		queries = append(queries, fmt.Sprintf("DELETE FROM schema_migrations WHERE version >= %d", schema.LatestVersion()))
	}
	for _, q := range queries {
		if _, err := store.db.ExecContext(ctx, q); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	return store, cleanup
}

func schemaCommittableIgnoredDirtySnapshot(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		"SELECT CONCAT_WS(':', version, COALESCE(content_hash, '')) FROM schema_migrations ORDER BY version",
		"SELECT CONCAT_WS(':', version, COALESCE(content_hash, '')) FROM ignored_schema_migrations ORDER BY version",
		"SELECT HASHOF('HEAD')",
		"SELECT CONCAT_WS('|', parent_id, last_child) FROM child_counters WHERE parent_id = '" + schemaCommittableParentID + "'",
		"SELECT CONCAT_WS('|', id, title) FROM wisps WHERE id = '" + schemaCommittableWispID + "'",
		"SELECT CONCAT_WS('|', id, title) FROM issues WHERE id = '" + schemaCommittableDirtyIssueID + "'",
	} {
		rows, err := store.db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				t.Fatalf("%s: %v", q, err)
			}
			b.WriteString(s)
			b.WriteByte('\n')
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		b.WriteString("--\n")
	}
	return b.String()
}

// TestSchemaIgnoredCommittableDirtyRefusesBeforeMigrating is the server-mode
// twin of TestEmbeddedIgnoredCommittableDirtyRefusesBeforeMigrating
// (gastownhall/beads#5816 AC-3).
func TestSchemaIgnoredCommittableDirtyRefusesBeforeMigrating(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rewindMain bool
	}{
		{name: "ignored series pending"},
		{name: "main series pending too", rewindMain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupCommittableIgnoredDirtyStore(t, tc.rewindMain)
			defer cleanup()

			ctx, cancel := testContext(t)
			defer cancel()

			before := schemaCommittableIgnoredDirtySnapshot(t, ctx, store)
			_, err := rerunSchemaInitOnTestStore(ctx, store)
			if err == nil {
				t.Fatal("schema init = nil, want *schema.DirtyTablesError for dirty child_counters")
			}
			var dirtyErr *schema.DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("schema init error = %T (%v), want error wrapping *schema.DirtyTablesError", err, err)
			}
			if !slices.Equal(dirtyErr.Tables, []string{"child_counters"}) {
				t.Fatalf("DirtyTablesError.Tables = %v, want [child_counters]", dirtyErr.Tables)
			}
			if after := schemaCommittableIgnoredDirtySnapshot(t, ctx, store); after != before {
				t.Fatalf("state after refusal =\n%s\nwant unchanged\n%s", after, before)
			}
		})
	}
}

// TestSchemaIgnoredCommittableRefusalWithPendingMainMigrations locks the
// relocation (gastownhall/beads#5816 AC-3): the ignored-series committable
// check runs beside the main one, ahead of every numbered migration, and the
// two intersections merge into one sorted, de-duplicated list. Main 0054+
// name only issues (0053, the last child_counters migration, stays recorded)
// while ignored 0002 names child_counters, so the refusal carries both names
// and neither numbered series advances.
func TestSchemaIgnoredCommittableRefusalWithPendingMainMigrations(t *testing.T) {
	store, cleanup := setupCommittableIgnoredDirtyStore(t, false)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Raw SQL, not CreateIssue: the store auto-commits its own writes, so
	// only a raw insert leaves issues dirty for the pending main series.
	for _, q := range []string{
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes) VALUES ('" +
			schemaCommittableDirtyIssueID + "', 'uncommitted issue', '', '', '', '')",
		"DELETE FROM schema_migrations WHERE version > 53",
	} {
		if _, err := store.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}

	before := schemaCommittableIgnoredDirtySnapshot(t, ctx, store)
	_, err := rerunSchemaInitOnTestStore(ctx, store)
	if err == nil {
		t.Fatal("schema init = nil, want *schema.DirtyTablesError with both migration series pending")
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("schema init error = %T (%v), want error wrapping *schema.DirtyTablesError", err, err)
	}
	if want := []string{"child_counters", "issues"}; !slices.Equal(dirtyErr.Tables, want) {
		t.Fatalf("DirtyTablesError.Tables = %v, want the sorted, de-duplicated merge %v", dirtyErr.Tables, want)
	}
	if after := schemaCommittableIgnoredDirtySnapshot(t, ctx, store); after != before {
		t.Fatalf("state after refusal =\n%s\nwant unchanged\n%s", after, before)
	}
}
