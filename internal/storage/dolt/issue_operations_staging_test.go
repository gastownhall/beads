package dolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestIssueOperationsCreateIgnoresDerivedFieldsAndRejectsMalformedAggregate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := operations.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: &types.Issue{ID: "ops-staging-create", Title: "create", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, RowVersion: 91, LeaseExpiresAt: &now, ContentHash: "derived"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Issue.RowVersion == 91 || created.Issue.LeaseExpiresAt != nil || created.Issue.ContentHash == "derived" {
		t.Fatalf("derived fields persisted: %#v", created.Issue)
	}
	_, err = operations.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: &types.Issue{ID: "ops-staging-invalid-create", Title: "invalid", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, Dependencies: []publicops.CreateDependency{{TargetID: "ops-staging-invalid-create", Type: types.DepBlocks}}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("malformed aggregate error = %v, want ErrValidation", err)
	}
	if _, err := store.GetIssue(ctx, "ops-staging-invalid-create"); !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("malformed aggregate persisted issue: %v", err)
	}
}

func TestIssueOperationsCreateEmptyIDUsesChildIDAndStagesCounter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	parent := &types.Issue{ID: "ops-staging-create-parent", Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "seed"); err != nil {
		t.Fatal(err)
	}

	create := func(title string) *types.Issue {
		t.Helper()
		result, err := operations.Create(ctx, publicops.CreateRequest{
			Actor:         "writer",
			ForceIDPrefix: true,
			Issue:         &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			ParentID:      parent.ID,
		})
		if err != nil {
			t.Fatalf("Create(%q): %v", title, err)
		}
		return result.Issue
	}

	first := create("first child")
	if first.ID != parent.ID+".1" {
		t.Fatalf("first child ID = %q, want %q", first.ID, parent.ID+".1")
	}
	if !hasIssueOperationDependency(first.Dependencies, first.ID, parent.ID, types.DepParentChild, "{}", "") {
		t.Fatalf("first child dependencies = %#v, want parent edge", first.Dependencies)
	}
	var counter int
	if err := store.db.QueryRowContext(ctx, "SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", parent.ID).Scan(&counter); err != nil || counter != 1 {
		t.Fatalf("committed first child counter = %d, %v; want 1", counter, err)
	}

	second := create("second child")
	if second.ID != parent.ID+".2" {
		t.Fatalf("second child ID = %q, want %q", second.ID, parent.ID+".2")
	}
	if err := store.db.QueryRowContext(ctx, "SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", parent.ID).Scan(&counter); err != nil || counter != 2 {
		t.Fatalf("committed second child counter = %d, %v; want 2", counter, err)
	}
}

func TestIssueOperationsCreateEmptyIDPersistsReverseDependenciesInSourceTier(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	durableSource := &types.Issue{ID: "test-staging-durable-source", Title: "durable source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	wispSource := &types.Issue{ID: "test-staging-wisp-source", Title: "wisp source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{durableSource, wispSource} {
		if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("durable source points at generated wisp", func(t *testing.T) {
		result, err := operations.Create(ctx, publicops.CreateRequest{
			Actor:         "writer",
			ForceIDPrefix: true,
			Issue:         &types.Issue{Title: "generated wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			Dependencies:  []publicops.CreateDependency{{TargetID: durableSource.ID, Type: types.DepBlocks, Reverse: true, Metadata: `{"direction":"durable"}`, ThreadID: "durable-thread"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Issue.ID == "" {
			t.Fatal("generated wisp ID is empty")
		}
		var metadata, threadID string
		if err := store.db.QueryRowContext(ctx, "SELECT metadata, thread_id FROM dependencies WHERE issue_id = ? AND depends_on_wisp_id = ?", durableSource.ID, result.Issue.ID).Scan(&metadata, &threadID); err != nil || metadata != `{"direction":"durable"}` || threadID != "durable-thread" {
			t.Fatalf("durable dependency = metadata %q, thread %q, err %v", metadata, threadID, err)
		}
		if blocked, _, err := store.IsBlocked(ctx, durableSource.ID); err != nil || !blocked {
			t.Fatalf("durable source blocked = %t, %v; want true", blocked, err)
		}
	})

	t.Run("wisp source points at generated durable issue", func(t *testing.T) {
		result, err := operations.Create(ctx, publicops.CreateRequest{
			Actor:         "writer",
			ForceIDPrefix: true,
			Issue:         &types.Issue{Title: "generated durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			Dependencies:  []publicops.CreateDependency{{TargetID: wispSource.ID, Type: types.DepBlocks, Reverse: true, Metadata: `{"direction":"wisp"}`, ThreadID: "wisp-thread"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Issue.ID == "" {
			t.Fatal("generated durable issue ID is empty")
		}
		var metadata, threadID string
		if err := store.db.QueryRowContext(ctx, "SELECT metadata, thread_id FROM wisp_dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", wispSource.ID, result.Issue.ID).Scan(&metadata, &threadID); err != nil || metadata != `{"direction":"wisp"}` || threadID != "wisp-thread" {
			t.Fatalf("wisp dependency = metadata %q, thread %q, err %v", metadata, threadID, err)
		}
		if blocked, _, err := store.IsBlocked(ctx, wispSource.ID); err != nil || !blocked {
			t.Fatalf("wisp source blocked = %t, %v; want true", blocked, err)
		}
	})
}

func TestIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	conformance.RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t, ctx, conformance.IssueOperationsStagingFixture{
		IssuePrefix: "test",
		Operations:  operations,
		CreateIssue: store.CreateIssue,
		Commit:      store.Commit,
		Exec: func(ctx context.Context, query string, args ...any) error {
			_, err := store.db.ExecContext(ctx, query, args...)
			return err
		},
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest...)
		},
	})
}

func TestIssueOperationsCreateParentChildRecomputesWaitsForClosure(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	conformance.RunIssueOperationsCreateParentChildRecomputesWaitsForClosure(t, ctx, conformance.IssueOperationsStagingFixture{
		IssuePrefix:   "test",
		Operations:    operations,
		CreateIssue:   store.CreateIssue,
		AddDependency: store.AddDependency,
		GetReadyWork:  store.GetReadyWork,
		Commit:        store.Commit,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest...)
		},
	})
}

func hasIssueOperationDependency(dependencies []*types.Dependency, issueID, dependsOnID string, dependencyType types.DependencyType, metadata, threadID string) bool {
	for _, dependency := range dependencies {
		if dependency != nil && dependency.IssueID == issueID && dependency.DependsOnID == dependsOnID && dependency.Type == dependencyType && dependency.Metadata == metadata && dependency.ThreadID == threadID {
			return true
		}
	}
	return false
}

func TestIssueOperationsReopenWispStagesOnlyConcreteDurableChanges(t *testing.T) {
	newStore := func(t *testing.T) (*DoltStore, context.Context) {
		t.Helper()
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		ctx, cancel := testContext(t)
		t.Cleanup(cancel)
		return store, ctx
	}
	t.Run("isolated wisp leaves dirty durable rows uncommitted", func(t *testing.T) {
		store, ctx := newStore(t)
		operations, err := NewIssueOperations(store)
		if err != nil {
			t.Fatal(err)
		}
		createPerm(t, ctx, store, "ops-staging-reopen-dirty")
		createWisp(t, ctx, store, "ops-staging-reopen-wisp")
		if err := store.CloseIssue(ctx, "ops-staging-reopen-wisp", "done", "tester", ""); err != nil {
			t.Fatal(err)
		}
		before := reopenDoltHead(t, ctx, store)
		stageReopenDirtyIssue(t, ctx, store, "ops-staging-reopen-dirty")
		stageReopenDirtyEvent(t, ctx, store, "ops-staging-reopen-event", "ops-staging-reopen-dirty")
		if _, err := operations.Reopen(ctx, publicops.ReopenRequest{Actor: "tester", IssueID: "ops-staging-reopen-wisp"}); err != nil {
			t.Fatal(err)
		}
		if after := reopenDoltHead(t, ctx, store); after != before {
			t.Fatalf("isolated adapter reopen changed HEAD from %s to %s", before, after)
		}
		assertReopenDirtyRowsUncommitted(t, ctx, store, "ops-staging-reopen-dirty", "ops-staging-reopen-event")
	})
	t.Run("durable recompute stages issues only", func(t *testing.T) {
		store, ctx := newStore(t)
		operations, err := NewIssueOperations(store)
		if err != nil {
			t.Fatal(err)
		}
		createPerm(t, ctx, store, "ops-staging-reopen-depender")
		createWisp(t, ctx, store, "ops-staging-reopen-flip")
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "ops-staging-reopen-depender", DependsOnID: "ops-staging-reopen-flip", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatal(err)
		}
		if err := store.CloseIssue(ctx, "ops-staging-reopen-flip", "done", "tester", ""); err != nil {
			t.Fatal(err)
		}
		commitReopenIssueWorkingSet(t, ctx, store, "seed closed wisp")
		before := reopenDoltHead(t, ctx, store)
		stageReopenDirtyEvent(t, ctx, store, "ops-staging-reopen-flip-event", "ops-staging-reopen-depender")
		if _, err := operations.Reopen(ctx, publicops.ReopenRequest{Actor: "tester", IssueID: "ops-staging-reopen-flip"}); err != nil {
			t.Fatal(err)
		}
		if after := reopenDoltHead(t, ctx, store); after == before {
			t.Fatal("durable recompute did not advance HEAD")
		}
		var blocked bool
		if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", "ops-staging-reopen-depender").Scan(&blocked); err != nil || !blocked {
			t.Fatalf("committed durable recompute = %t, %v", blocked, err)
		}
		// events is dolt_ignored since migration 0062 (bd-red8u): the unrelated
		// audit row has no HEAD state and cannot ride the issues commit, but it
		// must survive it in the working set. assertEventsNotCommitted keeps the
		// plane invariant pinned here, where the recompute does advance HEAD.
		var events int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE id = ?", "ops-staging-reopen-flip-event").Scan(&events); err != nil || events != 1 {
			t.Fatalf("unrelated working-set event = %d, %v", events, err)
		}
		assertEventsNotCommitted(ctx, t, store.db)
	})
}

func TestIssueOperationsUpdateClaimPatchAppliesAfterClaim(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "ops-staging-claim-patch", Title: "claim patch", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	result, err := operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "worker",
		IssueID: issue.ID,
		Claim:   true,
		Patch: publicops.IssuePatch{
			Status:   publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusOpen},
			Assignee: publicops.Field[string]{Set: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Issue.Status != publicops.StatusOpen || result.Issue.Assignee != "" {
		t.Fatalf("claim then patch = %#v, want open and unassigned", result)
	}
}

func TestIssueOperationsUpdateInvalidIssueTypePreservesValidationSentinel(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "ops-staging-invalid-type", Title: "invalid type", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{IssueType: publicops.Field[publicops.IssueType]{Set: true, Value: "not-a-type"}}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("invalid issue type error = %v, want ErrValidation", err)
	}
}

func TestIssueOperationsUpdateNotesConflictPreservesValidationSentinel(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{ID: "ops-staging-notes-conflict", Title: "notes conflict", Notes: "before", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	_, err = operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "writer",
		IssueID: issue.ID,
		Patch: publicops.IssuePatch{
			Notes:       publicops.Field[string]{Set: true, Value: "replacement"},
			AppendNotes: publicops.Field[string]{Set: true, Value: "append"},
		},
	})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("notes conflict error = %v, want ErrValidation", err)
	}
	stored, err := store.GetIssue(ctx, issue.ID)
	if err != nil || stored.Notes != "before" {
		t.Fatalf("notes conflict persisted notes = %q, %v; want before", stored.Notes, err)
	}
}

func TestIssueOperationsUpdateParentStagesBlockedStateAndRollsBackFailure(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	issues := []*types.Issue{
		{ID: "ops-staging-parent-blocker", Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ops-staging-parent-clean", Title: "clean parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
		{ID: "ops-staging-parent-blocked", Title: "blocked parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
		{ID: "ops-staging-parent-child", Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ops-staging-parent-rollback", Title: "rollback child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	for _, dependency := range []*types.Dependency{
		{IssueID: "ops-staging-parent-blocked", DependsOnID: "ops-staging-parent-blocker", Type: types.DepBlocks},
		{IssueID: "ops-staging-parent-child", DependsOnID: "ops-staging-parent-clean", Type: types.DepParentChild},
		{IssueID: "ops-staging-parent-rollback", DependsOnID: "ops-staging-parent-blocked", Type: types.DepParentChild},
	} {
		if err := store.AddDependency(ctx, dependency, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Commit(ctx, "seed blocked parent state"); err != nil {
		t.Fatal(err)
	}
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}

	result, err := operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "writer",
		IssueID: "ops-staging-parent-child",
		Patch:   publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true, Value: "ops-staging-parent-blocked"}},
	})
	if err != nil || !result.Changed || issueParentIDs(result.Issue) != "ops-staging-parent-blocked" {
		t.Fatalf("blocked parent replacement = %#v, %v", result, err)
	}
	var workingBlocked, headBlocked bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", "ops-staging-parent-child").Scan(&workingBlocked); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", "ops-staging-parent-child").Scan(&headBlocked); err != nil {
		t.Fatal(err)
	}
	if !workingBlocked || !headBlocked {
		t.Fatalf("blocked child state = working %t, HEAD %t; want both true", workingBlocked, headBlocked)
	}

	_, err = operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "writer",
		IssueID: "ops-staging-parent-rollback",
		Patch: publicops.IssuePatch{
			Title:    publicops.Field[string]{Set: true, Value: "must rollback"},
			ParentID: publicops.Field[string]{Set: true, Value: "ops-staging-parent-missing"},
		},
	})
	if err == nil {
		t.Fatal("missing parent replacement succeeded")
	}
	rolledBack, err := store.GetIssue(ctx, "ops-staging-parent-rollback")
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := store.GetDependencyRecords(ctx, rolledBack.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Title != "rollback child" || !hasIssueOperationDependency(dependencies, rolledBack.ID, "ops-staging-parent-blocked", types.DepParentChild, "{}", "") {
		t.Fatalf("failed parent replacement persisted issue = %#v, dependencies %#v", rolledBack, dependencies)
	}
	var rollbackWorkingTitle, rollbackHeadTitle string
	if err := store.db.QueryRowContext(ctx, "SELECT title, is_blocked FROM issues WHERE id = ?", rolledBack.ID).Scan(&rollbackWorkingTitle, &workingBlocked); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT title, is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", rolledBack.ID).Scan(&rollbackHeadTitle, &headBlocked); err != nil {
		t.Fatal(err)
	}
	if rollbackWorkingTitle != "rollback child" || rollbackHeadTitle != "rollback child" || !workingBlocked || !headBlocked {
		t.Fatalf("failed parent replacement state = working (%q, %t), HEAD (%q, %t)", rollbackWorkingTitle, workingBlocked, rollbackHeadTitle, headBlocked)
	}
}
