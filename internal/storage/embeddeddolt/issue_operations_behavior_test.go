//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestEmbeddedIssueOperationsGuardedVerbs gives the embedded adapter the same
// public guarded-operation coverage as the direct implementation.
func TestEmbeddedIssueOperationsGuardedVerbs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	create := func(t *testing.T, id string) *types.Issue {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return issue
	}

	t.Run("create rejects occupied ID", func(t *testing.T) {
		create(t, "ops-duplicate")
		_, err := operations.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: &types.Issue{ID: "ops-duplicate", Title: "replacement", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}})
		if !errors.Is(err, publicops.ErrAlreadyExists) {
			t.Fatalf("Create error = %v, want ErrAlreadyExists", err)
		}
		got, getErr := te.store.GetIssue(ctx, "ops-duplicate")
		if getErr != nil || got.Title != "ops-duplicate" {
			t.Fatalf("stored duplicate = %#v, %v; want original title", got, getErr)
		}
	})

	t.Run("create validates prefix and aggregates relations", func(t *testing.T) {
		parent := create(t, "ops-parent")
		spawner := create(t, "ops-spawner")
		if err := te.store.AddLabel(ctx, parent.ID, "parent-label", "seed"); err != nil {
			t.Fatal(err)
		}
		target := create(t, "ops-target")
		request := publicops.CreateRequest{Actor: "writer", Issue: &types.Issue{ID: "foreign-aggregate", Title: "aggregate", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, ParentID: parent.ID, InheritLabelsFromParent: true, Dependencies: []publicops.CreateDependency{{TargetID: target.ID, Type: types.DepRelated, ThreadID: "thread"}, {TargetID: target.ID, Type: types.DepRelatesTo, Reverse: true, ThreadID: "reverse-thread"}}, WaitsFor: &publicops.WaitsFor{SpawnerID: spawner.ID}}
		_, err := operations.Create(ctx, request)
		if !errors.Is(err, publicops.ErrPrefixMismatch) {
			t.Fatalf("unforced foreign prefix error = %v, want ErrPrefixMismatch", err)
		}
		request.ForceIDPrefix = true
		result, err := operations.Create(ctx, request)
		if err != nil {
			t.Fatalf("forced Create: %v", err)
		}
		if len(result.Issue.Labels) != 1 || result.Issue.Labels[0] != "parent-label" || len(result.Issue.Dependencies) != 3 {
			t.Fatalf("Create result aggregate = %#v", result.Issue)
		}
		inbound, err := te.store.GetDependentRecords(ctx, result.Issue.ID, "", 10, "")
		if err != nil || len(inbound) != 1 || inbound[0].IssueID != target.ID || inbound[0].ThreadID != "reverse-thread" {
			t.Fatalf("reverse dependency = %#v, %v", inbound, err)
		}
	})

	t.Run("update enforces version and claim", func(t *testing.T) {
		issue := create(t, "ops-update")
		stale := issue.RowVersion + 1
		_, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, ExpectedVersion: &stale, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "must not persist"}}})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("stale Update error = %v, want ErrVersionMismatch", err)
		}
		result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Claim: true})
		if err != nil || !result.Changed || result.Issue.Assignee != "writer" || result.Issue.Status != publicops.StatusInProgress {
			t.Fatalf("claim result = %#v, %v", result, err)
		}
	})

	t.Run("close and reopen enforce versions and children", func(t *testing.T) {
		parent := create(t, "ops-close-parent")
		child := create(t, "ops-close-child")
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}, "seed"); err != nil {
			t.Fatal(err)
		}
		stale := parent.RowVersion + 1
		_, err := operations.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent.ID, ExpectedVersion: &stale})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("stale Close error = %v, want ErrVersionMismatch", err)
		}
		_, err = operations.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent.ID})
		if !errors.Is(err, publicops.ErrCloseOpenChildren) {
			t.Fatalf("unforced close error = %v, want ErrCloseOpenChildren", err)
		}
		closed, err := operations.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent.ID, Force: true})
		if err != nil || !closed.Changed || closed.OpenChildren != 1 {
			t.Fatalf("forced close = %#v, %v", closed, err)
		}
		stale = closed.Issue.RowVersion + 1
		_, err = operations.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: parent.ID, ExpectedVersion: &stale})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("stale Reopen error = %v, want ErrVersionMismatch", err)
		}
	})
}

func TestEmbeddedIssueOperationsUpdateMetadataPatchOrderingAndReplacementGuard(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_metadata_order")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	issue := &types.Issue{ID: "ops-metadata-order", Title: "metadata", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Metadata: json.RawMessage(`{"keep":"old","remove":"old"}`)}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	updated, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Metadata: publicops.MetadataPatch{
		Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"keep":"merged","merge":true}`)},
		Set:   map[string]json.RawMessage{"after": json.RawMessage(`"set"`), "keep": json.RawMessage(`"set"`)},
		Unset: []string{"keep", "remove"},
	}}})
	if err != nil || !updated.Changed || !sameEmbeddedMetadataJSON(updated.Issue.Metadata, json.RawMessage(`{"after":"set","merge":true}`)) {
		t.Fatalf("ordered metadata update = %#v, %v", updated, err)
	}
	replaced, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Metadata: publicops.MetadataPatch{Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)}}}})
	if err != nil || !replaced.Changed || !sameEmbeddedMetadataJSON(replaced.Issue.Metadata, json.RawMessage(`{"replacement":true}`)) {
		t.Fatalf("metadata replacement = %#v, %v", replaced, err)
	}
	_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Metadata: publicops.MetadataPatch{
		Replace: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"replacement":true}`)},
		Set:     map[string]json.RawMessage{"must_not_persist": json.RawMessage(`true`)},
	}}})
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("combined replacement error = %v, want ErrValidation", err)
	}
	stored, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil || !sameEmbeddedMetadataJSON(stored.Metadata, json.RawMessage(`{"replacement":true}`)) {
		t.Fatalf("replacement guard persisted metadata = %#v, %v", stored, err)
	}
}

func TestEmbeddedIssueOperationsUpdateLabelPatchOrderingAndNoop(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_label_order")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	issue := &types.Issue{ID: "ops-label-order", Title: "labels", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"old", "shared"}}
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	patch := publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "shared"}}, Add: []string{"add", "shared"}, Remove: []string{"old", "shared"}}
	updated, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Labels: patch}})
	if err != nil || !updated.Changed || strings.Join(updated.Issue.Labels, ",") != "add,replace" {
		t.Fatalf("ordered labels update = %#v, %v", updated, err)
	}
	noOp, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{Set: true, Value: []string{"replace", "add"}}, Add: []string{"add"}, Remove: []string{"missing"}}}})
	if err != nil || noOp.Changed || strings.Join(noOp.Issue.Labels, ",") != "add,replace" {
		t.Fatalf("same labels update = %#v, %v", noOp, err)
	}
}

func TestEmbeddedIssueOperationsUpdateParentReplacementClearAndNoop(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_parent_order")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	create := func(id string) *types.Issue {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	parentA, parentB, parentC := create("ops-parent-a"), create("ops-parent-b"), create("ops-parent-c")
	child := create("ops-parent-child")
	for _, parent := range []*types.Issue{parentA, parentB} {
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}, "seed"); err != nil {
			t.Fatal(err)
		}
	}
	replaced, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: child.ID, Patch: publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true, Value: parentC.ID}}})
	if err != nil || !replaced.Changed || embeddedIssueParentIDs(replaced.Issue) != parentC.ID {
		t.Fatalf("parent replacement = %#v, %v", replaced, err)
	}
	noOp, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: child.ID, Patch: publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true, Value: parentC.ID}}})
	if err != nil || noOp.Changed || embeddedIssueParentIDs(noOp.Issue) != parentC.ID {
		t.Fatalf("same parent replacement = %#v, %v", noOp, err)
	}
	cleared, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: child.ID, Patch: publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true}}})
	if err != nil || !cleared.Changed || embeddedIssueParentIDs(cleared.Issue) != "" {
		t.Fatalf("parent clear = %#v, %v", cleared, err)
	}
	noOp, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: child.ID, Patch: publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true}}})
	if err != nil || noOp.Changed || embeddedIssueParentIDs(noOp.Issue) != "" {
		t.Fatalf("same parent clear = %#v, %v", noOp, err)
	}
}

func TestEmbeddedIssueOperationsUpdateClaimCASAndTransferMatrix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_claim_matrix")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	create := func(id, assignee string, status types.Status, wisp bool) *types.Issue {
		issue := &types.Issue{ID: id, Title: id, Assignee: assignee, Status: status, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	t.Run("version and field CAS refuse before mutation", func(t *testing.T) {
		issue := create("ops-cas", "owner", types.StatusOpen, false)
		stale := issue.RowVersion + 1
		_, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, ExpectedVersion: &stale, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "no"}}})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("version error = %v", err)
		}
		expectedAssignee, expectedStatus := "other", publicops.StatusOpen
		_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, ExpectedAssignee: &expectedAssignee, ExpectedStatus: &expectedStatus, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "no"}}})
		if !errors.Is(err, publicops.ErrAssigneeMismatch) {
			t.Fatalf("assignee error = %v", err)
		}
		expectedAssignee = "owner"
		wrongStatus := publicops.StatusInProgress
		_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, ExpectedAssignee: &expectedAssignee, ExpectedStatus: &wrongStatus, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "no"}}})
		if !errors.Is(err, publicops.ErrStatusMismatch) {
			t.Fatalf("status error = %v", err)
		}
		expectedStatus = publicops.StatusOpen
		matched, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, ExpectedAssignee: &expectedAssignee, ExpectedStatus: &expectedStatus, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "matched"}}})
		if err != nil || !matched.Changed || matched.Issue.Title != "matched" {
			t.Fatalf("matching CAS = %#v, %v", matched, err)
		}
		got, getErr := te.store.GetIssue(ctx, issue.ID)
		if getErr != nil || got.Title != "matched" {
			t.Fatalf("CAS mutation = %#v, %v", got, getErr)
		}
	})
	t.Run("claim eligibility and overrides", func(t *testing.T) {
		claimable := create("ops-claim", "", types.StatusOpen, false)
		claimed, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: claimable.ID, Claim: true})
		if err != nil || !claimed.Changed || claimed.Issue.Assignee != "worker" || claimed.Issue.Status != publicops.StatusInProgress {
			t.Fatalf("claim = %#v, %v", claimed, err)
		}
		noOp, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: claimable.ID, Claim: true})
		if err != nil || noOp.Changed {
			t.Fatalf("same actor claim = %#v, %v", noOp, err)
		}
		foreign := create("ops-foreign", "owner", types.StatusOpen, false)
		_, err = operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: foreign.ID, Claim: true})
		if !errors.Is(err, publicops.ErrAlreadyClaimed) {
			t.Fatalf("foreign claim = %v", err)
		}
		if err := te.store.SetConfig(ctx, "claim.pools", "crew"); err != nil {
			t.Fatal(err)
		}
		pool := create("ops-pool", "crew", types.StatusOpen, false)
		pooled, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: pool.ID, Claim: true})
		if err != nil || !pooled.Changed || pooled.Issue.Assignee != "worker" {
			t.Fatalf("pool claim = %#v, %v", pooled, err)
		}
		poolTransfer := create("ops-pool-transfer", "crew", types.StatusInProgress, false)
		transferred, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: poolTransfer.ID, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}})
		if err != nil || !transferred.Changed || transferred.Issue.Assignee != "next" {
			t.Fatalf("pool transfer = %#v, %v", transferred, err)
		}
		override := create("ops-override", "", types.StatusOpen, false)
		result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: override.ID, Claim: true, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "reviewer"}, Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusOpen}}})
		if err != nil || !result.Changed || result.Issue.Assignee != "reviewer" || result.Issue.Status != publicops.StatusOpen {
			t.Fatalf("claim override = %#v, %v", result, err)
		}
	})
	t.Run("transfer fence, force, expected assignee, and wisp routing", func(t *testing.T) {
		foreign := create("ops-transfer", "owner", types.StatusInProgress, false)
		_, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: foreign.ID, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}})
		if !errors.Is(err, publicops.ErrAlreadyClaimed) {
			t.Fatalf("unforced transfer = %v", err)
		}
		forced, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: foreign.ID, ForceAssigneeTransfer: true, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}})
		if err != nil || !forced.Changed || forced.Issue.Assignee != "next" {
			t.Fatalf("forced transfer = %#v, %v", forced, err)
		}
		expected := "owner"
		authorized := create("ops-authorized", "owner", types.StatusInProgress, false)
		result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: authorized.ID, ExpectedAssignee: &expected, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}})
		if err != nil || !result.Changed || result.Issue.Assignee != "next" {
			t.Fatalf("authorized transfer = %#v, %v", result, err)
		}
		wisp := create("ops-wisp", "", types.StatusOpen, true)
		wispResult, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: wisp.ID, Claim: true})
		if err != nil || !wispResult.Changed || wispResult.Issue.Assignee != "worker" {
			t.Fatalf("wisp claim = %#v, %v", wispResult, err)
		}
	})
	t.Run("invalid claim combinations fail without mutation", func(t *testing.T) {
		issue := create("ops-invalid", "", types.StatusOpen, false)
		expected := ""
		status := publicops.StatusOpen
		for _, request := range []publicops.UpdateRequest{
			{Actor: "worker", IssueID: issue.ID, Claim: true, ExpectedAssignee: &expected},
			{Actor: "worker", IssueID: issue.ID, Claim: true, ExpectedStatus: &status},
			{Actor: "worker", IssueID: issue.ID, Claim: true, ForceAssigneeTransfer: true},
			{Actor: "worker", IssueID: issue.ID, ForceAssigneeTransfer: true},
			{Actor: "worker", IssueID: issue.ID, ForceAssigneeTransfer: true, ExpectedAssignee: &expected, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}},
		} {
			_, err := operations.Update(ctx, request)
			if !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("invalid request error = %v", err)
			}
		}
		got, err := te.store.GetIssue(ctx, issue.ID)
		if err != nil || got.Assignee != "" || got.Status != types.StatusOpen {
			t.Fatalf("invalid request mutated = %#v, %v", got, err)
		}
	})
}

func TestEmbeddedIssueOperationsLifecycleContractMatrix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_lifecycle")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	makeIssue := func(id string, status types.Status, wisp bool) *types.Issue {
		issue := &types.Issue{ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	t.Run("close session and reclose", func(t *testing.T) {
		issue := makeIssue("ops-life", types.StatusOpen, false)
		result, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: issue.ID, Reason: "because", Session: "session-1"})
		if err != nil || !result.Changed || result.Issue.ClosedBySession != "session-1" || result.Issue.CloseReason != "because" {
			t.Fatalf("first close session=%q reason=%q changed=%t error=%v", result.Issue.ClosedBySession, result.Issue.CloseReason, result.Changed, err)
		}
		reclose, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: issue.ID})
		if err != nil || reclose.Changed {
			t.Fatalf("idempotent reclose = %#v, %v; want unchanged", reclose, err)
		}
	})
	t.Run("force reopen wisp", func(t *testing.T) {
		parent, child := makeIssue("ops-parent", types.StatusOpen, false), makeIssue("ops-child", types.StatusOpen, false)
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}, "seed"); err != nil {
			t.Fatal(err)
		}
		_, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent.ID})
		if !errors.Is(err, publicops.ErrCloseOpenChildren) {
			t.Fatalf("children=%v", err)
		}
		forced, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: parent.ID, Force: true})
		if err != nil || forced.OpenChildren != 1 {
			t.Fatalf("force=%#v,%v", forced, err)
		}
		opened, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: parent.ID})
		if err != nil || !opened.Changed {
			t.Fatalf("reopen=%#v,%v", opened, err)
		}
		wisp := makeIssue("ops-wisp", types.StatusOpen, true)
		if _, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: wisp.ID}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEmbeddedIssueOperationsLifecycleRemainingCausalCases(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_life_remaining")
	ctx := t.Context()
	ops, _ := embeddeddolt.NewIssueOperations(te.store)
	makeIssue := func(id string, status types.Status) *types.Issue {
		issue := &types.Issue{ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	t.Run("blocker refusal and force leave no partial mutation", func(t *testing.T) {
		blocker, target := makeIssue("ops-blocker", types.StatusOpen), makeIssue("ops-blocked", types.StatusOpen)
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: target.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}, "seed"); err != nil {
			t.Fatal(err)
		}
		_, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: target.ID})
		if !errors.Is(err, publicops.ErrCloseBlocked) {
			t.Fatalf("blocker=%v", err)
		}
		got, _ := te.store.GetIssue(ctx, target.ID)
		if got.Status != types.StatusOpen {
			t.Fatalf("refusal mutated=%#v", got)
		}
		forced, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: target.ID, Force: true})
		if err != nil || !forced.Changed {
			t.Fatalf("force=%#v,%v", forced, err)
		}
	})
	t.Run("reopen version before non-done noop and custom done", func(t *testing.T) {
		open := makeIssue("ops-open", types.StatusOpen)
		stale := open.RowVersion + 1
		_, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: open.ID, ExpectedVersion: &stale})
		if !errors.Is(err, publicops.ErrVersionMismatch) {
			t.Fatalf("reopen version=%v", err)
		}
		if err := te.store.SetConfig(ctx, "status.custom", "archived:done"); err != nil {
			t.Fatal(err)
		}
		custom := makeIssue("ops-custom", types.Status("archived"))
		reopened, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: custom.ID})
		if err != nil || !reopened.Changed || reopened.Issue.Status != types.StatusOpen {
			t.Fatalf("custom=%#v,%v", reopened, err)
		}
	})
}

func embeddedIssueParentIDs(issue *types.Issue) string {
	parents := make([]string, 0)
	for _, dependency := range issue.Dependencies {
		if dependency.Type == types.DepParentChild {
			parents = append(parents, dependency.DependsOnID)
		}
	}
	return strings.Join(parents, ",")
}

func sameEmbeddedMetadataJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func TestEmbeddedIssueOperationsUpdateAllScalarAndPointerFieldsReportChanged(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_scalar_changed")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	minutes, external := 15, "external"
	base := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "title", Description: "description", Design: "design", AcceptanceCriteria: "criteria", Notes: "notes", SpecID: "spec", AwaitID: "await", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Assignee: "worker", Owner: "owner", ClosedBySession: "session", EstimatedMinutes: &minutes, ExternalRef: &external, DueAt: &when, DeferUntil: &when}
	}
	cases := []struct {
		name  string
		patch publicops.IssuePatch
		check func(*types.Issue) bool
	}{
		{"title", publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "changed"}}, func(i *types.Issue) bool { return i.Title == "changed" }},
		{"description clear", publicops.IssuePatch{Description: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.Description == "" }},
		{"design clear", publicops.IssuePatch{Design: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.Design == "" }},
		{"acceptance criteria clear", publicops.IssuePatch{AcceptanceCriteria: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.AcceptanceCriteria == "" }},
		{"notes clear", publicops.IssuePatch{Notes: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.Notes == "" }},
		{"append notes", publicops.IssuePatch{AppendNotes: publicops.Field[string]{Set: true, Value: "later"}}, func(i *types.Issue) bool { return i.Notes == "notes\nlater" }},
		{"spec ID clear", publicops.IssuePatch{SpecID: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.SpecID == "" }},
		{"await ID clear", publicops.IssuePatch{AwaitID: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.AwaitID == "" }},
		{"status", publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusInProgress}}, func(i *types.Issue) bool { return i.Status == publicops.StatusInProgress }},
		{"priority zero", publicops.IssuePatch{Priority: publicops.Field[int]{Set: true}}, func(i *types.Issue) bool { return i.Priority == 0 }},
		{"issue type", publicops.IssuePatch{IssueType: publicops.Field[publicops.IssueType]{Set: true, Value: types.TypeBug}}, func(i *types.Issue) bool { return i.IssueType == types.TypeBug }},
		{"assignee clear", publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.Assignee == "" }},
		{"owner clear", publicops.IssuePatch{Owner: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.Owner == "" }},
		{"closed by session clear", publicops.IssuePatch{ClosedBySession: publicops.Field[string]{Set: true}}, func(i *types.Issue) bool { return i.ClosedBySession == "" }},
		{"estimated minutes clear", publicops.IssuePatch{EstimatedMinutes: publicops.Field[*int]{Set: true}}, func(i *types.Issue) bool { return i.EstimatedMinutes == nil }},
		{"external ref clear", publicops.IssuePatch{ExternalRef: publicops.Field[*string]{Set: true}}, func(i *types.Issue) bool { return i.ExternalRef == nil }},
		{"due at clear", publicops.IssuePatch{DueAt: publicops.Field[*time.Time]{Set: true}}, func(i *types.Issue) bool { return i.DueAt == nil }},
		{"defer until clear", publicops.IssuePatch{DeferUntil: publicops.Field[*time.Time]{Set: true}}, func(i *types.Issue) bool { return i.DeferUntil == nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := base("ops-scalars-" + strings.ReplaceAll(tc.name, " ", "-"))
			if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
				t.Fatal(err)
			}
			result, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: tc.patch})
			if err != nil || !result.Changed || !tc.check(result.Issue) {
				t.Fatalf("Update(%s) = %#v, %v", tc.name, result, err)
			}
			stored, err := te.store.GetIssue(ctx, issue.ID)
			if err != nil || !tc.check(stored) {
				t.Fatalf("stored %s = %#v, %v", tc.name, stored, err)
			}
			if tc.name != "append notes" {
				noOp, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: tc.patch})
				if err != nil || noOp.Changed {
					t.Fatalf("same-value %s = %#v, %v", tc.name, noOp, err)
				}
				after, err := te.store.GetIssue(ctx, issue.ID)
				if err != nil || after.RowVersion != stored.RowVersion {
					t.Fatalf("same-value %s wrote row version %d -> %d, %v", tc.name, stored.RowVersion, after.RowVersion, err)
				}
			}
		})
	}
	issue := base("ops-notes-conflict")
	if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	_, err = ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "must rollback"}, Notes: publicops.Field[string]{Set: true, Value: "replace"}, AppendNotes: publicops.Field[string]{Set: true, Value: "append"}}})
	if err == nil {
		t.Fatal("Notes plus AppendNotes succeeded")
	}
	stored, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil || stored.Title != "title" || stored.Notes != "notes" {
		t.Fatalf("notes conflict persisted %#v, %v", stored, err)
	}
}

func TestEmbeddedIssueOperationsPersistenceRollbackWispAndDetachedResults(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_persistence_aggregate")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []struct{ from, to publicops.PersistenceMode }{{publicops.PersistenceModePersistent, publicops.PersistenceModeEphemeral}, {publicops.PersistenceModeEphemeral, publicops.PersistenceModeNoHistory}, {publicops.PersistenceModeNoHistory, publicops.PersistenceModePersistent}} {
		id := "ops-persistence-" + string(transition.from) + "-" + string(transition.to)
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: transition.from == publicops.PersistenceModeEphemeral, NoHistory: transition.from == publicops.PersistenceModeNoHistory}, "seed"); err != nil {
			t.Fatal(err)
		}
		result, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: transition.to}}})
		if err != nil || !result.Changed || result.Issue.Ephemeral != (transition.to == publicops.PersistenceModeEphemeral) || result.Issue.NoHistory != (transition.to == publicops.PersistenceModeNoHistory) {
			t.Fatalf("transition %s->%s = %#v, %v", transition.from, transition.to, result, err)
		}
		noOp, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: transition.to}}})
		if err != nil || noOp.Changed {
			t.Fatalf("same persistence %s = %#v, %v", transition.to, noOp, err)
		}
	}
	unversioned := &types.Issue{ID: "ops-persistence-rollback", Title: "original", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, StorageClass: types.StorageClassUnversioned, Labels: []string{"keep"}, Metadata: json.RawMessage(`{"keep":true}`)}
	if err := te.store.CreateIssue(ctx, unversioned, "seed"); err != nil {
		t.Fatal(err)
	}
	_, err = ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: unversioned.ID, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "must rollback"}, Labels: publicops.LabelPatch{Add: []string{"must-rollback"}}, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"must_rollback": json.RawMessage(`true`)}}, Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModeEphemeral}}})
	if err == nil {
		t.Fatal("unversioned demotion succeeded")
	}
	stored, err := te.store.GetIssue(ctx, unversioned.ID)
	if err != nil || stored.Title != "original" || strings.Join(stored.Labels, ",") != "keep" || !sameEmbeddedMetadataJSON(stored.Metadata, json.RawMessage(`{"keep":true}`)) || stored.Ephemeral || stored.NoHistory {
		t.Fatalf("failed demotion left state %#v, %v", stored, err)
	}
	external := "external"
	wisp := &types.Issue{ID: "ops-wisp-detached", Title: "wisp", Notes: "before", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true, Labels: []string{"keep"}, Metadata: json.RawMessage(`{"keep":true}`), ExternalRef: &external}
	if err := te.store.CreateIssue(ctx, wisp, "seed"); err != nil {
		t.Fatal(err)
	}
	updated, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: wisp.ID, Patch: publicops.IssuePatch{AppendNotes: publicops.Field[string]{Set: true, Value: "after"}, Labels: publicops.LabelPatch{Add: []string{"added"}}, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"added": json.RawMessage(`true`)}}}})
	if err != nil || !updated.Changed || updated.Issue.Notes != "before\nafter" || strings.Join(updated.Issue.Labels, ",") != "added,keep" {
		t.Fatalf("wisp update = %#v, %v", updated, err)
	}
	updated.Issue.Labels[0], updated.Issue.Metadata[2], *updated.Issue.ExternalRef = "corrupt", 'X', "corrupt"
	stored, err = te.store.GetIssue(ctx, wisp.ID)
	if err != nil || strings.Join(stored.Labels, ",") != "added,keep" || !sameEmbeddedMetadataJSON(stored.Metadata, json.RawMessage(`{"added":true,"keep":true}`)) || stored.ExternalRef == nil || *stored.ExternalRef != "external" {
		t.Fatalf("update result aliases store %#v, %v", stored, err)
	}
	closed, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: wisp.ID})
	if err != nil || !closed.Changed || closed.Issue.Status != publicops.StatusClosed {
		t.Fatalf("wisp close = %#v, %v", closed, err)
	}
	closed.Issue.Metadata[2] = 'X'
	reopened, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: wisp.ID})
	if err != nil || !reopened.Changed || reopened.Issue.Status != publicops.StatusOpen {
		t.Fatalf("wisp reopen = %#v, %v", reopened, err)
	}
	reopened.Issue.Labels[0] = "corrupt"
	stored, err = te.store.GetIssue(ctx, wisp.ID)
	if err != nil || strings.Join(stored.Labels, ",") != "added,keep" || !sameEmbeddedMetadataJSON(stored.Metadata, json.RawMessage(`{"added":true,"keep":true}`)) {
		t.Fatalf("lifecycle result aliases store %#v, %v", stored, err)
	}
}
