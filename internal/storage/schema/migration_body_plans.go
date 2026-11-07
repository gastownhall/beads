package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Execution plans for frozen migration bodies (gastownhall/beads#6520).
//
// A shipped migration file is frozen: its bytes, and the SHA-256 recorded in
// the cursor row, are its identity (see migration_freeze_test.go and
// scripts/check-migration-hygiene.sh). That identity does not have to be the
// literal text the runner executes. Main 0059 and its ignored/0015 twin
// recompute is_blocked with correlated EXISTS probes OR'd together and
// recursive joins on an OR of two target columns; on 0059 they also read an
// unindexed stand-in copy of wisp_dependencies. A clone with ~14k wisp
// dependencies did not finish 0059 in two hours.
//
// A plan swaps exactly the recursive recompute CTE list of one registered
// file for an equivalent set-based one, optionally preceded by index DDL on
// tables the file itself creates and drops. Everything before WITH RECURSIVE
// (stand-in setup, guarded copies, the zeroing UPDATE) and from the final
// UPDATE on (the assignment, stand-in DROPs) is kept byte for byte. A plan
// only applies to the exact embedded bytes it was written against: any drift
// in source, file name, hash or anchors is an error, never a silent fallback
// to the slow body. The cursor row still records the frozen file's hash.

// migrationBodyPlan describes how to execute one registered frozen file.
type migrationBodyPlan struct {
	// dir and name are the embedded source directory and file name the plan
	// was written for; a same-numbered file from another source or with
	// another name must not select it.
	dir  string
	name string
	// sha256 is the hex SHA-256 of the frozen file's bytes.
	sha256 string
	// head opens the replaced CTE list; finalUpdate opens the preserved
	// suffix. Each must occur exactly once, head first.
	head        string
	finalUpdate string
	// indexDDL runs between the preserved prefix and the replacement CTE
	// list. It may only index tables the frozen body creates and drops.
	indexDDL []string
	// cte is the replacement for the text from head up to finalUpdate.
	cte string
}

const migrationPlanHead = "\nWITH RECURSIVE\n"

// recomputeBlockedCTE is the set-based equivalent of the frozen recompute CTE
// list shared by main 0059 and ignored/0015. wispDeps and wisps name the wisp
// tables the frozen text reads (0059's stand-ins, or the real tables).
//
// The frozen directly_blocked probes each candidate with correlated EXISTS
// subqueries; here every probe is an uncorrelated set. A waits-for row's
// target is checked for membership in the open/closed child sets of its
// target kind, with the target guarded IS NOT NULL and NULLs excluded from
// the sets, so each membership test stays two-valued exactly as the frozen
// EXISTS did. The frozen recursive members join on
// (r.kind = 'issue' AND x = r.id) OR (r.kind = 'wisp' AND y = r.id); each is
// split into one UNION member per source kind, which reaches the same fixed
// point.
func recomputeBlockedCTE(wispDeps, wisps string) string {
	return strings.NewReplacer("{wisp_deps}", wispDeps, "{wisps}", wisps).Replace(`WITH RECURSIVE
  open_child_of_issue(target) AS (
    SELECT cd.depends_on_issue_id
    FROM dependencies cd
    JOIN issues child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT cd.depends_on_issue_id
    FROM {wisp_deps} cd
    JOIN {wisps} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
  ),
  open_child_of_wisp(target) AS (
    SELECT cd.depends_on_wisp_id
    FROM dependencies cd
    JOIN issues child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT cd.depends_on_wisp_id
    FROM {wisp_deps} cd
    JOIN {wisps} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status NOT IN ('closed', 'pinned')
  ),
  closed_child_of_issue(target) AS (
    SELECT cd.depends_on_issue_id
    FROM dependencies cd
    JOIN issues child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status = 'closed'
    UNION
    SELECT cd.depends_on_issue_id
    FROM {wisp_deps} cd
    JOIN {wisps} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_issue_id IS NOT NULL
      AND child.status = 'closed'
  ),
  closed_child_of_wisp(target) AS (
    SELECT cd.depends_on_wisp_id
    FROM dependencies cd
    JOIN issues child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status = 'closed'
    UNION
    SELECT cd.depends_on_wisp_id
    FROM {wisp_deps} cd
    JOIN {wisps} child ON child.id = cd.issue_id
    WHERE cd.type = 'parent-child'
      AND cd.depends_on_wisp_id IS NOT NULL
      AND child.status = 'closed'
  ),
  blocking_source(kind, id) AS (
    SELECT 'issue', d.issue_id
    FROM dependencies d
    JOIN issues t ON t.id = d.depends_on_issue_id
    WHERE d.type IN ('blocks', 'conditional-blocks')
      AND t.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'issue', d.issue_id
    FROM dependencies d
    JOIN {wisps} t ON t.id = d.depends_on_wisp_id
    WHERE d.type IN ('blocks', 'conditional-blocks')
      AND t.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'issue', d.issue_id
    FROM dependencies d
    WHERE d.type = 'waits-for'
      AND (
        (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM open_child_of_issue))
        OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM open_child_of_wisp))
      )
      AND NOT (
        COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
        AND (
          (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM closed_child_of_issue))
          OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM closed_child_of_wisp))
        )
      )
    UNION
    SELECT 'wisp', d.issue_id
    FROM {wisp_deps} d
    JOIN issues t ON t.id = d.depends_on_issue_id
    WHERE d.type IN ('blocks', 'conditional-blocks')
      AND t.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM {wisp_deps} d
    JOIN {wisps} t ON t.id = d.depends_on_wisp_id
    WHERE d.type IN ('blocks', 'conditional-blocks')
      AND t.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM {wisp_deps} d
    WHERE d.type = 'waits-for'
      AND (
        (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM open_child_of_issue))
        OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM open_child_of_wisp))
      )
      AND NOT (
        COALESCE(JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')), 'all-children') = 'any-children'
        AND (
          (d.depends_on_issue_id IS NOT NULL AND d.depends_on_issue_id IN (SELECT target FROM closed_child_of_issue))
          OR (d.depends_on_wisp_id IS NOT NULL AND d.depends_on_wisp_id IN (SELECT target FROM closed_child_of_wisp))
        )
      )
  ),
  directly_blocked(kind, id) AS (
    SELECT 'issue', i.id
    FROM issues i
    JOIN blocking_source s ON s.kind = 'issue' AND s.id = i.id
    WHERE i.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', w.id
    FROM {wisps} w
    JOIN blocking_source s ON s.kind = 'wisp' AND s.id = w.id
    WHERE w.status NOT IN ('closed', 'pinned')
  ),
  reachable(kind, id) AS (
    SELECT kind, id FROM directly_blocked
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d ON d.type = 'parent-child' AND d.depends_on_issue_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE r.kind = 'issue'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'issue', d.issue_id
    FROM reachable r
    JOIN dependencies d ON d.type = 'parent-child' AND d.depends_on_wisp_id = r.id
    JOIN issues child ON child.id = d.issue_id
    WHERE r.kind = 'wisp'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN {wisp_deps} d ON d.type = 'parent-child' AND d.depends_on_issue_id = r.id
    JOIN {wisps} child ON child.id = d.issue_id
    WHERE r.kind = 'issue'
      AND child.status NOT IN ('closed', 'pinned')
    UNION
    SELECT 'wisp', d.issue_id
    FROM reachable r
    JOIN {wisp_deps} d ON d.type = 'parent-child' AND d.depends_on_wisp_id = r.id
    JOIN {wisps} child ON child.id = d.issue_id
    WHERE r.kind = 'wisp'
      AND child.status NOT IN ('closed', 'pinned')
  )`)
}

// migrationBodyPlans is keyed like preMigrationRepairs. Tests swap it to
// exercise drift; production never mutates it.
var migrationBodyPlans = map[repairKey]migrationBodyPlan{
	{"schema_migrations", 59}: {
		dir:         "migrations",
		name:        "0059_recompute_null_gate_is_blocked.up.sql",
		sha256:      "f0186029a1c87d579b67e0540ec22ad0bd0fc59793b07f7d24a24e122b77c394",
		head:        migrationPlanHead,
		finalUpdate: "\nUPDATE issues\n",
		// The stand-in is created, filled and dropped by the frozen body
		// itself; its indexes go with it.
		indexDDL: []string{
			"CREATE INDEX idx_bd0059_deps_issue_type ON __bd_0059_recompute_wisp_deps (issue_id, type)",
			"CREATE INDEX idx_bd0059_deps_type_issue ON __bd_0059_recompute_wisp_deps (type, depends_on_issue_id, issue_id)",
			"CREATE INDEX idx_bd0059_deps_type_wisp ON __bd_0059_recompute_wisp_deps (type, depends_on_wisp_id, issue_id)",
		},
		cte: recomputeBlockedCTE("__bd_0059_recompute_wisp_deps", "__bd_0059_recompute_wisps"),
	},
	{"ignored_schema_migrations", 15}: {
		dir:         "migrations/ignored",
		name:        "0015_recompute_null_gate_wisp_is_blocked.up.sql",
		sha256:      "e761d5c3ceb9610edbb2e1d153e82d235cc7d7f0b87f28a45bec43835e6a4e19",
		head:        migrationPlanHead,
		finalUpdate: "\nUPDATE wisps\n",
		cte:         recomputeBlockedCTE("wisp_dependencies", "wisps"),
	},
}

// plannedMigrationBody returns the SQL the runner executes for one migration
// file of src: the embedded bytes unchanged when no plan is registered, else
// the plan applied to them. A registered plan that does not match the bytes
// is an error.
func (m migrationSource) plannedMigrationBody(version int, name string, data []byte) (string, error) {
	plan, ok := migrationBodyPlans[repairKey{m.cursorTable, version}]
	if !ok {
		return string(data), nil
	}
	return plan.apply(m.dir, name, data)
}

// apply validates dir, name and data against the plan and returns the
// planned body.
func (p migrationBodyPlan) apply(dir, name string, data []byte) (string, error) {
	if dir != p.dir || name != p.name {
		return "", fmt.Errorf("execution plan for %s/%s does not match migration %s/%s", p.dir, p.name, dir, name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != p.sha256 {
		return "", fmt.Errorf("execution plan for %s/%s expects content hash %s, embedded file hashes to %s", p.dir, p.name, p.sha256, got)
	}
	body := string(data)
	if p.head == "" || p.finalUpdate == "" {
		return "", fmt.Errorf("execution plan for %s/%s has an empty anchor", p.dir, p.name)
	}
	if n := strings.Count(body, p.head); n != 1 {
		return "", fmt.Errorf("execution plan for %s/%s: anchor %q occurs %d times, want 1", p.dir, p.name, p.head, n)
	}
	if n := strings.Count(body, p.finalUpdate); n != 1 {
		return "", fmt.Errorf("execution plan for %s/%s: anchor %q occurs %d times, want 1", p.dir, p.name, p.finalUpdate, n)
	}
	head, tail := strings.Index(body, p.head), strings.Index(body, p.finalUpdate)
	if tail < head+len(p.head) {
		return "", fmt.Errorf("execution plan for %s/%s: anchor %q does not follow %q", p.dir, p.name, p.finalUpdate, p.head)
	}

	var b strings.Builder
	b.WriteString(body[:head+1]) // through the newline that ends the prefix
	for _, ddl := range p.indexDDL {
		b.WriteString(ddl)
		b.WriteString(";\n")
	}
	b.WriteString(p.cte)
	b.WriteString(body[tail:])
	return b.String(), nil
}
