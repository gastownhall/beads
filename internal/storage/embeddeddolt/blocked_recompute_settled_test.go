//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestCloseSendsClosedAndPinnedRowsToThePlainUnmark closes the blocker of a
// wide parent through the real close body and records its statements. The
// recompute covers 39 ids, past the size from which a chunk is split, so the
// closed and pinned rows in it must reach only the plain unmark, never a union
// statement, while the open rows still ride the union and settle:
//
//	su-x  <--blocks--  su-p
//	su-p
//	├── su-pad-0..31   closed
//	├── su-stale       closed, a stale is_blocked = 1 planted
//	├── su-pin         pinned
//	├── su-open        open, inherits
//	└── su-mid ── su-leaf   su-mid closed the way a merge closes a row, so
//	                        it and the open su-leaf keep is_blocked = 1
func TestCloseSendsClosedAndPinnedRowsToThePlainUnmark(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, wisp := range []bool{false, true} {
		table := map[bool]string{false: "issues", true: "wisps"}[wisp]
		t.Run(table, func(t *testing.T) {
			te := newTestEnv(t, "su")
			ctx := t.Context()
			now := time.Now()
			var issues []*types.Issue
			mk := func(id, parent string, status types.Status) {
				iss := &types.Issue{ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
				if status == types.StatusClosed {
					iss.ClosedAt = &now
				}
				if parent != "" {
					iss.Dependencies = []*types.Dependency{{IssueID: id, DependsOnID: parent, Type: types.DepParentChild}}
				}
				issues = append(issues, iss)
			}
			mk("su-x", "", types.StatusOpen)
			mk("su-p", "", types.StatusOpen)
			settled := map[string]bool{"su-x": true, "su-stale": true, "su-pin": true, "su-mid": true}
			for i := 0; i < 32; i++ {
				id := fmt.Sprintf("su-pad-%d", i)
				mk(id, "su-p", types.StatusClosed)
				settled[id] = true
			}
			mk("su-stale", "su-p", types.StatusClosed)
			mk("su-pin", "su-p", types.StatusPinned)
			mk("su-open", "su-p", types.StatusOpen)
			mk("su-mid", "su-p", types.StatusOpen)
			mk("su-leaf", "su-mid", types.StatusOpen)
			if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "su-p", DependsOnID: "su-x", Type: types.DepBlocks}, "tester"); err != nil {
				t.Fatalf("block su-p: %v", err)
			}
			te.exec(t, ctx, "UPDATE "+table+" SET is_blocked = 1 WHERE id = 'su-stale'")
			te.exec(t, ctx, "UPDATE "+table+" SET status = 'closed', closed_at = NOW() WHERE id = 'su-mid'")
			for _, id := range []string{"su-p", "su-open", "su-mid", "su-leaf", "su-stale"} {
				var v int
				te.queryScalar(t, ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", []any{id}, &v)
				if v != 1 {
					t.Fatalf("precondition: %s is_blocked = %d, want 1", id, v)
				}
			}

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			rec := &statementRecorder{DBTX: tx}
			if _, err := issueops.CloseIssueInTx(ctx, rec, "su-x", "done", "tester", ""); err != nil {
				t.Fatalf("close su-x: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}

			inUnion, inPlain := map[string]bool{}, map[string]bool{}
			for _, st := range rec.stmts {
				if !strings.Contains(st.query, "UPDATE") || !strings.Contains(st.query, "is_blocked = 0") {
					continue
				}
				for _, arg := range st.args {
					if id, ok := arg.(string); ok && strings.Contains(st.query, "UNION") {
						inUnion[id] = true
					} else if ok {
						inPlain[id] = true
					}
				}
			}
			for id := range settled {
				if inUnion[id] || !inPlain[id] {
					t.Errorf("%s: in a union statement = %v, in the plain unmark = %v; want only the plain unmark", id, inUnion[id], inPlain[id])
				}
			}
			for _, id := range []string{"su-p", "su-open", "su-leaf"} {
				if !inUnion[id] {
					t.Errorf("open %s did not reach the union statements", id)
				}
			}
			// The embedded engine is single-writer: read back on the same connection.
			for _, id := range []string{"su-p", "su-open", "su-mid", "su-leaf", "su-stale", "su-pin"} {
				var v int
				if err := db.QueryRowContext(ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&v); err != nil || v != 0 {
					t.Errorf("%s is_blocked = %d (err %v) after the close, want 0", id, v, err)
				}
			}
			n, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, db)
			if err != nil || n != 0 {
				t.Errorf("stale count = %d, err = %v, want 0", n, err)
			}
		})
	}
}

// BenchmarkCloseAffectedSetAndRecompute measures the derived-state half of
// closing a blocked parent on the embedded engine: the affected set, the status
// move and the recompute, rolled back per iteration over committed data. It
// uses only API older than the recompute's closed-row shortcut, so the file
// measures either side of it. closed_children is the finished-epic close of
// #5427 and #5939; mostly_open closes every tenth child.
func BenchmarkCloseAffectedSetAndRecompute(b *testing.B) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		b.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt benchmarks")
	}
	for _, shape := range []struct {
		name        string
		closedEvery int
	}{{"closed_children/500", 1}, {"mostly_open/500", 10}} {
		b.Run(shape.name, func(b *testing.B) {
			ctx := b.Context()
			beadsDir := filepath.Join(b.TempDir(), ".beads")
			store, err := embeddeddolt.Open(ctx, beadsDir, "bench", "main")
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			defer func() { _ = store.Close() }()
			if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
				b.Fatalf("set issue_prefix: %v", err)
			}
			mk := func(id string) *types.Issue {
				return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			}
			issues := []*types.Issue{mk("bench-blocker"), mk("bench-parent")}
			for i := 0; i < 500; i++ {
				kid := mk(fmt.Sprintf("bench-kid-%d", i))
				kid.Dependencies = []*types.Dependency{{IssueID: kid.ID, DependsOnID: "bench-parent", Type: types.DepParentChild}}
				if i%shape.closedEvery == 0 {
					closedAt := time.Now()
					kid.Status, kid.ClosedAt = types.StatusClosed, &closedAt
				}
				issues = append(issues, kid)
			}
			if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
				b.Fatalf("create: %v", err)
			}
			if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bench-parent", DependsOnID: "bench-blocker", Type: types.DepBlocks}, "bench"); err != nil {
				b.Fatalf("block parent: %v", err)
			}
			if err := store.Commit(ctx, "bench fixture"); err != nil {
				b.Fatalf("commit: %v", err)
			}
			// The embedded engine is single-writer: the store stays idle from here.
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "bench", "main")
			if err != nil {
				b.Fatalf("OpenSQL: %v", err)
			}
			defer func() { _ = cleanup() }()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatalf("begin: %v", err)
				}
				ids, wisps, err := issueops.AffectedByStatusChangeInTx(ctx, tx, "bench-parent")
				if err != nil {
					b.Fatalf("affected set: %v", err)
				}
				if _, err := tx.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = 'bench-parent'"); err != nil {
					b.Fatalf("close parent: %v", err)
				}
				if err := issueops.RecomputeIsBlockedInTx(ctx, tx, ids, wisps); err != nil {
					b.Fatalf("recompute: %v", err)
				}
				_ = tx.Rollback()
			}
		})
	}
}
