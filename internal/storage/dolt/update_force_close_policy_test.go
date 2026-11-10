package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateIssueRefusesUnpoppedClosePolicyOverride pins the fail-loud half of
// the reserved-key transport on the embedded write funnel. A well-formed
// override is popped and leaves no trace; a malformed one survives the pop and
// is refused by name, so a caller that spells the override wrong learns about
// it instead of silently running unforced.
func TestUpdateIssueRefusesUnpoppedClosePolicyOverride(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "ufc-target"
	createPerm(t, ctx, store, id)

	err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"priority":                  1,
		issueops.OpForceClosePolicy: "yes",
	}, "tester")
	if err == nil {
		t.Fatal("UpdateIssue accepted a malformed close-policy override")
	}
	if !strings.Contains(err.Error(), "invalid field for update") || !strings.Contains(err.Error(), issueops.OpForceClosePolicy) {
		t.Fatalf("error = %v, want an \"invalid field for update\" refusal naming %q", err, issueops.OpForceClosePolicy)
	}
	issue, getErr := store.GetIssue(ctx, id)
	if getErr != nil {
		t.Fatalf("GetIssue: %v", getErr)
	}
	if issue.Priority != 2 {
		t.Errorf("priority = %d after a refused update, want the seeded 2", issue.Priority)
	}

	// A well-formed override is transport, not a column: it is popped, the rest
	// of the update applies, and nothing about it reaches the row.
	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"priority":                  1,
		issueops.OpForceClosePolicy: true,
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue with a well-formed override: %v", err)
	}
	issue, getErr = store.GetIssue(ctx, id)
	if getErr != nil {
		t.Fatalf("GetIssue: %v", getErr)
	}
	if issue.Priority != 1 {
		t.Errorf("priority = %d, want 1", issue.Priority)
	}
	if issue.Status != types.StatusOpen {
		t.Errorf("status = %q, want open", issue.Status)
	}
}

// TestUpdateIssueRefusesUnreadableStatusInsteadOfSkippingClosePolicy is the
// embedded funnel's proof that the gate cannot be stepped around by getting the
// status transport wrong. An in-process embedder can hand this map any Go value
// it likes; a []byte reaches SQL as the string 'closed' just fine, so a status
// the gate cannot read used to mean the close landed with the policy check
// never run — on a parent with an open child, which the gate exists to refuse.
// The update is now refused up front and the row is untouched.
func TestUpdateIssueRefusesUnreadableStatusInsteadOfSkippingClosePolicy(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const parent, child = "urs-parent", "urs-child"
	createPerm(t, ctx, store, parent)
	createPerm(t, ctx, store, child)
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	err := store.UpdateIssue(ctx, parent, map[string]interface{}{"status": []byte("closed")}, "tester")
	if !errors.Is(err, storage.ErrValidation) {
		t.Fatalf("UpdateIssue error = %v, want storage.ErrValidation", err)
	}
	if got := getClosePolicyStatus(t, ctx, store, parent); got != types.StatusOpen {
		t.Errorf("status = %q after a refused update, want open", got)
	}

	// The refusal is about the transport, not about closing: the same close
	// spelled with a string reaches the gate and is refused on the open child.
	err = store.UpdateIssue(ctx, parent, map[string]interface{}{"status": string(types.StatusClosed)}, "tester")
	if err == nil {
		t.Fatal("UpdateIssue closed a parent with an open child")
	}
	if errors.Is(err, storage.ErrValidation) {
		t.Fatalf("error = %v, want a close-policy refusal, not a validation error", err)
	}
}
