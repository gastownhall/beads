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

// TestGetDependencyTreeInTxKeepsDirectEdgeToRevisitedNode reproduces GH#5282:
// on a diamond (D→E, D→F, E→F) the direct D→F edge must stay in the tree even
// though F was already reached through E. The answer has exactly four rows —
// D, E and F expanded once, plus one leaf Deduped stub of F carrying the other
// path's ParentID, Depth and EdgeFromParent — in either child order, and
// producing the stub must read no issue and no dependency rows (the sqlmock
// expectations cover exactly one read of each).
func TestGetDependencyTreeInTxKeepsDirectEdgeToRevisitedNode(t *testing.T) {
	for _, order := range [][]dependencyRow{
		{{id: "E", depType: string(types.DepBlocks)}, {id: "F", depType: string(types.DepBlocks)}},
		{{id: "F", depType: string(types.DepBlocks)}, {id: "E", depType: string(types.DepBlocks)}},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}

		mock.ExpectBegin()
		expectIssue(mock, "D", "D")
		expectDependencies(mock, "D", order)
		expectIssueBatchOf(mock, order[0].id, order[1].id)
		expectIssue(mock, "E", "E")
		expectDependencies(mock, "E", []dependencyRow{
			{id: "F", depType: string(types.DepBlocks)},
		})
		expectIssueBatchOf(mock, "F")
		expectIssue(mock, "F", "F")
		expectDependencies(mock, "F", nil)
		mock.MatchExpectationsInOrder(false)

		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		tree, err := GetDependencyTreeInTx(context.Background(), tx, "D", 5, false, false)
		_ = tx.Rollback()
		db.Close()
		if err != nil {
			t.Fatalf("order %v: GetDependencyTreeInTx: %v", order, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("order %v: the stub must read nothing: %v", order, err)
		}

		if len(tree) != 4 {
			t.Fatalf("order %v: len(tree) = %d, want 4 rows (D, E, F and one stub of F): %v", order, len(tree), treeIDs(tree))
		}

		edges := make([]string, 0, len(tree))
		wantStubParent, wantStubDepth := "D", 1
		if order[0].id == "F" {
			// F is expanded directly under D first, so the stub records the
			// E→F edge.
			wantStubParent, wantStubDepth = "E", 2
		}
		directF := false
		deduped := 0
		for _, node := range tree {
			edges = append(edges, node.ParentID+"->"+node.ID)
			if node.ID == "F" && node.ParentID == "D" && node.Depth == 1 {
				directF = true
			}
			if node.Deduped {
				deduped++
				if node.ID != "F" || node.EdgeFromParent != types.DepBlocks ||
					node.ParentID != wantStubParent || node.Depth != wantStubDepth {
					t.Fatalf("order %v: the stub must be F under %s at depth %d via blocks, got %+v", order, wantStubParent, wantStubDepth, node)
				}
			}
		}
		if !directF {
			t.Fatalf("order %v: direct edge D->F missing from dependency tree; got parent->child edges %v (want D->E, E->F and D->F)", order, edges)
		}
		if deduped != 1 {
			t.Fatalf("order %v: %d Deduped rows, want exactly one: %v", order, deduped, treeIDs(tree))
		}
	}
}

// expectIssueBatchOf is expectIssueBatch for any arity: the IN placeholder
// list is built from the ids the batch actually loads.
func expectIssueBatchOf(mock sqlmock.Sqlmock, ids ...string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)

	placeholders := "?" + strings.Repeat(",?", len(ids)-1)
	args := make([]driver.Value, 0, len(ids))
	rows := issueRows()
	for _, id := range ids {
		args = append(args, id)
		rows.AddRow(issueRowValues(id, id)...)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT "+IssueSelectColumns+" FROM issues "+sqlbuild.LeaseJoin("issues")+" WHERE id IN ("+placeholders+")")).
		WithArgs(args...).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN ("+placeholders+") ORDER BY issue_id, label")).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
}
// TestGetDependencyTreeInTxStubsACrossEdgeButNeverABackEdge walks a→b→c→a
// plus the cross edge a→c from the root a: the back-edge into a must add no
// row, while the cross edge into the already-finished c must yield exactly one
// extra c row, so the answer is a, b, c and one revisit of c — in either child
// order.
func TestGetDependencyTreeInTxStubsACrossEdgeButNeverABackEdge(t *testing.T) {
	for _, order := range [][]dependencyRow{
		{{id: "b", depType: string(types.DepBlocks)}, {id: "c", depType: string(types.DepBlocks)}},
		{{id: "c", depType: string(types.DepBlocks)}, {id: "b", depType: string(types.DepBlocks)}},
	} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}

		mock.ExpectBegin()
		expectIssue(mock, "a", "a")
		expectDependencies(mock, "a", order)
		expectIssueBatchOf(mock, order[0].id, order[1].id)
		expectIssue(mock, "b", "b")
		expectDependencies(mock, "b", []dependencyRow{{id: "c", depType: string(types.DepBlocks)}})
		expectIssueBatchOf(mock, "c")
		expectIssue(mock, "c", "c")
		expectDependencies(mock, "c", []dependencyRow{{id: "a", depType: string(types.DepBlocks)}})
		expectIssueBatchOf(mock, "a")
		mock.MatchExpectationsInOrder(false)

		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		tree, err := GetDependencyTreeInTx(context.Background(), tx, "a", 5, false, false)
		_ = tx.Rollback()
		db.Close()
		if err != nil {
			t.Fatalf("GetDependencyTreeInTx: %v", err)
		}

		if len(tree) != 4 {
			t.Fatalf("order %v: len(tree) = %d, want 4 rows (a, b, c and one revisit of c): %v", order, len(tree), treeIDs(tree))
		}
		counts := map[string]int{}
		deduped := 0
		for _, node := range tree {
			counts[node.ID]++
			if node.Deduped {
				deduped++
				if node.ID != "c" || node.EdgeFromParent != types.DepBlocks {
					t.Fatalf("order %v: the stub must be c via blocks, got %+v", order, node)
				}
			}
		}
		if counts["a"] != 1 || counts["b"] != 1 || counts["c"] != 2 {
			t.Fatalf("order %v: node counts = %v, want a once, b once, c twice: %v", order, counts, treeIDs(tree))
		}
		if deduped != 1 {
			t.Fatalf("order %v: %d Deduped rows, want exactly one (of c): %v", order, deduped, treeIDs(tree))
		}
	}
}
