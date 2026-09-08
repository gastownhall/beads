package dolt

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestIssueOperationsCreateAggregatesEveryRelationItWasGiven is the residue of
// TestIssueOperationsGuardedVerbs.
//
// Its other five subtests went to the contract, which states the same promises
// at three backends and is stronger on each: the occupied-ID refusal is
// RunLifecycleCreateRefusesAnOccupiedID (both planes, both directions,
// raw title/type/labels), the foreign prefix is
// RunLifecycleCreateRefusesAForeignIDPrefix, the inherited label is
// RunLifecycleCreateInheritsParentLabels, the CAS and guard legs are
// RunLifecycleUpdateAssigneeTransferFence and
// RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits, the metadata
// patch is RunLifecycleUpdateMetadataPatchOrdersMergeSetUnset, and the
// close/reopen legs are the whole Lifecycle contract.
//
// What no contract case does is COUNT the edges one aggregate create produces.
// Three request fields each contribute one -- ParentID, WaitsFor, and the
// Dependencies list -- and an implementation that dropped any of them still
// satisfies every case that reads only the field it cares about. The reverse
// edge's thread id is deliberately not re-asserted here:
// issue_operations_staging_test.go:111-133 already pins it at this backend,
// with its metadata.
func TestIssueOperationsCreateAggregatesEveryRelationItWasGiven(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}

	for _, id := range []string{"ops-parent", "ops-spawner", "ops-target"} {
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	result, err := operations.Create(ctx, publicops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{ID: "foreign-aggregate", Title: "aggregate", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		ParentID:      "ops-parent",
		WaitsFor:      &publicops.WaitsFor{SpawnerID: "ops-spawner"},
		Dependencies:  []publicops.CreateDependency{{TargetID: "ops-target", Type: types.DepRelated}},
	})
	if err != nil {
		t.Fatalf("aggregate Create: %v", err)
	}
	if len(result.Issue.Dependencies) != 3 {
		t.Fatalf("Create dependencies = %#v, want three: the parent edge, the waits-for edge, and the requested one", result.Issue.Dependencies)
	}
	byType := map[types.DependencyType]string{}
	for _, dependency := range result.Issue.Dependencies {
		byType[dependency.Type] = dependency.DependsOnID
	}
	for _, want := range []struct {
		kind   types.DependencyType
		target string
	}{
		{types.DepParentChild, "ops-parent"},
		{types.DepWaitsFor, "ops-spawner"},
		{types.DepRelated, "ops-target"},
	} {
		if byType[want.kind] != want.target {
			t.Errorf("%s edge points at %q, want %q", want.kind, byType[want.kind], want.target)
		}
	}
}

// TestIssueOperationsUpdateRefusesIncoherentClaimRequests is what is left of
// TestIssueOperationsUpdateClaimCASAndTransferMatrix.
//
// Its CAS legs, its claim-eligibility legs and its transfer legs all state
// promises RunLifecycleUpdateAssigneeTransferFence,
// RunIssueOperationsUpdateClaimConflictCarriesTheLosingState and
// RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits make at three
// backends, each of them reading the stored row rather than only the result.
//
// The table below does not: it is the only pin in the tree on
// ValidateUpdateRequest's refusal of REQUEST SHAPES that cannot mean anything
// -- a claim that also names an expected assignee, a claim that also forces a
// transfer, a force with nothing to transfer. Those are deterministic
// validation failures with no backend in them at all, which is why the natural
// home is issueops's own millisecond-level update_validation_test.go rather
// than a contract case; moving it there is a follow-up outside this file's
// partition, so it stays here until then.
//
// The wisp claim stays with it. SPEC-GAP bd-yby99.31 records that no leaf
// clause says which plane a lifecycle id resolves against, so a contract case
// for it would be inventing the promise.
func TestIssueOperationsUpdateRefusesIncoherentClaimRequests(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string, wisp bool) *types.Issue {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
		if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
			t.Fatal(err)
		}
		return issue
	}

	wisp := create("ops-wisp", true)
	wispResult, err := operations.Update(ctx, publicops.UpdateRequest{Actor: "worker", IssueID: wisp.ID, Claim: true})
	if err != nil || !wispResult.Changed {
		t.Fatalf("wisp claim = %#v, %v", wispResult, err)
	}

	subject := create("ops-override", false)
	unassigned := ""
	for name, request := range map[string]publicops.UpdateRequest{
		"claim with an expected assignee":  {Actor: "worker", IssueID: subject.ID, Claim: true, ExpectedAssignee: &unassigned},
		"claim with a forced transfer":     {Actor: "worker", IssueID: subject.ID, Claim: true, ForceAssigneeTransfer: true},
		"forced transfer with no assignee": {Actor: "worker", IssueID: subject.ID, ForceAssigneeTransfer: true},
		"forced transfer under a guard":    {Actor: "worker", IssueID: subject.ID, ForceAssigneeTransfer: true, ExpectedAssignee: &unassigned, Patch: publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "next"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := operations.Update(ctx, request); !errors.Is(err, publicops.ErrValidation) {
				t.Fatalf("%s: err = %v, want ErrValidation", name, err)
			}
		})
	}
}

// TestIssueOperationsCloseAndReopenAWisp is what is left of
// TestIssueOperationsLifecycleContractMatrix.
//
// Its durable legs -- the first close's attribution, the idempotent re-close,
// the open-child refusal and its forced count, the ExpectedVersion ordering,
// and the reopen no-op -- are RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose,
// RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing,
// RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps and
// RunLifecycleReopenLeavesNonDoneStatusesUnchanged, all at three backends and
// all reading the stored row.
//
// The ephemeral leg is not, and cannot be until the leaf says so: SPEC-GAP
// bd-yby99.31 records that neither Close nor Reopen states which PLANE an id
// resolves against, and the contract declines to assert a promise the doc does
// not make. Both `bd close` and `bd reopen` depend on it, so it is pinned here.
func TestIssueOperationsCloseAndReopenAWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	ops, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	wisp := &types.Issue{ID: "ops-life-wisp", Title: "ops-life-wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wisp, "seed"); err != nil {
		t.Fatal(err)
	}

	closed, err := ops.Close(ctx, publicops.CloseRequest{Actor: "writer", IssueID: wisp.ID})
	if err != nil || !closed.Changed || closed.Issue.Status != publicops.StatusClosed {
		t.Fatalf("wisp close = %#v, %v", closed, err)
	}
	reopened, err := ops.Reopen(ctx, publicops.ReopenRequest{Actor: "writer", IssueID: wisp.ID})
	if err != nil || !reopened.Changed || reopened.Issue.Status != publicops.StatusOpen {
		t.Fatalf("wisp reopen = %#v, %v", reopened, err)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM wisps WHERE id = ?", wisp.ID).Scan(&status); err != nil {
		t.Fatalf("read the wisp row: %v", err)
	}
	if types.Status(status) != types.StatusOpen {
		t.Fatalf("stored wisp status = %q, want %q -- the verbs resolved the id somewhere else", status, types.StatusOpen)
	}
}

// issueParentIDs is also called from issue_operations_staging_test.go, so it
// outlives the parent-replacement tests this file used to hold. stringPtr went
// with them: nothing else called it.
func issueParentIDs(issue *types.Issue) string {
	parents := make([]string, 0)
	for _, dependency := range issue.Dependencies {
		if dependency.Type == types.DepParentChild {
			parents = append(parents, dependency.DependsOnID)
		}
	}
	return strings.Join(parents, ",")
}

func sameMetadataJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func TestIssueOperationsUpdateAllScalarAndPointerFieldsReportChanged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	ops, err := NewIssueOperations(store)
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
	for n, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := base("ops-scalars-" + tc.name)
			issue.ID = strings.ReplaceAll(issue.ID, " ", "-")
			if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
				t.Fatal(err)
			}
			result, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: tc.patch})
			if err != nil || !result.Changed || !tc.check(result.Issue) {
				t.Fatalf("Update(%s) = %#v, %v", tc.name, result, err)
			}
			stored, err := store.GetIssue(ctx, issue.ID)
			if err != nil || !tc.check(stored) {
				t.Fatalf("stored %s = %#v, %v", tc.name, stored, err)
			}
			if tc.name != "append notes" {
				noOp, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: tc.patch})
				if err != nil || noOp.Changed {
					t.Fatalf("same-value %s = %#v, %v", tc.name, noOp, err)
				}
				after, err := store.GetIssue(ctx, issue.ID)
				if err != nil || after.RowVersion != stored.RowVersion {
					t.Fatalf("same-value %s wrote row version %d -> %d, %v", tc.name, stored.RowVersion, after.RowVersion, err)
				}
			}
			_ = n
		})
	}
	issue := base("ops-notes-conflict")
	if err := store.CreateIssue(ctx, issue, "seed"); err != nil {
		t.Fatal(err)
	}
	_, err = ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: issue.ID, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "must rollback"}, Notes: publicops.Field[string]{Set: true, Value: "replace"}, AppendNotes: publicops.Field[string]{Set: true, Value: "append"}}})
	if err == nil {
		t.Fatal("Notes plus AppendNotes succeeded")
	}
	stored, err := store.GetIssue(ctx, issue.ID)
	if err != nil || stored.Title != "title" || stored.Notes != "notes" {
		t.Fatalf("notes conflict persisted %#v, %v", stored, err)
	}
}

func TestIssueOperationsUpdatePersistenceContributionAndRollback(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	ops, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, transition := range []struct {
		from, to publicops.PersistenceMode
	}{
		{publicops.PersistenceModePersistent, publicops.PersistenceModeEphemeral}, {publicops.PersistenceModeEphemeral, publicops.PersistenceModeNoHistory}, {publicops.PersistenceModeNoHistory, publicops.PersistenceModePersistent},
	} {
		id := "ops-persistence-" + string(transition.from) + "-" + string(transition.to)
		ephemeral := transition.from == publicops.PersistenceModeEphemeral
		noHistory := transition.from == publicops.PersistenceModeNoHistory
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral, NoHistory: noHistory}, "seed"); err != nil {
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
	if err := store.CreateIssue(ctx, unversioned, "seed"); err != nil {
		t.Fatal(err)
	}
	_, err = ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: unversioned.ID, Patch: publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "must rollback"}, Labels: publicops.LabelPatch{Add: []string{"must-rollback"}}, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"must_rollback": json.RawMessage(`true`)}}, Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: publicops.PersistenceModeEphemeral}}})
	if err == nil {
		t.Fatal("unversioned demotion succeeded")
	}
	stored, err := store.GetIssue(ctx, unversioned.ID)
	if err != nil || stored.Title != "original" || strings.Join(stored.Labels, ",") != "keep" || !sameMetadataJSON(stored.Metadata, json.RawMessage(`{"keep":true}`)) || stored.Ephemeral || stored.NoHistory {
		t.Fatalf("failed demotion left state %#v, %v", stored, err)
	}
}

func TestIssueOperationsWispAggregateLifecycleAndResultDetachment(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	ops, err := NewIssueOperations(store)
	if err != nil {
		t.Fatal(err)
	}
	external := "external"
	wisp := &types.Issue{ID: "ops-wisp-detached", Title: "wisp", Notes: "before", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true, Labels: []string{"keep"}, Metadata: json.RawMessage(`{"keep":true}`), ExternalRef: &external}
	if err := store.CreateIssue(ctx, wisp, "seed"); err != nil {
		t.Fatal(err)
	}
	updated, err := ops.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: wisp.ID, Patch: publicops.IssuePatch{AppendNotes: publicops.Field[string]{Set: true, Value: "after"}, Labels: publicops.LabelPatch{Add: []string{"added"}}, Metadata: publicops.MetadataPatch{Set: map[string]json.RawMessage{"added": json.RawMessage(`true`)}}}})
	if err != nil || !updated.Changed || updated.Issue.Notes != "before\nafter" || strings.Join(updated.Issue.Labels, ",") != "added,keep" {
		t.Fatalf("wisp update = %#v, %v", updated, err)
	}
	updated.Issue.Labels[0], updated.Issue.Metadata[2], *updated.Issue.ExternalRef = "corrupt", 'X', "corrupt"
	stored, err := store.GetIssue(ctx, wisp.ID)
	if err != nil || strings.Join(stored.Labels, ",") != "added,keep" || !sameMetadataJSON(stored.Metadata, json.RawMessage(`{"added":true,"keep":true}`)) || stored.ExternalRef == nil || *stored.ExternalRef != "external" {
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
	stored, err = store.GetIssue(ctx, wisp.ID)
	if err != nil || strings.Join(stored.Labels, ",") != "added,keep" || !sameMetadataJSON(stored.Metadata, json.RawMessage(`{"added":true,"keep":true}`)) {
		t.Fatalf("lifecycle result aliases store %#v, %v", stored, err)
	}
}
