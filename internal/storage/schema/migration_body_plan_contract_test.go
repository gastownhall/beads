package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestMigrationBodyPlanContract is the execution-plan half of bd-f1s6sq's
// AC-5 (gastownhall/beads#6520): every registered body plan is pinned to the
// exact frozen file the freeze test pins, substitutes only the middle
// segment (index DDL plus the replacement CTE list) between the preserved
// frozen prefix and suffix, leaves AllMigrationsSQL and the CLI bundle
// plan-free, writes nothing persistent on real tables, and carries no
// nondeterministic constructs. The differential, scaled-graph, replay and
// full-chain tests in internal/storage/dolt prove the same properties
// behaviorally; this contract pins them structurally so a future registry
// edit cannot regress them quietly.
func TestMigrationBodyPlanContract(t *testing.T) {
	if len(migrationBodyPlans) == 0 {
		t.Fatalf("no body plans registered; the 0059/ignored-15 pair (#6520) must be")
	}
	for key, plan := range migrationBodyPlans {
		t.Run(fmt.Sprintf("%s/%d", key.cursorTable, key.version), func(t *testing.T) {
			src, ok := map[string]migrationSource{
				mainSource.cursorTable:    mainSource,
				ignoredSource.cursorTable: ignoredSource,
			}[key.cursorTable]
			if !ok {
				t.Fatalf("plan registered for unknown cursor table %q", key.cursorTable)
			}
			if src.dir != plan.dir {
				t.Errorf("plan dir %q is not its source's %q", plan.dir, src.dir)
			}
			data, err := src.files.ReadFile(src.dir + "/" + plan.name)
			if err != nil {
				t.Fatalf("read the pinned file: %v", err)
			}
			sum := sha256.Sum256(data)
			if got := hex.EncodeToString(sum[:]); got != plan.expectedSHA256 {
				t.Errorf("pinned hash %s does not match the embedded file's %s", plan.expectedSHA256, got)
			}
			// The freeze test's own pin: a plan may not pin a file the
			// freeze registry does not also pin (or the two could diverge).
			frozen := frozenMigrationHashes
			if key.cursorTable == ignoredSource.cursorTable {
				frozen = frozenIgnoredMigrationHashes
			}
			if freeze, ok := frozen[plan.name]; !ok {
				t.Errorf("%s has no freeze-test pin; a plan may only register released, frozen files", plan.name)
			} else if freeze != plan.expectedSHA256 {
				t.Errorf("plan pin %s and freeze pin %s disagree on %s", plan.expectedSHA256, freeze, plan.name)
			}

			mf := migrationFile{version: key.version, name: plan.name}
			planned, err := resolveMigrationBody(src, mf, data)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			exported, err := plannedBodySQL(src, key.version)
			if err != nil {
				t.Fatalf("plannedBodySQL: %v", err)
			}
			if exported != planned {
				t.Errorf("plannedBodySQL output differs from resolveMigrationBody's (%d vs %d bytes)", len(exported), len(planned))
			}

			frozenBody := string(data)
			const head = "\nWITH RECURSIVE\n"
			i := strings.Index(frozenBody, head)
			j := strings.Index(frozenBody, plan.finalUpdate)
			if i < 0 || j <= i {
				t.Fatalf("anchors not found (head %d, final %d)", i, j)
			}
			// Prefix and suffix preserved byte-for-byte; the middle is
			// exactly the plan's DDL and CTE list, nothing else.
			var middle strings.Builder
			for _, ddl := range plan.indexDDL {
				middle.WriteString("\n")
				middle.WriteString(ddl)
			}
			middle.WriteString("\n")
			middle.WriteString(plan.replacementCTEs)
			if want := frozenBody[:i] + middle.String() + frozenBody[j:]; planned != want {
				t.Errorf("planned body is not frozen-prefix + DDL + CTEs + frozen-suffix byte-for-byte")
			}
			if !strings.HasPrefix(planned, frozenBody[:i]) {
				t.Errorf("frozen prefix not preserved at the head of the planned body")
			}
			if !strings.HasSuffix(planned, frozenBody[j:]) {
				t.Errorf("frozen suffix not preserved at the tail of the planned body")
			}

			// Nothing persistent: on the real tables the plan may not carry
			// any DDL at all; whatever DDL it does carry may only touch the
			// step's own ephemeral stand-ins, and the frozen suffix must
			// drop those stand-ins (their indexes die with them).
			for _, fragment := range planDDLFragments(plan) {
				upper := strings.ToUpper(fragment)
				standIn := strings.Contains(fragment, "__bd_")
				switch {
				case strings.Contains(upper, "CREATE INDEX"), strings.Contains(upper, "CREATE TABLE"), strings.Contains(upper, "DROP INDEX"), strings.Contains(upper, "DROP TABLE"), strings.Contains(upper, "ALTER TABLE"):
					if !standIn {
						t.Errorf("plan DDL touches a persistent object: %.60q", fragment)
					}
				}
			}
			if key.cursorTable == ignoredSource.cursorTable && len(plan.indexDDL) != 0 {
				t.Errorf("ignored-lane plan carries index DDL on the real wisp tables: %q", plan.indexDDL)
			}
			if upper := strings.ToUpper(plan.replacementCTEs); strings.Contains(upper, "CREATE ") || strings.Contains(upper, "DROP ") || strings.Contains(upper, "ALTER ") {
				t.Errorf("replacement CTE list carries DDL; it must be a pure WITH clause")
			}
			suffix := frozenBody[j:]
			for _, standIn := range planStandIns(plan) {
				dropped := false
				for _, line := range strings.Split(suffix, "\n") {
					if strings.Contains(strings.ToUpper(line), "DROP TABLE") && strings.Contains(line, standIn) {
						dropped = true
					}
				}
				if !dropped {
					t.Errorf("frozen suffix does not DROP the %s stand-in; its plan indexes would persist", standIn)
				}
			}

			// No nondeterministic constructs in what the plan adds.
			added := middle.String()
			for _, marker := range []string{"NOW(", "CURRENT_TIMESTAMP", "RAND(", "UUID(", "RANDOM"} {
				if strings.Contains(strings.ToUpper(added), marker) {
					t.Errorf("plan-added SQL contains nondeterministic marker %q", marker)
				}
			}
		})
	}

	// AllMigrationsSQL (the CLI bundle) stays plan-free: no plan-added DDL or
	// replacement CTE appears in it, and the main cursor line it records for
	// a planned version is the frozen file's hash, never a planned body's.
	all := AllMigrationsSQL()
	for _, plan := range migrationBodyPlans {
		for _, ddl := range plan.indexDDL {
			if strings.Contains(all, ddl) {
				t.Errorf("AllMigrationsSQL contains plan index DDL %.60q; the CLI bundle must carry only frozen bytes", ddl)
			}
		}
		for _, marker := range []string{"open_child_issue(", "open_child_wisp(", "closed_child_issue(", "closed_child_wisp("} {
			if strings.Contains(all, marker) {
				t.Errorf("AllMigrationsSQL contains replacement-CTE marker %q; the CLI bundle must carry only frozen bytes", marker)
			}
		}
	}
	if plan, ok := migrationBodyPlans[repairKey{mainSource.cursorTable, 59}]; ok {
		want := fmt.Sprintf("INSERT IGNORE INTO %s (version, content_hash) VALUES (59, '%s')", mainSource.cursorTable, plan.expectedSHA256)
		if !strings.Contains(all, want) {
			t.Errorf("AllMigrationsSQL does not record the frozen hash for main 59")
		}
	}
}

// planDDLFragments returns every plan-added DDL statement plus the
// replacement CTE list, for the persistence and determinism sweeps.
func planDDLFragments(plan migrationBodyPlan) []string {
	return append(slices.Clone(plan.indexDDL), plan.replacementCTEs)
}

// planStandIns extracts the table names a plan's index DDL targets: the
// frozen suffix must DROP each of them, or the plan's indexes would persist
// past the step. A plan with no DDL (the ignored twin, whose CTEs run
// against the real wisp tables) has none.
func planStandIns(plan migrationBodyPlan) []string {
	var out []string
	for _, ddl := range plan.indexDDL {
		if _, after, ok := strings.Cut(ddl, " ON "); ok {
			if table, _, _ := strings.Cut(after, " "); table != "" {
				out = append(out, table)
			}
		}
	}
	return out
}
