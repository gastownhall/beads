package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestUpdateRefusesDualPresence pins GH#5991's core fix on the guarded role
// path: an ID that names a row in both the issues and the wisps tables makes
// `bd update`'s executor refuse the whole request, whatever the patch shape —
// field writes, note appends, metadata merges, claims, label edits, reparents
// and persistence moves all mutate something, and the resolver runs before
// the pre-image read, so no patch type can slip past it, not even a no-op
// restatement. Every variant leaves both copies, their auxiliary rows and the
// Dolt commit count byte-identical and returns *issueops.DualPresenceError,
// the ID recoverable through errors.As.
//
// Before the fix the role path read its pre-image issues-first but wrote
// wisps-first, so these updates reported success while editing the copy
// GetIssue never reads.
func TestUpdateRefusesDualPresence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	// One durable target per case for the reparent variant: the parent patch
	// is refused before it validates anything, but the request must still be
	// one the row would otherwise accept.
	parent := &types.Issue{Title: "dual presence parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	variants := []struct {
		name       string
		requestFor func(id string) issueops.UpdateRequest
	}{
		{
			name: "field write",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.Notes = issueops.Field[string]{Set: true, Value: "probe"}
				})
			},
		},
		{
			name: "append notes",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.AppendNotes = issueops.Field[string]{Set: true, Value: " probe"}
				})
			},
		},
		{
			name: "metadata merge",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.Metadata.Merge = issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{"probe":"merge"}`)}
				})
			},
		},
		{
			name: "claim only",
			requestFor: func(id string) issueops.UpdateRequest {
				request := dualPresenceUpdate(id, nil)
				request.Claim = true
				return request
			},
		},
		{
			name: "label only",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.Labels.Add = []string{"probe-label"}
				})
			},
		},
		{
			name: "parent only",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.ParentID = issueops.Field[string]{Set: true, Value: parent.ID}
				})
			},
		},
		{
			name: "persistence only",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.Persistence = issueops.Field[issueops.PersistenceMode]{Set: true, Value: types.PersistenceModePersistent}
				})
			},
		},
		{
			name: "status restatement is still an update",
			requestFor: func(id string) issueops.UpdateRequest {
				return dualPresenceUpdate(id, func(patch *issueops.IssuePatch) {
					patch.Status = issueops.Field[issueops.Status]{Set: true, Value: types.StatusOpen}
				})
			},
		},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			id := seedDualPresenceCanary(t, ctx, store, variant.name)
			before := snapshotDualPresenceState(t, ctx, store, id)

			_, err := lifecycle.Update(ctx, variant.requestFor(id))
			if err == nil {
				t.Fatalf("role update (%s) on dual-present %s succeeded; want refusal", variant.name, id)
			}
			var dual *issueops.DualPresenceError
			if !errors.As(err, &dual) {
				t.Fatalf("role update (%s) error = %v, want *issueops.DualPresenceError", variant.name, err)
			}
			if dual.ID != id {
				t.Errorf("DualPresenceError.ID = %q, want %q", dual.ID, id)
			}
			if !errors.Is(err, issueops.ErrDualPresence) {
				t.Errorf("refusal does not match issueops.ErrDualPresence: %v", err)
			}

			after := snapshotDualPresenceState(t, ctx, store, id)
			before.assertEqual(t, variant.name, after)
		})
	}
}

func dualPresenceUpdate(id string, shape func(*issueops.IssuePatch)) issueops.UpdateRequest {
	patch := issueops.IssuePatch{}
	if shape != nil {
		shape(&patch)
	}
	return issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: patch}
}

// seedDualPresenceCanary builds the corrupted state the reporter's database
// was in: a demote moved the row into wisps (the last commit it ever
// received), and a later stale import recreated the durable row beside it.
// The two copies carry different notes and metadata so a wrong-table write is
// observable in content, not just in routing.
func seedDualPresenceCanary(t *testing.T, ctx context.Context, store *DoltStore, label string) string {
	t.Helper()
	issue := &types.Issue{
		Title:     "dual presence canary " + label,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create canary: %v", err)
	}
	id := issue.ID
	if err := store.UpdateIssue(ctx, id, map[string]any{"no_history": true}, "tester"); err != nil {
		t.Fatalf("demote canary to wisp: %v", err)
	}
	if err := store.UpdateIssue(ctx, id, map[string]any{"notes": "wisp notes"}, "tester"); err != nil {
		t.Fatalf("set wisp notes: %v", err)
	}

	db := store.UnderlyingDB()
	_, err := db.ExecContext(ctx, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) `+
		`SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type FROM wisps WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("recreate durable row beside the wisp: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE issues SET notes = 'durable notes', metadata = '{"plane":"durable"}' WHERE id = ?`,
		`UPDATE wisps  SET metadata = '{"plane":"wisp"}' WHERE id = ?`,
	} {
		if _, err := db.ExecContext(ctx, stmt, id); err != nil {
			t.Fatalf("stamp copy (%s): %v", stmt, err)
		}
	}

	var inIssues, inWisps int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, id).Scan(&inIssues); err != nil {
		t.Fatalf("count issues copy: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wisps WHERE id = ?`, id).Scan(&inWisps); err != nil {
		t.Fatalf("count wisps copy: %v", err)
	}
	if inIssues != 1 || inWisps != 1 {
		t.Fatalf("fixture: %s counts issues=%d wisps=%d, want 1 and 1", id, inIssues, inWisps)
	}
	return id
}

// dualPresenceState is the full before/after equality the refusal promises:
// row content and row version on both planes, auxiliary row counts on both
// planes, and the Dolt commit count.
type dualPresenceState struct {
	durableRow  string
	wispRow     string
	durableAux  int
	wispAux     int
	commitCount int
}

func snapshotDualPresenceState(t *testing.T, ctx context.Context, store *DoltStore, id string) dualPresenceState {
	t.Helper()
	db := store.UnderlyingDB()
	state := dualPresenceState{}
	row := func(table string) string {
		var notes, rowLock, updatedAt sql.NullString
		if err := db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT notes, CAST(row_lock AS CHAR), CAST(updated_at AS CHAR) FROM %s WHERE id = ?`, table), id).
			Scan(&notes, &rowLock, &updatedAt); err != nil {
			t.Fatalf("snapshot %s row: %v", table, err)
		}
		return fmt.Sprintf("notes=%q row_lock=%q updated_at=%q", notes.String, rowLock.String, updatedAt.String)
	}
	state.durableRow = row("issues")
	state.wispRow = row("wisps")
	count := func(query string, args ...any) int {
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("snapshot count (%s): %v", query, err)
		}
		return n
	}
	state.durableAux = count(`SELECT (SELECT COUNT(*) FROM labels WHERE issue_id = ?) + (SELECT COUNT(*) FROM events WHERE issue_id = ?) + (SELECT COUNT(*) FROM comments WHERE issue_id = ?) + (SELECT COUNT(*) FROM dependencies WHERE issue_id = ?)`, id, id, id, id)
	state.wispAux = count(`SELECT (SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?) + (SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?) + (SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?) + (SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?)`, id, id, id, id)
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dolt_log`).Scan(&state.commitCount); err != nil {
		t.Fatalf("snapshot dolt_log count: %v", err)
	}
	return state
}

func (s dualPresenceState) assertEqual(t *testing.T, label string, other dualPresenceState) {
	t.Helper()
	if s != other {
		t.Errorf("state changed across the refused update (%s):\nbefore: %+v\nafter:  %+v", label, s, other)
	}
}
