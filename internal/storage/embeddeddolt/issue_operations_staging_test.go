//go:build cgo

package embeddeddolt_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestEmbeddedIssueOperationsUpdateClaimPatchAppliesAfterClaim(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_staging_claim_patch")
	ctx := t.Context()
	issue := &types.Issue{ID: "ops-staging-claim-patch", Title: "claim patch", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
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

func TestEmbeddedIssueOperationsCreateEmptyIDPreservesParentAndReverseDependencies(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_staging_create_reverse")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	parent := &types.Issue{ID: "ops-staging-embedded-parent", Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	durableSource := &types.Issue{ID: "test-staging-embedded-durable", Title: "durable source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	wispSource := &types.Issue{ID: "test-staging-embedded-wisp", Title: "wisp source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{parent, durableSource, wispSource} {
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
	}

	child, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		ParentID:      parent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.Issue.ID != parent.ID+".1" || len(child.Issue.Dependencies) != 1 || child.Issue.Dependencies[0].IssueID != child.Issue.ID || child.Issue.Dependencies[0].DependsOnID != parent.ID || child.Issue.Dependencies[0].Type != types.DepParentChild || child.Issue.Dependencies[0].Metadata != "{}" {
		t.Fatalf("child aggregate = %#v", child.Issue)
	}

	generatedWisp, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{Title: "generated wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		Dependencies:  []publicops.CreateDependency{{TargetID: durableSource.ID, Type: types.DepRelatesTo, Reverse: true, Metadata: `{"direction":"durable"}`, ThreadID: "durable-thread"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	durableDependencies, err := te.store.GetDependencyRecords(ctx, durableSource.ID)
	if err != nil || len(durableDependencies) != 1 {
		t.Fatalf("durable reverse dependency = %#v, %v", durableDependencies, err)
	}
	durableDependency := durableDependencies[0]
	if durableDependency.IssueID != durableSource.ID || durableDependency.DependsOnID != generatedWisp.Issue.ID || durableDependency.Type != types.DepRelatesTo || !sameEmbeddedMetadataJSON(json.RawMessage(durableDependency.Metadata), json.RawMessage(`{"direction":"durable"}`)) || durableDependency.ThreadID != "durable-thread" {
		t.Fatalf("durable reverse dependency = %+v", durableDependency)
	}

	generatedDurable, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{Title: "generated durable", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		Dependencies:  []publicops.CreateDependency{{TargetID: wispSource.ID, Type: types.DepRelatesTo, Reverse: true, Metadata: `{"direction":"wisp"}`, ThreadID: "wisp-thread"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wispDependencies, err := te.store.GetDependencyRecords(ctx, wispSource.ID)
	if err != nil || len(wispDependencies) != 1 {
		t.Fatalf("wisp reverse dependency = %#v, %v", wispDependencies, err)
	}
	wispDependency := wispDependencies[0]
	if wispDependency.IssueID != wispSource.ID || wispDependency.DependsOnID != generatedDurable.Issue.ID || wispDependency.Type != types.DepRelatesTo || !sameEmbeddedMetadataJSON(json.RawMessage(wispDependency.Metadata), json.RawMessage(`{"direction":"wisp"}`)) || wispDependency.ThreadID != "wisp-thread" {
		t.Fatalf("wisp reverse dependency = %+v", wispDependency)
	}
}

func TestEmbeddedIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}

	conformance.RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables(t, ctx, conformance.IssueOperationsStagingFixture{
		IssuePrefix: "ops",
		Operations:  operations,
		CreateIssue: te.store.CreateIssue,
		Commit:      te.store.Commit,
		Exec: func(ctx context.Context, query string, args ...any) error {
			te.exec(t, ctx, query, args...)
			return nil
		},
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			te.queryScalar(t, ctx, query, args, dest...)
			return nil
		},
	})
}

func TestEmbeddedIssueOperationsCreateParentChildRecomputesWaitsForClosure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}

	conformance.RunIssueOperationsCreateParentChildRecomputesWaitsForClosure(t, ctx, conformance.IssueOperationsStagingFixture{
		IssuePrefix:   "ops",
		Operations:    operations,
		CreateIssue:   te.store.CreateIssue,
		AddDependency: te.store.AddDependency,
		GetReadyWork:  te.store.GetReadyWork,
		Commit:        te.store.Commit,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			te.queryScalar(t, ctx, query, args, dest...)
			return nil
		},
	})
}

func TestEmbeddedIssueOperationsUpdateInvalidIssueTypePreservesValidationSentinel(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_staging_invalid_type")
	ctx := t.Context()
	issue := &types.Issue{ID: "ops-staging-invalid-type", Title: "invalid type", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}

	_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{IssueType: publicops.Field[publicops.IssueType]{Set: true, Value: "not-a-type"}}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("invalid issue type error = %v, want ErrValidation", err)
	}
}

func TestEmbeddedIssueOperationsUpdateNotesConflictPreservesValidationSentinel(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_staging_notes_conflict")
	ctx := t.Context()
	issue := &types.Issue{ID: "ops-staging-notes-conflict", Title: "notes conflict", Notes: "before", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
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
	stored, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil || stored.Notes != "before" {
		t.Fatalf("notes conflict persisted notes = %q, %v; want before", stored.Notes, err)
	}
}

func TestEmbeddedIssueOperationsUpdateParentStagesBlockedStateAndRollsBackFailure(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_staging_parent_state")
	ctx := t.Context()
	issues := []*types.Issue{
		{ID: "ops-staging-parent-blocker", Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ops-staging-parent-clean", Title: "clean parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
		{ID: "ops-staging-parent-blocked", Title: "blocked parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic},
		{ID: "ops-staging-parent-child", Title: "child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "ops-staging-parent-rollback", Title: "rollback child", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, issue := range issues {
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	for _, dependency := range []*types.Dependency{
		{IssueID: "ops-staging-parent-blocked", DependsOnID: "ops-staging-parent-blocker", Type: types.DepBlocks},
		{IssueID: "ops-staging-parent-child", DependsOnID: "ops-staging-parent-clean", Type: types.DepParentChild},
		{IssueID: "ops-staging-parent-rollback", DependsOnID: "ops-staging-parent-blocked", Type: types.DepParentChild},
	} {
		if err := te.store.AddDependency(ctx, dependency, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := te.store.Commit(ctx, "seed blocked parent state"); err != nil {
		t.Fatal(err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}

	result, err := operations.Update(ctx, publicops.UpdateRequest{
		Actor:   "writer",
		IssueID: "ops-staging-parent-child",
		Patch:   publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true, Value: "ops-staging-parent-blocked"}},
	})
	if err != nil || !result.Changed || embeddedIssueParentIDs(result.Issue) != "ops-staging-parent-blocked" {
		t.Fatalf("blocked parent replacement = %#v, %v", result, err)
	}
	var workingBlocked, headBlocked bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{"ops-staging-parent-child"}, &workingBlocked)
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{"ops-staging-parent-child"}, &headBlocked)
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
	rolledBack, err := te.store.GetIssue(ctx, "ops-staging-parent-rollback")
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := te.store.GetDependencyRecords(ctx, rolledBack.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Title != "rollback child" || len(dependencies) != 1 || dependencies[0].IssueID != rolledBack.ID || dependencies[0].DependsOnID != "ops-staging-parent-blocked" || dependencies[0].Type != types.DepParentChild {
		t.Fatalf("failed parent replacement persisted issue = %#v, dependencies %#v", rolledBack, dependencies)
	}
	var rollbackWorkingTitle, rollbackHeadTitle string
	te.queryScalar(t, ctx, "SELECT title, is_blocked FROM issues WHERE id = ?", []any{rolledBack.ID}, &rollbackWorkingTitle, &workingBlocked)
	te.queryScalar(t, ctx, "SELECT title, is_blocked FROM issues AS OF 'HEAD' WHERE id = ?", []any{rolledBack.ID}, &rollbackHeadTitle, &headBlocked)
	if rollbackWorkingTitle != "rollback child" || rollbackHeadTitle != "rollback child" || !workingBlocked || !headBlocked {
		t.Fatalf("failed parent replacement state = working (%q, %t), HEAD (%q, %t)", rollbackWorkingTitle, workingBlocked, rollbackHeadTitle, headBlocked)
	}
}
