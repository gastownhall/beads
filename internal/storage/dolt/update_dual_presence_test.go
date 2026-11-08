package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// GH#5991: an ID with a row in BOTH issues and wisps is read issues-first, but
// an update used to route wisps-first, report success, and change the copy no
// reader returns. These tests seed that state directly — the import path that
// used to create it now refuses the collision — and require every update entry
// point to refuse it with *publicops.DualPresenceError before writing anything.

// seedDualPresentIssue creates a durable issue with its own notes, metadata
// and label, then inserts a wisp twin of the same ID whose values all differ,
// the way a pre-guard JSONL import beside a demoted bead left it.
func seedDualPresentIssue(t *testing.T, ctx context.Context, store *DoltStore, id string) {
	t.Helper()
	durable := &types.Issue{
		ID: id, Title: "durable " + id, Notes: "durable notes", Metadata: []byte(`{"copy":"durable"}`),
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"durable-label"},
	}
	if err := store.CreateIssue(ctx, durable, "seed"); err != nil {
		t.Fatalf("create durable %s: %v", id, err)
	}
	tx, err := store.UnderlyingDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := issueops.InsertIssueStrictInTx(ctx, tx, "wisps", &types.Issue{
		ID: id, Title: "wisp " + id, Notes: "wisp notes", Metadata: []byte(`{"copy":"wisp"}`),
		Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask, Ephemeral: true,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed wisp twin of %s: %v", id, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", id, "wisp-label"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed wisp twin label of %s: %v", id, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit wisp twin of %s: %v", id, err)
	}
}

// dualPresenceSnapshot is every persisted row that names id across both
// planes, each rendered as text, plus the Dolt commit count. Comparing whole
// rows (not counts) catches a write that touched content, timestamps or
// row_lock without adding or removing a row.
type dualPresenceSnapshot struct {
	rows    map[string][]string
	commits int
}

var dualPresenceSnapshotTables = []struct{ table, key string }{
	{"issues", "id"}, {"wisps", "id"},
	{"labels", "issue_id"}, {"wisp_labels", "issue_id"},
	{"events", "issue_id"}, {"wisp_events", "issue_id"},
	{"comments", "issue_id"}, {"wisp_comments", "issue_id"},
	{"dependencies", "issue_id"}, {"wisp_dependencies", "issue_id"},
	{"issue_versions", "issue_id"},
}

func readDualPresenceSnapshot(t *testing.T, ctx context.Context, db *sql.DB, id string) dualPresenceSnapshot {
	t.Helper()
	snap := dualPresenceSnapshot{rows: map[string][]string{}}
	for _, tk := range dualPresenceSnapshotTables {
		//nolint:gosec // G201: table and key come from the fixed list above.
		rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", tk.table, tk.key), id)
		if err != nil {
			t.Fatalf("snapshot %s: %v", tk.table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("snapshot %s columns: %v", tk.table, err)
		}
		var rendered []string
		for rows.Next() {
			values := make([]sql.RawBytes, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatalf("snapshot %s scan: %v", tk.table, err)
			}
			fields := make([]string, len(cols))
			for i, v := range values {
				if v == nil {
					fields[i] = cols[i] + "=NULL"
				} else {
					fields[i] = cols[i] + "=" + string(v)
				}
			}
			rendered = append(rendered, strings.Join(fields, "|"))
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot %s rows: %v", tk.table, err)
		}
		sort.Strings(rendered)
		snap.rows[tk.table] = rendered
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&snap.commits); err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	return snap
}

func assertDualPresenceSnapshotUnchanged(t *testing.T, before, after dualPresenceSnapshot) {
	t.Helper()
	if before.commits != after.commits {
		t.Errorf("dolt_log grew from %d to %d commits on a refused update", before.commits, after.commits)
	}
	for _, tk := range dualPresenceSnapshotTables {
		if !reflect.DeepEqual(before.rows[tk.table], after.rows[tk.table]) {
			t.Errorf("%s rows changed on a refused update:\nbefore %q\nafter  %q", tk.table, before.rows[tk.table], after.rows[tk.table])
		}
	}
	if len(before.rows["issues"]) != 1 || len(before.rows["wisps"]) != 1 {
		t.Fatalf("fixture is not dual-present: issues=%d wisps=%d", len(before.rows["issues"]), len(before.rows["wisps"]))
	}
}

// assertDualPresenceRefusal requires err to be the typed refusal for id: it
// matches both sentinels' spellings and recovers the ID through errors.As.
func assertDualPresenceRefusal(t *testing.T, err error, id string) {
	t.Helper()
	if err == nil {
		t.Fatalf("update of dual-present %s succeeded, want a DualPresenceError refusal", id)
	}
	if !errors.Is(err, publicops.ErrDualPresence) || !errors.Is(err, storage.ErrDualPresence) {
		t.Fatalf("update of dual-present %s: err = %v, want ErrDualPresence", id, err)
	}
	var dual *publicops.DualPresenceError
	if !errors.As(err, &dual) || dual.ID != id {
		t.Fatalf("update of dual-present %s: errors.As = %#v from %v, want ID %q", id, dual, err, id)
	}
}

// TestLegacyUpdateRefusesDualPresence covers the map-based server entry points
// (UpdateIssue, UpdateIssueChecked) — the ones `bd update` reached in GH#5991 —
// and the raw persistence move, which already refused a twin but untyped.
func TestLegacyUpdateRefusesDualPresence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	cases := []struct {
		name string
		run  func(id string) error
	}{
		{"UpdateIssue notes", func(id string) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"notes": "probe"}, "tester")
		}},
		{"UpdateIssue metadata", func(id string) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"metadata": `{"copy":"probe"}`}, "tester")
		}},
		{"UpdateIssue same value", func(id string) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"notes": "durable notes"}, "tester")
		}},
		{"UpdateIssue no_history", func(id string) error {
			return store.UpdateIssue(ctx, id, map[string]interface{}{"no_history": false}, "tester")
		}},
		{"UpdateIssueChecked notes", func(id string) error {
			return store.UpdateIssueChecked(ctx, id, map[string]interface{}{"notes": "probe"}, "tester", storage.UpdateIssueOptions{})
		}},
		{"MoveIssuePersistenceInTx", func(id string) error {
			return store.withRetryTx(ctx, func(tx *sql.Tx) error {
				current, err := issueops.GetIssueInTx(ctx, tx, id)
				if err != nil {
					return err
				}
				_, err = issueops.MoveIssuePersistenceInTx(ctx, tx, current, types.PersistenceModePersistent, "tester")
				return err
			})
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("dual-legacy-%d", i)
			seedDualPresentIssue(t, ctx, store, id)
			before := readDualPresenceSnapshot(t, ctx, store.UnderlyingDB(), id)
			assertDualPresenceRefusal(t, tc.run(id), id)
			assertDualPresenceSnapshotUnchanged(t, before, readDualPresenceSnapshot(t, ctx, store.UnderlyingDB(), id))
		})
	}
}

// dualPresenceRolePatches are valid, authorized Lifecycle updates, one per
// write path ExecuteUpdate can take: fields, claim, labels, parent and
// persistence. The notes replacement carries --force so the notes-overwrite
// fence stays out of the way; the same-value notes already match the copy the
// fence reads.
func dualPresenceRolePatches(parentID string) []struct {
	name    string
	request publicops.UpdateRequest
} {
	return []struct {
		name    string
		request publicops.UpdateRequest
	}{
		{"notes", publicops.UpdateRequest{ForceNotesOverwrite: true, Patch: publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "probe"}}}},
		{"same-value notes", publicops.UpdateRequest{Patch: publicops.IssuePatch{Notes: publicops.Field[string]{Set: true, Value: "durable notes"}}}},
		{"append notes", publicops.UpdateRequest{Patch: publicops.IssuePatch{AppendNotes: publicops.Field[string]{Set: true, Value: "probe"}}}},
		{"metadata", publicops.UpdateRequest{Patch: publicops.IssuePatch{Metadata: publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: []byte(`{"copy":"probe"}`)}}}}},
		{"claim only", publicops.UpdateRequest{Claim: true}},
		{"label only", publicops.UpdateRequest{Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"probe-label"}}}}},
		{"parent only", publicops.UpdateRequest{Patch: publicops.IssuePatch{ParentID: publicops.Field[string]{Set: true, Value: parentID}}}},
		{"persistence only", publicops.UpdateRequest{Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: types.PersistenceModePersistent}}}},
	}
}

// TestUpdateRefusesDualPresence covers the server Lifecycle role. Its
// ExecuteUpdate reads the issues copy as the pre-image but used to write
// claims, labels and parents through helpers that route wisps-first, so every
// write path must refuse a twin before the first write.
func TestUpdateRefusesDualPresence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	operations, err := NewIssueOperations(store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	parent := &types.Issue{ID: "dual-role-parent", Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	if err := store.CreateIssue(ctx, parent, "seed"); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	for i, tc := range dualPresenceRolePatches(parent.ID) {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("dual-role-%d", i)
			seedDualPresentIssue(t, ctx, store, id)
			before := readDualPresenceSnapshot(t, ctx, store.UnderlyingDB(), id)
			request := tc.request
			request.Actor, request.IssueID = "writer", id
			_, err := operations.Update(ctx, request)
			assertDualPresenceRefusal(t, err, id)
			assertDualPresenceSnapshotUnchanged(t, before, readDualPresenceSnapshot(t, ctx, store.UnderlyingDB(), id))
		})
	}
}
