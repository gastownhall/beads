package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
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

// TestGetDependencyTreeInTxEmitsDedupedCycleStub pins the cycle shape of the
// stub (steveyegge's SHOULD-FIX on this PR: only the diamond was tested). A
// cycle a → b → c → a walked from a must terminate: a, b, c expand once and the
// back-edge re-emits a as a Deduped stub — no subtree, no recursion, no hang.
// The cycle-shape stub is what makes the cycle OBSERVABLE in the answer where
// previously the descent simply vanished; `bd dep cycles` remains where a cycle
// is the answer.
func TestGetDependencyTreeInTxEmitsDedupedCycleStub(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "a", "A")
	expectDependencies(mock, "a", []dependencyRow{
		{id: "b", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "b")
	expectIssue(mock, "b", "B")
	expectDependencies(mock, "b", []dependencyRow{
		{id: "c", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "c")
	expectIssue(mock, "c", "C")
	expectDependencies(mock, "c", []dependencyRow{
		{id: "a", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "a")
	// The edge c → a revisits the root: one fetch, one stub, no recursion.
	expectIssue(mock, "a", "A")

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

	if got := treeIDs(tree); len(tree) != 4 || got[0] != "a" || got[1] != "b" || got[2] != "c" || got[3] != "a" {
		t.Fatalf("walk = %v, want [a b c a-stub]: %v", treeIDs(tree), treeIDs(tree))
	}
	stub := tree[3]
	if !stub.Deduped || stub.Depth != 3 || stub.ParentID != "c" || stub.EdgeFromParent != types.DepBlocks {
		t.Fatalf("cycle stub = deduped %v depth %d parent %q edge %q, want true/3/c/blocks",
			stub.Deduped, stub.Depth, stub.ParentID, stub.EdgeFromParent)
	}
	for _, node := range tree[:3] {
		if node.Deduped {
			t.Errorf("node %s is Deduped, want only the closing re-visit flagged", node.ID)
		}
	}
}

// TestGetDependencyTreeInTxDepthCutoffBeatsStub pins that the depth bound wins
// over the stub: the depth cutoff is checked BEFORE the visited check, so a
// revisited node reached at or beyond max_depth is ABSENT from the answer
// rather than present as a stub — the max-depth semantics are preserved
// untouched by this PR. Shape: root expands X fully at depth 1; the longer
// path root→A→B→X reaches X at depth 3 == maxDepth, and the cutoff fires
// before any fetch could produce a stub.
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
	// X first: expanded in full at depth 1, marking it visited.
	expectIssue(mock, "x", "X")
	expectDependencies(mock, "x", nil)
	// A: expands B, whose edge back to X reaches it AT the cutoff — the
	// depth check returns nil before the visited-stub branch, so no fetch.
	expectIssue(mock, "a", "A")
	expectDependencies(mock, "a", []dependencyRow{
		{id: "b", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "b")
	expectIssue(mock, "b", "B")
	expectDependencies(mock, "b", []dependencyRow{
		{id: "x", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "x")

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
	if got := treeIDs(tree); len(got) != 4 || got[0] != "root" || got[1] != "x" || got[2] != "a" || got[3] != "b" {
		t.Fatalf("tree = %v, want [root x a b] with no x-stub", got)
	}
	for _, node := range tree {
		if node.Deduped {
			t.Fatalf("node %s is Deduped, want no stub at or beyond the cutoff", node.ID)
		}
	}
}

// TestGetDependencyTreeInTxEmitsDedupedDirectBlocker reproduces the diamond
// omission behind gastownhall/beads sys-7yjr8: root depends on mid and shared,
// mid also depends on shared. When traversal reaches shared through mid first,
// the direct root->shared blocker used to be dropped entirely, so `bd dep tree`
// and `bd dep list` disagreed on the direct-blocker set (and a bounded reader
// concluded the graph was stale). The visited node must be re-emitted as a
// Deduped stub so every direct edge is visible.
func TestGetDependencyTreeInTxEmitsDedupedDirectBlocker(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	expectIssue(mock, "root", "Root")
	expectDependencies(mock, "root", []dependencyRow{
		{id: "mid", depType: string(types.DepBlocks)},
		{id: "shared", depType: string(types.DepBlocks)},
	})
	expectIssueBatch(mock, []string{"mid", "shared"})

	// First child: mid, which expands shared beneath it.
	expectIssue(mock, "mid", "Mid")
	expectDependencies(mock, "mid", []dependencyRow{
		{id: "shared", depType: string(types.DepBlocks)},
	})
	expectIssueBatchOne(mock, "shared")
	expectIssue(mock, "shared", "Shared")
	expectDependencies(mock, "shared", nil)

	// Second direct child: shared again — already visited. The fix fetches it
	// once more and emits a Deduped stub instead of dropping the edge.
	expectIssue(mock, "shared", "Shared")

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

	if len(tree) != 4 {
		t.Fatalf("len(tree) = %d, want 4 nodes [root mid shared shared-stub]: %v", len(tree), treeIDs(tree))
	}
	stub := tree[3]
	if stub.ID != "shared" || stub.Depth != 1 || stub.ParentID != "root" {
		t.Fatalf("stub = id %q depth %d parent %q, want shared/1/root", stub.ID, stub.Depth, stub.ParentID)
	}
	if stub.EdgeFromParent != types.DepBlocks {
		t.Fatalf("stub edge = %q, want %q", stub.EdgeFromParent, types.DepBlocks)
	}
	if !stub.Deduped {
		t.Fatalf("stub.Deduped = false, want true")
	}
	full := tree[2]
	if full.ID != "shared" || full.Depth != 2 || full.ParentID != "mid" || full.Deduped {
		t.Fatalf("first occurrence = id %q depth %d parent %q deduped %v, want shared/2/mid/false", full.ID, full.Depth, full.ParentID, full.Deduped)
	}
}

func expectIssueBatchOne(mock sqlmock.Sqlmock, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)
	rows := issueRows()
	rows.AddRow(issueRowValues(id, id)...)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id IN (?)")).
		WithArgs(id).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (?) ORDER BY issue_id, label")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
}
