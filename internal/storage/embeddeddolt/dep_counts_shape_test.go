//go:build cgo

package embeddeddolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// TestInboundEdgeCountSQLUsesGuardedTargetUnion pins the SHAPE of the inbound
// dependency-edge count SQL (gascity#7666): one UNION ALL branch per target
// column, each matching a bare indexed column and guarding every earlier
// column IS NULL, with no COALESCE around the target key. The old shape
// filtered every inbound count through COALESCE(depends_on_issue_id,
// depends_on_wisp_id, depends_on_external) = ?, which no index can match, so
// each count scanned the whole table (measured 80ms vs 3ms by column on a
// 40,804-row dependencies). Parity with the old arithmetic is pinned by
// TestDependentCountsMatchCoalesceOracle; this test pins that the statements
// actually sent are the guarded per-column unions, and that their bind order
// is branch order (branch target keys first, then the branch's type filter,
// for each of the three branches in turn).
//
// The guards are load-bearing even though ck_dep_one_target already holds
// exactly one non-NULL target per row: without them a branch-2 match on
// depends_on_wisp_id would also count rows whose depends_on_issue_id is set,
// double-counting on any fixture or engine that does not enforce the CHECK.
func TestInboundEdgeCountSQLUsesGuardedTargetUnion(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	fixture := newPristineEmbeddedDoltFixture(t, "dcshape")
	t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
	db, cleanup, err := openCountedConn(t.Context(), fixture.dataDir, fixture.database, &sqlcount.Counts{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	const target = "dcshape-tgt"
	for _, tc := range []struct {
		name   string
		filter []types.DependencyType
	}{
		{"unfiltered", nil},
		{"type-filtered", []types.DependencyType{types.DepBlocks, types.DepRelated}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &statementRecorder{DBTX: tx}
			if _, err := issueops.CountDependencyEdgesInTx(ctx, rec, target, domain.DepDirectionIn, tc.filter); err != nil {
				t.Fatal(err)
			}
			if len(rec.stmts) != 2 {
				t.Fatalf("recorded %d statements, want 2 (durable count, then wisp count): %+v", len(rec.stmts), rec.stmts)
			}

			durable, wisp := rec.stmts[0], rec.stmts[1]
			if !strings.Contains(durable.query, "FROM dependencies") {
				t.Fatalf("first statement is not the durable count:\n%s", durable.query)
			}
			if !strings.Contains(wisp.query, "FROM wisp_dependencies") {
				t.Fatalf("second statement is not the wisp count:\n%s", wisp.query)
			}

			// The wisp statement embeds the unfiltered durable union as its
			// NOT IN exclusion once per branch: the outer union's 2
			// separators plus three copies of the subquery's own 2. A durable
			// branch binds its target key then the type filter
			// (1 + len(filter) placeholders); a wisp branch binds its target
			// key, three exclusion-subquery target keys, then the type filter
			// (4 + len(filter)).
			for _, st := range []struct {
				name   string
				stmt   recordedStatement
				unions int
				perBr  int
				// keysPerBranch is how many leading placeholders per branch
				// bind the target key.
				keysPerBranch int
			}{
				{"durable", durable, 2, 1 + len(tc.filter), 1},
				{"wisp", wisp, 8, 4 + len(tc.filter), 4},
			} {
				if strings.Contains(st.stmt.query, "COALESCE(") {
					t.Errorf("%s inbound count still filters through a COALESCE expression no index can match:\n%s", st.name, st.stmt.query)
				}
				if got := strings.Count(st.stmt.query, "UNION ALL"); got != st.unions {
					t.Errorf("%s inbound count has %d UNION ALL separators, want %d (guarded per-column branches):\n%s", st.name, got, st.unions, st.stmt.query)
				}
				for _, want := range []string{
					"depends_on_issue_id = ?",
					"depends_on_wisp_id = ? AND depends_on_issue_id IS NULL",
					"depends_on_external = ? AND depends_on_issue_id IS NULL AND depends_on_wisp_id IS NULL",
				} {
					if !strings.Contains(st.stmt.query, want) {
						t.Errorf("%s inbound count is missing the guarded branch %q:\n%s", st.name, want, st.stmt.query)
					}
				}
				if got := len(st.stmt.args); got != 3*st.perBr {
					t.Errorf("%s inbound count binds %d args, want %d (3 branches × %d each): %v", st.name, got, 3*st.perBr, st.perBr, st.stmt.args)
					continue
				}
				for i, arg := range st.stmt.args {
					if i%st.perBr < st.keysPerBranch && arg != target {
						t.Errorf("%s inbound count arg %d = %v, want target %q (branch key / exclusion subquery position):\n%v", st.name, i, arg, target, st.stmt.args)
					}
					if i%st.perBr >= st.keysPerBranch && arg == target {
						t.Errorf("%s inbound count arg %d binds the target where a type filter belongs:\n%v", st.name, i, st.stmt.args)
					}
				}
			}

			// The exclusion subquery inside the wisp statement must be
			// UNFILTERED by type: durable ownership suppresses the wisp copy
			// of an edge whatever type the durable row carries, so a type
			// filter applied inside the subquery would resurrect wisp rows
			// whose durable twin holds a different type.
			excl := "NOT IN ("
			at := strings.Index(wisp.query, excl)
			if at < 0 {
				t.Fatalf("wisp count has no NOT IN durable-exclusion subquery:\n%s", wisp.query)
			}
			depth, end := 1, -1
			for i := at + len(excl); i < len(wisp.query); i++ {
				switch wisp.query[i] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						end = i
					}
				}
				if end >= 0 {
					break
				}
			}
			if end < 0 {
				t.Fatalf("wisp count's NOT IN subquery is unbalanced:\n%s", wisp.query)
			}
			sub := wisp.query[at+len(excl) : end]
			if strings.Contains(sub, "type IN") {
				t.Errorf("wisp count's durable-exclusion subquery is type-filtered; durable ownership must suppress regardless of type:\n%s", sub)
			}
			if strings.Count(sub, "UNION ALL") != 2 || strings.Contains(sub, "COALESCE(") {
				t.Errorf("wisp count's durable-exclusion subquery is not the guarded per-column durable union:\n%s", sub)
			}
		})
	}
}
