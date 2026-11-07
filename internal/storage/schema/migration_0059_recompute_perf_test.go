package schema

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestMigration0059RecomputeBoundedOnWispGraph reproduces
// gastownhall/beads#6520 (bd-f1s6sq): migration 0059's mixed issue/wisp
// recursive recompute reads the __bd_0059_recompute_wisp_deps stand-in, which
// the frozen body creates without any index, so its nested EXISTS probes and
// the recursive reachable() join scan the whole stand-in per row. A
// production-shaped clone with ~14k wisp dependencies did not finish in two
// hours; the registered execution plan (migration_body_plans.go) indexes the
// stand-in and replaces the correlated probes with the equivalent set-based
// CTEs.
//
// The dolt CLI batch path applies no prepared DML (see the 0059 header), so
// the migration's own INSERT..SELECT copies would leave the stand-ins empty
// here. The test therefore runs the body the runtime migration path executes
// — resolved through the registered plan — around a direct fill of the
// stand-ins, and asserts the recompute finishes within a bounded window on a
// modest wisp graph. Against the frozen bytes alone the recompute takes
// ~10s on this graph (over the 5s bound); the planned body is subsecond.
func TestMigration0059RecomputeBoundedOnWispGraph(t *testing.T) {
	testutil.RequireDoltCLIOnly(t)

	data, err := MigrationSQL("0059_recompute_null_gate_is_blocked.up.sql")
	if err != nil {
		t.Fatalf("MigrationSQL(0059): %v", err)
	}
	body, err := resolveMigrationBody(mainSource, migrationFile{version: 59, name: "0059_recompute_null_gate_is_blocked.up.sql"}, []byte(data))
	if err != nil {
		t.Fatalf("resolving the planned 0059 body: %v", err)
	}
	const copyStart = "SET @has_wisps = ("
	const recomputeStart = "UPDATE issues SET is_blocked = 0, updated_at = updated_at;"
	i, j := strings.Index(body, copyStart), strings.Index(body, recomputeStart)
	if i < 0 || j < i {
		t.Fatalf("0059 body no longer has the stand-in setup / recompute split this test relies on")
	}
	standIns, recompute := body[:i], body[j:]

	dir := t.TempDir()
	runDoltCommand(t, dir, "init", "--name", "test", "--email", "test@example.com")
	runDoltSQL(t, dir, AllMigrationsSQL())

	// A few open durable issues (#6520 had 285): with none, the
	// UPDATE ... WHERE id IN (reachable) never evaluates the CTE at all.
	// Plus 300 open wisps in a 10-ary parent-child tree and a blocks edge
	// on every fourth wisp: ~375 wisp dependencies, under 3% of #6520's
	// 14,146.
	runDoltSQL(t, dir, `INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status) VALUES
		('bd-1', 't', 'd', 'des', 'ac', 'n', 'open'),
		('bd-2', 't', 'd', 'des', 'ac', 'n', 'open'),
		('bd-3', 't', 'd', 'des', 'ac', 'n', 'open')`)
	const wisps = 300
	var w, d []string
	for n := 0; n < wisps; n++ {
		w = append(w, fmt.Sprintf("('w-%d','open')", n))
		if n > 0 {
			d = append(d, fmt.Sprintf("('w-%d',NULL,'w-%d','parent-child',NULL)", n, n/10))
		}
		if n%4 == 0 {
			d = append(d, fmt.Sprintf("('w-%d',NULL,'w-%d','blocks',NULL)", n, (n*7+3)%wisps))
		}
	}
	fill := "INSERT INTO __bd_0059_recompute_wisps (id, status) VALUES " + strings.Join(w, ",") + ";\n" +
		"INSERT INTO __bd_0059_recompute_wisp_deps (issue_id, depends_on_issue_id, depends_on_wisp_id, type, metadata) VALUES " +
		strings.Join(d, ",") + ";\n"

	runDoltSQL(t, dir, standIns+fill)
	start := time.Now()
	runDoltSQL(t, dir, recompute)
	elapsed := time.Since(start)
	t.Logf("0059 recompute over %d wisps / %d wisp dependencies: %s", wisps, len(d), elapsed)

	const bound = 5 * time.Second
	if elapsed > bound {
		t.Fatalf("migration 0059 recompute over %d wisps / %d wisp dependencies took %s, want <= %s: the unindexed __bd_0059_recompute_wisp_deps stand-in makes the recursive UPDATE superlinear (#6520)",
			wisps, len(d), elapsed.Round(time.Second), bound)
	}
}
