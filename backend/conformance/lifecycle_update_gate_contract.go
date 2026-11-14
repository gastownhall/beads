package conformance

import (
	"context"
	"testing"
	"time"

	publicops "github.com/steveyegge/beads/issueops"
)

// The patch half of the gate-member clauses; see lifecycle_create_gate_contract.go.

// RunLifecycleUpdatePatchesTheGateMembers pins IssuePatch.AwaitID and
// IssuePatch.Waiters: a set value lands and is answered on the result, waiters
// is a WHOLE-VALUE replacement stored as given (duplicates kept), a same-value
// patch is a no-op, a clear (empty await_id, nil waiters) empties both, and a
// second clear is a no-op. await_type and timeout are not patch members, so
// they ride through every edit untouched. The row is a task: no gate-type-only
// rule.
func RunLifecycleUpdatePatchesTheGateMembers(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lug-gate"
	seeded := lifecycleUpdateIssue(id)
	seeded.AwaitType = "timer"
	seeded.AwaitID = "seeded-await"
	seeded.Timeout = time.Minute
	seeded.Waiters = []string{"seeded"}
	seedLifecycleUpdateIssue(t, ctx, fixture, seeded)

	patch := publicops.IssuePatch{
		AwaitID: publicops.Field[string]{Set: true, Value: "run-42"},
		Waiters: publicops.Field[[]string]{Set: true, Value: []string{"w1", "w2", "w1"}},
	}
	wantWaiters := []string{"w1", "w2", "w1"}
	updated, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: patch})
	if err != nil {
		t.Fatalf("patch the gate members of %s: %v", id, err)
	}
	if !updated.Changed {
		t.Errorf("patching the gate members of %s reported Changed = false, want a committed edit", id)
	}
	assertGateMembers(t, id, "in the update result", updated.Issue, "timer", "run-42", time.Minute, wantWaiters)
	assertGateMembers(t, id, "in the stored row", lifecycleUpdateRow(t, ctx, fixture, id), "timer", "run-42", time.Minute, wantWaiters)

	after := lifecycleUpdateRow(t, ctx, fixture, id)
	again, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: patch})
	if err != nil {
		t.Fatalf("re-apply the same gate patch to %s: %v", id, err)
	}
	if again.Changed {
		t.Errorf("re-applying the same gate patch to %s reported Changed = true, want false", id)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the same-value gate patch", after)

	clear := publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		AwaitID: publicops.Field[string]{Set: true},
		Waiters: publicops.Field[[]string]{Set: true},
	}}
	cleared, err := fixture.Lifecycle.Update(ctx, clear)
	if err != nil {
		t.Fatalf("clear the gate members of %s: %v", id, err)
	}
	if !cleared.Changed {
		t.Errorf("clearing the gate members of %s reported Changed = false, want a committed edit", id)
	}
	assertGateMembers(t, id, "in the clear's result", cleared.Issue, "timer", "", time.Minute, nil)
	assertGateMembers(t, id, "in the cleared row", lifecycleUpdateRow(t, ctx, fixture, id), "timer", "", time.Minute, nil)

	afterClear := lifecycleUpdateRow(t, ctx, fixture, id)
	reclear, err := fixture.Lifecycle.Update(ctx, clear)
	if err != nil {
		t.Fatalf("re-clear the gate members of %s: %v", id, err)
	}
	if reclear.Changed {
		t.Errorf("re-clearing the already-empty gate members of %s reported Changed = true, want false", id)
	}
	assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the re-clear", afterClear)

	// An explicitly empty list clears exactly as a nil one does.
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Waiters: publicops.Field[[]string]{Set: true, Value: []string{"back"}},
	}}); err != nil {
		t.Fatalf("re-set the waiters of %s: %v", id, err)
	}
	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		Waiters: publicops.Field[[]string]{Set: true, Value: []string{}},
	}}); err != nil {
		t.Fatalf("clear the waiters of %s with an empty list: %v", id, err)
	}
	assertGateMembers(t, id, "after the empty-list clear", lifecycleUpdateRow(t, ctx, fixture, id), "timer", "", time.Minute, nil)
}

// RunLifecycleUpdateBoundsTheGateMembers pins the patch half of the
// GateFieldError bounds from both sides: at-bound await_id and waiters land
// whole, one past either is refused as ErrValidation naming its member, and
// the refusal writes nothing — not the gate member, and not an ordinary member
// riding in the same patch.
func RunLifecycleUpdateBoundsTheGateMembers(t *testing.T, ctx context.Context, fixture LifecycleUpdateFixture) {
	t.Helper()

	id := fixture.IssuePrefix + "-lug-bounds"
	seedLifecycleUpdateIssue(t, ctx, fixture, lifecycleUpdateIssue(id))

	if _, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: publicops.IssuePatch{
		AwaitID: publicops.Field[string]{Set: true, Value: gateFieldAtBoundAwaitID},
		Waiters: publicops.Field[[]string]{Set: true, Value: gateFieldAtBoundWaiters()},
	}}); err != nil {
		t.Fatalf("patch %s with await_id and waiters at their bounds: %v", id, err)
	}
	assertGateMembers(t, id, "in the stored row", lifecycleUpdateRow(t, ctx, fixture, id), "", gateFieldAtBoundAwaitID, 0, gateFieldAtBoundWaiters())

	before := lifecycleUpdateRow(t, ctx, fixture, id)
	for _, test := range []struct {
		name  string
		field string
		patch publicops.IssuePatch
	}{
		{"await_id", publicops.GateFieldAwaitID, publicops.IssuePatch{
			Title:   publicops.Field[string]{Set: true, Value: "must not land"},
			AwaitID: publicops.Field[string]{Set: true, Value: gateFieldAtBoundAwaitID + "x"},
		}},
		{"waiters", publicops.GateFieldWaiters, publicops.IssuePatch{
			Title:   publicops.Field[string]{Set: true, Value: "must not land"},
			Waiters: publicops.Field[[]string]{Set: true, Value: gateFieldOverBoundWaiters()},
		}},
	} {
		_, err := fixture.Lifecycle.Update(ctx, publicops.UpdateRequest{Actor: "writer", IssueID: id, Patch: test.patch})
		assertGateFieldError(t, err, "patch with "+test.name+" past its bound", test.field)
		assertLifecycleUpdateRowUnchanged(t, ctx, fixture, id, "after the refused "+test.name, before)
		assertGateMembers(t, id, "after the refused "+test.name, lifecycleUpdateRow(t, ctx, fixture, id), "", gateFieldAtBoundAwaitID, 0, gateFieldAtBoundWaiters())
	}
}
