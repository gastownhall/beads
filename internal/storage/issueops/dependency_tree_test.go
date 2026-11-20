package issueops

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func TestIsDependencyTreeEdge(t *testing.T) {
	tests := []struct {
		name    string
		depType types.DependencyType
		want    bool
	}{
		{
			name:    "blocks remains a tree edge",
			depType: types.DepBlocks,
			want:    true,
		},
		{
			name:    "parent-child remains a tree edge",
			depType: types.DepParentChild,
			want:    true,
		},
		{
			name:    "related remains a tree edge",
			depType: types.DepRelated,
			want:    true,
		},
		{
			name:    "relates-to is a graph link, not a tree edge",
			depType: types.DepRelatesTo,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDependencyTreeEdge(tt.depType); got != tt.want {
				t.Fatalf("isDependencyTreeEdge(%q) = %v, want %v", tt.depType, got, tt.want)
			}
		})
	}
}

func TestGetDependencyTreeInTxSkipsRelatesToEdges(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "blocker", string(types.DepBlocks)},
		treeEdge{"root", "related", string(types.DepRelatesTo)})
	// The relates-to neighbor is not a tree edge, so it is never read.
	expectTreeIssues(mock, "blocker")
	expectTreeEdges(mock, []string{"blocker"}, false)
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "root", 3, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}

	if len(tree) != 2 {
		t.Fatalf("len(tree) = %d, want 2 nodes: %+v", len(tree), tree)
	}
	if tree[0].ID != "root" || tree[1].ID != "blocker" {
		t.Fatalf("tree IDs = %v, want [root blocker]", treeIDs(tree))
	}
	if tree[1].EdgeFromParent != types.DepBlocks {
		t.Fatalf("blocker edge = %q, want %q", tree[1].EdgeFromParent, types.DepBlocks)
	}
}

// TestGetDependencyTreeInTxDepthBoundBeatsARevisit pins that the depth bound
// wins over a revisit. sx is first reached on the last level, but its edges were
// read through its shorter path from s0, so the walk has an edge from sx into
// sm, which is already expanded. That edge is past the bound and adds no row.
func TestGetDependencyTreeInTxDepthBoundBeatsARevisit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	blocks := string(types.DepBlocks)
	mock.ExpectBegin()
	expectIssue(mock, "s0", "S0")
	expectTreeEdges(mock, []string{"s0"}, false, treeEdge{"s0", "sa", blocks}, treeEdge{"s0", "sx", blocks})
	expectTreeIssues(mock, "sa", "sx")
	expectTreeEdges(mock, []string{"sa", "sx"}, false,
		treeEdge{"sa", "sm", blocks}, treeEdge{"sa", "sx", blocks}, treeEdge{"sx", "sm", blocks})
	expectTreeIssues(mock, "sm")
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "s0", 3, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}

	want := []string{"s0", "sa", "sm", "sx", "sx"}
	if got := treeIDs(tree); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tree IDs = %v, want %v", got, want)
	}
	if tree[3].Deduped || !tree[4].Deduped || tree[4].Depth != 1 {
		t.Fatalf("sx rows = %+v and %+v, want it expanded at depth 2 and stubbed at depth 1", tree[3], tree[4])
	}
}

// TestGetDependencyTreeInTxReadsAnAbsentFarEndOnce pins that a far end in
// neither table is looked up once, not again by each later level that names it.
func TestGetDependencyTreeInTxReadsAnAbsentFarEndOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	blocks := string(types.DepBlocks)
	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false, treeEdge{"root", "a", blocks}, treeEdge{"root", "gone", blocks})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+IssueSelectColumns+" FROM issues "+sqlbuild.LeaseJoin("issues")+" WHERE id IN (?,?)")).
		WithArgs("a", "gone").
		WillReturnRows(issueRows().AddRow(issueRowValues("a", "A")...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (?,?) ORDER BY issue_id, label")).
		WithArgs("a", "gone").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	// The second level names gone again and reads no issue.
	expectTreeEdges(mock, []string{"a"}, false, treeEdge{"a", "gone", blocks})
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "root", 4, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := treeIDs(tree); strings.Join(got, " ") != "root a" {
		t.Fatalf("tree IDs = %v, want [root a]", got)
	}
}

// treeEdge is one scripted row of a level's adjacency read.
type treeEdge struct {
	near, far, depType string
}

func expectIssue(mock sqlmock.Sqlmock, id, title string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(issueRows().AddRow(issueRowValues(id, title)...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))
}

// expectTreeEdges scripts one level's adjacency reads, durable plane then wisp
// plane, with every edge in the durable plane.
func expectTreeEdges(mock sqlmock.Sqlmock, anchors []string, reverse bool, edges ...treeEdge) {
	near, far := "issue_id", DepTargetExpr
	if reverse {
		near, far = DepTargetExpr, "issue_id"
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(anchors)), ",")
	args := make([]driver.Value, len(anchors))
	for i, id := range anchors {
		args[i] = id
	}
	query := func(table string) string {
		return regexp.QuoteMeta("SELECT " + near + ", " + far + ", type FROM " + table + " WHERE " + near + " IN (" + in + ")")
	}
	columns := []string{"near_id", "far_id", "type"}
	rows := sqlmock.NewRows(columns)
	for _, edge := range edges {
		rows.AddRow(edge.near, edge.far, edge.depType)
	}
	mock.ExpectQuery(query("dependencies")).WithArgs(args...).WillReturnRows(rows)
	mock.ExpectQuery(query("wisp_dependencies")).WithArgs(args...).WillReturnRows(sqlmock.NewRows(columns))
}

// expectTreeIssues scripts one level's issue read, each titled with its id
// capitalized.
func expectTreeIssues(mock sqlmock.Sqlmock, ids ...string) {
	in := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]driver.Value, len(ids))
	rows := issueRows()
	for i, id := range ids {
		args[i] = id
		rows.AddRow(issueRowValues(id, strings.ToUpper(id[:1])+id[1:])...)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id IN (" + in + ")")).
		WithArgs(args...).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (" + in + ") ORDER BY issue_id, label")).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
}

func issueRows() *sqlmock.Rows {
	return sqlmock.NewRows(issueColumns())
}

func issueColumns() []string {
	parts := strings.Split(strings.ReplaceAll(IssueSelectColumns, "\n", " "), ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func issueRowValues(id, title string) []driver.Value {
	values := make([]driver.Value, 0, len(issueColumns()))
	for _, col := range issueColumns() {
		switch col {
		case "id":
			values = append(values, id)
		case "title":
			values = append(values, title)
		case "description", "design", "acceptance_criteria", "notes":
			values = append(values, "")
		case "status":
			values = append(values, string(types.StatusOpen))
		case "priority":
			values = append(values, 1)
		case "issue_type":
			values = append(values, string(types.TypeTask))
		case "compaction_level":
			values = append(values, 0)
		default:
			values = append(values, nil)
		}
	}
	return values
}

func treeIDs(tree []*types.TreeNode) []string {
	ids := make([]string, 0, len(tree))
	for _, node := range tree {
		ids = append(ids, node.ID)
	}
	return ids
}

// TestGetDependencyTreeInTxEmitsDedupedDirectBlocker reproduces GH#5282 on a
// diamond: root depends on mid and shared, mid also depends on shared. Shared
// is expanded once, under whichever parent the walk reaches first, and the
// other parent's edge into it is kept as a Deduped leaf stub instead of being
// dropped, so `bd dep tree` and `bd dep list` agree on the direct blockers.
// The stub reuses the issue already read: sqlmock refuses any query the walk
// was not told to expect, so a second issue or dependency read fails here.
func TestGetDependencyTreeInTxEmitsDedupedDirectBlocker(t *testing.T) {
	for _, tt := range []struct {
		name      string
		mid       string
		rootDeps  []string
		wantIDs   []string
		stubDepth int
		stubFrom  string
		fullDepth int
		fullFrom  string
	}{
		{
			name:      "shared reached through mid first",
			mid:       "mid",
			rootDeps:  []string{"mid", "shared"},
			wantIDs:   []string{"root", "mid", "shared", "shared"},
			stubDepth: 1, stubFrom: "root",
			fullDepth: 2, fullFrom: "mid",
		},
		{
			name:      "shared reached directly first",
			mid:       "z-mid",
			rootDeps:  []string{"shared", "z-mid"},
			wantIDs:   []string{"root", "shared", "z-mid", "shared"},
			stubDepth: 2, stubFrom: "z-mid",
			fullDepth: 1, fullFrom: "root",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			expectIssue(mock, "root", "Root")
			expectTreeEdges(mock, []string{"root"}, false,
				treeEdge{"root", tt.rootDeps[0], string(types.DepBlocks)},
				treeEdge{"root", tt.rootDeps[1], string(types.DepBlocks)})
			expectTreeIssues(mock, tt.rootDeps...)
			expectTreeEdges(mock, tt.rootDeps, false, treeEdge{tt.mid, "shared", string(types.DepBlocks)})
			// No expectation for the stub: it must read nothing.

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			tree, err := GetDependencyTreeInTx(context.Background(), tx, "root", 5, false, false)
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("GetDependencyTreeInTx: %v", err)
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}

			if got := treeIDs(tree); strings.Join(got, ",") != strings.Join(tt.wantIDs, ",") {
				t.Fatalf("tree = %v, want %v", got, tt.wantIDs)
			}
			var stubs []*types.TreeNode
			for _, node := range tree {
				if node.Deduped {
					stubs = append(stubs, node)
				}
			}
			if len(stubs) != 1 || stubs[0] != tree[3] {
				t.Fatalf("Deduped rows = %d, want exactly one, the last row", len(stubs))
			}
			stub := tree[3]
			if stub.ID != "shared" || stub.Depth != tt.stubDepth || stub.ParentID != tt.stubFrom || stub.EdgeFromParent != types.DepBlocks {
				t.Fatalf("stub = id %q depth %d parent %q edge %q, want shared/%d/%s/blocks",
					stub.ID, stub.Depth, stub.ParentID, stub.EdgeFromParent, tt.stubDepth, tt.stubFrom)
			}
			if stub.Title != "Shared" {
				t.Fatalf("stub title = %q, want the expanded issue's %q", stub.Title, "Shared")
			}
			for _, node := range tree[:3] {
				if node.ID == "shared" && (node.Depth != tt.fullDepth || node.ParentID != tt.fullFrom) {
					t.Fatalf("expanded shared = depth %d parent %q, want %d/%s", node.Depth, node.ParentID, tt.fullDepth, tt.fullFrom)
				}
			}
		})
	}
}

// TestGetDependencyTreeInTxDepthCutoffBeatsStub pins that the depth bound wins
// over the revisit: root expands x at depth 1, and the longer path root→a→b→x
// would reach x at depth 3 == maxDepth, where it is absent rather than a stub.
// b sits on the last level the bound allows, so its edges, including the one
// into x, are not read at all.
func TestGetDependencyTreeInTxDepthCutoffBeatsStub(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "x", string(types.DepBlocks)},
		treeEdge{"root", "a", string(types.DepBlocks)})
	expectTreeIssues(mock, "a", "x")
	expectTreeEdges(mock, []string{"a", "x"}, false, treeEdge{"a", "b", string(types.DepBlocks)})
	expectTreeIssues(mock, "b")

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "root", 3, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := treeIDs(tree); strings.Join(got, ",") != "root,a,b,x" {
		t.Fatalf("tree = %v, want [root a b x] with no x stub", got)
	}
	for _, node := range tree {
		if node.Deduped {
			t.Fatalf("node %s is Deduped, want no stub at or beyond the cutoff", node.ID)
		}
	}
}

// TestGetDependencyTreeInTxRecordsALastLevelNodeAsExpanded pins that a node
// on the last level maxDepth allows still counts as expanded, although the
// walk returns it without reading its edges. root depends on a then b, and a
// depends on b, with maxDepth 3: b is first reached through a at depth 2, the
// last level, and the root's own edge into b must then be a Deduped stub at
// depth 1 that reuses the issue already read. If the early return came before
// b was recorded, the walk would expand b a second time at depth 1, sending
// reads this mock does not script.
func TestGetDependencyTreeInTxRecordsALastLevelNodeAsExpanded(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "a", string(types.DepBlocks)},
		treeEdge{"root", "b", string(types.DepBlocks)})
	expectTreeIssues(mock, "a", "b")
	// Both are read at level 1, where the bound still allows their edges. The
	// walk reaches b through a at depth 2 first and must not descend from there.
	expectTreeEdges(mock, []string{"a", "b"}, false,
		treeEdge{"a", "b", string(types.DepBlocks)},
		treeEdge{"b", "c", string(types.DepBlocks)})
	expectTreeIssues(mock, "c")
	// c was read because b sits on level 1, but the walk reaches b at depth 2
	// first, so c would be at depth 3 and must not appear. No expectation for
	// the stub: it must read nothing.

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "root", 3, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}

	if got := treeIDs(tree); strings.Join(got, ",") != "root,a,b,b" {
		t.Fatalf("tree = %v, want [root a b b]", got)
	}
	for i, node := range tree {
		if node.Deduped != (i == 3) {
			t.Fatalf("row %d (%s) Deduped = %v, want only the last row to be a stub", i, node.ID, node.Deduped)
		}
	}
	if full := tree[2]; full.Depth != 2 || full.ParentID != "a" {
		t.Fatalf("expanded b = depth %d parent %q, want 2/a", full.Depth, full.ParentID)
	}
	stub := tree[3]
	if stub.Depth != 1 || stub.ParentID != "root" || stub.EdgeFromParent != types.DepBlocks {
		t.Fatalf("stub of b = depth %d parent %q edge %q, want 1/root/blocks", stub.Depth, stub.ParentID, stub.EdgeFromParent)
	}
	if stub.Title != "B" {
		t.Fatalf("stub title = %q, want the expanded issue's %q", stub.Title, "B")
	}
}

// TestGetDependencyTreeInTxReadsNothingPastTheDepthLimit walks from root, in
// each direction, with a depth limit of one level and of two. A node on the
// last level the limit allows is read and returned, but its edges lead only to
// nodes the walk will not return, so the walk must not read its adjacency or
// hydrate its neighbors. The mock scripts exactly the reads the walk needs and
// is reached through sqlcount, so a read beyond them is counted even if the
// walk dropped the error sqlmock answers it with.
func TestGetDependencyTreeInTxReadsNothingPastTheDepthLimit(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, tt := range []struct {
			maxDepth       int
			wantIDs        string
			wantStatements int64
		}{
			// The root's issue row and labels.
			{maxDepth: 1, wantIDs: "root", wantStatements: 2},
			// Plus the root's adjacency in both planes (2) and a's issue row
			// and labels (2).
			{maxDepth: 2, wantIDs: "root,a", wantStatements: 6},
		} {
			t.Run(fmt.Sprintf("reverse=%v/maxDepth=%d", reverse, tt.maxDepth), func(t *testing.T) {
				mock, tx, counts := countedMockTx(t)
				expectIssue(mock, "root", "Root")
				if tt.maxDepth > 1 {
					expectTreeEdges(mock, []string{"root"}, reverse, treeEdge{"root", "a", string(types.DepBlocks)})
					expectTreeIssues(mock, "a")
				}

				tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", tt.maxDepth, false, reverse)
				if err != nil {
					t.Fatalf("GetDependencyTreeInTx: %v (statements sent: %d)", err, counts.Total())
				}
				if n := counts.Total(); n != tt.wantStatements {
					t.Fatalf("the walk sent %d statements, want exactly the %d scripted", n, tt.wantStatements)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet sql expectations: %v", err)
				}
				if got := strings.Join(treeIDs(tree), ","); got != tt.wantIDs {
					t.Fatalf("tree = %s, want %s", got, tt.wantIDs)
				}
			})
		}
	}
}

// TestGetDependencyTreeInTxTellsABackEdgeFromACrossEdge walks a cycle
// a → b → c → a that also has a cross edge a → c. The two revisits are
// answered differently: the back-edge into a (on the current path) adds no
// row, so the root is never a stub, while the second edge into the finished c
// is kept as exactly one Deduped stub, in either child order of a.
func TestGetDependencyTreeInTxTellsABackEdgeFromACrossEdge(t *testing.T) {
	for _, tt := range []struct {
		name     string
		via      string // the node on the path from a to c
		rootDeps []string
		wantIDs  []string
		stub     [2]string // parent, then the edge's depth as a string
	}{
		{name: "the path first", via: "b", rootDeps: []string{"b", "c"}, wantIDs: []string{"a", "b", "c", "c"}, stub: [2]string{"a", "1"}},
		{name: "c first", via: "z", rootDeps: []string{"c", "z"}, wantIDs: []string{"a", "c", "z", "c"}, stub: [2]string{"z", "2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			expectIssue(mock, "a", "A")
			expectTreeEdges(mock, []string{"a"}, false,
				treeEdge{"a", tt.rootDeps[0], string(types.DepBlocks)},
				treeEdge{"a", tt.rootDeps[1], string(types.DepBlocks)})
			expectTreeIssues(mock, tt.rootDeps...)
			// a is already read, so the back-edge into it reads nothing more.
			expectTreeEdges(mock, tt.rootDeps, false,
				treeEdge{tt.via, "c", string(types.DepBlocks)},
				treeEdge{"c", "a", string(types.DepBlocks)})

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			tree, err := GetDependencyTreeInTx(context.Background(), tx, "a", 50, false, false)
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("GetDependencyTreeInTx: %v", err)
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}

			if got := treeIDs(tree); strings.Join(got, ",") != strings.Join(tt.wantIDs, ",") {
				t.Fatalf("tree = %v, want %v", got, tt.wantIDs)
			}
			for i, node := range tree {
				if node.Deduped != (i == 3) {
					t.Fatalf("row %d (%s) Deduped = %v, want only the last row to be a stub", i, node.ID, node.Deduped)
				}
			}
			stub := tree[3]
			if stub.ParentID != tt.stub[0] || strconv.Itoa(stub.Depth) != tt.stub[1] || stub.EdgeFromParent != types.DepBlocks {
				t.Fatalf("stub of c = parent %q depth %d edge %q, want %s/%s/blocks", stub.ParentID, stub.Depth, stub.EdgeFromParent, tt.stub[0], tt.stub[1])
			}
		})
	}
}

// TestGetDependencyTreeInTxReadsLevelsNotNodes pins the cost of a wide tree: a
// root with 30 children that each have one child takes one read per level and
// plane, however many nodes each level holds.
func TestGetDependencyTreeInTxReadsLevelsNotNodes(t *testing.T) {
	const fanout = 30
	mock, tx, counts := countedMockTx(t)
	expectIssue(mock, "root", "Root")

	children := make([]string, fanout)
	grandchildren := make([]string, fanout)
	rootEdges := make([]treeEdge, fanout)
	childEdges := make([]treeEdge, fanout)
	for i := range children {
		children[i] = fmt.Sprintf("c%02d", i)
		grandchildren[i] = fmt.Sprintf("g%02d", i)
		rootEdges[i] = treeEdge{"root", children[i], string(types.DepBlocks)}
		childEdges[i] = treeEdge{children[i], grandchildren[i], string(types.DepBlocks)}
	}
	expectTreeEdges(mock, []string{"root"}, false, rootEdges...)
	expectTreeIssues(mock, children...)
	expectTreeEdges(mock, children, false, childEdges...)
	expectTreeIssues(mock, grandchildren...)

	tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", 3, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if len(tree) != 1+2*fanout {
		t.Fatalf("len(tree) = %d, want %d", len(tree), 1+2*fanout)
	}
	// Root (2), then adjacency (2) and issues with labels (2) for each of two levels.
	if n := counts.Total(); n != 10 {
		t.Fatalf("the walk sent %d statements, want 10", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	// The order is depth-first: a child, then its own child, children by id.
	if tree[1].ID != "c00" || tree[2].ID != "g00" || tree[3].ID != "c01" {
		t.Fatalf("first rows = %v, want c00, g00, c01", treeIDs(tree[:4]))
	}
}

// TestGetDependencyTreeInTxResolvesEachIDAsGetIssueDoes pins the two tables'
// precedence on the batched read: an id issues holds is taken from issues, an
// id only wisps holds is taken from wisps with its wisp_labels, and an edge
// whose far end is in neither table adds no row and does not stop the walk.
func TestGetDependencyTreeInTxResolvesEachIDAsGetIssueDoes(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "wisp", string(types.DepBlocks)},
		treeEdge{"root", "external:proj:cap", string(types.DepBlocks)},
		treeEdge{"root", "durable", string(types.DepBlocks)})

	issueSQL := "SELECT " + IssueSelectColumns + " FROM %s " + "%s WHERE id IN (%s)"
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(issueSQL, "issues", sqlbuild.LeaseJoin("issues"), "?,?,?"))).
		WithArgs("durable", "external:proj:cap", "wisp").
		WillReturnRows(issueRows().AddRow(issueRowValues("durable", "From issues")...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (?,?,?) ORDER BY issue_id, label")).
		WithArgs("durable", "external:proj:cap", "wisp").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps WHERE id IN (?,?)")).
		WithArgs("external:proj:cap", "wisp").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wisp"))
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(issueSQL, "wisps", sqlbuild.LeaseJoin("wisps"), "?"))).
		WithArgs("wisp").
		WillReturnRows(issueRows().AddRow(issueRowValues("wisp", "From wisps")...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM wisp_labels WHERE issue_id IN (?) ORDER BY issue_id, label")).
		WithArgs("wisp").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).AddRow("wisp", "ephemeral-label"))

	tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", 2, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := strings.Join(treeIDs(tree), ","); got != "root,durable,wisp" {
		t.Fatalf("tree = %s, want root,durable,wisp", got)
	}
	if tree[1].Title != "From issues" || tree[2].Title != "From wisps" {
		t.Fatalf("titles = %q, %q", tree[1].Title, tree[2].Title)
	}
	if len(tree[2].Labels) != 1 || tree[2].Labels[0] != "ephemeral-label" {
		t.Fatalf("wisp labels = %v", tree[2].Labels)
	}
}

// TestGetDependencyTreeInTxToleratesAMissingWispPlane pins the adjacency read's
// one tolerance: wisp_dependencies can be dropped by a working-set reset, and
// its absence means no wisp-plane edges.
func TestGetDependencyTreeInTxToleratesAMissingWispPlane(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	expectIssue(mock, "root", "Root")
	columns := []string{"near_id", "far_id", "type"}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM dependencies WHERE issue_id IN (?)")).
		WithArgs("root").
		WillReturnRows(sqlmock.NewRows(columns).AddRow("root", "a", string(types.DepBlocks)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM wisp_dependencies WHERE issue_id IN (?)")).
		WithArgs("root").
		WillReturnError(tableNotFound("wisp_dependencies"))
	expectTreeIssues(mock, "a")

	tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", 2, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if got := strings.Join(treeIDs(tree), ","); got != "root,a" {
		t.Fatalf("tree = %s, want root,a", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestGetDependencyTreeInTxOrderDecidesWhatADepthLimitKeeps pins the one way
// edge order reaches the set of issues returned, not only their arrangement.
// a sorts before z, so z is first reached through a at depth 2, the last level
// maxDepth 3 allows, and its child is cut. Had the walk taken z first, at depth
// 1, the child would be returned.
func TestGetDependencyTreeInTxOrderDecidesWhatADepthLimitKeeps(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "z", string(types.DepBlocks)},
		treeEdge{"root", "a", string(types.DepBlocks)})
	expectTreeIssues(mock, "a", "z")
	expectTreeEdges(mock, []string{"a", "z"}, false,
		treeEdge{"a", "z", string(types.DepBlocks)},
		treeEdge{"z", "child", string(types.DepBlocks)})
	expectTreeIssues(mock, "child")

	tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", 3, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := strings.Join(treeIDs(tree), ","); got != "root,a,z,z" {
		t.Fatalf("tree = %s, want root,a,z,z with child cut by the depth limit", got)
	}
}

// TestGetDependencyTreeInTxReadsASharedChildOnce pins that two nodes of one
// level pointing at the same unread issue read it once, and that the walk still
// answers the second edge with a stub.
func TestGetDependencyTreeInTxReadsASharedChildOnce(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	expectIssue(mock, "root", "Root")
	expectTreeEdges(mock, []string{"root"}, false,
		treeEdge{"root", "a", string(types.DepBlocks)},
		treeEdge{"root", "b", string(types.DepBlocks)})
	expectTreeIssues(mock, "a", "b")
	expectTreeEdges(mock, []string{"a", "b"}, false,
		treeEdge{"a", "shared", string(types.DepBlocks)},
		treeEdge{"b", "shared", string(types.DepBlocks)})
	expectTreeIssues(mock, "shared")

	tree, err := GetDependencyTreeInTx(t.Context(), tx, "root", 3, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	if got := strings.Join(treeIDs(tree), ","); got != "root,a,shared,b,shared" {
		t.Fatalf("tree = %s, want root,a,shared,b,shared", got)
	}
	if !tree[4].Deduped || tree[2].Deduped {
		t.Fatalf("only the second shared row is a stub: %v, %v", tree[2].Deduped, tree[4].Deduped)
	}
}

// TestGetDependencyTreeInTxReadsNothingForAZeroDepth pins that a walk with no
// levels to return sends nothing, so it also cannot fail on an absent root.
func TestGetDependencyTreeInTxReadsNothingForAZeroDepth(t *testing.T) {
	_, tx, counts := countedMockTx(t)
	tree, err := GetDependencyTreeInTx(t.Context(), tx, "absent", 0, false, false)
	if err != nil || tree != nil {
		t.Fatalf("tree = %v, err = %v, want nil, nil", tree, err)
	}
	if n := counts.Total(); n != 0 {
		t.Fatalf("sent %d statements, want none", n)
	}
}

// TestAnchorsEdgesInTxKeepsTheDurableRowOfAnEdgeInBothPlanes pins the per-anchor
// dedup: the same far end under two anchors is two edges, and one far end in
// both planes under one anchor is one edge with the durable row's type.
func TestAnchorsEdgesInTxKeepsTheDurableRowOfAnEdgeInBothPlanes(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	columns := []string{"near_id", "far_id", "type"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM dependencies WHERE issue_id IN (?,?)")).
		WithArgs("a", "b").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("a", "x", string(types.DepBlocks)).
			AddRow("b", "x", string(types.DepBlocks)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_dependencies WHERE issue_id IN (?,?)")).
		WithArgs("a", "b").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("a", "x", string(types.DepRelated)).
			AddRow("a", "y", string(types.DepRelated)))

	edges, err := anchorsEdgesInTx(t.Context(), tx, []string{"a", "b"}, false)
	if err != nil {
		t.Fatalf("anchorsEdgesInTx: %v", err)
	}
	want := map[string][]anchorEdge{
		"a": {{farID: "x", depType: string(types.DepBlocks)}, {farID: "y", depType: string(types.DepRelated)}},
		"b": {{farID: "x", depType: string(types.DepBlocks)}},
	}
	if fmt.Sprint(edges) != fmt.Sprint(want) {
		t.Fatalf("edges = %v, want %v", edges, want)
	}
}

// TestAnchorsEdgesInTxSortsEachAnchorsEdges pins that the order of an anchor's
// edges does not follow the order the server returned them in, across planes.
func TestAnchorsEdgesInTxSortsEachAnchorsEdges(t *testing.T) {
	mock, tx, _ := countedMockTx(t)
	columns := []string{"near_id", "far_id", "type"}
	mock.ExpectQuery(regexp.QuoteMeta("FROM dependencies WHERE issue_id IN (?)")).
		WithArgs("a").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("a", "z", string(types.DepBlocks)).
			AddRow("a", "m", string(types.DepBlocks)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_dependencies WHERE issue_id IN (?)")).
		WithArgs("a").
		WillReturnRows(sqlmock.NewRows(columns).AddRow("a", "c", string(types.DepBlocks)))

	edges, err := anchorsEdgesInTx(t.Context(), tx, []string{"a"}, false)
	if err != nil {
		t.Fatalf("anchorsEdgesInTx: %v", err)
	}
	var got []string
	for _, edge := range edges["a"] {
		got = append(got, edge.farID)
	}
	if strings.Join(got, ",") != "c,m,z" {
		t.Fatalf("far ends = %v, want c, m, z", got)
	}
}

// TestAnchorsEdgesInTxBatchesLargeFrontiers pins the IN-list bound: a frontier
// over queryBatchSize is split, in each plane, and its rows are merged.
func TestAnchorsEdgesInTxBatchesLargeFrontiers(t *testing.T) {
	anchors := make([]string, queryBatchSize+1)
	for i := range anchors {
		anchors[i] = fmt.Sprintf("n%d", i)
	}
	mock, tx, counts := countedMockTx(t)
	columns := []string{"near_id", "far_id", "type"}
	first := strings.TrimSuffix(strings.Repeat("?,", queryBatchSize), ",")
	mock.ExpectQuery(regexp.QuoteMeta("FROM dependencies WHERE issue_id IN (" + first + ")")).
		WillReturnRows(sqlmock.NewRows(columns).AddRow("n0", "x", string(types.DepBlocks)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM dependencies WHERE issue_id IN (?)")).
		WithArgs(anchors[queryBatchSize]).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(anchors[queryBatchSize], "y", string(types.DepBlocks)))
	mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_dependencies WHERE issue_id IN (" + first + ")")).
		WillReturnRows(sqlmock.NewRows(columns))
	mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_dependencies WHERE issue_id IN (?)")).
		WithArgs(anchors[queryBatchSize]).
		WillReturnRows(sqlmock.NewRows(columns))

	edges, err := anchorsEdgesInTx(t.Context(), tx, anchors, false)
	if err != nil {
		t.Fatalf("anchorsEdgesInTx: %v", err)
	}
	if n := counts.Total(); n != 4 {
		t.Fatalf("statements = %d, want 4", n)
	}
	if len(edges["n0"]) != 1 || len(edges[anchors[queryBatchSize]]) != 1 {
		t.Fatalf("edges = %v", edges)
	}
}
