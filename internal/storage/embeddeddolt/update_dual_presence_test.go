//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestEmbeddedUpdateRefusesDualPresence is the embedded mirror of the server
// regression (TestUpdateRefusesDualPresence in internal/storage/dolt): the two
// stores share the guarded executor, so the resolver wired into it protects
// embedded role updates too — including the label patch, which bypasses the
// row updater the field write goes through. The matrix is not repeated here;
// this covers the distinct wiring once, per the design's test plan.
func TestEmbeddedUpdateRefusesDualPresence(t *testing.T) {
	te := newTestEnv(t, "edp")
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "embedded dual presence canary",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("create canary: %v", err)
	}
	id := issue.ID

	// Seed the wisp twin beside the durable row and make the copies disagree,
	// the state GH#5991's silent-loss report was about. Embedded mode has no
	// demote verb, so the twin goes in the way the corruption actually
	// arrives: raw SQL beside a live durable row.
	te.exec(t, ctx, `INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) `+
		`SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type FROM issues WHERE id = ?`, id)
	te.exec(t, ctx, `UPDATE issues SET notes = 'durable notes' WHERE id = ?`, id)
	te.exec(t, ctx, `UPDATE wisps SET notes = 'wisp notes' WHERE id = ?`, id)

	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	variants := []struct {
		name    string
		request issueops.UpdateRequest
	}{
		{
			name: "field write",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
				Notes: issueops.Field[string]{Set: true, Value: "probe"},
			}},
		},
		{
			name: "label only",
			request: issueops.UpdateRequest{Actor: "tester", IssueID: id, Patch: issueops.IssuePatch{
				Labels: issueops.LabelPatch{Add: []string{"probe-label"}},
			}},
		},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			before := embeddedDualPresenceRowState(t, ctx, te, id)

			_, err := lifecycle.Update(ctx, variant.request)
			if err == nil {
				t.Fatalf("embedded role update (%s) on dual-present %s succeeded; want refusal", variant.name, id)
			}
			var dual *issueops.DualPresenceError
			if !errors.As(err, &dual) {
				t.Fatalf("embedded role update (%s) error = %v, want *issueops.DualPresenceError", variant.name, err)
			}
			if dual.ID != id {
				t.Errorf("DualPresenceError.ID = %q, want %q", dual.ID, id)
			}
			if !errors.Is(err, issueops.ErrDualPresence) {
				t.Errorf("refusal does not match issueops.ErrDualPresence: %v", err)
			}

			after := embeddedDualPresenceRowState(t, ctx, te, id)
			if before != after {
				t.Errorf("state changed across the refused embedded update (%s):\nbefore: %s\nafter:  %s", variant.name, before, after)
			}
		})
	}
}

// embeddedDualPresenceRowState reads both copies' content and row version —
// the equality a refused update promises.
func embeddedDualPresenceRowState(t *testing.T, ctx context.Context, te *testEnv, id string) string {
	t.Helper()
	row := func(table string) string {
		var notes, rowLock, updatedAt sql.NullString
		te.queryScalar(t, ctx, fmt.Sprintf(
			`SELECT notes, CAST(row_lock AS CHAR), CAST(updated_at AS CHAR) FROM %s WHERE id = ?`, table),
			[]any{id}, &notes, &rowLock, &updatedAt)
		return fmt.Sprintf("notes=%q row_lock=%q updated_at=%q", notes.String, rowLock.String, updatedAt.String)
	}
	return row("issues") + " | " + row("wisps")
}
