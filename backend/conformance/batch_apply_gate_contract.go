package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The BatchApplier half of the gate-member clauses; see
// lifecycle_create_gate_contract.go.

// RunBatchApplyCarriesTheGateMembers pins the gate members through
// BatchApplier: a create item stores all four, an update item replaces the
// waiter list of an existing row whole, a clear through an update item stores
// the empty list `[]`, and a create or update item past a bound refuses the
// WHOLE request as ErrValidation naming its member (on its *ItemError for the
// update item), so the earlier item rolls back with it.
func RunBatchApplyCarriesTheGateMembers(t *testing.T, ctx context.Context, fixture BatchApplyFixture) {
	t.Helper()

	gate := fixture.IssuePrefix + "-bag-gate"
	existing := fixture.IssuePrefix + "-bag-existing"
	batchApplySeedIssue(t, ctx, fixture, existing, types.StatusOpen)

	issue := batchApplyIssue(gate, "a gate made by a plan")
	issue.IssueType = types.TypeGate
	issue.AwaitType = "timer"
	issue.AwaitID = "await-plan"
	issue.Timeout = 2 * time.Hour
	issue.Waiters = []string{"a", "a", "b"}
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("gate", issue),
			batchApplyUpdate(publicops.Ref{ID: existing}, publicops.IssuePatch{
				AwaitID: publicops.Field[string]{Set: true, Value: "bead:" + gate},
				Waiters: publicops.Field[[]string]{Set: true, Value: []string{"x", "y"}},
			}),
		},
	})
	assertBatchApplyGateColumns(t, ctx, fixture, gate, "timer", "await-plan", 2*time.Hour, []string{"a", "a", "b"})
	assertBatchApplyGateColumns(t, ctx, fixture, existing, "", "bead:"+gate, 0, []string{"x", "y"})

	// A clear through an update item stores the empty JSON list, not NULL and
	// not the JSON null: the column is read raw here because every reader
	// above treats the three alike.
	batchApplyMust(t, ctx, fixture, publicops.ApplyBatchRequest{
		Actor: "apply-writer",
		Items: []publicops.ApplyItem{
			batchApplyUpdate(publicops.Ref{ID: existing}, publicops.IssuePatch{
				AwaitID: publicops.Field[string]{Set: true},
				Waiters: publicops.Field[[]string]{Set: true},
			}),
		},
	})
	assertBatchApplyGateColumns(t, ctx, fixture, existing, "", "", 0, nil)
	var clearedWaiters string
	if err := fixture.QueryScalar(ctx, "SELECT COALESCE(waiters, '<NULL>') FROM issues WHERE id = ?", []any{existing}, &clearedWaiters); err != nil {
		t.Fatalf("read the waiters column of %s: %v", existing, err)
	}
	if clearedWaiters != "[]" {
		t.Errorf("%s waiters column after a clear = %q, want the stored empty list %q", existing, clearedWaiters, "[]")
	}

	first := fixture.IssuePrefix + "-bag-rolledback"
	refused := batchApplyIssue(fixture.IssuePrefix+"-bag-refused", "past the bound")
	refused.AwaitType = gateFieldAtBoundAwaitType + "x"
	_, err := fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("first", batchApplyIssue(first, "rolls back with the refusal")),
			batchApplyCreate("refused", refused),
		},
	})
	assertGateFieldError(t, err, "a create item with await_type past its bound", publicops.GateFieldAwaitType)
	assertBatchApplyRowCount(t, ctx, fixture, "issues", first, 0)

	// An update item past a bound refuses the same way, named on its own
	// item: the earlier create rolls back, and the target keeps its columns.
	firstOfUpdate := fixture.IssuePrefix + "-bag-rolledback-upd"
	_, err = fixture.BatchApplier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Actor:         "apply-writer",
		ForceIDPrefix: true,
		Items: []publicops.ApplyItem{
			batchApplyCreate("first", batchApplyIssue(firstOfUpdate, "rolls back with the refusal")),
			batchApplyUpdate(publicops.Ref{ID: gate}, publicops.IssuePatch{
				Title:   publicops.Field[string]{Set: true, Value: "must not land"},
				AwaitID: publicops.Field[string]{Set: true, Value: gateFieldAtBoundAwaitID + "x"},
			}),
		},
	})
	assertGateFieldError(t, err, "an update item with await_id past its bound", publicops.GateFieldAwaitID)
	var itemErr *publicops.ItemError
	if !errors.As(err, &itemErr) {
		t.Fatalf("an update item with await_id past its bound: error = %v, want an *ItemError naming the item", err)
	}
	if itemErr.Index != 1 || itemErr.Kind != publicops.ItemUpdate {
		t.Errorf("ItemError = %#v, want Index 1 and Kind %q", itemErr, publicops.ItemUpdate)
	}
	assertBatchApplyRowCount(t, ctx, fixture, "issues", firstOfUpdate, 0)
	assertBatchApplyGateColumns(t, ctx, fixture, gate, "timer", "await-plan", 2*time.Hour, []string{"a", "a", "b"})
	if got := batchApplyColumn(t, ctx, fixture, "title", gate); got != "a gate made by a plan" {
		t.Errorf("%s title = %q, want the created title: a refused item writes nothing", gate, got)
	}
}

// assertBatchApplyGateColumns reads the four gate columns of one durable row
// raw, so neither side of the comparison comes from the role.
func assertBatchApplyGateColumns(t *testing.T, ctx context.Context, fixture BatchApplyFixture, id, awaitType, awaitID string, timeout time.Duration, waiters []string) {
	t.Helper()
	var gotType, gotID, gotWaiters string
	var gotTimeout int64
	if err := fixture.QueryScalar(ctx,
		"SELECT COALESCE(await_type, ''), COALESCE(await_id, ''), COALESCE(timeout_ns, 0), COALESCE(waiters, '') FROM issues WHERE id = ?",
		[]any{id}, &gotType, &gotID, &gotTimeout, &gotWaiters); err != nil {
		t.Fatalf("read the gate columns of %s: %v", id, err)
	}
	if gotType != awaitType || gotID != awaitID || time.Duration(gotTimeout) != timeout {
		t.Errorf("%s gate columns = (%q, %q, %s), want (%q, %q, %s)", id, gotType, gotID, time.Duration(gotTimeout), awaitType, awaitID, timeout)
	}
	if stored := parseGateWaiters(gotWaiters); !slices.Equal(stored, waiters) {
		t.Errorf("%s waiters column = %q (%q), want %q", id, gotWaiters, stored, waiters)
	}
}

// parseGateWaiters reads the waiters TEXT column's JSON array, treating the
// empty string, `null` and `[]` as the one empty list they all mean.
func parseGateWaiters(raw string) []string {
	if raw == "" || raw == "null" || raw == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{"<unparseable: " + err.Error() + ">"}
	}
	return out
}
