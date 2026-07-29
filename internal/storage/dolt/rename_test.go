package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueIDUpdatesWispDependencyTargets verifies that renaming a
// regular issue also updates wisp_dependencies rows that target it. Wisp
// auxiliary table source rows always belong to wisps and are covered by
// TestUpdateIssueIDRenamesWisp.
func TestUpdateIssueIDUpdatesWispDependencyTargets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a permanent issue that we'll rename
	issue := &types.Issue{
		ID:        "test-old1",
		Title:     "Test issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Create a wisp that references the issue via wisp_dependencies
	wisp := &types.Issue{
		ID:        "test-wisp-abc",
		Title:     "Test wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("failed to create wisp: %v", err)
	}

	// Add wisp dependency: wisp depends on the permanent issue
	dep := &types.Dependency{
		IssueID:     wisp.ID,
		DependsOnID: "test-old1",
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add wisp dependency: %v", err)
	}

	// Now rename the issue
	newID := "test-new1"
	if err := store.UpdateIssueID(ctx, "test-old1", newID, issue, "test"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify wisp_dependencies typed target columns were updated
	var depCount int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`, newID).Scan(&depCount)
	if err != nil {
		t.Fatalf("failed to query wisp_dependencies target: %v", err)
	}
	if depCount != 1 {
		t.Errorf("expected 1 wisp_dependencies row targeting %q, got %d", newID, depCount)
	}

	// Verify wisp_dependencies.issue_id still points at the source wisp.
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?`, wisp.ID).Scan(&depCount)
	if err != nil {
		t.Fatalf("failed to query wisp_dependencies issue_id: %v", err)
	}
	if depCount != 1 {
		t.Errorf("expected 1 wisp_dependencies row with issue_id=%q, got %d", wisp.ID, depCount)
	}

	// Verify old ID is gone from wisp_dependencies
	var oldCount int
	err = store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? OR COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`,
		"test-old1", "test-old1").Scan(&oldCount)
	if err != nil {
		t.Fatalf("failed to query old wisp_dependencies: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("expected 0 wisp_dependencies rows with old ID, got %d", oldCount)
	}
}

func TestUpdateIssueIDUpdatesPersistentDependencyTargets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	source := &types.Issue{
		ID:        "test-source1",
		Title:     "Source issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	target := &types.Issue{
		ID:        "test-target-old1",
		Title:     "Target issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	for _, issue := range []*types.Issue{source, target} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %q: %v", issue.ID, err)
		}
	}

	dep := &types.Dependency{
		IssueID:     source.ID,
		DependsOnID: target.ID,
		Type:        types.DepRelated,
		Metadata:    `{"reason":"rename-regression"}`,
		ThreadID:    "thread-rename-1",
	}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	before := readDependencyTargetRow(t, ctx, store, source.ID, target.ID)
	newID := "test-target-new1"
	if err := store.UpdateIssueID(ctx, target.ID, newID, target, "test"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	after := readDependencyTargetRow(t, ctx, store, source.ID, newID)
	if after.dependsOnIssueID != newID {
		t.Fatalf("depends_on_issue_id = %q, want %q", after.dependsOnIssueID, newID)
	}
	if after.dependsOnID != newID {
		t.Fatalf("depends_on_id = %q, want %q", after.dependsOnID, newID)
	}
	if after.depType != string(dep.Type) {
		t.Errorf("type = %q, want %q", after.depType, dep.Type)
	}
	if after.metadata != dep.Metadata {
		t.Errorf("metadata = %q, want %q", after.metadata, dep.Metadata)
	}
	if after.threadID != dep.ThreadID {
		t.Errorf("thread_id = %q, want %q", after.threadID, dep.ThreadID)
	}
	if after.createdBy != "test" {
		t.Errorf("created_by = %q, want test", after.createdBy)
	}
	if after.createdAt != before.createdAt {
		t.Errorf("created_at changed from %q to %q", before.createdAt, after.createdAt)
	}

	var oldCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`,
		source.ID, target.ID).Scan(&oldCount); err != nil {
		t.Fatalf("failed to count old dependency target: %v", err)
	}
	if oldCount != 0 {
		t.Fatalf("expected old dependency target to be gone, got %d row(s)", oldCount)
	}

	var rowCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ?`,
		source.ID).Scan(&rowCount); err != nil {
		t.Fatalf("failed to count dependency rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected dependency row count to stay 1, got %d", rowCount)
	}
}

func TestUpdateIssueIDUpdatesWispTargetDependencyRows(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	persistentSource := &types.Issue{ID: "test-wisp-target-source", Title: "Persistent source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	wispSource := &types.Issue{ID: "test-wisp-target-wisp-source", Title: "Wisp source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	wispTarget := &types.Issue{ID: "test-wisp-target-old", Title: "Wisp target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{persistentSource, wispSource, wispTarget} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %q: %v", issue.ID, err)
		}
	}

	deps := []*types.Dependency{
		{
			IssueID:     persistentSource.ID,
			DependsOnID: wispTarget.ID,
			Type:        types.DepRelated,
			Metadata:    `{"source":"persistent"}`,
			ThreadID:    "thread-wisp-target-persistent",
		},
		{
			IssueID:     wispSource.ID,
			DependsOnID: wispTarget.ID,
			Type:        types.DepBlocks,
			Metadata:    `{"source":"wisp"}`,
			ThreadID:    "thread-wisp-target-wisp",
		},
	}
	for _, dep := range deps {
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("failed to add dependency %q -> %q: %v", dep.IssueID, dep.DependsOnID, err)
		}
	}

	newID := "test-wisp-target-new"
	wispTarget.ID = newID
	if err := store.UpdateIssueID(ctx, "test-wisp-target-old", newID, wispTarget, "test"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	persistentRow := readWispTargetDependencyRow(t, ctx, store, "dependencies", persistentSource.ID, newID)
	if persistentRow.dependsOnWispID != newID {
		t.Fatalf("dependencies.depends_on_wisp_id = %q, want %q", persistentRow.dependsOnWispID, newID)
	}
	if persistentRow.metadata != deps[0].Metadata {
		t.Errorf("dependencies.metadata = %q, want %q", persistentRow.metadata, deps[0].Metadata)
	}
	if persistentRow.threadID != deps[0].ThreadID {
		t.Errorf("dependencies.thread_id = %q, want %q", persistentRow.threadID, deps[0].ThreadID)
	}

	wispRow := readWispTargetDependencyRow(t, ctx, store, "wisp_dependencies", wispSource.ID, newID)
	if wispRow.dependsOnWispID != newID {
		t.Fatalf("wisp_dependencies.depends_on_wisp_id = %q, want %q", wispRow.dependsOnWispID, newID)
	}
	if wispRow.metadata != deps[1].Metadata {
		t.Errorf("wisp_dependencies.metadata = %q, want %q", wispRow.metadata, deps[1].Metadata)
	}
	if wispRow.threadID != deps[1].ThreadID {
		t.Errorf("wisp_dependencies.thread_id = %q, want %q", wispRow.threadID, deps[1].ThreadID)
	}

	if oldCount := countOldWispTargetRows(t, ctx, store, "dependencies", "test-wisp-target-old"); oldCount != 0 {
		t.Fatalf("expected old target rows in dependencies to be gone, got %d", oldCount)
	}
	if oldCount := countOldWispTargetRows(t, ctx, store, "wisp_dependencies", "test-wisp-target-old"); oldCount != 0 {
		t.Fatalf("expected old target rows in wisp_dependencies to be gone, got %d", oldCount)
	}
}

func TestUpdateIssueIDDependencyTargetsIgnoreNonIssueTargets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	source := &types.Issue{ID: "test-ignore-source", Title: "Source issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	wispTarget := &types.Issue{ID: "test-ignore-wisp", Title: "Wisp target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	newIssueTarget := &types.Issue{ID: "test-ignore-new", Title: "New issue target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{source, wispTarget, newIssueTarget} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %q: %v", issue.ID, err)
		}
	}

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata)
		VALUES (UUID(), ?, ?, ?, NOW(), ?, ?)
	`, source.ID, wispTarget.ID, types.DepRelated, "test", "{}"); err != nil {
		t.Fatalf("failed to seed wisp-target dependency: %v", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := issueops.UpdateIssueIDInDependencyTargetsInTx(ctx, tx, wispTarget.ID, newIssueTarget.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("UpdateIssueIDInDependencyTargetsInTx failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	var dependsOnIssueID sql.NullString
	var dependsOnWispID string
	if err := store.db.QueryRowContext(ctx, `
		SELECT depends_on_issue_id, depends_on_wisp_id
		FROM dependencies
		WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?
	`, source.ID, wispTarget.ID).Scan(&dependsOnIssueID, &dependsOnWispID); err != nil {
		t.Fatalf("failed to read dependency target columns: %v", err)
	}
	if dependsOnIssueID.Valid {
		t.Fatalf("depends_on_issue_id = %q, want NULL", dependsOnIssueID.String)
	}
	if dependsOnWispID != wispTarget.ID {
		t.Fatalf("depends_on_wisp_id = %q, want %q", dependsOnWispID, wispTarget.ID)
	}
}

func TestUpdateIssueIDDependencyTargetCollisionFails(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	source := &types.Issue{ID: "test-collision-source", Title: "Source issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	target := &types.Issue{ID: "test-collision-old", Title: "Target issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{source, target} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %q: %v", issue.ID, err)
		}
	}

	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     source.ID,
		DependsOnID: target.ID,
		Type:        types.DepRelated,
		Metadata:    `{"target":"issue"}`,
	}, "test"); err != nil {
		t.Fatalf("failed to add issue-target dependency: %v", err)
	}

	newID := "other-collision-new"
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     source.ID,
		DependsOnID: newID,
		Type:        types.DepRelated,
		Metadata:    `{"target":"external"}`,
	}, "test"); err != nil {
		t.Fatalf("failed to add external dependency: %v", err)
	}

	if err := store.UpdateIssueID(ctx, target.ID, newID, target, "test"); err == nil {
		t.Fatal("UpdateIssueID succeeded despite a colliding dependency target")
	}

	var issueCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, target.ID).Scan(&issueCount); err != nil {
		t.Fatalf("failed to count old issue ID after failed rename: %v", err)
	}
	if issueCount != 1 {
		t.Fatalf("expected failed rename to keep old issue ID, got %d row(s)", issueCount)
	}

	var depCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dependencies WHERE issue_id = ?`, source.ID).Scan(&depCount); err != nil {
		t.Fatalf("failed to count dependencies after failed rename: %v", err)
	}
	if depCount != 2 {
		t.Fatalf("expected failed rename to preserve both dependency rows, got %d", depCount)
	}

	var oldTargetCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?`,
		source.ID, target.ID).Scan(&oldTargetCount); err != nil {
		t.Fatalf("failed to count old issue-target dependency: %v", err)
	}
	if oldTargetCount != 1 {
		t.Fatalf("expected old issue-target dependency to remain, got %d", oldTargetCount)
	}

	var externalTargetCount int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_external = ?`,
		source.ID, newID).Scan(&externalTargetCount); err != nil {
		t.Fatalf("failed to count external dependency: %v", err)
	}
	if externalTargetCount != 1 {
		t.Fatalf("expected external dependency to remain, got %d", externalTargetCount)
	}
}

type dependencyTargetRow struct {
	dependsOnIssueID string
	dependsOnID      string
	depType          string
	metadata         string
	threadID         string
	createdBy        string
	createdAt        string
}

func readDependencyTargetRow(t *testing.T, ctx context.Context, store *DoltStore, issueID, targetID string) dependencyTargetRow {
	t.Helper()

	var row dependencyTargetRow
	if err := store.db.QueryRowContext(ctx, `
		SELECT depends_on_issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id, type, metadata, thread_id, created_by, CAST(created_at AS CHAR)
		FROM dependencies
		WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?
	`, issueID, targetID).Scan(
		&row.dependsOnIssueID,
		&row.dependsOnID,
		&row.depType,
		&row.metadata,
		&row.threadID,
		&row.createdBy,
		&row.createdAt,
	); err != nil {
		t.Fatalf("failed to read dependency %s -> %s: %v", issueID, targetID, err)
	}
	return row
}

type wispTargetDependencyRow struct {
	dependsOnIssueID sql.NullString
	dependsOnWispID  string
	dependsOnID      string
	metadata         string
	threadID         string
}

func readWispTargetDependencyRow(t *testing.T, ctx context.Context, store *DoltStore, table, issueID, targetID string) wispTargetDependencyRow {
	t.Helper()

	var query string
	switch table {
	case "dependencies":
		query = `
		SELECT depends_on_issue_id, depends_on_wisp_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id, metadata, thread_id
		FROM dependencies
		WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?
	`
	case "wisp_dependencies":
		query = `
			SELECT depends_on_issue_id, depends_on_wisp_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id, metadata, thread_id
			FROM wisp_dependencies
			WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?
		`
	default:
		t.Fatalf("unknown dependency table %q", table)
	}

	var row wispTargetDependencyRow
	if err := store.db.QueryRowContext(ctx, query, issueID, targetID).Scan(
		&row.dependsOnIssueID,
		&row.dependsOnWispID,
		&row.dependsOnID,
		&row.metadata,
		&row.threadID,
	); err != nil {
		t.Fatalf("failed to read dependency %s -> %s from %s: %v", issueID, targetID, table, err)
	}
	if row.dependsOnIssueID.Valid {
		t.Fatalf("%s.depends_on_issue_id = %q, want NULL", table, row.dependsOnIssueID.String)
	}
	if row.dependsOnID != targetID {
		t.Fatalf("%s.depends_on_id = %q, want %q", table, row.dependsOnID, targetID)
	}
	return row
}

func countOldWispTargetRows(t *testing.T, ctx context.Context, store *DoltStore, table, targetID string) int {
	t.Helper()

	var query string
	switch table {
	case "dependencies":
		query = `SELECT COUNT(*) FROM dependencies WHERE COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`
	case "wisp_dependencies":
		query = `SELECT COUNT(*) FROM wisp_dependencies WHERE COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?`
	default:
		t.Fatalf("unknown dependency table %q", table)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, query, targetID).Scan(&count); err != nil {
		t.Fatalf("failed to count old target rows in %s: %v", table, err)
	}
	return count
}

// TestUpdateIssueIDRenamesWisp verifies that UpdateIssueID correctly renames
// wisps in the wisps table and all wisp_* auxiliary tables when the target
// is itself a wisp (not a regular issue). (bd-8ykk)
func TestUpdateIssueIDRenamesWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a wisp (goes to wisps + wisp_* tables)
	wisp := &types.Issue{
		Title:     "Wisp to rename",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue (wisp) failed: %v", err)
	}
	oldID := wisp.ID
	if oldID == "" {
		t.Fatal("wisp got empty ID")
	}

	// Verify wisp is in the wisps table
	if !store.isActiveWisp(ctx, oldID) {
		t.Fatalf("expected %q to be an active wisp", oldID)
	}

	// Verify creation event was recorded in wisp_events
	var eventCount int
	err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, oldID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 wisp_events row for old ID, got %d", eventCount)
	}

	// Add a label to the wisp
	if err := store.AddLabel(ctx, oldID, "test-label", "tester"); err != nil {
		t.Fatalf("failed to add wisp label: %v", err)
	}

	// Rename the wisp
	newID := "test-renamed-wisp"
	wisp.ID = newID
	if err := store.UpdateIssueID(ctx, oldID, newID, wisp, "tester"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify the old ID no longer exists in wisps table
	if store.isActiveWisp(ctx, oldID) {
		t.Fatal("old wisp ID should not exist after rename")
	}

	// Verify the new ID exists in wisps table
	if !store.isActiveWisp(ctx, newID) {
		t.Fatal("new wisp ID should exist in wisps table after rename")
	}

	// Verify wisp_events were updated (1 creation + 1 label_added + 1 rename = 3)
	var newEventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, newID).Scan(&newEventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events for new ID: %v", err)
	}
	if newEventCount != 3 {
		t.Fatalf("expected 3 wisp_events rows for new ID (creation + label_added + rename), got %d", newEventCount)
	}

	// Verify no events left under old ID
	var oldEventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?`, oldID).Scan(&oldEventCount)
	if err != nil {
		t.Fatalf("failed to count wisp_events for old ID: %v", err)
	}
	if oldEventCount != 0 {
		t.Fatalf("expected 0 wisp_events rows for old ID, got %d", oldEventCount)
	}

	// Verify wisp_labels were updated
	var labelCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, newID).Scan(&labelCount)
	if err != nil {
		t.Fatalf("failed to count wisp_labels for new ID: %v", err)
	}
	if labelCount != 1 {
		t.Fatalf("expected 1 wisp_labels row for new ID, got %d", labelCount)
	}

	// Verify no labels left under old ID
	var oldLabelCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?`, oldID).Scan(&oldLabelCount)
	if err != nil {
		t.Fatalf("failed to count wisp_labels for old ID: %v", err)
	}
	if oldLabelCount != 0 {
		t.Fatalf("expected 0 wisp_labels rows for old ID, got %d", oldLabelCount)
	}
}

// TestUpdateIssueIDStillWorksForRegularIssues verifies that the refactored
// UpdateIssueID still correctly handles regular (non-wisp) issues.
func TestUpdateIssueIDStillWorksForRegularIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Create a regular issue
	issue := &types.Issue{
		ID:        "test-regular-1",
		Title:     "Regular issue to rename",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Verify it's not a wisp
	if store.isActiveWisp(ctx, issue.ID) {
		t.Fatal("regular issue should not be an active wisp")
	}

	// Rename it
	newID := "test-regular-renamed"
	issue.ID = newID
	if err := store.UpdateIssueID(ctx, "test-regular-1", newID, issue, "tester"); err != nil {
		t.Fatalf("UpdateIssueID failed: %v", err)
	}

	// Verify the old ID is gone and new ID exists
	got, err := store.GetIssue(ctx, newID)
	if err != nil {
		t.Fatalf("GetIssue failed for renamed issue: %v", err)
	}
	if got.Title != "Regular issue to rename" {
		t.Fatalf("expected original title, got %q", got.Title)
	}

	// Verify rename event in events table
	var eventCount int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'renamed'`, newID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to count rename events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 rename event for new ID, got %d", eventCount)
	}
}
