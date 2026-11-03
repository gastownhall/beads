package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/dberrors"
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
	expectDependencies(mock, "root", []dependencyRow{
		{id: "blocker", depType: string(types.DepBlocks)},
		{id: "related", depType: string(types.DepRelatesTo)},
	})
	expectIssueBatch(mock, []string{"blocker", "related"})
	expectIssue(mock, "blocker", "Blocker")
	expectDependencies(mock, "blocker", nil)
	expectTreeBlocked(mock, []string{"root", "blocker"}, map[string]bool{"root": true, "blocker": false}, nil)
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
	if !tree[0].IsBlocked || tree[1].IsBlocked {
		t.Fatalf("IsBlocked = root %v, blocker %v; want the stored true, false", tree[0].IsBlocked, tree[1].IsBlocked)
	}
}

type dependencyRow struct {
	id      string
	depType string
}

func expectIssue(mock sqlmock.Sqlmock, id, title string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(issueRows().AddRow(issueRowValues(id, title)...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"label"}))
}

func expectDependencies(mock sqlmock.Sqlmock, issueID string, deps []dependencyRow) {
	rows := sqlmock.NewRows([]string{"depends_on_id", "type"})
	for _, dep := range deps {
		rows.AddRow(dep.id, dep.depType)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM dependencies WHERE issue_id = ?")).
		WithArgs(issueID).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM wisp_dependencies WHERE issue_id = ?")).
		WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"depends_on_id", "type"}))
}

func expectIssueBatch(mock sqlmock.Sqlmock, ids []string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)

	rows := issueRows()
	for _, id := range ids {
		rows.AddRow(issueRowValues(id, id)...)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+IssueSelectColumns+" FROM issues "+sqlbuild.LeaseJoin("issues")+" WHERE id IN (?,?)")).
		WithArgs(ids[0], ids[1]).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (?,?) ORDER BY issue_id, label")).
		WithArgs(ids[0], ids[1]).
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
		rootDeps  []string
		wantIDs   []string
		stubDepth int
		stubFrom  string
		fullDepth int
		fullFrom  string
	}{
		{
			name:      "shared reached through mid first",
			rootDeps:  []string{"mid", "shared"},
			wantIDs:   []string{"root", "mid", "shared", "shared"},
			stubDepth: 1, stubFrom: "root",
			fullDepth: 2, fullFrom: "mid",
		},
		{
			name:      "shared reached directly first",
			rootDeps:  []string{"shared", "mid"},
			wantIDs:   []string{"root", "shared", "mid", "shared"},
			stubDepth: 2, stubFrom: "mid",
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
			expectDependencies(mock, "root", []dependencyRow{
				{id: tt.rootDeps[0], depType: string(types.DepBlocks)},
				{id: tt.rootDeps[1], depType: string(types.DepBlocks)},
			})
			expectIssueBatch(mock, tt.rootDeps)
			if tt.rootDeps[0] == "mid" {
				expectIssue(mock, "mid", "Mid")
				expectDependencies(mock, "mid", []dependencyRow{{id: "shared", depType: string(types.DepBlocks)}})
				expectIssueBatchOne(mock, "shared")
				expectIssue(mock, "shared", "Shared")
				expectDependencies(mock, "shared", nil)
			} else {
				expectIssue(mock, "shared", "Shared")
				expectDependencies(mock, "shared", nil)
				expectIssue(mock, "mid", "Mid")
				expectDependencies(mock, "mid", []dependencyRow{{id: "shared", depType: string(types.DepBlocks)}})
				expectIssueBatchOne(mock, "shared")
			}
			// No expectation for the stub: it must read nothing. The one flag
			// read covers the walk's distinct ids.
			expectTreeBlocked(mock, tt.wantIDs[:3], map[string]bool{"root": true, "mid": true, "shared": false}, nil)

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
			for _, node := range tree {
				if node.IsBlocked != (node.ID != "shared") {
					t.Fatalf("%s IsBlocked = %v, want the stored flag", node.ID, node.IsBlocked)
				}
			}
			for _, node := range tree[:3] {
				if node.ID == "shared" && (node.Depth != tt.fullDepth || node.ParentID != tt.fullFrom) {
					t.Fatalf("expanded shared = depth %d parent %q, want %d/%s", node.Depth, node.ParentID, tt.fullDepth, tt.fullFrom)
				}
			}
		})
	}
}

// TestGetDependencyTreeInTxDepthCutoffBeatsStub pins that the depth bound is
// checked before the revisit: root expands x at depth 1, and the longer path
// root→a→b→x reaches x again at depth 3 == maxDepth, where it is absent rather
// than a stub.
func TestGetDependencyTreeInTxDepthCutoffBeatsStub(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectDependencies(mock, "root", []dependencyRow{
		{id: "x", depType: string(types.DepBlocks)},
		{id: "a", depType: string(types.DepBlocks)},
	})
	expectIssueBatch(mock, []string{"x", "a"})
	expectIssue(mock, "x", "X")
	expectDependencies(mock, "x", nil)
	expectIssue(mock, "a", "A")
	expectDependencies(mock, "a", []dependencyRow{{id: "b", depType: string(types.DepBlocks)}})
	expectIssueBatchOne(mock, "b")
	expectIssue(mock, "b", "B")
	expectDependencies(mock, "b", []dependencyRow{{id: "x", depType: string(types.DepBlocks)}})
	expectIssueBatchOne(mock, "x")
	expectTreeBlocked(mock, []string{"root", "x", "a", "b"}, map[string]bool{"root": false, "x": false, "a": false, "b": false}, nil)

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
	if got := treeIDs(tree); strings.Join(got, ",") != "root,x,a,b" {
		t.Fatalf("tree = %v, want [root x a b] with no x stub", got)
	}
	for _, node := range tree {
		if node.Deduped {
			t.Fatalf("node %s is Deduped, want no stub at or beyond the cutoff", node.ID)
		}
	}
}

// expectTreeBlocked expects the walk's one post-walk read of the effective
// blocked flag for ids (its distinct ids in encounter order), every true flag
// backed by an own ready-affecting edge. Flags absent from a map return no row
// from that table.
func expectTreeBlocked(mock sqlmock.Sqlmock, ids []string, issues, wisps map[string]bool) {
	expectTreeBlockedEdges(mock, ids, treeFamily{flags: issues, edges: issues}, treeFamily{flags: wisps, edges: wisps})
}

// treeFamily is one table family's answers to the post-walk blocked read: the
// stored flags its main table returns and the ids its dependency table names
// as having an own ready-affecting edge (a true value). A non-nil flagsErr or
// edgesErr is returned by that query instead.
type treeFamily struct {
	flags, edges       map[string]bool
	flagsErr, edgesErr error
}

// expectTreeBlockedEdges expects the post-walk blocked read: per family,
// issues then wisps, the is_blocked read, then the own-edge probe for that
// family's first-seen true flags only (none when there are none), each split
// into queryBatchSize batches the way the reads run. No ids, no read. An error
// ends the expectations at the first batch: issues errors propagate, and
// wisps is the last read.
func expectTreeBlockedEdges(mock sqlmock.Sqlmock, ids []string, issues, wisps treeFamily) {
	seen := map[string]bool{}
	for _, family := range []struct {
		tables sqlbuild.FilterTables
		treeFamily
	}{{sqlbuild.IssuesFilterTables, issues}, {sqlbuild.WispsFilterTables, wisps}} {
		var flagged []string
		for _, batch := range idBatches(ids) {
			read := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, is_blocked FROM " + family.tables.Main + " WHERE id IN (" + placeholderList(len(batch)) + ")")).
				WithArgs(stringArgs(batch)...)
			if family.flagsErr != nil {
				read.WillReturnError(family.flagsErr)
				return
			}
			rows := sqlmock.NewRows([]string{"id", "is_blocked"})
			for _, id := range batch {
				b, ok := family.flags[id]
				if !ok {
					continue
				}
				rows.AddRow(id, map[bool]int{false: 0, true: 1}[b])
				if b && !seen[id] {
					flagged = append(flagged, id)
				}
				seen[id] = true
			}
			read.WillReturnRows(rows)
		}
		for _, batch := range idBatches(flagged) {
			query, _ := sqlbuild.OwnReadyAffectingEdgeIDsSQL(family.tables, batch)
			probe := mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(stringArgs(batch)...)
			if family.edgesErr != nil {
				probe.WillReturnError(family.edgesErr)
				return
			}
			edgeRows := sqlmock.NewRows([]string{"issue_id"})
			for _, id := range batch {
				if family.edges[id] {
					edgeRows.AddRow(id)
				}
			}
			probe.WillReturnRows(edgeRows)
		}
	}
}

// idBatches splits ids into the queryBatchSize batches the blocked reads use.
func idBatches(ids []string) [][]string {
	var batches [][]string
	for start := 0; start < len(ids); start += queryBatchSize {
		batches = append(batches, ids[start:min(start+queryBatchSize, len(ids))])
	}
	return batches
}

func placeholderList(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringArgs(ids []string) []driver.Value {
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

func expectIssueBatchOne(mock sqlmock.Sqlmock, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id IN (?)")).
		WithArgs(id).
		WillReturnRows(issueRows().AddRow(issueRowValues(id, id)...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (?) ORDER BY issue_id, label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
}

// TestGetDependencyTreeInTxTellsABackEdgeFromACrossEdge walks a cycle
// a → b → c → a that also has a cross edge a → c. The two revisits are
// answered differently: the back-edge into a (on the current path) adds no
// row, so the root is never a stub, while the second edge into the finished c
// is kept as exactly one Deduped stub, in either child order of a.
func TestGetDependencyTreeInTxTellsABackEdgeFromACrossEdge(t *testing.T) {
	for _, tt := range []struct {
		name     string
		rootDeps []string
		wantIDs  []string
		stub     [2]string // parent, then the edge's depth as a string
	}{
		{name: "b first", rootDeps: []string{"b", "c"}, wantIDs: []string{"a", "b", "c", "c"}, stub: [2]string{"a", "1"}},
		{name: "c first", rootDeps: []string{"c", "b"}, wantIDs: []string{"a", "c", "b", "c"}, stub: [2]string{"b", "2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			expectIssue(mock, "a", "A")
			expectDependencies(mock, "a", []dependencyRow{
				{id: tt.rootDeps[0], depType: string(types.DepBlocks)},
				{id: tt.rootDeps[1], depType: string(types.DepBlocks)},
			})
			expectIssueBatch(mock, tt.rootDeps)
			expectC := func() {
				expectIssue(mock, "c", "C")
				expectDependencies(mock, "c", []dependencyRow{{id: "a", depType: string(types.DepBlocks)}})
				// The back-edge into a reads a's row for the adjacency, then stops.
				expectIssueBatchOne(mock, "a")
			}
			if tt.rootDeps[0] == "b" {
				expectIssue(mock, "b", "B")
				expectDependencies(mock, "b", []dependencyRow{{id: "c", depType: string(types.DepBlocks)}})
				expectIssueBatchOne(mock, "c")
				expectC()
			} else {
				expectC()
				expectIssue(mock, "b", "B")
				expectDependencies(mock, "b", []dependencyRow{{id: "c", depType: string(types.DepBlocks)}})
				expectIssueBatchOne(mock, "c")
			}
			expectTreeBlocked(mock, tt.wantIDs[:3], map[string]bool{"a": true, "b": true, "c": true}, nil)

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

// TestTreeBlockedBatchInTxQualifiesStoredFlags pins the flag a dependency tree
// shows (bd-42wxtt): the stored is_blocked flag, issues first, except that a
// true flag on a row with no ready-affecting edge of its own in its family's
// dependency table is stale and reads false, as ready work reads it (GH#3268).
// Only first-seen true flags are probed, so an all-false read issues no probe:
// sqlmock refuses a query it was not told to expect.
func TestTreeBlockedBatchInTxQualifiesStoredFlags(t *testing.T) {
	probeErr := errors.New("probe failed")
	readErr := errors.New("connection lost")
	// Past the batch boundary: 250 flagged issues, so the flag reads and the
	// probes each split at queryBatchSize (200) into two batches.
	var batchIDs []string
	batchFlags := make(map[string]bool, 250)
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("flagged-%03d", i)
		batchIDs = append(batchIDs, id)
		batchFlags[id] = true
	}
	for _, tt := range []struct {
		name           string
		ids            []string
		issues, wisps  treeFamily
		want           map[string]bool
		wantErr        error
		wantErrMissing string
	}{
		{
			name:   "stale edgeless flag reads false",
			ids:    []string{"a"},
			issues: treeFamily{flags: map[string]bool{"a": true}},
			want:   map[string]bool{"a": false},
		},
		{
			name:   "flag backed by an own edge reads true",
			ids:    []string{"a", "b"},
			issues: treeFamily{flags: map[string]bool{"a": true, "b": true}, edges: map[string]bool{"b": true}},
			want:   map[string]bool{"a": false, "b": true},
		},
		{
			name:   "all false probes nothing",
			ids:    []string{"a", "b"},
			issues: treeFamily{flags: map[string]bool{"a": false, "b": false}},
			want:   map[string]bool{"a": false, "b": false},
		},
		{
			name:   "absent id reads false",
			ids:    []string{"a", "gone"},
			issues: treeFamily{flags: map[string]bool{"a": true}, edges: map[string]bool{"a": true}},
			want:   map[string]bool{"a": true, "gone": false},
		},
		{
			name:  "wisp flags are qualified by wisp_dependencies",
			ids:   []string{"w1", "w2"},
			wisps: treeFamily{flags: map[string]bool{"w1": true, "w2": true}, edges: map[string]bool{"w1": true}},
			want:  map[string]bool{"w1": true, "w2": false},
		},
		{
			name:   "issues row wins a collision, a false one included",
			ids:    []string{"x", "y"},
			issues: treeFamily{flags: map[string]bool{"x": false, "y": true}, edges: map[string]bool{"y": true}},
			wisps:  treeFamily{flags: map[string]bool{"x": true, "y": false}, edges: map[string]bool{"x": true}},
			want:   map[string]bool{"x": false, "y": true},
		},
		{
			name:   "missing wisps table is skipped",
			ids:    []string{"a"},
			issues: treeFamily{flags: map[string]bool{"a": true}, edges: map[string]bool{"a": true}},
			wisps:  treeFamily{flagsErr: tableNotFound("wisps")},
			want:   map[string]bool{"a": true},
		},
		{
			name:  "missing wisp_dependencies keeps the raw wisp flag",
			ids:   []string{"w"},
			wisps: treeFamily{flags: map[string]bool{"w": true}, edgesErr: tableNotFound("wisp_dependencies")},
			want:  map[string]bool{"w": true},
		},
		{
			name:    "issues edge probe error propagates",
			ids:     []string{"a"},
			issues:  treeFamily{flags: map[string]bool{"a": true}, edgesErr: probeErr},
			wantErr: probeErr,
		},
		{
			name: "empty request reads nothing",
			want: map[string]bool{},
		},
		{
			name:   "reads and probes batch at queryBatchSize",
			ids:    batchIDs,
			issues: treeFamily{flags: batchFlags, edges: batchFlags},
			wisps:  treeFamily{flagsErr: tableNotFound("wisps")},
			want:   batchFlags,
		},
		{
			name:    "issues read error propagates",
			ids:     []string{"a"},
			issues:  treeFamily{flagsErr: readErr},
			wantErr: readErr,
		},
		{
			name:    "wisps read error other than a missing table propagates",
			ids:     []string{"a"},
			issues:  treeFamily{flags: map[string]bool{"a": true}, edges: map[string]bool{"a": true}},
			wisps:   treeFamily{flagsErr: readErr},
			wantErr: readErr,
		},
		{
			name:    "wisp edge probe error propagates",
			ids:     []string{"w"},
			wisps:   treeFamily{flags: map[string]bool{"w": true}, edgesErr: probeErr},
			wantErr: probeErr,
		},
		{
			name:           "missing dependencies table propagates",
			ids:            []string{"a"},
			issues:         treeFamily{flags: map[string]bool{"a": true}, edgesErr: tableNotFound("dependencies")},
			wantErrMissing: "dependencies",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			mock.ExpectBegin()
			expectTreeBlockedEdges(mock, tt.ids, tt.issues, tt.wisps)
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}

			got, err := treeBlockedBatchInTx(context.Background(), tx, tt.ids)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.wantErrMissing != "":
				if name, ok := dberrors.MissingTableName(err); !ok || name != tt.wantErrMissing {
					t.Fatalf("err = %v, want missing table %s", err, tt.wantErrMissing)
				}
			default:
				if err != nil {
					t.Fatalf("treeBlockedBatchInTx: %v", err)
				}
				for _, id := range tt.ids {
					if got[id] != tt.want[id] {
						t.Errorf("%s = %v, want %v (all: %v)", id, got[id], tt.want[id], got)
					}
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

// TestStampTreeBlockedInTxEmptyWalkReadsNothing pins that an empty walk (a
// depth bound of 0, a nonexistent root) reads no flags at all: the ordered
// mock names only the transaction markers, so any read would fail it.
func TestStampTreeBlockedInTxEmptyWalkReadsNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := stampTreeBlockedInTx(context.Background(), tx, nil); err != nil {
		_ = tx.Rollback()
		t.Fatalf("stampTreeBlockedInTx on an empty walk: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
