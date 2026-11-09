//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// nodeByNodeTree is the walk GetDependencyTreeInTx replaced: one issue read and
// one adjacency read per expanded node, with each node's edges taken in the
// order the level-by-level read defines. The test keeps it as the statement of
// what that read must still answer.
func nodeByNodeTree(ctx context.Context, tx *sql.Tx, id string, depth, maxDepth int, reverse bool, expanded map[string]*types.Issue, onPath map[string]bool, parent string, edge types.DependencyType) ([]*types.TreeNode, error) {
	if depth >= maxDepth || onPath[id] {
		return nil, nil
	}
	if seen, ok := expanded[id]; ok {
		return []*types.TreeNode{{Issue: *seen, Depth: depth, ParentID: parent, EdgeFromParent: edge, Deduped: true}}, nil
	}
	issue, err := issueops.GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	expanded[id] = issue
	nodes := []*types.TreeNode{{Issue: *issue, Depth: depth, ParentID: parent, EdgeFromParent: edge}}
	if depth+1 >= maxDepth {
		return nodes, nil
	}
	onPath[id] = true
	defer delete(onPath, id)
	var related []*types.IssueWithDependencyMetadata
	if reverse {
		related, err = issueops.GetDependentsWithMetadataInTx(ctx, tx, id)
	} else {
		related, err = issueops.GetDependenciesWithMetadataInTx(ctx, tx, id)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(related, func(i, j int) bool { return related[i].ID < related[j].ID })
	for _, rel := range related {
		if rel.DependencyType == types.DepRelatesTo {
			continue
		}
		children, err := nodeByNodeTree(ctx, tx, rel.ID, depth+1, maxDepth, reverse, expanded, onPath, id, rel.DependencyType)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, children...)
	}
	return nodes, nil
}

func treeRows(tree []*types.TreeNode) string {
	var b strings.Builder
	for _, n := range tree {
		fmt.Fprintf(&b, "%d %s parent=%s edge=%s dedup=%v title=%q labels=%v\n", n.Depth, n.ID, n.ParentID, n.EdgeFromParent, n.Deduped, n.Title, n.Labels)
	}
	return b.String()
}

// TestDependencyTreeMatchesANodeByNodeWalk seeds a random graph on the real
// engine, with diamonds, cycles, relates-to edges, wisps, an id held by both
// tables, an edge held by both planes and edges to external targets, and
// requires the rows of every walk,
// in order, to equal the node-by-node walk's.
func TestDependencyTreeMatchesANodeByNodeWalk(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	fixture := newPristineEmbeddedDoltFixture(t, "treediff")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()

	const issues, wisps, edges = 80, 20, 280
	rnd := rand.New(rand.NewSource(8234))
	issueID := func(i int) string { return fmt.Sprintf("td-%03d", i) }
	wispID := func(i int) string { return fmt.Sprintf("td-w%03d", i) }
	const bothTables = "td-both"
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seed, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := seed.ExecContext(ctx, q, args...); err != nil {
			_ = seed.Rollback()
			t.Fatalf("%s: %v", q, err)
		}
	}
	insertIssue := func(table, id, title string) {
		t.Helper()
		exec("INSERT INTO "+table+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) VALUES (?, ?, '', '', '', '', 'open', 2, 'task', ?, ?)", id, title, at, at)
	}
	for i := range issues {
		insertIssue("issues", issueID(i), "issue "+issueID(i))
		if i%7 == 0 {
			exec("INSERT INTO labels (issue_id, label) VALUES (?, 'z-second'), (?, 'a-first')", issueID(i), issueID(i))
		}
	}
	for i := range wisps {
		insertIssue("wisps", wispID(i), "wisp "+wispID(i))
		if i%5 == 0 {
			exec("INSERT INTO wisp_labels (issue_id, label) VALUES (?, 'wisp-label')", wispID(i))
		}
	}
	insertIssue("issues", bothTables, "the issues row")
	insertIssue("wisps", bothTables, "the wisps row")

	depTypes := []types.DependencyType{types.DepBlocks, types.DepBlocks, types.DepParentChild, types.DepRelated, types.DepRelatesTo}
	seen := make(map[string]bool)
	for n := 0; n < edges; n++ {
		source, sourceTable, column := issueID(rnd.Intn(issues)), "dependencies", "depends_on_issue_id"
		if rnd.Intn(5) == 0 {
			source, sourceTable = wispID(rnd.Intn(wisps)), "wisp_dependencies"
		}
		var target string
		switch r := rnd.Intn(20); {
		case r == 0:
			target, column = "external:proj:cap"+fmt.Sprint(rnd.Intn(3)), "depends_on_external"
		case r == 1:
			target = bothTables
		case r <= 5:
			target, column = wispID(rnd.Intn(wisps)), "depends_on_wisp_id"
		default:
			target = issueID(rnd.Intn(issues))
		}
		if target == source || seen[source+">"+target] {
			continue
		}
		seen[source+">"+target] = true
		exec("INSERT INTO "+sourceTable+" (id, issue_id, "+column+", type, created_by, created_at, metadata) VALUES (?, ?, ?, ?, 'fixture', ?, '{}')",
			fmt.Sprintf("e-%04d", n), source, target, string(depTypes[rnd.Intn(len(depTypes))]), at)
	}
	exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata) VALUES ('e-both', ?, ?, 'blocks', 'fixture', ?, '{}')", bothTables, issueID(0), at)
	// One edge stored in both planes with different types, from the id held by
	// both tables. The durable row's type is the one a walk reports, in either
	// direction.
	const bothPlanes = "td-planes"
	insertIssue("issues", bothPlanes, "target of an edge in both planes")
	exec("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata) VALUES ('e-planes-d', ?, ?, 'blocks', 'fixture', ?, '{}')", bothTables, bothPlanes, at)
	exec("INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata) VALUES ('e-planes-w', ?, ?, 'parent-child', 'fixture', ?, '{}')", bothTables, bothPlanes, at)
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	roots := []string{bothTables, bothPlanes, wispID(0), wispID(1)}
	for i := 0; i < issues; i += 16 {
		roots = append(roots, issueID(i))
	}
	nonTrivial := 0
	for _, root := range roots {
		for _, maxDepth := range []int{2, 3, 50} {
			for _, reverse := range []bool{false, true} {
				want, err := nodeByNodeTree(ctx, tx, root, 0, maxDepth, reverse, map[string]*types.Issue{}, map[string]bool{}, "", "")
				if err != nil {
					t.Fatalf("reference walk of %s: %v", root, err)
				}
				got, err := issueops.GetDependencyTreeInTx(ctx, tx, root, maxDepth, false, reverse)
				if err != nil {
					t.Fatalf("GetDependencyTreeInTx(%s, %d, %v): %v", root, maxDepth, reverse, err)
				}
				if treeRows(got) != treeRows(want) {
					t.Errorf("root %s maxDepth %d reverse %v differs:\n got:\n%s\nwant:\n%s", root, maxDepth, reverse, treeRows(got), treeRows(want))
				}
				if len(want) > 5 {
					nonTrivial++
				}
			}
		}
	}
	if nonTrivial < 10 {
		t.Fatalf("only %d walks returned more than 5 rows; the fixture is too sparse to prove anything", nonTrivial)
	}

	if _, err := issueops.GetDependencyTreeInTx(ctx, tx, "td-absent", 3, false, false); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("absent root: err = %v, want storage.ErrNotFound", err)
	}
}
