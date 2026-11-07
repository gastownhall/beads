package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Execution plans for frozen migration bodies (gastownhall/beads#6520).
//
// Migration 0059 (and its ignored/0015 twin) ships a mixed issue/wisp
// is_blocked recompute whose frozen SQL probes an unindexed stand-in copy of
// wisp_dependencies with correlated EXISTS predicates and OR-joined recursive
// members. On a restored production-shaped graph (14k wisp dependencies) the
// recursive UPDATE ran for over two hours without completing; the set-based
// rewrite of the same fixed point runs in seconds, but the frozen file
// cannot be edited: its bytes are the migration's identity, pinned by
// migration_freeze_test.go and recorded in the cursor table.
//
// A body execution plan is the escape hatch: a narrowly registered,
// hash-gated replacement body with the same semantics. The registry below is
// keyed like preMigrationRepairs (repairKey: source cursor table + pending
// version) and additionally pins the expected source directory and the exact
// migration filename, so a same-numbered migration from a different source
// can never select a plan. resolveMigrationBody validates the frozen bytes'
// SHA-256 and the anchor layout before substituting anything, and fails
// closed: drift between a plan and the shipped file is an error, never a
// silent fallback to the slow body and never a partial substitution.
//
// What a plan may replace: only the segment between the frozen prefix (up to
// the body's single "\nWITH RECURSIVE\n") and the final UPDATE anchor. The
// prefix keeps stand-in creation, guarded copies and the zeroing UPDATE
// byte-for-byte; the suffix keeps the final assignments and the stand-in
// DROPs byte-for-byte. A plan may additionally insert deterministic index
// DDL (0059's stand-in indexes) ahead of the replacement CTEs; everything it
// creates must be dropped by the frozen suffix, so a plan leaves no
// persistent schema behind. AllMigrationsSQL and the CLI bundle
// (cliCompatibleMigrationSQL) read the frozen bytes directly and are
// unaffected: plans apply only to the runtime designated-migrator path, and
// migration identity remains the frozen hash — runMigrations still records
// the SHA-256 of the original file bytes in the cursor table.

// migrationBodyPlan describes one hash-gated execution plan: how to rebuild
// the body a registered migration executes without touching its frozen file.
type migrationBodyPlan struct {
	// dir and name pin the plan to one exact migration file, on top of the
	// repairKey's source and version: the key alone would let a same-numbered
	// migration in another source select a plan written for different SQL.
	dir  string
	name string
	// expectedSHA256 is the raw SHA-256 hex of the frozen file bytes — the
	// same digest migration_freeze_test.go pins and runMigrations records in
	// the cursor table.
	expectedSHA256 string
	// finalUpdate anchors the preserved suffix: the body's single occurrence
	// of "\nUPDATE issues\n" or "\nUPDATE wisps\n". The zeroing UPDATEs
	// write "UPDATE issues SET" (a space follows the table name), so the
	// anchor matches only the final assignment statement.
	finalUpdate string
	// indexDDL is optional deterministic DDL inserted between the frozen
	// prefix and the replacement CTEs — after the tables it indexes exist
	// and are filled (0059's stand-in copies). It must not outlive the body.
	indexDDL []string
	// replacementCTEs replaces the frozen segment from "\nWITH RECURSIVE\n"
	// to the final UPDATE anchor: an equivalent WITH RECURSIVE CTE list that
	// begins with "WITH RECURSIVE" and ends at the close of its last CTE.
	replacementCTEs string
}

// migrationBodyPlans is the registry of execution plans, keyed like
// preMigrationRepairs. Every migration without an entry here executes its
// frozen bytes verbatim. Entries are data (not a switch) for the same reason
// preMigrationRepairs are: tests iterate and mutate the registry to prove the
// fail-closed paths (see migration_body_plans_test.go).
var migrationBodyPlans = map[repairKey]migrationBodyPlan{}

// resolveMigrationBody returns the SQL body runMigrations executes for one
// pending migration step: the frozen bytes verbatim for every unregistered
// migration, or the planned replacement body for a registered one. It is
// pure — no database calls — so runMigrations resolves the body ahead of
// every step mutation and fails closed on drift.
func resolveMigrationBody(src migrationSource, mf migrationFile, data []byte) (string, error) {
	plan, ok := migrationBodyPlans[repairKey{src.cursorTable, mf.version}]
	if !ok {
		return string(data), nil
	}
	what := fmt.Sprintf("migration body plan %s/%s", src.cursorTable, mf.name)
	if src.dir != plan.dir || mf.name != plan.name {
		return "", fmt.Errorf("%s: identity mismatch (source dir %q, file %q; the plan is registered for dir %q, file %q)",
			what, src.dir, mf.name, plan.dir, plan.name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != plan.expectedSHA256 {
		return "", fmt.Errorf("%s: frozen content hash %s does not match the plan's pinned hash %s; refusing to execute either body",
			what, got, plan.expectedSHA256)
	}
	const head = "\nWITH RECURSIVE\n"
	body := string(data)
	if n := strings.Count(body, head); n != 1 {
		return "", fmt.Errorf("%s: frozen body has %d occurrences of the head anchor %q, want exactly 1", what, n, head)
	}
	if n := strings.Count(body, plan.finalUpdate); n != 1 {
		return "", fmt.Errorf("%s: frozen body has %d occurrences of the final UPDATE anchor %q, want exactly 1", what, n, plan.finalUpdate)
	}
	i := strings.Index(body, head)
	j := strings.Index(body, plan.finalUpdate)
	if j <= i {
		return "", fmt.Errorf("%s: final UPDATE anchor at %d does not follow the head anchor at %d", what, j, i)
	}
	var b strings.Builder
	b.WriteString(body[:i]) // frozen prefix: stand-in creation, guarded copies, zeroing UPDATE
	for _, ddl := range plan.indexDDL {
		b.WriteString("\n")
		b.WriteString(ddl)
	}
	b.WriteString("\n")
	b.WriteString(plan.replacementCTEs)
	b.WriteString(body[j:]) // frozen suffix: final assignments, stand-in DROPs
	return b.String(), nil
}
