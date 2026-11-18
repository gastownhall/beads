package dolt

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// Server-mode regressions for gastownhall/beads#6092 bug 2: ignored migration
// 0005 failing with "Duplicate key name 'idx_wisp_dep_type_issue'" on a store
// whose main cursor is already at latest but whose clone-local
// wisp_dependencies is still the legacy generated-column shape.
//
// The population. Main 0022 adds the three idx_wisp_dep_type_* indexes to any
// wisp_dependencies that already carries the split target columns, and the
// pre-0058 forward repair only runs while main 58 is pending. Ignored state and
// the wisp tables are clone-local, so a clone whose main cursor is past 58 can
// still hold the legacy table at ignored cursor 4. ignored/0005 computes
// @needs_drop once, rebuilds the table, and then adds the same three indexes
// unconditionally -- colliding with the ones 0022 already created. The fix
// registers the existing forward repair before ignored 5, so the frozen body
// takes its @needs_drop = 0 branch on a table already in the final shape.
//
// Every test here runs on its own Dolt branch (setupTestStore) with a single
// pooled connection, so the session variables and the clone-local tables the
// migrations touch stay on that branch.

// issue6092IgnoredCursorBefore0005 is the ignored cursor these fixtures rewind
// to: ignored/0005 and everything after it is pending.
const issue6092IgnoredCursorBefore0005 = 4

// issue6092LegacyWispDepDDL is the generated-column shape ignored/0003 (and
// main 0047's delegate branch) leaves behind: split target columns underneath
// a STORED COALESCE depends_on_id, the composite primary key and the
// idx_wisp_dep_type_target index over the generated column. It is ported from
// legacyDelegateWispDepDDL in
// internal/storage/embeddeddolt/migrate_wisp_dep_forward_repair_test.go; the
// source foreign key and main 0022's indexes are added by the fixture.
const issue6092LegacyWispDepDDL = `
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

// issue6092Referenced are the rows the seeded dependencies point at, each with
// a distinct, months-old updated_at the upgrade must not touch.
var issue6092Referenced = []struct {
	table, id string
	stamp     time.Time
}{
	{"wisps", "i6092-w1", time.Date(2025, time.March, 1, 9, 30, 1, 0, time.UTC)},
	{"wisps", "i6092-w2", time.Date(2025, time.March, 2, 9, 30, 2, 0, time.UTC)},
	{"wisps", "i6092-w3", time.Date(2025, time.March, 3, 9, 30, 3, 0, time.UTC)},
	{"issues", "i6092-i1", time.Date(2025, time.March, 4, 9, 30, 4, 0, time.UTC)},
}

// issue6092Dep is one wisp_dependencies row as the tests compare it. id is
// filled only when the test reads it back.
type issue6092Dep struct {
	id, issueID, issueTarget, wispTarget, externalTarget, depType, createdBy string
}

// issue6092ValidDeps are distinct single-target rows of all three target
// kinds: none of them is removed or rewritten by the repair's cleanup.
var issue6092ValidDeps = []issue6092Dep{
	{issueID: "i6092-w1", wispTarget: "i6092-w2", depType: "blocks", createdBy: "alice"},
	{issueID: "i6092-w1", issueTarget: "i6092-i1", depType: "related", createdBy: "bob"},
	{issueID: "i6092-w2", externalTarget: "external:proj:cap", depType: "blocks", createdBy: "carol"},
	{issueID: "i6092-w3", wispTarget: "i6092-w1", depType: "parent-child", createdBy: "dave"},
}

func issue6092Exec(t *testing.T, ctx context.Context, db *sql.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %.80q: %v", stmt, err)
	}
}

func issue6092Nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// issue6092SeedReferenced inserts the referenced wisps and issue with their
// historical timestamps.
func issue6092SeedReferenced(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, r := range issue6092Referenced {
		extra, extraVal := "", ""
		if r.table == "wisps" {
			extra, extraVal = ", ephemeral", ", 1"
		}
		issue6092Exec(t, ctx, db,
			"INSERT INTO "+r.table+" (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at"+extra+") "+
				"VALUES (?, 'issue 6092 fixture', '', '', '', '', 'open', 2, 'task', ?, ?"+extraVal+")",
			r.id, r.stamp, r.stamp)
	}
}

// issue6092SeedDeps inserts deps into wisp_dependencies. When withIDs is set
// each row gets an explicit, recognizable id (for tables that have one).
func issue6092SeedDeps(t *testing.T, ctx context.Context, db *sql.DB, deps []issue6092Dep, withIDs bool) {
	t.Helper()
	for _, d := range deps {
		cols := "issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_by"
		vals := "?, ?, ?, ?, ?, ?"
		args := []any{d.issueID, issue6092Nullable(d.issueTarget), issue6092Nullable(d.wispTarget),
			issue6092Nullable(d.externalTarget), d.depType, d.createdBy}
		if withIDs {
			cols = "id, " + cols
			vals = "?, " + vals
			args = append([]any{d.id}, args...)
		}
		issue6092Exec(t, ctx, db, "INSERT INTO wisp_dependencies ("+cols+") VALUES ("+vals+")", args...)
	}
}

// issue6092CommitAndRewindIgnored commits the tracked part of a fixture (the
// referenced issue row) so MigrateUp's dirty-table guard sees a clean working
// set, then rewinds only the clone-local ignored cursor. The cursor is
// dolt-ignored, so the rewind needs no commit.
func issue6092CommitAndRewindIgnored(t *testing.T, ctx context.Context, db *sql.DB, cursor int) {
	t.Helper()
	issue6092Exec(t, ctx, db, "CALL DOLT_ADD('-A')")
	issue6092Exec(t, ctx, db, "CALL DOLT_COMMIT('-m', 'test: issue 6092 fixture', '--skip-empty')")
	issue6092Exec(t, ctx, db, "DELETE FROM ignored_schema_migrations WHERE version > ?", cursor)
}

// issue6092BuildLegacyWispDeps replaces the store's wisp_dependencies with the
// legacy generated-column shape, adds its source foreign key, runs main 0022's
// shipped body to create the real colliding type indexes, and seeds the
// referenced rows and the valid dependencies.
func issue6092BuildLegacyWispDeps(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	issue6092Exec(t, ctx, db, "DROP TABLE wisp_dependencies")
	issue6092Exec(t, ctx, db, issue6092LegacyWispDepDDL)
	issue6092SeedReferenced(t, ctx, db)
	issue6092Exec(t, ctx, db,
		"ALTER TABLE wisp_dependencies ADD CONSTRAINT fk_wisp_dep_issue FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE")

	body, err := schema.MigrationSQL("0022_wisp_dep_type_index.up.sql")
	if err != nil {
		t.Fatalf("read main 0022: %v", err)
	}
	issue6092Exec(t, ctx, db, body)
	for _, idx := range []string{"idx_wisp_dep_type_issue", "idx_wisp_dep_type_wisp", "idx_wisp_dep_type_external"} {
		if !issue6092IndexExists(t, ctx, db, idx) {
			t.Fatalf("fixture: main 0022 did not create %s on the legacy table", idx)
		}
	}
	issue6092SeedDeps(t, ctx, db, issue6092ValidDeps, false)
}

func issue6092IndexExists(t *testing.T, ctx context.Context, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies' AND INDEX_NAME = ?`, name).Scan(&n); err != nil {
		t.Fatalf("probe index %s: %v", name, err)
	}
	return n > 0
}

// issue6092RequireCursorsAtLatest asserts both migration cursors reached head.
func issue6092RequireCursorsAtLatest(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if v, err := schema.CurrentVersion(ctx, db); err != nil || v != schema.LatestVersion() {
		t.Fatalf("main cursor = %d (err %v), want latest %d", v, err, schema.LatestVersion())
	}
	if v, err := schema.CurrentIgnoredVersion(ctx, db); err != nil || v != schema.LatestIgnoredVersion() {
		t.Fatalf("ignored cursor = %d (err %v), want latest %d", v, err, schema.LatestIgnoredVersion())
	}
}

// issue6092ReadDeps returns every wisp_dependencies row, sorted by its
// natural identity.
func issue6092ReadDeps(t *testing.T, ctx context.Context, db *sql.DB) []issue6092Dep {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT id, issue_id, COALESCE(depends_on_issue_id, ''), COALESCE(depends_on_wisp_id, ''),
       COALESCE(depends_on_external, ''), type, COALESCE(created_by, '')
FROM wisp_dependencies`)
	if err != nil {
		t.Fatalf("read wisp_dependencies: %v", err)
	}
	defer rows.Close()
	var out []issue6092Dep
	for rows.Next() {
		var d issue6092Dep
		if err := rows.Scan(&d.id, &d.issueID, &d.issueTarget, &d.wispTarget, &d.externalTarget, &d.depType, &d.createdBy); err != nil {
			t.Fatalf("scan wisp_dependencies: %v", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate wisp_dependencies: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return issue6092Identity(out[i]) < issue6092Identity(out[j]) })
	return out
}

func issue6092Identity(d issue6092Dep) string {
	return strings.Join([]string{d.issueID, d.issueTarget, d.wispTarget, d.externalTarget}, "|")
}

// issue6092RequireDepsPreserved asserts the table holds exactly want (by
// natural identity, type and created_by) under distinct nonempty ids.
func issue6092RequireDepsPreserved(t *testing.T, got, want []issue6092Dep) {
	t.Helper()
	wantSorted := append([]issue6092Dep(nil), want...)
	sort.Slice(wantSorted, func(i, j int) bool { return issue6092Identity(wantSorted[i]) < issue6092Identity(wantSorted[j]) })
	if len(got) != len(wantSorted) {
		t.Fatalf("wisp_dependencies holds %d rows, want %d: %+v", len(got), len(wantSorted), got)
	}
	ids := map[string]bool{}
	for i, g := range got {
		w := wantSorted[i]
		if issue6092Identity(g) != issue6092Identity(w) || g.depType != w.depType || g.createdBy != w.createdBy {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
		if g.id == "" {
			t.Errorf("row %d (%s) has an empty id", i, issue6092Identity(g))
		}
		if ids[g.id] {
			t.Errorf("row %d (%s) shares id %q with another row", i, issue6092Identity(g), g.id)
		}
		ids[g.id] = true
	}
}

// issue6092RequireReferencedUnchanged asserts the referenced wisps and issue
// still exist with their historical updated_at.
func issue6092RequireReferencedUnchanged(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, r := range issue6092Referenced {
		var got time.Time
		if err := db.QueryRowContext(ctx, "SELECT updated_at FROM "+r.table+" WHERE id = ?", r.id).Scan(&got); err != nil {
			t.Fatalf("read %s %s: %v", r.table, r.id, err)
		}
		if !got.Equal(r.stamp) {
			t.Errorf("%s %s: updated_at = %s, want its historical %s", r.table, r.id,
				got.UTC().Format(time.RFC3339), r.stamp.Format(time.RFC3339))
		}
	}
}

// issue6092Shape is the structural contract of wisp_dependencies: columns,
// primary key, unique keys, foreign keys, checks and the column lists every
// secondary index covers. Index names and column order are excluded: a healed
// legacy table legitimately keeps its own (see readWispDepShape in the
// embeddeddolt forward-repair test).
type issue6092Shape struct {
	columns     map[string]string
	primaryKey  string
	uniqueKeys  map[string]string
	foreignKeys map[string]string
	checks      map[string]bool
	indexCover  map[string]bool
}

func issue6092QueryEach(t *testing.T, ctx context.Context, db *sql.DB, query string, cols int, fn func([]string)) {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query %.60q: %v", query, err)
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]sql.NullString, cols)
		ptrs := make([]any, cols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %.60q: %v", query, err)
		}
		out := make([]string, cols)
		for i, v := range vals {
			out[i] = v.String
		}
		fn(out)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %.60q: %v", query, err)
	}
}

func issue6092ReadShape(t *testing.T, ctx context.Context, db *sql.DB) issue6092Shape {
	t.Helper()
	s := issue6092Shape{
		columns:     map[string]string{},
		uniqueKeys:  map[string]string{},
		foreignKeys: map[string]string{},
		checks:      map[string]bool{},
		indexCover:  map[string]bool{},
	}
	issue6092QueryEach(t, ctx, db, `
SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'`,
		3, func(v []string) { s.columns[v[0]] = v[1] + "|" + v[2] })
	issue6092QueryEach(t, ctx, db, `
SELECT INDEX_NAME, NON_UNIQUE, GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
FROM INFORMATION_SCHEMA.STATISTICS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
GROUP BY INDEX_NAME, NON_UNIQUE`,
		3, func(v []string) {
			switch {
			case v[0] == "PRIMARY":
				s.primaryKey = v[2]
			case v[1] == "0":
				s.uniqueKeys[v[0]] = v[2]
			default:
				s.indexCover[v[2]] = true
			}
		})
	issue6092QueryEach(t, ctx, db, `
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
			s.foreignKeys[v[0]] = v[1] + " -> " + v[2] + "(" + v[3] + ") ON DELETE " + v[4] + " ON UPDATE " + v[5]
		})
	issue6092QueryEach(t, ctx, db, `
SELECT CONSTRAINT_NAME FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_dependencies'
  AND CONSTRAINT_TYPE = 'CHECK'`,
		1, func(v []string) { s.checks[v[0]] = true })
	return s
}

// issue6092RequireFinalShape asserts got carries everything the reference
// (fresh, fully migrated) shape carries: same columns, primary key, unique
// keys, foreign keys and checks, and an index over every column list the
// reference indexes. Extra legacy secondary indexes are tolerated.
func issue6092RequireFinalShape(t *testing.T, got, want issue6092Shape) {
	t.Helper()
	issue6092RequireSameMap(t, "columns", got.columns, want.columns)
	if got.primaryKey != want.primaryKey {
		t.Errorf("primary key = %q, want %q", got.primaryKey, want.primaryKey)
	}
	issue6092RequireSameMap(t, "unique keys", got.uniqueKeys, want.uniqueKeys)
	issue6092RequireSameMap(t, "foreign keys", got.foreignKeys, want.foreignKeys)
	for name := range want.checks {
		if !got.checks[name] {
			t.Errorf("check constraint %s missing", name)
		}
	}
	for cols := range want.indexCover {
		if got.indexCover[cols] || got.primaryKey == cols {
			continue
		}
		covered := false
		for _, uk := range got.uniqueKeys {
			covered = covered || uk == cols
		}
		if !covered {
			t.Errorf("no index covers (%s)", cols)
		}
	}
}

func issue6092RequireSameMap(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	for k, wv := range want {
		if gv, ok := got[k]; !ok {
			t.Errorf("%s: %s missing", label, k)
		} else if gv != wv {
			t.Errorf("%s: %s = %q, want %q", label, k, gv, wv)
		}
	}
	for k, gv := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: unexpected %s = %q", label, k, gv)
		}
	}
}

// TestIssue6092IgnoredWispDepDropOverPreexistingTypeIndexes is the primary
// regression (AC-1/AC-2): main at latest, ignored cursor 4, and a
// generated-column wisp_dependencies that already carries main 0022's type
// indexes. Before the ignored-5 repair, MigrateUp failed in ignored/0005 with
// "Duplicate key name 'idx_wisp_dep_type_issue'".
func TestIssue6092IgnoredWispDepDropOverPreexistingTypeIndexes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 4*testTimeout)
	defer cancel()
	db := store.db

	// The fresh store's own table is the shape the upgrade must converge on.
	final := issue6092ReadShape(t, ctx, db)

	issue6092BuildLegacyWispDeps(t, ctx, db)
	issue6092CommitAndRewindIgnored(t, ctx, db, issue6092IgnoredCursorBefore0005)
	if v, err := schema.CurrentVersion(ctx, db); err != nil || v != schema.LatestVersion() {
		t.Fatalf("fixture: main cursor = %d (err %v), want latest %d", v, err, schema.LatestVersion())
	}

	if _, err := schema.MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp over a legacy wisp_dependencies at ignored cursor %d: %v",
			issue6092IgnoredCursorBefore0005, err)
	}
	issue6092RequireCursorsAtLatest(t, ctx, db)

	got := issue6092ReadShape(t, ctx, db)
	if _, ok := got.columns["depends_on_id"]; ok {
		t.Error("depends_on_id survived the upgrade")
	}
	issue6092RequireFinalShape(t, got, final)
	issue6092RequireDepsPreserved(t, issue6092ReadDeps(t, ctx, db), issue6092ValidDeps)
	issue6092RequireReferencedUnchanged(t, ctx, db)
}
