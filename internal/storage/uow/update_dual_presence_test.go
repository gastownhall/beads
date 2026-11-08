package uow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestLifecycleUpdateRefusesDualPresence pins the proxied route (GH#5991): a
// uow Update funnels into domain ApplyUpdate, whose claim, label and reparent
// branches bypass the SQL field updater, so the guard lives in ApplyUpdate
// itself — ahead of the first mutation, for every spec that asks to change
// something. A wisp row beside a durable twin therefore refuses every mutation
// shape the proxied surface can express, with the same typed error the guarded
// executor returns, and neither copy moves.
func TestLifecycleUpdateRefusesDualPresence(t *testing.T) {
	ctx := context.Background()
	operations, provider, id := seedUowDualPresence(t, ctx, "uow-dp")

	variants := []struct {
		name    string
		request issueops.UpdateRequest
	}{
		{
			// Title, not notes: the notes fence in the uow Update pipeline
			// runs ahead of ApplyUpdate and would refuse the request for a
			// reason that is not the guard under test.
			name: "field write",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
				Title: issueops.Field[string]{Set: true, Value: "probe"},
			}},
		},
		{
			name:    "claim only",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Claim: true},
		},
		{
			name: "label only",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
				Labels: issueops.LabelPatch{Add: []string{"probe-label"}},
			}},
		},
		{
			name: "persistence only",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
				Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModePersistent},
			}},
		},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			before := readUowDualPresenceRowState(t, ctx, provider, id)

			_, err := operations.Update(ctx, variant.request)
			if err == nil {
				t.Fatalf("uow update (%s) on dual-present %s succeeded; want refusal", variant.name, id)
			}
			var dual *issueops.DualPresenceError
			if !errors.As(err, &dual) {
				t.Fatalf("uow update (%s) error = %v, want *issueops.DualPresenceError", variant.name, err)
			}
			if dual.ID != id {
				t.Errorf("DualPresenceError.ID = %q, want %q", dual.ID, id)
			}
			if !errors.Is(err, issueops.ErrDualPresence) {
				t.Errorf("refusal does not match issueops.ErrDualPresence: %v", err)
			}

			after := readUowDualPresenceRowState(t, ctx, provider, id)
			if before != after {
				t.Errorf("state changed across the refused uow update (%s):\nbefore: %s\nafter:  %s", variant.name, before, after)
			}
		})
	}
}

// seedUowDualPresence builds the corrupted state through the uow surface
// itself: an ephemeral create puts the row in wisps, raw SQL recreates the
// durable twin beside it, and the copies are made to disagree so a
// wrong-plane write is observable in content.
func seedUowDualPresence(t *testing.T, ctx context.Context, label string) (issueops.Lifecycle, UnitOfWorkProvider, string) {
	t.Helper()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			return "", err
		}
		return "initialize dual presence fixture", nil
	}); err != nil {
		t.Fatalf("initialize dual presence fixture: %v", err)
	}
	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations(): %v", err)
	}
	created, err := operations.Create(ctx, issueops.CreateRequest{Actor: "tester",
		Issue: &issueops.Issue{ID: "bd-" + label, Title: label, IssueType: types.TypeTask, Priority: 2, Ephemeral: true}})
	if err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	id := created.Issue.ID

	seed := []struct {
		Query string
		Args  []any
	}{
		{`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) ` +
			`SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type FROM wisps WHERE id = ?`, []any{id}},
		{`UPDATE issues SET notes = 'durable notes' WHERE id = ?`, []any{id}},
		{`UPDATE wisps  SET notes = 'wisp notes' WHERE id = ?`, []any{id}},
	}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		for _, stmt := range seed {
			if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
				return "", err
			}
		}
		return "seed dual presence", nil
	}); err != nil {
		t.Fatalf("seed dual presence: %v", err)
	}

	var inIssues, inWisps int
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		var err error
		if inIssues, err = countUowRows(ctx, uw, "issues", id); err != nil {
			return "", err
		}
		if inWisps, err = countUowRows(ctx, uw, "wisps", id); err != nil {
			return "", err
		}
		return "verify fixture", nil
	}); err != nil {
		t.Fatalf("verify dual presence fixture: %v", err)
	}
	if inIssues != 1 || inWisps != 1 {
		t.Fatalf("fixture: %s counts issues=%d wisps=%d, want 1 and 1", id, inIssues, inWisps)
	}
	return operations, provider, id
}

func countUowRows(ctx context.Context, uw UnitOfWork, table, id string) (int, error) {
	result, err := uw.RawSQLUseCase().Query(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table), id)
	if err != nil {
		return 0, err
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return 0, fmt.Errorf("unexpected count rows for %s: %#v", table, result.Rows)
	}
	var n int
	if _, err := fmt.Sscanf(fmt.Sprint(result.Rows[0][0]), "%d", &n); err != nil {
		return 0, fmt.Errorf("parse count %v: %w", result.Rows[0][0], err)
	}
	return n, nil
}

// readUowDualPresenceRowState reads both copies' content and row version —
// the equality a refused mutation promises.
func readUowDualPresenceRowState(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) string {
	t.Helper()
	var state string
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		row := func(table string) (string, error) {
			result, err := uw.RawSQLUseCase().Query(ctx,
				fmt.Sprintf("SELECT notes, CAST(row_lock AS CHAR), CAST(updated_at AS CHAR) FROM %s WHERE id = ?", table), id)
			if err != nil {
				return "", err
			}
			if len(result.Rows) != 1 || len(result.Rows[0]) != 3 {
				return "", fmt.Errorf("unexpected %s row: %#v", table, result.Rows)
			}
			return fmt.Sprintf("notes=%q row_lock=%q updated_at=%q",
				fmt.Sprint(result.Rows[0][0]), fmt.Sprint(result.Rows[0][1]), fmt.Sprint(result.Rows[0][2])), nil
		}
		durable, err := row("issues")
		if err != nil {
			return "", err
		}
		wisp, err := row("wisps")
		if err != nil {
			return "", err
		}
		state = durable + " | " + wisp
		return "read dual presence state", nil
	}); err != nil {
		t.Fatalf("read dual presence row state for %s: %v", id, err)
	}
	return state
}

// TestDualPresenceCloseReopenUnchanged pins the boundary GH#5991's refusal
// draws: Close and Reopen are separate operations whose wisp targeting a
// dual-present ID keeps, while Update refuses. The uow Close/Reopen paths
// run their version precondition through ApplyUpdate with a version-only
// spec — a spec that mutates nothing — so the refusal the mutation guard
// raises must not swallow them: the wisp copy closes and reopens through
// that path, and the durable twin the issues-first reader shows never
// moves. The settled create-batch equivalence golden pins the same
// twin-close contract at the storage layer and passes unmodified.
//
// The final Update is the load-bearing contrast: before the fix it silently
// misrouted (the copy bd show never reads), so the test fails there, and
// after the fix it refuses while the closes around it still succeed.
func TestDualPresenceCloseReopenUnchanged(t *testing.T) {
	ctx := context.Background()
	operations, provider, id := seedUowDualPresence(t, ctx, "uow-dp-cr")

	if durable, wisp := readUowDualPresencePlaneStatus(t, ctx, provider, id); durable != "open" || wisp != "open" {
		t.Fatalf("fixture: %s durable status = %q, wisp status = %q, want both open", id, durable, wisp)
	}
	durableBefore := readUowDualPresenceRowState(t, ctx, provider, id)

	// Close without a precondition: the wisp copy closes, the durable twin
	// stays exactly as it was.
	closed, err := operations.Close(ctx, issueops.CloseRequest{Actor: "tester", IssueID: id, Reason: "done"})
	if err != nil {
		t.Fatalf("close dual-present %s: %v", id, err)
	}
	if !closed.Changed || closed.Issue == nil || closed.Issue.Status != types.StatusClosed {
		t.Fatalf("close of dual-present %s reported %+v; want a real close of the wisp copy", id, closed)
	}
	if durable, wisp := readUowDualPresencePlaneStatus(t, ctx, provider, id); durable != "open" || wisp != "closed" {
		t.Fatalf("after close: %s durable status = %q, wisp status = %q, want open/closed", id, durable, wisp)
	}
	if state := readUowDualPresenceRowState(t, ctx, provider, id); durablePortion(state) != durablePortion(durableBefore) {
		t.Errorf("close of the wisp twin changed the durable copy:\nbefore: %s\nafter:  %s", durableBefore, state)
	}

	// Reopen through the version-precondition path: ApplyUpdate sees a
	// version-only spec on a dual-present ID and must pass it through.
	closedVersion := closed.Issue.RowVersion
	reopened, err := operations.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: id, Reason: "reopened", ExpectedVersion: &closedVersion})
	if err != nil {
		t.Fatalf("reopen dual-present %s through the version precondition: %v", id, err)
	}
	if !reopened.Changed || reopened.Issue == nil || reopened.Issue.Status != types.StatusOpen {
		t.Fatalf("reopen of dual-present %s reported %+v; want a real reopen of the wisp copy", id, reopened)
	}
	if durable, wisp := readUowDualPresencePlaneStatus(t, ctx, provider, id); durable != "open" || wisp != "open" {
		t.Fatalf("after reopen: %s durable status = %q, wisp status = %q, want both open", id, durable, wisp)
	}
	if state := readUowDualPresenceRowState(t, ctx, provider, id); durablePortion(state) != durablePortion(durableBefore) {
		t.Errorf("reopen of the wisp twin changed the durable copy:\nbefore: %s\nafter:  %s", durableBefore, state)
	}

	// Close again through the same precondition path.
	reopenedVersion := reopened.Issue.RowVersion
	reclosed, err := operations.Close(ctx, issueops.CloseRequest{Actor: "tester", IssueID: id, Reason: "done again", ExpectedVersion: &reopenedVersion})
	if err != nil {
		t.Fatalf("close dual-present %s through the version precondition: %v", id, err)
	}
	if !reclosed.Changed || reclosed.Issue == nil || reclosed.Issue.Status != types.StatusClosed {
		t.Fatalf("preconditioned close of dual-present %s reported %+v; want a real close of the wisp copy", id, reclosed)
	}
	if durable, wisp := readUowDualPresencePlaneStatus(t, ctx, provider, id); durable != "open" || wisp != "closed" {
		t.Fatalf("after second close: %s durable status = %q, wisp status = %q, want open/closed", id, durable, wisp)
	}
	if state := readUowDualPresenceRowState(t, ctx, provider, id); durablePortion(state) != durablePortion(durableBefore) {
		t.Errorf("preconditioned close of the wisp twin changed the durable copy:\nbefore: %s\nafter:  %s", durableBefore, state)
	}

	// The boundary itself: the same twin's Update refuses while its Close and
	// Reopen went through. The typed-refusal pin (errors.As
	// *issueops.DualPresenceError) lives in TestLifecycleUpdateRefusesDualPresence;
	// this asserts the boundary the AC names — Update may not succeed where
	// the separate operations keep working.
	_, err = operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
		Title: issueops.Field[string]{Set: true, Value: "probe"},
	}})
	if err == nil {
		t.Fatalf("uow update on dual-present %s succeeded beside the unchanged Close/Reopen; want the dual-presence refusal", id)
	}
	if durable, wisp := readUowDualPresencePlaneStatus(t, ctx, provider, id); durable != "open" || wisp != "closed" {
		t.Errorf("after the refused update: %s durable status = %q, wisp status = %q, want open/closed", id, durable, wisp)
	}
}

// readUowDualPresencePlaneStatus reads both copies' lifecycle status, the
// plane a Close or Reopen moves. It returns (durable, wisp).
func readUowDualPresencePlaneStatus(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) (string, string) {
	t.Helper()
	var durable, wisp string
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		row := func(table string) (string, error) {
			result, err := uw.RawSQLUseCase().Query(ctx, fmt.Sprintf("SELECT status FROM %s WHERE id = ?", table), id)
			if err != nil {
				return "", err
			}
			if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
				return "", fmt.Errorf("unexpected %s row: %#v", table, result.Rows)
			}
			return fmt.Sprint(result.Rows[0][0]), nil
		}
		var err error
		if durable, err = row("issues"); err != nil {
			return "", err
		}
		if wisp, err = row("wisps"); err != nil {
			return "", err
		}
		return "read dual presence statuses", nil
	}); err != nil {
		t.Fatalf("read dual presence plane status for %s: %v", id, err)
	}
	return durable, wisp
}

// durablePortion splits readUowDualPresenceRowState's "durable | wisp"
// string and keeps the durable half, so a wisp-plane change does not fail a
// comparison that only promises the durable copy is untouched.
func durablePortion(state string) string {
	if before, _, found := strings.Cut(state, " | "); found {
		return before
	}
	return state
}
