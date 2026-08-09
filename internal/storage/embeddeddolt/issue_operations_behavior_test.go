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

// This file is the residue of the embedded Lifecycle behavior suite. Most of
// what it used to hold now runs on all three backends from
// backend/conformance/issue_operations_contract.go and
// lifecycle_close_reopen_contract.go, which assert the same promises against
// the stored ROWS rather than through the role's own answer.
//
// What moved out, and where it landed:
//   - the occupied-ID refusal -> RunIssueOperationsCreateRefusesAnOccupiedID;
//   - the foreign-ID-prefix refusal -> RunIssueOperationsCreateRefusesAForeignIDPrefix;
//   - the metadata Merge/Set/Unset ordering and the Replace exclusivity guard ->
//     RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset;
//   - the LabelPatch ordering and its no-op -> RunIssueOperationsUpdateLabelPatchOrdering;
//   - the ParentID replacement, clear and both no-ops ->
//     RunIssueOperationsUpdateParentIDReplacesTheParentEdge and
//     ...ReplacesEveryParent;
//   - the version and field compare-and-sets ->
//     RunIssueOperationsUpdateAssigneeTransferFence and
//     ...UpdateConditionalGuardsGateOrdinaryEdits;
//   - the close/reopen refusals, the idempotent re-close and the configured
//     done category -> the Lifecycle close/reopen contract.
//
// What stayed, and why:
//   - CreateRequest's relation AGGREGATE: the reverse edge's ThreadID and the
//     WaitsFor spawner count have no contract case yet, and the leaf clauses
//     they would cite are about the created issue's Dependencies field rather
//     than about staging, which is what the staging contract covers;
//   - the claim matrix, because its invalid-combination table is the only pin
//     on issueops.ValidateUpdateRequest's two request-shape rules, and because
//     a wisp claim is a plane the contract deliberately does not speak for;
//   - the scalar/pointer field table, which pins Changed and the same-value
//     no-op for every field the patch carries — breadth the contract does not
//     attempt;
//   - the persistence transitions and the wisp-plane result detachment, for
//     the same plane reason.

// TestEmbeddedIssueOperationsCreateAggregatesRelations pins what a create's
// RESULT carries when the request names several relationships at once: the
// parent's inherited labels, a forward and a reverse edge, and a waits-for
// spawner all land on one detached snapshot, and the reverse edge keeps the
// thread id the request gave it.
//
// The ThreadID round trip is the reason this survived the move to conformance:
// RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables proves the
// reverse row is staged and committed, but reads no thread id, so nothing else
// notices an implementation that drops it.
func TestEmbeddedIssueOperationsCreateAggregatesRelations(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops")
	ctx := t.Context()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	create := func(id string) *types.Issue {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return issue
	}

	parent := create("ops-parent")
	spawner := create("ops-spawner")
	target := create("ops-target")
	if err := te.store.AddLabel(ctx, parent.ID, "parent-label", "seed"); err != nil {
		t.Fatal(err)
	}

	result, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:                   "writer",
		ForceIDPrefix:           true,
		Issue:                   &types.Issue{ID: "ops-aggregate", Title: "aggregate", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		ParentID:                parent.ID,
		InheritLabelsFromParent: true,
		Dependencies: []publicops.CreateDependency{
			{TargetID: target.ID, Type: types.DepRelated, ThreadID: "thread"},
			{TargetID: target.ID, Type: types.DepRelatesTo, Reverse: true, ThreadID: "reverse-thread"},
		},
		WaitsFor: &publicops.WaitsFor{SpawnerID: spawner.ID},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(result.Issue.Labels) != 1 || result.Issue.Labels[0] != "parent-label" || len(result.Issue.Dependencies) != 3 {
		t.Fatalf("Create result aggregate = %#v", result.Issue)
	}
	inbound, err := te.store.GetDependentRecords(ctx, result.Issue.ID, "", 10, "")
	if err != nil || len(inbound) != 1 || inbound[0].IssueID != target.ID || inbound[0].ThreadID != "reverse-thread" {
		t.Fatalf("reverse dependency = %#v, %v", inbound, err)
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
		override := create("ops-override", "", types.StatusOpen, false)
		result, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: override.ID, Claim: true, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "reviewer"}, Status: publicops.Field[publicops.Status]{Set: true, Value: publicops.StatusOpen}}})
		if err != nil || !result.Changed || result.Issue.Assignee != "reviewer" || result.Issue.Status != publicops.StatusOpen {
			t.Fatalf("claim override = %#v, %v", result, err)
		}
	})
	t.Run("wisp routing", func(t *testing.T) {
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

// TestEmbeddedIssueOperationsCloseAndReopenAWisp is the ephemeral half of the
// close/reopen lifecycle. The three-backend contract deliberately says nothing
// about which PLANE a Close or Reopen id resolves against — the leaf makes no
// promise to assert (see the SPEC-GAP note in
// backend/conformance/lifecycle_close_reopen_contract.go) — so this is the only
// pin that a wisp closes and reopens at all.
func TestEmbeddedIssueOperationsCloseAndReopenAWisp(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ops_lifecycle")
	ctx := t.Context()
	ops, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatal(err)
	}
	wisp := &types.Issue{ID: "ops-wisp", Title: "wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := te.store.CreateIssue(ctx, wisp, "seed"); err != nil {
		t.Fatal(err)
	}
	closed, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: wisp.ID, Reason: "because", Session: "session-1"})
	if err != nil || !closed.Changed || closed.Issue.Status != publicops.StatusClosed {
		t.Fatalf("wisp close = %#v, %v", closed, err)
	}
	if closed.Issue.ClosedBySession != "session-1" || closed.Issue.CloseReason != "because" {
		t.Fatalf("wisp close attribution = (%q, %q), want (%q, %q)",
			closed.Issue.CloseReason, closed.Issue.ClosedBySession, "because", "session-1")
	}
	reopened, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: wisp.ID})
	if err != nil || !reopened.Changed || reopened.Issue.Status != publicops.StatusOpen {
		t.Fatalf("wisp reopen = %#v, %v", reopened, err)
	}
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
