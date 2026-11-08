//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func scopedCountsFixture(t testing.TB, count int) *sql.DB {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, dir, "counts", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(dir, "embeddeddolt"), "counts", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, family := range []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables, sqlbuild.WispsFilterTables} {
		var rows []string
		var args []any
		for i := range count {
			rows = append(rows, "(?, 'fixture', '', '', '', '', 'open', 2, 'task')")
			args = append(args, fmt.Sprintf("%s-%04d", family.Main, i))
		}
		exec("INSERT INTO "+family.Main+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES "+strings.Join(rows, ","), args...)
	}
	for _, family := range []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables, sqlbuild.WispsFilterTables} {
		for i := range count {
			id := fmt.Sprintf("%s-%04d", family.Main, i)
			exec("INSERT INTO "+family.Labels+" (issue_id, label) VALUES (?, 'z'), (?, 'a')", id, id)
			exec("INSERT INTO "+family.Comments+" (id, issue_id, author, text) VALUES (UUID(), ?, 'fixture', 'comment')", id)
			for j, column := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
				target := []string{"issues-0000", "wisps-0000", "external:fixture"}[j]
				kind := "blocks"
				if j == 2 {
					kind = "parent-child"
				}
				edgeID := fmt.Sprintf("%s-%04d-%d", family.Main, i, 2-j)
				exec("INSERT INTO "+family.Dependencies+" (id, issue_id, "+column+", type, created_by) VALUES (?, ?, ?, ?, 'fixture')", edgeID, id, target, kind)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func readScopedCounts(ctx context.Context, db *sql.DB, query string, args []any, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*types.IssueWithCounts
	for rows.Next() {
		item, err := issueops.ScanReadyWorkRowWithCounts(rows, hyd)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func TestScopedCountsMatchPredicateForm(t *testing.T) {
	db := scopedCountsFixture(t, 8)
	for _, family := range []sqlbuild.FilterTables{sqlbuild.IssuesFilterTables, sqlbuild.WispsFilterTables} {
		for mask := range 8 {
			hyd := sqlbuild.CountsHydration{SkipLabels: mask&1 != 0, SkipCounts: mask&2 != 0, Lite: mask&4 != 0}
			for _, suffixes := range [][]string{{"0000"}, {"0000", "0003", "0000", "missing"}, {"missing"}} {
				var scope []string
				for _, suffix := range suffixes {
					scope = append(scope, family.Main+"-"+suffix)
				}
				t.Run(fmt.Sprintf("%s/%d/%v", family.Main, mask, suffixes), func(t *testing.T) {
					in, args := sqlbuild.InPlaceholders(scope)
					where := "WHERE i.status = ? AND i.id IN (" + in + ")"
					whereArgs := append([]any{"open"}, args...)
					plain, _ := sqlbuild.SearchCountsSQL(family, nil, where, "ORDER BY i.id DESC", "LIMIT 2", true, hyd)
					scoped, bound := sqlbuild.SearchCountsSQLScoped(family, nil, scope, where, "ORDER BY i.id DESC", "LIMIT 2", true, hyd)
					want, err := readScopedCounts(t.Context(), db, plain, whereArgs, hyd)
					if err != nil {
						t.Fatal(err)
					}
					wantRows := 1
					if len(suffixes) > 1 {
						wantRows = 2
					} else if suffixes[0] == "missing" {
						wantRows = 0
					}
					if len(want) != wantRows {
						t.Fatalf("reference rows = %d, want %d", len(want), wantRows)
					}
					got, err := readScopedCounts(t.Context(), db, scoped, append(whereArgs, bound...), hyd)
					if err != nil {
						t.Fatal(err)
					}
					wantJSON, err := json.Marshal(want)
					if err != nil {
						t.Fatal(err)
					}
					gotJSON, err := json.Marshal(got)
					if err != nil {
						t.Fatal(err)
					}
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("scoped = %s\nunscoped = %s", gotJSON, wantJSON)
					}
				})
			}
		}
	}
	t.Run("dependency_table_without_id", func(t *testing.T) {
		_, err := db.ExecContext(t.Context(), `CREATE TABLE legacy_dependencies AS
			SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external,
			       type, created_at, created_by, metadata, thread_id FROM dependencies`)
		if err != nil {
			t.Fatal(err)
		}
		family := sqlbuild.IssuesFilterTables
		family.Dependencies = "legacy_dependencies"
		query, args := sqlbuild.SearchCountsSQLScoped(family, nil, []string{"issues-0000"}, "", "", "", true, sqlbuild.CountsHydration{})
		rows, err := readScopedCounts(t.Context(), db, query, args, sqlbuild.CountsHydration{})
		if err != nil || len(rows) != 1 || len(rows[0].Issue.Dependencies) != 3 {
			t.Fatalf("legacy dependency hydration: rows=%v err=%v", rows, err)
		}
	})
	t.Run("dependent_reads", func(t *testing.T) {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		rows, err := issueops.GetDependentsWithMetadataInTx(t.Context(), tx, "issues-0000")
		if err != nil || len(rows) != 16 {
			t.Fatalf("dependent read: rows=%d err=%v", len(rows), err)
		}
		seen := make(map[string]bool)
		for _, row := range rows {
			if seen[row.ID] || row.DependencyType != types.DepBlocks {
				t.Fatalf("unexpected dependent: %s/%s", row.ID, row.DependencyType)
			}
			seen[row.ID] = true
		}
	})
}

func BenchmarkScopedCountsHydration(b *testing.B) {
	db := scopedCountsFixture(b, 1000)
	hyd := sqlbuild.CountsHydration{}
	scope := []string{"issues-0007"}
	where := "WHERE i.id = ?"
	plain, _ := sqlbuild.SearchCountsSQL(sqlbuild.IssuesFilterTables, nil, where, "", "", true, hyd)
	scoped, args := sqlbuild.SearchCountsSQLScoped(sqlbuild.IssuesFilterTables, nil, scope, where, "", "", true, hyd)
	for _, arm := range []struct {
		name, query string
		args        []any
	}{{"unscoped", plain, []any{scope[0]}}, {"scoped", scoped, append([]any{scope[0]}, args...)}} {
		b.Run(arm.name, func(b *testing.B) {
			for b.Loop() {
				rows, err := readScopedCounts(b.Context(), db, arm.query, arm.args, hyd)
				if err != nil || len(rows) != 1 {
					b.Fatalf("rows=%d err=%v", len(rows), err)
				}
			}
		})
	}
}
