package uow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// uowDualPresenceTables are the tables whose rows for a twin are compared
// before and after a refused update.
var uowDualPresenceTables = []struct{ table, key string }{
	{"issues", "id"}, {"wisps", "id"},
	{"labels", "issue_id"}, {"wisp_labels", "issue_id"},
	{"events", "issue_id"}, {"wisp_events", "issue_id"},
	{"dependencies", "issue_id"}, {"wisp_dependencies", "issue_id"},
	{"issue_versions", "issue_id"},
}

func readUOWDualPresenceSnapshot(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) []string {
	t.Helper()
	snap, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]string, error) {
		var snap []string
		for _, tk := range uowDualPresenceTables {
			result, err := uw.RawSQLUseCase().Query(ctx, fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", tk.table, tk.key), id)
			if err != nil {
				return nil, fmt.Errorf("snapshot %s: %w", tk.table, err)
			}
			var rendered []string
			for _, row := range result.Rows {
				rendered = append(rendered, fmt.Sprintf("%s: %v", tk.table, row))
			}
			sort.Strings(rendered)
			snap = append(snap, rendered...)
		}
		commits, err := uw.RawSQLUseCase().Query(ctx, "SELECT COUNT(*) FROM dolt_log")
		if err != nil {
			return nil, fmt.Errorf("count dolt_log: %w", err)
		}
		return append(snap, fmt.Sprintf("dolt_log=%v", commits.Rows)), nil
	})
	if err != nil {
		t.Fatalf("read dual-presence snapshot for %s: %v", id, err)
	}
	return snap
}

// seedUOWDualPresentIssue creates a durable issue and a wisp twin of the same
// ID whose notes, metadata, priority and labels differ (GH#5991).
func seedUOWDualPresentIssue(t *testing.T, ctx context.Context, operations issueops.Lifecycle, provider UnitOfWorkProvider, id string) {
	t.Helper()
	if _, err := operations.Create(ctx, issueops.CreateRequest{Actor: "seed", Issue: &types.Issue{
		ID: id, Title: "durable " + id, Notes: "durable notes", Metadata: []byte(`{"copy":"durable"}`),
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"durable-label"},
	}}); err != nil {
		t.Fatalf("create durable %s: %v", id, err)
	}
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if _, err := uw.RawSQLUseCase().Exec(ctx, `INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, metadata)
			VALUES (?, ?, '', '', '', 'wisp notes', 'open', 3, 'task', 1, '{"copy":"wisp"}')`, id, "wisp "+id); err != nil {
			return "", err
		}
		if _, err := uw.RawSQLUseCase().Exec(ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'wisp-label')", id); err != nil {
			return "", err
		}
		// A non-empty message commits; RunTx rolls back an empty one.
		return "seed wisp twin " + id, nil
	}); err != nil {
		t.Fatalf("seed wisp twin of %s: %v", id, err)
	}
}

// requireUOWDualPresentFixture fails unless snap holds exactly one issues row
// and one wisps row, so a seed that silently did not land cannot pass.
func requireUOWDualPresentFixture(t *testing.T, snap []string) {
	t.Helper()
	var issues, wisps int
	for _, row := range snap {
		switch {
		case strings.HasPrefix(row, "issues: "):
			issues++
		case strings.HasPrefix(row, "wisps: "):
			wisps++
		}
	}
	if issues != 1 || wisps != 1 {
		t.Fatalf("fixture is not dual-present: issues=%d wisps=%d", issues, wisps)
	}
}

// TestLifecycleUpdateRefusesDualPresence covers the proxied update stack: the
// unit-of-work Lifecycle resolves the row wisp-first and domain ApplyUpdate
// routes every mutation to the wisp plane, including the claim, label, parent
// and persistence paths that never reach the shared SQL field updater. Each
// must refuse a dual-present ID and leave both copies untouched.
func TestLifecycleUpdateRefusesDualPresence(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	if _, err := operations.Create(ctx, issueops.CreateRequest{Actor: "seed", Issue: &types.Issue{
		ID: "bd-dual-parent", Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic,
	}}); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// The notes replacements carry --force: without it the notes-overwrite
	// fence refuses first, and this test is about valid, authorized requests.
	cases := []struct {
		name  string
		claim bool
		force bool
		patch issueops.IssuePatch
	}{
		{name: "notes", force: true, patch: issueops.IssuePatch{Notes: issueops.Field[string]{Set: true, Value: "probe"}}},
		{name: "same-value notes", force: true, patch: issueops.IssuePatch{Notes: issueops.Field[string]{Set: true, Value: "durable notes"}}},
		{name: "append notes", patch: issueops.IssuePatch{AppendNotes: issueops.Field[string]{Set: true, Value: "probe"}}},
		{name: "metadata", patch: issueops.IssuePatch{Metadata: issueops.MetadataPatch{Merge: issueops.Field[json.RawMessage]{Set: true, Value: []byte(`{"copy":"probe"}`)}}}},
		{name: "claim only", claim: true},
		{name: "label only", patch: issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{"probe-label"}}}},
		{name: "parent only", patch: issueops.IssuePatch{ParentID: issueops.Field[string]{Set: true, Value: "bd-dual-parent"}}},
		{name: "persistence only", patch: issueops.IssuePatch{Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: types.PersistenceModePersistent}}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("bd-dual-uow-%d", i)
			seedUOWDualPresentIssue(t, ctx, operations, provider, id)
			before := readUOWDualPresenceSnapshot(t, ctx, provider, id)
			requireUOWDualPresentFixture(t, before)
			_, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "writer", IssueID: id, Claim: tc.claim, ForceNotesOverwrite: tc.force, Patch: tc.patch})
			if !errors.Is(err, issueops.ErrDualPresence) {
				t.Fatalf("Update of dual-present %s: err = %v, want ErrDualPresence", id, err)
			}
			var dual *issueops.DualPresenceError
			if !errors.As(err, &dual) || dual.ID != id {
				t.Fatalf("Update of dual-present %s: errors.As = %#v from %v, want ID %q", id, dual, err, id)
			}
			if after := readUOWDualPresenceSnapshot(t, ctx, provider, id); !reflect.DeepEqual(before, after) {
				t.Errorf("refused update changed persisted state:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// TestDualPresenceCloseReopenUnchanged pins what the GH#5991 refusal leaves
// alone: Close and Reopen are separate verbs that keep targeting the wisp copy
// of a dual-present ID, and their version-only ApplyUpdate precondition (which
// mutates nothing) still checks the wisp row instead of refusing.
func TestDualPresenceCloseReopenUnchanged(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	id := "bd-dual-close"
	seedUOWDualPresentIssue(t, ctx, operations, provider, id)
	seeded := readUOWDualPresenceSnapshot(t, ctx, provider, id)
	requireUOWDualPresentFixture(t, seeded)

	// durableRows keeps only the issues-plane rows, which neither verb may touch.
	durableRows := func(snap []string) []string {
		var rows []string
		for _, row := range snap {
			for _, prefix := range []string{"issues: ", "labels: ", "events: ", "dependencies: ", "issue_versions: "} {
				if strings.HasPrefix(row, prefix) {
					rows = append(rows, row)
				}
			}
		}
		return rows
	}
	wisp := func() *types.Issue {
		t.Helper()
		issue, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
			return uw.IssueUseCase().GetWisp(ctx, id)
		})
		if err != nil || issue == nil {
			t.Fatalf("read wisp copy of %s: %v", id, err)
		}
		return issue
	}
	durableStatus := func() string {
		t.Helper()
		issue, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
			return uw.IssueUseCase().GetIssue(ctx, id)
		})
		if err != nil || issue == nil {
			t.Fatalf("read issues copy of %s: %v", id, err)
		}
		return string(issue.Status)
	}

	stale := wisp().RowVersion + 1000
	if _, err := operations.Close(ctx, issueops.CloseRequest{Actor: "writer", IssueID: id, Reason: "done", ExpectedVersion: &stale}); !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Fatalf("Close with a stale version: err = %v, want ErrVersionMismatch", err)
	}

	version := wisp().RowVersion
	closed, err := operations.Close(ctx, issueops.CloseRequest{Actor: "writer", IssueID: id, Reason: "done", ExpectedVersion: &version})
	if err != nil {
		t.Fatalf("Close of dual-present %s: %v", id, err)
	}
	if closed.Issue == nil || closed.Issue.Title != "wisp "+id || closed.Issue.Status != types.StatusClosed {
		t.Fatalf("Close result = %+v, want the closed wisp copy", closed.Issue)
	}
	if got := wisp().Status; got != types.StatusClosed {
		t.Errorf("wisp status after Close = %q, want closed", got)
	}
	if got := durableStatus(); got != string(types.StatusOpen) {
		t.Errorf("issues status after Close = %q, want open", got)
	}

	version = wisp().RowVersion
	reopened, err := operations.Reopen(ctx, issueops.ReopenRequest{Actor: "writer", IssueID: id, ExpectedVersion: &version})
	if err != nil {
		t.Fatalf("Reopen of dual-present %s: %v", id, err)
	}
	if reopened.Issue == nil || reopened.Issue.Title != "wisp "+id || reopened.Issue.Status != types.StatusOpen {
		t.Fatalf("Reopen result = %+v, want the reopened wisp copy", reopened.Issue)
	}
	if got := wisp().Status; got != types.StatusOpen {
		t.Errorf("wisp status after Reopen = %q, want open", got)
	}

	if before, after := durableRows(seeded), durableRows(readUOWDualPresenceSnapshot(t, ctx, provider, id)); !reflect.DeepEqual(before, after) {
		t.Errorf("Close/Reopen of the wisp copy changed the issues copy:\nbefore %q\nafter  %q", before, after)
	}
}
