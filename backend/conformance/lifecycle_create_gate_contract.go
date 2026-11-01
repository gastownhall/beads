package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// This file and its two siblings (lifecycle_update_gate_contract.go,
// batch_apply_gate_contract.go) hold the gate-member clauses of Lifecycle.Create,
// Lifecycle.Update and BatchApplier: await_type, await_id, timeout and waiters
// on a create, await_id and waiters on a patch, and the one set of bounds every
// route refuses with a *publicops.GateFieldError.
//
// THE BOUNDS ARE THE COLUMNS', and the boundary is asserted from both sides: a
// value AT the bound lands whole, a value one past it is refused naming its
// member and writes nothing. await_type is VARCHAR(32) and await_id
// VARCHAR(255), both counted in characters, so each at-bound value is
// multi-byte — a byte count would refuse it. waiters is a TEXT column holding
// a JSON array, bounded in BYTES. A negative timeout is the one refusal that is
// not a width.
//
// NOTHING ELSE IS A RULE, and two cases say so: a gate member lands on an
// issue of any type (local `bd update --await-id` has always worked on any
// row), and waiters are stored as given, duplicates included (deduplication is
// the CLI's). A backend that grew a stricter rule fails here.

// gateFieldAtBoundAwaitType is a 32-character, 64-byte await_type.
var gateFieldAtBoundAwaitType = strings.Repeat("é", 32)

// gateFieldAtBoundAwaitID is a 255-character, 510-byte await_id.
var gateFieldAtBoundAwaitID = strings.Repeat("é", types.MaxFieldLen)

// gateFieldAtBoundWaiters is one waiter whose JSON array encoding is exactly
// types.MaxTextBytes bytes: the entry plus `["` and `"]`.
func gateFieldAtBoundWaiters() []string {
	return []string{strings.Repeat("w", types.MaxTextBytes-4)}
}

// gateFieldOverBoundWaiters encodes to one byte past the TEXT column.
func gateFieldOverBoundWaiters() []string {
	return []string{strings.Repeat("w", types.MaxTextBytes-3)}
}

// assertGateFieldError checks err is ErrValidation carried by a
// *publicops.GateFieldError naming field.
func assertGateFieldError(t *testing.T, err error, label, field string) {
	t.Helper()
	if !errors.Is(err, publicops.ErrValidation) {
		t.Fatalf("%s: error = %v, want ErrValidation", label, err)
	}
	var fieldErr *publicops.GateFieldError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("%s: error = %v, want a *GateFieldError naming %s", label, err, field)
	}
	if fieldErr.Field != field {
		t.Errorf("%s: GateFieldError.Field = %q, want %q", label, fieldErr.Field, field)
	}
}

// gateMembers renders the four gate members off an issue for comparison.
func gateMembers(issue *types.Issue) string {
	waiters := issue.Waiters
	if len(waiters) == 0 {
		waiters = nil
	}
	return fmt.Sprintf("await_type=%q await_id=%q timeout=%s waiters=%q", issue.AwaitType, issue.AwaitID, issue.Timeout, waiters)
}

func assertGateMembers(t *testing.T, id, label string, issue *types.Issue, awaitType, awaitID string, timeout time.Duration, waiters []string) {
	t.Helper()
	if issue == nil {
		t.Fatalf("%s %s: no issue to read the gate members off", id, label)
	}
	if len(waiters) == 0 {
		waiters = nil
	}
	want := fmt.Sprintf("await_type=%q await_id=%q timeout=%s waiters=%q", awaitType, awaitID, timeout, waiters)
	if got := gateMembers(issue); got != want {
		// The at-bound values are long; report sizes rather than the values.
		if len(got)+len(want) > 2000 {
			t.Errorf("%s %s gate members differ (got %d bytes, want %d bytes; await_type %d/%d runes, await_id %d/%d runes, waiters %d/%d entries)",
				id, label, len(got), len(want),
				len([]rune(issue.AwaitType)), len([]rune(awaitType)), len([]rune(issue.AwaitID)), len([]rune(awaitID)),
				len(issue.Waiters), len(waiters))
			return
		}
		t.Errorf("%s %s gate members = %s, want %s", id, label, got, want)
	}
}

// RunLifecycleCreateWritesTheGateMembers pins that Lifecycle.Create stores all
// four gate members (CreateRequest.Issue's "gate" fields) on the row and
// answers them on the result, with the waiter list stored as given, duplicate
// included, and on an issue that is not of the gate type.
func RunLifecycleCreateWritesTheGateMembers(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	gate := fixture.IssuePrefix + "-lcg-gate"
	issue := lifecycleCreateIssue(gate)
	issue.IssueType = types.TypeGate
	issue.AwaitType = "gh:run"
	issue.AwaitID = "12345"
	issue.Timeout = 90 * time.Minute
	issue.Waiters = []string{"rig/workers/a", "rig/workers/b", "rig/workers/a"}
	created, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: issue})
	if err != nil {
		t.Fatalf("create gate %s: %v", gate, err)
	}
	want := []string{"rig/workers/a", "rig/workers/b", "rig/workers/a"}
	assertGateMembers(t, gate, "in the create result", created.Issue, "gh:run", "12345", 90*time.Minute, want)
	assertGateMembers(t, gate, "in the stored row", lifecycleCreateRow(t, ctx, fixture, gate), "gh:run", "12345", 90*time.Minute, want)

	// Any type carries them: no gate-type-only rule.
	task := fixture.IssuePrefix + "-lcg-task"
	carrier := lifecycleCreateIssue(task)
	carrier.AwaitType = "formula-defined"
	carrier.AwaitID = "bead:" + gate
	carrier.Waiters = []string{"mail:someone"}
	if _, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: carrier}); err != nil {
		t.Fatalf("create task %s carrying gate members: %v", task, err)
	}
	assertGateMembers(t, task, "in the stored row", lifecycleCreateRow(t, ctx, fixture, task), "formula-defined", "bead:"+gate, 0, []string{"mail:someone"})
}

// RunLifecycleCreateBoundsTheGateMembers pins the four GateFieldError bounds
// on create from both sides: each at-bound value lands whole, each value one
// past it is refused as ErrValidation naming its member, and the refused
// create leaves no row.
func RunLifecycleCreateBoundsTheGateMembers(t *testing.T, ctx context.Context, fixture LifecycleCreateFixture) {
	t.Helper()

	atBound := fixture.IssuePrefix + "-lcg-atbound"
	issue := lifecycleCreateIssue(atBound)
	issue.IssueType = types.TypeGate
	issue.AwaitType = gateFieldAtBoundAwaitType
	issue.AwaitID = gateFieldAtBoundAwaitID
	issue.Timeout = 0
	issue.Waiters = gateFieldAtBoundWaiters()
	if _, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: issue}); err != nil {
		t.Fatalf("create %s with every gate member at its bound: %v", atBound, err)
	}
	assertGateMembers(t, atBound, "in the stored row", lifecycleCreateRow(t, ctx, fixture, atBound),
		gateFieldAtBoundAwaitType, gateFieldAtBoundAwaitID, 0, gateFieldAtBoundWaiters())

	for _, test := range []struct {
		name   string
		field  string
		mutate func(*types.Issue)
	}{
		{"await_type", publicops.GateFieldAwaitType, func(i *types.Issue) { i.AwaitType = gateFieldAtBoundAwaitType + "x" }},
		{"await_id", publicops.GateFieldAwaitID, func(i *types.Issue) { i.AwaitID = gateFieldAtBoundAwaitID + "x" }},
		{"timeout", publicops.GateFieldTimeout, func(i *types.Issue) { i.Timeout = -time.Nanosecond }},
		{"waiters", publicops.GateFieldWaiters, func(i *types.Issue) { i.Waiters = gateFieldOverBoundWaiters() }},
	} {
		id := fixture.IssuePrefix + "-lcg-over-" + strings.ReplaceAll(test.name, "_", "")
		refused := lifecycleCreateIssue(id)
		refused.IssueType = types.TypeGate
		test.mutate(refused)
		_, err := fixture.Lifecycle.Create(ctx, publicops.CreateRequest{Actor: "writer", ForceIDPrefix: true, Issue: refused})
		assertGateFieldError(t, err, "create with "+test.name+" past its bound", test.field)
		assertLifecycleCreateAbsent(t, ctx, fixture, id, "after the refused "+test.name)
	}
}
