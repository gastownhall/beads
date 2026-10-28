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
