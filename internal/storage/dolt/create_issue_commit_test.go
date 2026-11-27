package dolt

import (
	"context"
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

	// events is dolt_ignored since migration 0062 (bd-red8u): the audit rows
	// must be durable in the working set but never part of committed history.
	var labelEventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		issue.ID, types.EventLabelAdded,
	).Scan(&labelEventCount); err != nil {
		t.Fatalf("count label events: %v", err)
	}
	if labelEventCount != 2 {
		t.Fatalf("label_added event count = %d, want 2", labelEventCount)
	}
	var committedEventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events AS OF 'HEAD'",
	).Scan(&committedEventCount); err == nil && committedEventCount != 0 {
		t.Fatalf("events has %d rows at HEAD; want none in committed history (dolt_ignored, 0062)", committedEventCount)
	}

	// events is excluded: it is permanently working-set-resident since 0062,
	// and on the shared branch-per-test database (events materialized at HEAD)
	// its audit rows legitimately show in dolt_status as a modification.
	var dirtyRelationalTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments')
	`).Scan(&dirtyRelationalTables); err != nil {
		t.Fatalf("count dirty relational tables: %v", err)
	}
	if dirtyRelationalTables != 0 {
		t.Fatalf("dirty relational table count = %d, want 0", dirtyRelationalTables)
	}
}

// pendingMetadataKey writes a metadata row outside any of the store's
// writes: a pending change in a table outside the issue closure, which no
// issue write may commit.
func pendingMetadataKey(ctx context.Context, t *testing.T, store *DoltStore, key string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO metadata (`key`, value) VALUES (?, 'pending')", key); err != nil {
		t.Fatalf("pending metadata insert: %v", err)
	}
}

// requireMetadataKeyUncommitted fails unless key is still only in the working
// set: an issue write stages its own tables and the issue closure
// (withIssueClosure), never a table outside both.
func requireMetadataKeyUncommitted(ctx context.Context, t *testing.T, store *DoltStore, key string) {
	t.Helper()
	var committed int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM metadata AS OF 'HEAD' WHERE `key` = ?", key).Scan(&committed); err != nil {
		t.Fatalf("count committed metadata: %v", err)
	}
	if committed != 0 {
		t.Fatalf("metadata %q is at HEAD: the write committed a pending row outside its tables and the issue closure", key)
	}
	if !tableDirty(ctx, t, store, "metadata") {
		t.Fatalf("metadata is clean: the write committed the pending %q row", key)
	}
}

// TestCreateIssueCarriesPendingIssueClosureRows: a create without labels or
// comments still stages the whole issue closure (withIssueClosure), so the
// pending labels and comments rows another write left in the working set
// ride its commit whole rather than staying behind while issues is
// published; a pending row outside the closure stays where it is. Until the
// closure, the create staged only the tables it wrote (#4149) and left both.
func TestCreateIssueCarriesPendingIssueClosureRows(t *testing.T) {
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

	pendingMetadataKey(ctx, t, store, "test.create-closure")

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
	if committedLabels != 1 {
		t.Fatalf("committed pending labels = %d, want 1: the create's commit stages the issue closure", committedLabels)
	}

	var committedComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}
	if committedComments != 1 {
		t.Fatalf("committed pending comments = %d, want 1: the create's commit stages the issue closure", committedComments)
	}

	requireCleanTables(ctx, t, store, "issues", "labels", "comments")
	requireMetadataKeyUncommitted(ctx, t, store, "test.create-closure")
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

// TestCreateIssueWithExplicitHierarchicalIDCarriesThePendingChildCounter: an
// explicit hierarchical id does not bump the parent's counter, but
// child_counters has a foreign key to issues, so the create's commit stages
// it with the rest of the issue closure (withIssueClosure) and the pending
// counter row rides along. Until the closure, the create left it pending
// (#4149).
func TestCreateIssueWithExplicitHierarchicalIDCarriesThePendingChildCounter(t *testing.T) {
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
	if committedDirtyCounters != 1 {
		t.Fatalf("committed pending child counters = %d, want 1: the create's commit stages the issue closure", committedDirtyCounters)
	}
	requireCleanTables(ctx, t, store, "child_counters")
}

// TestCreateIssuesCarriesPendingIssueClosureRows is
// TestCreateIssueCarriesPendingIssueClosureRows for a batch create, with a
// pending child counter as well.
func TestCreateIssuesCarriesPendingIssueClosureRows(t *testing.T) {
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

	pendingMetadataKey(ctx, t, store, "test.batch-create-closure")

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
	if committedLabels != 1 {
		t.Fatalf("committed pending labels = %d, want 1: the create's commit stages the issue closure", committedLabels)
	}

	var committedComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ?",
		dirtyOwner.ID,
	).Scan(&committedComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}
	if committedComments != 1 {
		t.Fatalf("committed pending comments = %d, want 1: the create's commit stages the issue closure", committedComments)
	}

	var committedChildCounters int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?",
		dirtyOwner.ID,
	).Scan(&committedChildCounters); err != nil {
		t.Fatalf("count committed dirty child counters: %v", err)
	}
	if committedChildCounters != 1 {
		t.Fatalf("committed pending child counters = %d, want 1: the create's commit stages the issue closure", committedChildCounters)
	}

	requireCleanTables(ctx, t, store, "issues", "labels", "comments", "child_counters")
	requireMetadataKeyUncommitted(ctx, t, store, "test.batch-create-closure")
}

// TestCreateIssuesWithExplicitHierarchicalIDCarriesThePendingChildCounter is
// TestCreateIssueWithExplicitHierarchicalIDCarriesThePendingChildCounter for
// a batch create.
func TestCreateIssuesWithExplicitHierarchicalIDCarriesThePendingChildCounter(t *testing.T) {
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
	if committedDirtyCounters != 1 {
		t.Fatalf("committed pending child counters = %d, want 1: the create's commit stages the issue closure", committedDirtyCounters)
	}
	requireCleanTables(ctx, t, store, "child_counters")
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

// TestCreateIssuesDuplicateSideTableInputsKeepPendingClosureRowsTogether: a
// re-create whose labels, comments and dependencies already exist writes no
// side row of its own. Whatever it stages, the pending side rows another
// write left behind move together: the commit stages the issue closure in one
// DOLT_ADD (withIssueClosure), so they are all at HEAD afterwards or all still
// pending, never split. A pending row outside the closure stays pending.
// Until the closure, the create left all of them pending (#4149).
func TestCreateIssuesDuplicateSideTableInputsKeepPendingClosureRowsTogether(t *testing.T) {
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

	pendingMetadataKey(ctx, t, store, "test.duplicate-create-closure")

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

	var committedDirtyComments int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments AS OF 'HEAD' WHERE issue_id = ? AND text = ?",
		dirtyOwner.ID, "uncommitted comment",
	).Scan(&committedDirtyComments); err != nil {
		t.Fatalf("count committed dirty comments: %v", err)
	}

	var committedDirtyDependencies int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?",
		dirtyOwner.ID, dirtyTarget.ID,
	).Scan(&committedDirtyDependencies); err != nil {
		t.Fatalf("count committed dirty dependencies: %v", err)
	}
	var dirtySideTables int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dolt_status
		WHERE table_name IN ('labels', 'comments', 'dependencies')
	`).Scan(&dirtySideTables); err != nil {
		t.Fatalf("count dirty side tables: %v", err)
	}
	committed := committedDirtyLabels + committedDirtyComments + committedDirtyDependencies
	switch {
	case committed == 3 && dirtySideTables == 0:
		// The re-create staged the closure and carried the pending rows.
	case committed == 0 && dirtySideTables == 3:
		// The re-create staged nothing of the closure.
	default:
		t.Fatalf("pending side rows split: %d labels, %d comments, %d dependencies at HEAD and %d side tables still dirty; want all three committed or none",
			committedDirtyLabels, committedDirtyComments, committedDirtyDependencies, dirtySideTables)
	}
	requireMetadataKeyUncommitted(ctx, t, store, "test.duplicate-create-closure")
}
