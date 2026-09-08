//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt coverage for the pre-0058 wisp_dependencies forward repair
// (repairWispDependenciesForwardShape, internal/storage/schema).
//
// Why this cannot be a sqlmock test. Every load-bearing fact in that repair is
// Dolt semantics a statement-echo mock cannot exercise: that Dolt rejects a
// foreign key on the base column of a stored generated column at all; that
// normalizing a multi-target row recomputes the COALESCE generated column and
// so can collide on the legacy composite primary key; that DEFAULT (UUID())
// mints a distinct value per existing row, which is what makes the MIN(id)
// dedup deterministic. A mock asserting the statement ORDER would have passed
// against the first draft of this repair, which normalized before the drop and
// aborted on real Dolt with "duplicate primary key given: [w2,external:e1]".
//
// The two subtests are the two populations that actually exist. Measured
// against a live 25-database Dolt server carrying the affected shape: 8
// databases are on the delegate path with no target foreign keys, and every one
// of the 14 databases still holding the generated column has no id column,
// confirming the lineages are disjoint.

// legacyDelegateWispDepDDL is the shape 0047's delegate branch leaves behind:
// the three split target columns present, depends_on_id still a STORED
// generated column over them, the composite primary key, and neither target
// foreign key. Taken verbatim from a production SHOW CREATE TABLE rather than
// hand-idealized, minus ck_wisp_dep_one_target so the multi-target rows this
// test seeds are insertable (a store carrying the check cannot hold them, which
// is the second subtest).
const legacyDelegateWispDepDDL = `
CREATE TABLE wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    depends_on_issue_id VARCHAR(255) NULL,
    depends_on_wisp_id VARCHAR(255) NULL,
    depends_on_external VARCHAR(255) NULL,
    depends_on_id VARCHAR(255) NOT NULL GENERATED ALWAYS AS (COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external)) STORED,
    PRIMARY KEY (issue_id, depends_on_id),
    KEY idx_wisp_dep_external_target (depends_on_external),
    KEY idx_wisp_dep_issue_target (depends_on_issue_id),
    KEY idx_wisp_dep_type (type),
    KEY idx_wisp_dep_type_target (type, depends_on_id),
    KEY idx_wisp_dep_wisp_target (depends_on_wisp_id)
)`

func TestEmbeddedPre0058RepairMarchesDriftedWispDependenciesToFinalShape(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	// The shape a fresh, fully migrated store converges on.
	//
	// The comparison is structural (columns with their types and defaults, the
	// primary key, every unique key, every foreign key with its referenced
	// table and columns, every check) rather than SHOW CREATE text equality.
	// Text equality is the wrong bar, and provably so: a legacy table healed by
	// the SHIPPED path keeps its original column order, because nothing in the
	// chain reorders columns. The already-migrated production database this
	// work came from has wisp_dependencies as (id, issue_id, type, created_at,
	// created_by, metadata, thread_id, depends_on_issue_id, depends_on_wisp_id,
	// depends_on_external) where a fresh store puts the three target columns
	// third through fifth. Asserting text equality would fail every correctly
	// healed store, including ones this repair never touched. Legacy secondary
	// index names survive for the same reason -- a fresh store names its
	// foreign-key backing indexes after the constraints because no index
	// existed to reuse, while a legacy table already has idx_wisp_dep_*_target
	// covering those columns and Dolt reuses them. Index COVERAGE is asserted;
	// index NAMES are not.
	fresh := readWispDepShape(t, ctx, referenceStoreConn(t, ctx))

	t.Run("delegate-path store carrying rows the final shape rejects", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, 53)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)

		mustExecConn(t, ctx, conn, "DROP TABLE wisp_dependencies")
		mustExecConn(t, ctx, conn, legacyDelegateWispDepDDL)

		// Referenced rows. issue_id and depends_on_wisp_id both point at
		// wisps(id); depends_on_issue_id points at issues(id).
		for _, id := range []string{"w1", "w2", "w3"} {
			seedWisp(t, ctx, conn, id)
		}
		seedIssue(t, ctx, conn, "i1")

		// Two rows that must survive untouched.
		seedWispDep(t, ctx, conn, "w1", nil, strptr("w2"), nil)
		seedWispDep(t, ctx, conn, "w3", strptr("i1"), nil, nil)
		// Three foreign-key orphans, one per constraint the repair adds.
		// "ghost" is not a wisp, so this row orphans fk_wisp_dep_issue --
		// which 0058 does not clean, because 0058 does not add that key.
		seedWispDep(t, ctx, conn, "ghost", nil, strptr("w2"), nil)
		seedWispDep(t, ctx, conn, "w1", nil, strptr("ghost-w"), nil)
		seedWispDep(t, ctx, conn, "w1", strptr("ghost-i"), nil, nil)
		// A multi-target row whose normalization lands it on the natural
		// identity of the row below it. This pair is the regression for the
		// ordering bug: normalize-before-drop rewrites this row's generated
		// primary key onto the other's and aborts.
		seedWispDep(t, ctx, conn, "w2", nil, strptr("w3"), strptr("external:e1"))
		seedWispDep(t, ctx, conn, "w2", nil, nil, strptr("external:e1"))

		commitSeed(t, ctx, conn, "test: reproduce the pre-0058 drifted wisp_dependencies")
		closeConn()

		conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
		defer closeConn2()

		// Without the repair this fails with
		// "Cannot add foreign key on the base column of a stored generated
		// column" and strands the cursor at 57.
		if _, err := schema.MigrateUp(ctx, conn2); err != nil {
			t.Fatalf("MigrateUp over a drifted wisp_dependencies: %v", err)
		}
		if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
			t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
		}

		requireSameWispDepShape(t, readWispDepShape(t, ctx, conn2), fresh)

		// Row outcomes, stated as the full surviving set rather than a count:
		// a count passes even when the wrong three rows survive.
		want := []wispDepRow{
			{"w1", "", "w2", ""},
			{"w2", "", "", "external:e1"},
			{"w3", "i1", "", ""},
		}
		if got := readWispDeps(t, ctx, conn2); !sameWispDepRows(got, want) {
			t.Fatalf("surviving rows = %v, want %v", got, want)
		}
	})

	t.Run("production shape: check constraint present, table empty", func(t *testing.T) {
		// The 8 affected databases measured on the live server: same legacy
		// shape but carrying ck_wisp_dep_one_target and zero rows. Nothing for
		// the cleanup or dedup to do, so this subtest isolates the structural
		// rebuild from the row surgery.
		dataDir := seedMainSchemaAt(t, ctx, 53)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)

		mustExecConn(t, ctx, conn, "DROP TABLE wisp_dependencies")
		mustExecConn(t, ctx, conn, legacyDelegateWispDepDDL)
		mustExecConn(t, ctx, conn, "ALTER TABLE wisp_dependencies ADD CONSTRAINT ck_wisp_dep_one_target CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)")
		commitSeed(t, ctx, conn, "test: reproduce the production pre-0058 wisp_dependencies")
		closeConn()

		conn2, closeConn2 := openPinnedConn(t, ctx, dataDir)
		defer closeConn2()

		if _, err := schema.MigrateUp(ctx, conn2); err != nil {
			t.Fatalf("MigrateUp over the production pre-0058 shape: %v", err)
		}
		requireSameWispDepShape(t, readWispDepShape(t, ctx, conn2), fresh)
	})

	t.Run("a fresh store is left alone", func(t *testing.T) {
		// The repair must be invisible to every database that does not need
		// it. A fresh store reaches 0058 with no generated column, so 0058
		// applies cleanly and the repair has to no-op rather than rebuild a
		// healthy table.
		dataDir := seedMainSchemaAt(t, ctx, 53)
		conn, closeConn := openPinnedConn(t, ctx, dataDir)
		defer closeConn()

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp on a fresh store: %v", err)
		}
		requireSameWispDepShape(t, readWispDepShape(t, ctx, conn), fresh)
	})
}

// referenceStoreConn returns a connection to a store that migrated normally to
// head, for use as the shape the repair must converge on.
func referenceStoreConn(t *testing.T, ctx context.Context) *sql.Conn {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	t.Cleanup(closeConn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp on the reference store: %v", err)
	}
	return conn
}

type wispDepRow struct {
	issueID, dependsOnIssueID, dependsOnWispID, dependsOnExternal string
}

func readWispDeps(t *testing.T, ctx context.Context, conn *sql.Conn) []wispDepRow {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
SELECT issue_id,
       COALESCE(depends_on_issue_id, ''),
       COALESCE(depends_on_wisp_id, ''),
       COALESCE(depends_on_external, '')
FROM wisp_dependencies
ORDER BY issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external`)
	if err != nil {
		t.Fatalf("read wisp_dependencies: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []wispDepRow
	for rows.Next() {
		var r wispDepRow
		if err := rows.Scan(&r.issueID, &r.dependsOnIssueID, &r.dependsOnWispID, &r.dependsOnExternal); err != nil {
			t.Fatalf("scan wisp_dependencies row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate wisp_dependencies: %v", err)
	}
	return out
}

func sameWispDepRows(got, want []wispDepRow) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// wispDepShape is the structural contract the repair must satisfy: what a
// query planner and a write path can actually observe. Column ORDER and plain
// index NAMES are deliberately excluded -- see the comment at the top of the
// test for why both legitimately differ on a healed legacy store.
type wispDepShape struct {
	columns     map[string]string // name -> "type|nullable|default"
	primaryKey  string            // ordered PK columns
	uniqueKeys  map[string]string // name -> ordered columns
	foreignKeys map[string]string // name -> "cols -> reftable(refcols)"
	checks      map[string]bool
	indexCover  map[string]bool // ordered column list of every index, name-independent
}

func readWispDepShape(t *testing.T, ctx context.Context, conn *sql.Conn) wispDepShape {
	t.Helper()
	shape := wispDepShape{
		columns:     map[string]string{},
		uniqueKeys:  map[string]string{},
		foreignKeys: map[string]string{},
		checks:      map[string]bool{},
		indexCover:  map[string]bool{},
	}

	queryEach(t, ctx, conn, `
SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, '<none>')
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'`,
		4, func(v []string) { shape.columns[v[0]] = v[1] + "|" + v[2] + "|" + v[3] })

	// Ordered column lists per index. GROUP_CONCAT over SEQ_IN_INDEX keeps
	// (type, depends_on_issue_id) distinct from (depends_on_issue_id, type).
	queryEach(t, ctx, conn, `
SELECT INDEX_NAME, NON_UNIQUE, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
GROUP BY INDEX_NAME, NON_UNIQUE`,
		3, func(v []string) {
			switch {
			case v[0] == "PRIMARY":
				shape.primaryKey = v[2]
			case v[1] == "0":
				shape.uniqueKeys[v[0]] = v[2]
			default:
				shape.indexCover[v[2]] = true
			}
		})

	queryEach(t, ctx, conn, `
SELECT rc.CONSTRAINT_NAME,
       GROUP_CONCAT(kcu.COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION),
       rc.REFERENCED_TABLE_NAME,
       GROUP_CONCAT(kcu.REFERENCED_COLUMN_NAME ORDER BY kcu.ORDINAL_POSITION),
       rc.DELETE_RULE, rc.UPDATE_RULE
FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS rc
JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE kcu
  ON kcu.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
 AND kcu.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
WHERE rc.CONSTRAINT_SCHEMA = DATABASE() AND rc.TABLE_NAME = 'wisp_dependencies'
GROUP BY rc.CONSTRAINT_NAME, rc.REFERENCED_TABLE_NAME, rc.DELETE_RULE, rc.UPDATE_RULE`,
		6, func(v []string) {
			shape.foreignKeys[v[0]] = v[1] + " -> " + v[2] + "(" + v[3] + ") ON DELETE " + v[4] + " ON UPDATE " + v[5]
		})

	queryEach(t, ctx, conn, `
SELECT CONSTRAINT_NAME FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
  AND CONSTRAINT_TYPE = 'CHECK'`,
		1, func(v []string) { shape.checks[v[0]] = true })

	return shape
}

func requireSameWispDepShape(t *testing.T, got, want wispDepShape) {
	t.Helper()
	requireSameStringMap(t, "columns", got.columns, want.columns)
	if got.primaryKey != want.primaryKey {
		t.Errorf("primary key = %q, want %q", got.primaryKey, want.primaryKey)
	}
	requireSameStringMap(t, "unique keys", got.uniqueKeys, want.uniqueKeys)
	requireSameStringMap(t, "foreign keys", got.foreignKeys, want.foreignKeys)
	for name := range want.checks {
		if !got.checks[name] {
			t.Errorf("check constraint %s missing", name)
		}
	}
	// Coverage, not names: every index the reference store carries must have a
	// counterpart over the same columns. Extra legacy indexes are allowed --
	// they cost a little write throughput, not correctness, and dropping them
	// is not this repair's job.
	for cols := range want.indexCover {
		if !got.indexCover[cols] && got.uniqueKeys[cols] == "" && got.primaryKey != cols {
			covered := false
			for _, uk := range got.uniqueKeys {
				if uk == cols {
					covered = true
				}
			}
			if !covered {
				t.Errorf("no index covers (%s)", cols)
			}
		}
	}
	if _, ok := got.columns["depends_on_id"]; ok {
		t.Error("depends_on_id survived the repair")
	}
}

func requireSameStringMap(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("%s: %s missing", label, k)
			continue
		}
		if gv != wv {
			t.Errorf("%s: %s = %q, want %q", label, k, gv, wv)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected %s = %q", label, k, got[k])
		}
	}
}

func queryEach(t *testing.T, ctx context.Context, conn *sql.Conn, query string, cols int, fn func([]string)) {
	t.Helper()
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query %.60q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		vals := make([]string, cols)
		ptrs := make([]any, cols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %.60q: %v", query, err)
		}
		fn(vals)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %.60q: %v", query, err)
	}
}

func mustExecConn(t *testing.T, ctx context.Context, conn *sql.Conn, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %.60q: %v", stmt, err)
	}
}

func seedWisp(t *testing.T, ctx context.Context, conn *sql.Conn, id string) {
	t.Helper()
	mustExecConn(t, ctx, conn,
		"INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral) VALUES (?, 'w', '', '', '', '', 'open', 2, 'task', 1)", id)
}

func seedIssue(t *testing.T, ctx context.Context, conn *sql.Conn, id string) {
	t.Helper()
	mustExecConn(t, ctx, conn,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'i', '', '', '', '', 'open', 2, 'task')", id)
}

func seedWispDep(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string, issueTarget, wispTarget, externalTarget *string) {
	t.Helper()
	mustExecConn(t, ctx, conn, `
INSERT INTO wisp_dependencies (issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())`,
		issueID, nullable(issueTarget), nullable(wispTarget), nullable(externalTarget))
}

// commitSeed stages and commits the seed. issues is a synced table and several
// pending migrations touch it, so an uncommitted insert trips MigrateUp's
// dirty-table guard.
func commitSeed(t *testing.T, ctx context.Context, conn *sql.Conn, message string) {
	t.Helper()
	mustExecConn(t, ctx, conn, "CALL DOLT_ADD('-A')")
	// --skip-empty: wisps and wisp_dependencies are dolt_ignore'd, so a seed
	// that only recreates those tables stages nothing and a bare commit fails
	// with "nothing to commit".
	mustExecConn(t, ctx, conn, "CALL DOLT_COMMIT('-m', ?, '--skip-empty')", message)
}

func nullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func strptr(s string) *string { return &s }
