package dolt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// wispPlaneState is one shape the dolt-ignored wisp plane can take in a
// server-mode working set (bd-cif6y4, #3445). The reporter saw `bd show <epic>`
// list 0, N or 2N children depending on which one the database was in.
type wispPlaneState struct {
	name  string
	apply func(t *testing.T, ctx context.Context, store *DoltStore, epic string)
}

var wispPlaneStates = []wispPlaneState{
	// The control: the table exists and holds none of the epic's edges.
	{name: "empty"},
	// Every inbound edge of the epic is also in wisp_dependencies, the shape a
	// pulled promotion next to a still-local wisp copy leaves behind.
	{name: "mirrored", apply: mirrorInboundEdgesIntoWispPlane},
	// A working-set reset (a received `dolt push`) dropped the ignored table.
	{name: "missing_wisp_dependencies", apply: func(t *testing.T, ctx context.Context, store *DoltStore, _ string) {
		execOnOneConn(t, ctx, store, "DROP TABLE wisp_dependencies")
	}},
	// The same reset with the wisps table gone instead; the wisp-plane tables
	// that reference it keep their (now dangling) foreign keys.
	{name: "missing_wisps", apply: func(t *testing.T, ctx context.Context, store *DoltStore, _ string) {
		execOnOneConn(t, ctx, store, "SET foreign_key_checks = 0", "DROP TABLE wisps", "SET foreign_key_checks = 1")
	}},
}

// seedEpicWithChildren creates an epic with n durable parent-child children
// and closes the first one, so the epic's rollup reads 1/n.
func seedEpicWithChildren(t *testing.T, ctx context.Context, store *DoltStore, prefix string, n int) (*types.Issue, []string) {
	t.Helper()
	epic := &types.Issue{
		ID:        prefix + "-epic",
		Title:     "epic " + prefix,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, epic, "tester"); err != nil {
		t.Fatalf("create epic: %v", err)
	}
	children := make([]string, n)
	for i := range children {
		children[i] = fmt.Sprintf("%s-child-%d", prefix, i+1)
		createPerm(t, ctx, store, children[i])
		addDependency(t, ctx, store, children[i], epic.ID, types.DepParentChild)
	}
	if err := store.CloseIssue(ctx, children[0], "done", "tester", ""); err != nil {
		t.Fatalf("close %s: %v", children[0], err)
	}
	return epic, children
}

// execOnOneConn runs stmts on one pooled connection, so a session setting such
// as foreign_key_checks applies to the statements after it.
func execOnOneConn(t *testing.T, ctx context.Context, store *DoltStore, stmts ...string) {
	t.Helper()
	conn, err := store.UnderlyingDB().Conn(ctx)
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

func mirrorInboundEdgesIntoWispPlane(t *testing.T, ctx context.Context, store *DoltStore, epic string) {
	t.Helper()
	const cols = "id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id"
	// The children are durable issues, so wisp_dependencies' foreign key to
	// wisps would refuse their rows; the reporter's database had no such key.
	execOnOneConn(t, ctx, store,
		"SET foreign_key_checks = 0",
		fmt.Sprintf("INSERT INTO wisp_dependencies (%s) SELECT %s FROM dependencies WHERE depends_on_issue_id = '%s'", cols, cols, epic),
		"SET foreign_key_checks = 1",
	)
	var mirrored int
	if err := store.UnderlyingDB().QueryRowContext(ctx,
		"SELECT count(*) FROM wisp_dependencies WHERE depends_on_issue_id = ?", epic).Scan(&mirrored); err != nil {
		t.Fatalf("count mirrored edges: %v", err)
	}
	if mirrored == 0 {
		t.Fatalf("mirroring copied no edges of %s into wisp_dependencies", epic)
	}
}

// TestDependentsAcrossWispPlaneStates pins that every read `bd show <epic>`
// makes agrees on the epic's N children whatever state the wisp plane is in:
// the listing behind CHILDREN, the count beside it (a count above the listing
// prints a false "edges whose far end has no row" warning), and the
// dependent_count `bd show --json` publishes, and the dependents and epic
// rollup `bd show --json --include-dependents` streams.
func TestDependentsAcrossWispPlaneStates(t *testing.T) {
	for _, state := range wispPlaneStates {
		t.Run(state.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			ctx, cancel := testContext(t)
			defer cancel()

			prefix := "wps-" + strings.ReplaceAll(state.name, "_", "-")
			epic, children := seedEpicWithChildren(t, ctx, store, prefix, 3)
			if state.apply != nil {
				state.apply(t, ctx, store, epic.ID)
			}
			want := int64(len(children))

			listed, err := store.GetDependentsWithMetadata(ctx, epic.ID)
			if err != nil {
				t.Fatalf("GetDependentsWithMetadata: %v", err)
			}
			got := make([]string, 0, len(listed))
			for _, d := range listed {
				if d.DependencyType != types.DepParentChild {
					t.Errorf("dependent %s has type %q, want %q", d.ID, d.DependencyType, types.DepParentChild)
				}
				got = append(got, d.ID)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(children, ",") {
				t.Fatalf("GetDependentsWithMetadata = %v, want %v", got, children)
			}

			if n, err := store.CountDependents(ctx, epic.ID); err != nil || n != want {
				t.Fatalf("CountDependents = %d, %v; want %d, nil", n, err, want)
			}
			if n, err := store.CountDependencies(ctx, children[1]); err != nil || n != 1 {
				t.Fatalf("CountDependencies(%s) = %d, %v; want 1, nil", children[1], n, err)
			}

			details, err := workapi.BuildIssueDetails(ctx, workapi.NewStoreDetailSource(store), epic, false, workapi.DetailOptions{IncludeDependents: true})
			if err != nil {
				t.Fatalf("BuildIssueDetails: %v", err)
			}
			if details.DependentCount == nil || *details.DependentCount != want {
				t.Fatalf("dependent_count = %v, want %d", derefCount(details.DependentCount), want)
			}
			if len(details.Dependents) != len(children) {
				t.Fatalf("streamed %d dependents, want %d", len(details.Dependents), len(children))
			}
			if details.UnresolvableDependents != nil {
				t.Fatalf("unresolvable_dependents = %d, want unset", *details.UnresolvableDependents)
			}
			if derefCount(details.EpicTotalChildren) != len(children) || derefCount(details.EpicClosedChildren) != 1 {
				t.Fatalf("epic progress = %v/%v, want 1/%d",
					derefCount(details.EpicClosedChildren), derefCount(details.EpicTotalChildren), len(children))
			}
		})
	}
}

// TestIterDependentsMissingLeasesStillErrors pins the named half of the
// iterator's fallback: it admits exactly a missing optional wisp table. leases
// is a core table every issue read joins, so a database without it is broken,
// and the dependents stream must surface that rather than answer from the
// durable leg alone (#3445).
func TestIterDependentsMissingLeasesStillErrors(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	epic, _ := seedEpicWithChildren(t, ctx, store, "wps-no-leases", 1)
	execOnOneConn(t, ctx, store, "DROP TABLE leases")

	iter, err := store.IterDependentsWithMetadata(ctx, epic.ID)
	if err == nil {
		_ = iter.Close()
		t.Fatal("IterDependentsWithMetadata over a database with no leases table answered, want the failure surfaced")
	}
	if !dberrors.IsMissingTable(err, "leases") {
		t.Fatalf("IterDependentsWithMetadata error = %v, want it to name the missing leases table", err)
	}
}

func derefCount[T int | int64](n *T) any {
	if n == nil {
		return nil
	}
	return *n
}
