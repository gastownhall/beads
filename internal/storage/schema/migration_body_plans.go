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
var migrationBodyPlans = map[repairKey]migrationBodyPlan{
	// gastownhall/beads#6520: the mixed issue/wisp is_blocked recompute. The
	// frozen body's correlated EXISTS probes and OR-joined recursive members
	// scan the unindexed __bd_0059_recompute_wisp_deps stand-in once per
	// candidate row; on the report's 14k-wisp-dependency clone the step ran
	// for over two hours without completing. The plan indexes the stand-in
	// and substitutes the equivalent set-based CTE list (same fixed point,
	// same UNION DISTINCT semantics, same gate predicate verbatim); the
	// frozen suffix's DROP TABLE removes stand-ins and indexes together, so
	// nothing persists. The cursor still records the frozen hash below.
	{mainSource.cursorTable, 59}: {
		dir:             mainSource.dir,
		name:            "0059_recompute_null_gate_is_blocked.up.sql",
		expectedSHA256:  "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394",
		finalUpdate:     "\nUPDATE issues\n",
		indexDDL:        main59StandInIndexes(),
		replacementCTEs: migrationBlockedRecomputeCTEs("__bd_0059_recompute_wisps", "__bd_0059_recompute_wisp_deps"),
	},
}

// main59StandInIndexes returns the three deterministic stand-in indexes of
// the 0059 plan: one per probe shape the replacement CTEs use (child lookup
// by waiter, membership by target kind). They are created after the guarded
// copies fill the stand-in and die with it in the frozen suffix's DROPs.
func main59StandInIndexes() []string {
	return []string{
		"CREATE INDEX idx_bd0059_deps_issue_type ON __bd_0059_recompute_wisp_deps (issue_id, type);",
		"CREATE INDEX idx_bd0059_deps_type_issue ON __bd_0059_recompute_wisp_deps (type, depends_on_issue_id, issue_id);",
		"CREATE INDEX idx_bd0059_deps_type_wisp ON __bd_0059_recompute_wisp_deps (type, depends_on_wisp_id, issue_id);",
	}
}

// migrationBlockedRecomputeCTEs is the set-based equivalent of the frozen
// 0059/ignored-15 recompute CTE list, parameterized only by the two
// wisp-plane table names (the 0059 stand-ins, or the real wisp tables for the
// ignored twin). It computes the same blocked-state fixed point:
//
//   - four uncorrelated child sets (open/closed x issue-target/wisp-target),
//     each a UNION of the durable-plane and wisp-plane parent-child edges
//     joined to the child entity's status, NULL projected targets excluded;
//   - blocked waiters: a waits-for row blocks its waiter when the target has
//     an open child and — only under the any-children gate — no closed child
//     yet, with the frozen gate COALESCE term verbatim and every membership
//     test guarded by IS NOT NULL so the NOT stays two-valued;
//   - directly_blocked(kind, id): open entities with a blocks or
//     conditional-blocks edge onto an open target of either kind, or a
//     blocking waits-for row, as an uncorrelated IN-UNION;
//   - reachable(kind, id): the same recursive UNION DISTINCT fixed point as
//     the frozen body, with the two OR-joined members split into four — one
//     per source-kind/target-kind pair — joined on equality on a single
//     target column.
//
// Equivalence was validated against the frozen bodies on real Dolt over a
// mixed small graph covering every predicate branch (both target kinds, all
// three gates, open/closed/pinned children, cross-plane parent-child
// chains, a mixed-kind cycle), and the scaled-graph and differential tests
// in internal/storage/dolt re-prove it on every run.
func migrationBlockedRecomputeCTEs(wispsTable, wispDepsTable string) string {
	return strings.NewReplacer("{WISPS}", wispsTable, "{WISP_DEPS}", wispDepsTable).Replace(`WITH RECURSIVE
  open_child_issue(target) AS (
    SELECT d.depends_on_issue_id
    FROM dependencies d
    JOIN issues child ON child.id = d.issue_id
    WHERE d.type = 'parent-child'
      AND d.depends_on_issue_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT cd.depends_on_issue_id
    FROM {WISP_DEPS} cd
    JOIN {WISPS} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
  ),
  open_child_wisp(target) AS (
    SELECT d.depends_on_wisp_id
    FROM dependencies d
    JOIN issues child ON child.id = d.issue_id
    WHERE d.type = 'parent-child'
      AND d.depends_on_wisp_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT cd.depends_on_wisp_id
    FROM {WISP_DEPS} cd
    JOIN {WISPS} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
  ),
  closed_child_issue(target) AS (
    SELECT d.depends_on_issue_id
    FROM dependencies d
    JOIN issues child ON child.id = d.issue_id
    WHERE d.type = 'parent-child'
      AND d.depends_on_issue_id IS NOT NULL
      AND child.status = 'closed'
    UNION
    SELECT cd.depends_on_issue_id
    FROM {WISP_DEPS} cd
    JOIN {WISPS} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status = 'closed'
  ),
  closed_child_wisp(target) AS (
    SELECT d.depends_on_wisp_id
    FROM dependencies d
    JOIN issues child ON child.id = d.issue_id
    WHERE d.type = 'parent-child'
      AND d.depends_on_wisp_id IS NOT NULL
      AND child.status = 'closed'
    UNION
    SELECT cd.depends_on_wisp_id
    FROM {WISP_DEPS} cd
    JOIN {WISPS} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status = 'closed'
  ),
  blocked_issue_waiters(waiter) AS (
    SELECT DISTINCT d.issue_id
    FROM dependencies d
    WHERE d.type = 'waits-for'
      AND (
        (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM open_child_issue))
        OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM open_child_wisp))
      )
      AND NOT (
        COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
        AND (
          (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM closed_child_issue))
          OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM closed_child_wisp))
        )
      )
  ),
  blocked_wisp_waiters(waiter) AS (
    SELECT DISTINCT d.issue_id
    FROM {WISP_DEPS} d
    WHERE d.type = 'waits-for'
      AND (
        (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM open_child_issue))
        OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM open_child_wisp))
      )
      AND NOT (
        COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
        AND (
          (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM closed_child_issue))
          OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM closed_child_wisp))
        )
      )
  ),
  directly_blocked(kind, id) AS (
    SELECT 'issue', i.id
    FROM issues i
    WHERE i.status NOT IN ('closed', 'pinned')
      AND (
        i.id IN (
          SELECT d.issue_id
          FROM dependencies d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR i.id IN (
          SELECT d.issue_id
          FROM dependencies d
          JOIN {WISPS} t ON t.id = d.depends_on_wisp_id
          WHERE d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR i.id IN (SELECT waiter FROM blocked_issue_waiters)
      )
    UNION
    SELECT 'wisp', w.id
    FROM {WISPS} w
    WHERE w.status NOT IN ('closed', 'pinned')
      AND (
        w.id IN (
          SELECT d.issue_id
          FROM {WISP_DEPS} d
          JOIN issues t ON t.id = d.depends_on_issue_id
          WHERE d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR w.id IN (
          SELECT d.issue_id
          FROM {WISP_DEPS} d
          JOIN {WISPS} t ON t.id = d.depends_on_wisp_id
          WHERE d.type IN ('blocks', 'conditional-blocks')
            AND t.status NOT IN ('closed', 'pinned')
        )
        OR w.id IN (SELECT waiter FROM blocked_wisp_waiters)
      )
  ),
  reachable(kind, id) AS (
    SELECT kind, id FROM directly_blocked
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d
      ON d.type = 'parent-child'
     AND r.kind = 'issue'
     AND d.depends_on_issue_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d
      ON d.type = 'parent-child'
     AND r.kind = 'wisp'
     AND d.depends_on_wisp_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN {WISP_DEPS} d
      ON d.type = 'parent-child'
     AND r.kind = 'issue'
     AND d.depends_on_issue_id = r.id
    JOIN {WISPS} child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN {WISP_DEPS} d
      ON d.type = 'parent-child'
     AND r.kind = 'wisp'
     AND d.depends_on_wisp_id = r.id
    JOIN {WISPS} child ON child.id = d.issue_id
    WHERE child.status NOT IN ('closed', 'pinned')
  )`)
}

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
