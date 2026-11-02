package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
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
	expectDependencies(mock, "root", []dependencyRow{
		{id: "blocker", depType: string(types.DepBlocks)},
		{id: "related", depType: string(types.DepRelatesTo)},
	})
	expectIssueBatch(mock, []string{"blocker", "related"})
	expectIssue(mock, "blocker", "Blocker")
	expectDependencies(mock, "blocker", nil)
	expectTreeBlockedFlags(mock, nil, "root", "blocker")
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
			expectTreeBlockedFlags(mock, nil, tt.wantIDs[:3]...)
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
	expectTreeBlockedFlags(mock, nil, "root", "x", "a", "b")

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

// expectTreeBlockedFlags expects the post-walk is_blocked batch read the tree
// stamps on its nodes (stampTreeBlockedInTx): the issues rows for ids in walk
// order — flags maps an id to its stored flag, and an id absent from the map
// reads 0 — then the wisps read, which this unit database answers with a
// missing table so the batch skips it. A nonzero flag makes the read
// non-final: the qualification's own-edge probe (expectOwnEdgeProbe) runs
// next for that family's flagged ids, so callers with flagged ids must add
// it.
func expectTreeBlockedFlags(mock sqlmock.Sqlmock, flags map[string]int, ids ...string) {
	rows := sqlmock.NewRows([]string{"id", "is_blocked"})
	args := make([]driver.Value, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		rows.AddRow(id, flags[id])
		args[i] = id
		placeholders[i] = "?"
	}
	in := strings.Join(placeholders, ",")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, is_blocked FROM issues WHERE id IN (" + in + ")")).
		WithArgs(args...).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, is_blocked FROM wisps WHERE id IN (" + in + ")")).
		WithArgs(args...).
		WillReturnError(missingTableError("wisps"))
}

// readyAffectingTypesSQLList is the type list the qualification probe renders
// (sqlbuild.readyAffectingTypesSQL, pinned by the sqlbuild test to be the
// shared ready-affecting list). Spelled here so the expected statement matches
// the real one exactly, and changes to the list fail this suite until the
// expectations are updated.
const readyAffectingTypesSQLList = "'blocks', 'parent-child', 'conditional-blocks', 'waits-for'"

// expectOwnEdgeProbe expects one batch of the qualification probe
// (sqlbuild.OwnReadyAffectingEdgeIDBatchSQL) against depTable: it answers with
// the ids withEdge (their own ready-affecting edge), and every other id of the
// batch is stale.
func expectOwnEdgeProbe(mock sqlmock.Sqlmock, depTable string, withEdge, ids []string) {
	placeholders := make([]string, len(ids))
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows := sqlmock.NewRows([]string{"issue_id"})
	for _, id := range withEdge {
		rows.AddRow(id)
	}
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(
		"SELECT DISTINCT issue_id FROM %s WHERE issue_id IN (%s) AND type IN (%s)",
		depTable, strings.Join(placeholders, ","), readyAffectingTypesSQLList))).
		WithArgs(args...).
		WillReturnRows(rows)
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
			expectTreeBlockedFlags(mock, nil, tt.wantIDs[:3]...)

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

// TestGetDependencyTreeInTxStaleFlagReadsReady is the AC-3 walk: an open root
// whose stored is_blocked flag is 1 with no own ready-affecting edge carries
// a stale flag (GH#3268) that ready work reads around, so the tree's
// effective flag is false, the JSON omits the member, and — because the
// ordered mock only names reads — any attempt to rewrite the stored flag
// would fail the test. Without the qualification the raw flag marks the root
// [BLOCKED] while `bd ready` still lists it.
func TestGetDependencyTreeInTxStaleFlagReadsReady(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "stale", "Stale")
	expectDependencies(mock, "stale", nil)
	expectTreeBlockedFlags(mock, map[string]int{"stale": 1}, "stale")
	// The probe answers with no id: the stale flag owns no edge.
	expectOwnEdgeProbe(mock, "dependencies", nil, []string{"stale"})

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	tree, err := GetDependencyTreeInTx(context.Background(), tx, "stale", 3, false, false)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("GetDependencyTreeInTx: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(tree) != 1 {
		t.Fatalf("len(tree) = %d, want only the root: %+v", len(tree), tree)
	}
	if tree[0].Issue.IsBlocked {
		t.Fatalf("root %s stamped IsBlocked from the raw flag, want the stale flag read around (bd ready lists it)", tree[0].ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
	wire, err := json.Marshal(tree[0])
	if err != nil {
		t.Fatalf("marshal tree node: %v", err)
	}
	if strings.Contains(string(wire), "is_blocked") {
		t.Fatalf("json node = %s, want is_blocked omitted", wire)
	}
}
