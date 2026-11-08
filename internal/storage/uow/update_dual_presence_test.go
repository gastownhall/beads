package uow

import (
	"context"
	"errors"
	"fmt"
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
