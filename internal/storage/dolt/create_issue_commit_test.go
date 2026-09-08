package dolt

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestCreateIssueCommitsInitialRelationalData(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createdAt := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	issue := &types.Issue{
		ID:          "create-relational-data",
		Title:       "Create with relational data",
		Description: "labels and comments should live in the create commit",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeTask,
		Labels:      []string{"gc:wisp", "status:pending"},
		Comments: []*types.Comment{
			{
				Author:    "tester",
				Text:      "seed comment",
				CreatedAt: createdAt,
			},
		},
	}

	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	var labelCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?",
		issue.ID,
	).Scan(&labelCount); err != nil {
		t.Fatalf("count committed labels: %v", err)
	}
	if labelCount != 2 {
		t.Fatalf("committed label count = %d, want 2", labelCount)
	}

	var commentCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ?",
		issue.ID,
	).Scan(&commentCount); err != nil {
		t.Fatalf("count committed comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("committed comment count = %d, want 1", commentCount)
	}

	var labelEventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ?",
		issue.ID, types.EventLabelAdded,
	).Scan(&labelEventCount); err != nil {
		t.Fatalf("count committed label events: %v", err)
	}
	if labelEventCount != 2 {
		t.Fatalf("committed label_added event count = %d, want 2", labelEventCount)
	}

	var dirtyRelationalTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments', 'events')
	`).Scan(&dirtyRelationalTables); err != nil {
		t.Fatalf("count dirty relational tables: %v", err)
	}
	if dirtyRelationalTables != 0 {
		t.Fatalf("dirty relational table count = %d, want 0", dirtyRelationalTables)
	}
}

func TestCreateIssueWithoutInitialRelationalDataDoesNotCommitDirtySideTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	dirtyOwner := &types.Issue{
		ID:        "dirty-owner",
		Title:     "Dirty side table owner",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, dirtyOwner, "tester"); err != nil {
		t.Fatalf("CreateIssue dirty owner: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		dirtyOwner.ID, "uncommitted-label",
	); err != nil {
		t.Fatalf("dirty label insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, ?, ?, ?)",
		dirtyOwner.ID, "tester", "uncommitted comment", time.Now().UTC(),
	); err != nil {
		t.Fatalf("dirty comment insert: %v", err)
	}

	plain := &types.Issue{
		ID:        "plain-create",
		Title:     "Plain create",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, plain, "tester"); err != nil {
		t.Fatalf("CreateIssue plain: %v", err)
	}

	var committedLabels int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedLabels); err != nil {
		t.Fatalf("count committed dirty labels: %v", err)
	}
	if committedLabels != 0 {
		t.Fatalf("committed dirty labels = %d, want 0", committedLabels)
	}

	var committedComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}
	if committedComments != 0 {
		t.Fatalf("committed dirty comments = %d, want 0", committedComments)
	}

	var dirtySideTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments')
	`).Scan(&dirtySideTables); err != nil {
		t.Fatalf("count dirty side tables: %v", err)
	}
	if dirtySideTables != 2 {
		t.Fatalf("dirty side table count = %d, want 2", dirtySideTables)
	}
}

func TestCreateIssueCommitsChildCounterForHierarchicalID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        "parent-child-counter",
		Title:     "Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}

	childID, err := store.GetNextChildID(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetNextChildID: %v", err)
	}
	child := &types.Issue{
		ID:        childID,
		Title:     "Child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	createCtx := storage.WithReservedChildCounter(ctx, parent.ID, childID)
	if err := store.CreateIssue(createCtx, child, "tester"); err != nil {
		t.Fatalf("CreateIssue child: %v", err)
	}

	var dirtyChildCounters int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name = 'child_counters'
	`).Scan(&dirtyChildCounters); err != nil {
		t.Fatalf("count dirty child counters: %v", err)
	}
	if dirtyChildCounters != 0 {
		t.Fatalf("dirty child_counters count = %d, want 0", dirtyChildCounters)
	}
}

func TestCreateIssueWithExplicitHierarchicalIDDoesNotCommitDirtyChildCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        "explicit-child-parent",
		Title:     "Explicit child parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)",
		parent.ID, 7,
	); err != nil {
		t.Fatalf("dirty child counter insert: %v", err)
	}

	explicitChild := &types.Issue{
		ID:        parent.ID + ".7",
		Title:     "Explicit child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, explicitChild, "tester"); err != nil {
		t.Fatalf("CreateIssue explicit child: %v", err)
	}

	var committedDirtyCounters int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?",
		parent.ID,
	).Scan(&committedDirtyCounters); err != nil {
		t.Fatalf("count committed dirty child counters: %v", err)
	}
	if committedDirtyCounters != 0 {
		t.Fatalf("committed dirty child counters = %d, want 0", committedDirtyCounters)
	}

	var dirtyChildCounters int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name = 'child_counters'
	`).Scan(&dirtyChildCounters); err != nil {
		t.Fatalf("count dirty child counters: %v", err)
	}
	if dirtyChildCounters != 1 {
		t.Fatalf("dirty child_counters count = %d, want 1", dirtyChildCounters)
	}
}

func TestCreateIssuesWithoutInitialRelationalDataDoesNotCommitDirtySideTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	dirtyOwner := &types.Issue{
		ID:        "batch-dirty-owner",
		Title:     "Batch dirty side table owner",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, dirtyOwner, "tester"); err != nil {
		t.Fatalf("CreateIssue dirty owner: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		dirtyOwner.ID, "batch-uncommitted-label",
	); err != nil {
		t.Fatalf("dirty label insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, ?, ?, ?)",
		dirtyOwner.ID, "tester", "batch uncommitted comment", time.Now().UTC(),
	); err != nil {
		t.Fatalf("dirty comment insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)",
		dirtyOwner.ID, 42,
	); err != nil {
		t.Fatalf("dirty child counter insert: %v", err)
	}

	plain := []*types.Issue{{
		ID:        "test-batch-plain-create",
		Title:     "Batch plain create",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}}
	if err := store.CreateIssues(ctx, plain, "tester"); err != nil {
		t.Fatalf("CreateIssues plain: %v", err)
	}

	var committedLabels int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedLabels); err != nil {
		t.Fatalf("count committed dirty labels: %v", err)
	}
	if committedLabels != 0 {
		t.Fatalf("committed dirty labels = %d, want 0", committedLabels)
	}

	var committedComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}
	if committedComments != 0 {
		t.Fatalf("committed dirty comments = %d, want 0", committedComments)
	}

	var committedChildCounters int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?",
		dirtyOwner.ID,
	).Scan(&committedChildCounters); err != nil {
		t.Fatalf("count committed dirty child counters: %v", err)
	}
	if committedChildCounters != 0 {
		t.Fatalf("committed dirty child counters = %d, want 0", committedChildCounters)
	}

	var dirtySideTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments', 'child_counters')
	`).Scan(&dirtySideTables); err != nil {
		t.Fatalf("count dirty side tables: %v", err)
	}
	if dirtySideTables != 3 {
		t.Fatalf("dirty side table count = %d, want 3", dirtySideTables)
	}
}

func TestCreateIssuesWithExplicitHierarchicalIDDoesNotCommitDirtyChildCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        "test-batch-explicit-child-parent",
		Title:     "Batch explicit child parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("CreateIssue parent: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)",
		parent.ID, 7,
	); err != nil {
		t.Fatalf("dirty child counter insert: %v", err)
	}

	explicitChildren := []*types.Issue{{
		ID:        parent.ID + ".7",
		Title:     "Batch explicit child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}}
	if err := store.CreateIssues(ctx, explicitChildren, "tester"); err != nil {
		t.Fatalf("CreateIssues explicit child: %v", err)
	}

	var committedDirtyCounters int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?",
		parent.ID,
	).Scan(&committedDirtyCounters); err != nil {
		t.Fatalf("count committed dirty child counters: %v", err)
	}
	if committedDirtyCounters != 0 {
		t.Fatalf("committed dirty child counters = %d, want 0", committedDirtyCounters)
	}

	var dirtyChildCounters int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name = 'child_counters'
	`).Scan(&dirtyChildCounters); err != nil {
		t.Fatalf("count dirty child counters: %v", err)
	}
	if dirtyChildCounters != 1 {
		t.Fatalf("dirty child_counters count = %d, want 1", dirtyChildCounters)
	}
}

func TestCreateIssuesHierarchicalIDsCommitChildCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	parent := &types.Issue{
		ID:        "test-batch-child-counter-parent",
		Title:     "Batch child counter parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	children := []*types.Issue{
		parent,
		{
			ID:        parent.ID + ".1",
			Title:     "Batch child one",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
		{
			ID:        parent.ID + ".2",
			Title:     "Batch child two",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
	}
	if err := store.CreateIssues(ctx, children, "tester"); err != nil {
		t.Fatalf("CreateIssues hierarchical children: %v", err)
	}

	var lastChild int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?",
		parent.ID,
	).Scan(&lastChild); err != nil {
		t.Fatalf("read committed child counter: %v", err)
	}
	if lastChild != 2 {
		t.Fatalf("committed last_child = %d, want 2", lastChild)
	}

	var dirtyChildCounters int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name = 'child_counters'
	`).Scan(&dirtyChildCounters); err != nil {
		t.Fatalf("count dirty child counters: %v", err)
	}
	if dirtyChildCounters != 0 {
		t.Fatalf("dirty child_counters count = %d, want 0", dirtyChildCounters)
	}
}

func TestCreateIssuesAllWispBatchPersistsDependenciesAndCounters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	source := &types.Issue{
		ID:        "test-wisp-batch-source",
		Title:     "Wisp source",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
		Dependencies: []*types.Dependency{{
			DependsOnID: "test-wisp-batch-target",
			Type:        types.DepBlocks,
		}},
	}
	target := &types.Issue{
		ID:        "test-wisp-batch-target",
		Title:     "Wisp target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	child := &types.Issue{
		ID:        source.ID + ".3",
		Title:     "Wisp child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}

	if err := store.CreateIssues(ctx, []*types.Issue{source, target, child}, "tester"); err != nil {
		t.Fatalf("CreateIssues all-wisp batch: %v", err)
	}

	records, err := store.GetDependencyRecords(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("wisp dependency count = %d, want 1", len(records))
	}
	if records[0].DependsOnID != target.ID {
		t.Fatalf("wisp dependency target = %q, want %q", records[0].DependsOnID, target.ID)
	}

	var lastChild int
	if err := store.db.QueryRowContext(ctx,
		"SELECT last_child FROM wisp_child_counters WHERE parent_id = ?",
		source.ID,
	).Scan(&lastChild); err != nil {
		t.Fatalf("read wisp child counter: %v", err)
	}
	if lastChild != 3 {
		t.Fatalf("wisp last_child = %d, want 3", lastChild)
	}
}

func TestCreateIssuesDuplicateSideTableInputsDoNotCommitDirtySideTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	createdAt := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)
	source := &types.Issue{
		ID:        "test-dedupe-source",
		Title:     "Dedupe source",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"existing-label"},
		Comments: []*types.Comment{{
			Author:    "tester",
			Text:      "existing comment",
			CreatedAt: createdAt,
		}},
		Dependencies: []*types.Dependency{{
			DependsOnID: "test-dedupe-target",
			Type:        types.DepBlocks,
		}},
	}
	target := &types.Issue{
		ID:        "test-dedupe-target",
		Title:     "Dedupe target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	dirtyOwner := &types.Issue{
		ID:        "test-dedupe-dirty-owner",
		Title:     "Dirty owner",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	dirtyTarget := &types.Issue{
		ID:        "test-dedupe-dirty-target",
		Title:     "Dirty target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssues(ctx, []*types.Issue{source, target, dirtyOwner, dirtyTarget}, "tester"); err != nil {
		t.Fatalf("CreateIssues seed: %v", err)
	}

	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)",
		dirtyOwner.ID, "uncommitted-label",
	); err != nil {
		t.Fatalf("dirty label insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, ?, ?, ?)",
		dirtyOwner.ID, "tester", "uncommitted comment", createdAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("dirty comment insert: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, ?, ?)",
		dirtyOwner.ID, dirtyTarget.ID, types.DepBlocks, createdAt.Add(2*time.Minute), "tester",
	); err != nil {
		t.Fatalf("dirty dependency insert: %v", err)
	}

	duplicateSource := &types.Issue{
		ID:        source.ID,
		Title:     source.Title,
		Status:    source.Status,
		Priority:  source.Priority,
		IssueType: source.IssueType,
		Labels:    []string{"existing-label"},
		Comments: []*types.Comment{{
			Author:    "tester",
			Text:      "existing comment",
			CreatedAt: createdAt,
		}},
		Dependencies: []*types.Dependency{{
			DependsOnID: target.ID,
			Type:        types.DepBlocks,
		}},
	}
	if err := store.CreateIssues(ctx, []*types.Issue{duplicateSource}, "tester"); err != nil {
		t.Fatalf("CreateIssues duplicate side tables: %v", err)
	}

	var committedDirtyLabels int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ? AND label = ?",
		dirtyOwner.ID, "uncommitted-label",
	).Scan(&committedDirtyLabels); err != nil {
		t.Fatalf("count committed dirty labels: %v", err)
	}
	if committedDirtyLabels != 0 {
		t.Fatalf("committed dirty labels = %d, want 0", committedDirtyLabels)
	}

	var committedDirtyComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ? AND text = ?",
		dirtyOwner.ID, "uncommitted comment",
	).Scan(&committedDirtyComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}
	if committedDirtyComments != 0 {
		t.Fatalf("committed dirty comments = %d, want 0", committedDirtyComments)
	}

	var committedDirtyDependencies int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?",
		dirtyOwner.ID, dirtyTarget.ID,
	).Scan(&committedDirtyDependencies); err != nil {
		t.Fatalf("count committed dirty dependencies: %v", err)
	}
	if committedDirtyDependencies != 0 {
		t.Fatalf("committed dirty dependencies = %d, want 0", committedDirtyDependencies)
	}

	var dirtySideTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments', 'dependencies')
	`).Scan(&dirtySideTables); err != nil {
		t.Fatalf("count dirty side tables: %v", err)
	}
	if dirtySideTables != 3 {
		t.Fatalf("dirty side table count = %d, want 3", dirtySideTables)
	}
}
