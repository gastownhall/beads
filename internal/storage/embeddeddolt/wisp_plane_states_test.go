//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// execOnOneConn runs stmts on one raw connection, so a session setting such as
// foreign_key_checks applies to the statements after it.
func (te *testEnv) execOnOneConn(t *testing.T, ctx context.Context, stmts ...string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Close()
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

// TestDependentsAcrossWispPlaneStates is the embedded twin of the dolt
// package's test of the same name (bd-cif6y4, #3445): the listing behind
// `bd show <epic>` CHILDREN and its k/N line, the count beside it, the
// published dependent_count, and the streamed dependents and epic rollup agree
// on the epic's N children whether wisp_dependencies is empty, mirrors the
// durable edges (with or without a wisp row for each child), or is missing.
func TestDependentsAcrossWispPlaneStates(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	const cols = "id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id"
	mirror := func(t *testing.T, ctx context.Context, te *testEnv, epic string) {
		// The children are durable issues, so wisp_dependencies' foreign key
		// to wisps would refuse their rows.
		te.execOnOneConn(t, ctx,
			"SET foreign_key_checks = 0",
			fmt.Sprintf("INSERT INTO wisp_dependencies (%s) SELECT %s FROM dependencies WHERE depends_on_issue_id = '%s'", cols, cols, epic),
			"SET foreign_key_checks = 1",
		)
		var mirrored int
		te.queryScalar(t, ctx, "SELECT count(*) FROM wisp_dependencies WHERE depends_on_issue_id = ?", []any{epic}, &mirrored)
		if mirrored == 0 {
			t.Fatalf("mirroring copied no edges of %s into wisp_dependencies", epic)
		}
	}
	states := []struct {
		name  string
		apply func(t *testing.T, ctx context.Context, te *testEnv, epic string)
		// closed is how many of the epic's children every reader sees closed.
		closed int
	}{
		{name: "empty", closed: 1},
		{name: "mirrored", closed: 1, apply: mirror},
		// Each child also holds an open wisps row, written the way a pull
		// leaves it. The wisp copy is canonical (be-iabdi), so the child
		// closed in issues reads open and the rollup reads 0/N.
		{name: "dual_resident", closed: 0, apply: func(t *testing.T, ctx context.Context, te *testEnv, epic string) {
			te.exec(t, ctx, "INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral, created_at, updated_at) "+
				"SELECT id, CONCAT('wisp twin of ', id), 'open', priority, issue_type, 1, created_at, updated_at FROM issues "+
				"WHERE id IN (SELECT issue_id FROM dependencies WHERE depends_on_issue_id = ?)", epic)
			mirror(t, ctx, te, epic)
		}},
		{name: "missing_wisp_dependencies", closed: 1, apply: func(t *testing.T, ctx context.Context, te *testEnv, _ string) {
			te.exec(t, ctx, "DROP TABLE wisp_dependencies")
		}},
	}

	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			ctx := t.Context()
			prefix := "wps" + strings.ReplaceAll(state.name, "_", "")
			te := newTestEnv(t, prefix)

			epic := &types.Issue{ID: prefix + "-epic", Title: "epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
			if err := te.store.CreateIssue(ctx, epic, "tester"); err != nil {
				t.Fatalf("create epic: %v", err)
			}
			children := make([]string, 3)
			for i := range children {
				children[i] = fmt.Sprintf("%s-child-%d", prefix, i+1)
				child := &types.Issue{ID: children[i], Title: children[i], Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := te.store.CreateIssue(ctx, child, "tester"); err != nil {
					t.Fatalf("create %s: %v", children[i], err)
				}
				if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: children[i], DependsOnID: epic.ID, Type: types.DepParentChild}, "tester"); err != nil {
					t.Fatalf("add dependency %s: %v", children[i], err)
				}
			}
			if err := te.store.CloseIssue(ctx, children[0], "done", "tester", ""); err != nil {
				t.Fatalf("close %s: %v", children[0], err)
			}
			if state.apply != nil {
				state.apply(t, ctx, te, epic.ID)
			}
			want := int64(len(children))

			listed, err := te.store.GetDependentsWithMetadata(ctx, epic.ID)
			if err != nil {
				t.Fatalf("GetDependentsWithMetadata: %v", err)
			}
			got := make([]string, 0, len(listed))
			closed := 0
			for _, d := range listed {
				if d.Status == types.StatusClosed {
					closed++
				}
				got = append(got, d.ID)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(children, ",") {
				t.Fatalf("GetDependentsWithMetadata = %v, want %v", got, children)
			}
			if closed != state.closed {
				t.Fatalf("GetDependentsWithMetadata lists %d closed children, want %d", closed, state.closed)
			}
			if n, err := te.store.CountDependents(ctx, epic.ID); err != nil || n != want {
				t.Fatalf("CountDependents = %d, %v; want %d, nil", n, err, want)
			}
			if n, err := te.store.CountDependencies(ctx, children[1]); err != nil || n != 1 {
				t.Fatalf("CountDependencies(%s) = %d, %v; want 1, nil", children[1], n, err)
			}
			details, err := workapi.BuildIssueDetails(ctx, workapi.NewStoreDetailSource(te.store), epic, false, workapi.DetailOptions{IncludeDependents: true})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			if details.DependentCount == nil || *details.DependentCount != want {
				t.Fatalf("dependent_count = %v, want %d", details.DependentCount, want)
			}
			if streamed, listedRows := dependentRows(details.Dependents), dependentRows(listed); streamed != listedRows {
				t.Fatalf("streamed dependents %s, want the listing's %s", streamed, listedRows)
			}
			if details.UnresolvableDependents != nil {
				t.Fatalf("unresolvable_dependents = %d, want unset", *details.UnresolvableDependents)
			}
			if progress, wantProgress := epicProgress(details), fmt.Sprintf("%d/%d", state.closed, len(children)); progress != wantProgress {
				t.Fatalf("epic progress = %s, want %s", progress, wantProgress)
			}
		})
	}
}

// dependentRows renders dependents as their sorted id/type/status triples: the
// fields a CHILDREN line prints and the epic rollup counts.
func dependentRows(deps []*types.IssueWithDependencyMetadata) string {
	rows := make([]string, 0, len(deps))
	for _, d := range deps {
		rows = append(rows, fmt.Sprintf("%s/%s/%s", d.ID, d.DependencyType, d.Status))
	}
	sort.Strings(rows)
	return "[" + strings.Join(rows, " ") + "]"
}

// epicProgress renders the epic rollup as closed/total, with "-" for a field
// the detail left unset.
func epicProgress(details *types.IssueDetails) string {
	field := func(n *int) string {
		if n == nil {
			return "-"
		}
		return fmt.Sprint(*n)
	}
	return field(details.EpicClosedChildren) + "/" + field(details.EpicTotalChildren)
}
