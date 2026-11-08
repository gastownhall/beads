//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// embeddedDualPresenceTables are the tables whose rows for the twin are
// compared before and after a refused update.
var embeddedDualPresenceTables = []struct{ table, key string }{
	{"issues", "id"}, {"wisps", "id"},
	{"labels", "issue_id"}, {"wisp_labels", "issue_id"},
	{"events", "issue_id"}, {"wisp_events", "issue_id"},
	{"dependencies", "issue_id"}, {"wisp_dependencies", "issue_id"},
	{"issue_versions", "issue_id"},
}

// embeddedDualPresenceSnapshot renders every row naming id in those tables,
// plus the Dolt commit count, as text.
func embeddedDualPresenceSnapshot(t *testing.T, ctx context.Context, te *testEnv, id string) []string {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var snap []string
	for _, tk := range embeddedDualPresenceTables {
		//nolint:gosec // G201: table and key come from the fixed list above.
		rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s WHERE %s = ?", tk.table, tk.key), id)
		if err != nil {
			t.Fatalf("snapshot %s: %v", tk.table, err)
		}
		cols, _ := rows.Columns()
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
				fields[i] = cols[i] + "=" + string(v)
			}
			rendered = append(rendered, tk.table+": "+strings.Join(fields, "|"))
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot %s rows: %v", tk.table, err)
		}
		sort.Strings(rendered)
		snap = append(snap, rendered...)
	}
	var commits int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commits); err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	return append(snap, fmt.Sprintf("dolt_log=%d", commits))
}

// seedEmbeddedDualPresentIssue creates a durable issue and a wisp twin of the
// same ID whose notes and metadata differ (GH#5991).
func seedEmbeddedDualPresentIssue(t *testing.T, ctx context.Context, te *testEnv, id string) {
	t.Helper()
	durable := &types.Issue{
		ID: id, Title: "durable " + id, Notes: "durable notes", Metadata: []byte(`{"copy":"durable"}`),
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: []string{"durable-label"},
	}
	if err := te.store.CreateIssue(ctx, durable, "seed"); err != nil {
		t.Fatalf("create durable %s: %v", id, err)
	}
	te.exec(t, ctx, `INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, metadata)
		VALUES (?, ?, '', '', '', 'wisp notes', 'open', 3, 'task', 1, '{"copy":"wisp"}')`, id, "wisp "+id)
	te.exec(t, ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'wisp-label')", id)
}

func requireEmbeddedDualPresenceRefusal(t *testing.T, err error, id string) {
	t.Helper()
	if !errors.Is(err, publicops.ErrDualPresence) {
		t.Fatalf("update of dual-present %s: err = %v, want ErrDualPresence", id, err)
	}
	var dual *publicops.DualPresenceError
	if !errors.As(err, &dual) || dual.ID != id {
		t.Fatalf("update of dual-present %s: errors.As = %#v from %v, want ID %q", id, dual, err, id)
	}
}

// TestEmbeddedUpdateRefusesDualPresence proves the shared guard reaches the
// embedded store: its legacy map update and its Lifecycle role both run the
// shared issueops update, which used to write the wisp twin of an ID that
// reads resolve to the issues copy.
func TestEmbeddedUpdateRefusesDualPresence(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dual")
	ctx := t.Context()

	cases := []struct {
		name string
		run  func(id string) error
	}{
		{"UpdateIssue notes", func(id string) error {
			return te.store.UpdateIssue(ctx, id, map[string]interface{}{"notes": "probe"}, "tester")
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("dual-emb-%d", i)
			seedEmbeddedDualPresentIssue(t, ctx, te, id)
			before := embeddedDualPresenceSnapshot(t, ctx, te, id)
			requireEmbeddedDualPresenceRefusal(t, tc.run(id), id)
			if after := embeddedDualPresenceSnapshot(t, ctx, te, id); !reflect.DeepEqual(before, after) {
				t.Errorf("refused update changed persisted state:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}
