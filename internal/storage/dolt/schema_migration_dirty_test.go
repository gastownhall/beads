package dolt

import (
	"strings"
	"testing"

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

	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
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

	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
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

	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
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

	// Leave an UNCOMMITTED user write on custom_statuses: the pass's backfill
	// (re-inserting 'review' from config) then changes a pre-existing dirty
	// table mid-pass, which the changed-signature guard must reject.
	// (Historically this test dirtied dolt_ignore and relied on the pass's
	// pattern re-seed to flip it; dolt_ignore is now pass-owned state exempt
	// from the guard, so the canary is a real user table.)
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO custom_statuses (name, category) VALUES ('scratch', 'wip')",
	); err != nil {
		t.Fatalf("dirty custom_statuses: %v", err)
	}

	_, err := initSchemaOnDB(ctx, store.db)
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
