package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// A numbered main migration step writes its SQL and its cursor row
// (runMigrations) and only then commits (commitMigrationStep). A pass killed
// between the two leaves that one step in the working set. The functions in
// this file let the next pass recognize such a step and commit it with the
// commit it missed, before the pre-existing-dirty-table guard would refuse it
// as user dirt (or, when no later migration touches its tables, before a later
// step's commit records its cursor row without its change).

// strandedMainStep reports the main-series step an interrupted pass applied
// and recorded but did not commit, when the working set holds exactly one.
// It is read-only. It reads the cursor table's status first and declines
// (ok=false) unless the table is modified in place: a new table is a fresh
// bootstrap, and a deleted or renamed one cannot be read AS OF HEAD without a
// failing statement that would pin the session's catalog (be-bv7x). It also
// declines when HEAD's cursor table has no content_hash column (the dead
// pass's own first step added it), since HEAD's rows then cannot be compared.
// The remaining conditions are classifyStrandedStep's. A read error is
// returned; only a shape mismatch declines.
func strandedMainStep(ctx context.Context, db DBConn) (mf migrationFile, ok bool, err error) {
	src := mainSource
	modified, err := cursorTableModifiedOnly(ctx, db, src.cursorTable)
	if err != nil || !modified {
		return migrationFile{}, false, err
	}
	headHasHash, err := headTableHasColumn(ctx, db, src.cursorTable, "content_hash")
	if err != nil || !headHasHash {
		return migrationFile{}, false, err
	}
	workingHasHash, err := src.hasContentHashColumn(ctx, db)
	if err != nil || !workingHasHash {
		return migrationFile{}, false, err
	}
	head, err := readCursorRows(ctx, db, "SELECT version, content_hash FROM "+src.cursorTable+" AS OF 'HEAD'")
	if err != nil {
		return migrationFile{}, false, fmt.Errorf("reading %s at HEAD: %w", src.cursorTable, err)
	}
	working, err := readCursorRows(ctx, db, "SELECT version, content_hash FROM "+src.cursorTable)
	if err != nil {
		return migrationFile{}, false, fmt.Errorf("reading %s: %w", src.cursorTable, err)
	}
	return classifyStrandedStep(head, working, src.list(), src.fileContentHash)
}

// classifyStrandedStep is strandedMainStep's DB-free core. It recognizes a
// stranded step only when all of these hold:
//
//   - the working cursor rows are HEAD's rows plus exactly one version v, and
//     every row already at HEAD keeps its content_hash (NULL-safe);
//   - v is the first version in series above HEAD's highest: a pass that
//     commits each step before it starts the next can strand no other;
//   - v's content_hash is non-NULL and equals fileHash of v's frozen file,
//     the value runMigrations records and nothing else writes.
//
// series must be sorted by version, as migrationSource.list returns it.
func classifyStrandedStep(head, working map[int]sql.NullString, series []migrationFile, fileHash func(migrationFile) (string, error)) (migrationFile, bool, error) {
	if len(working) != len(head)+1 {
		return migrationFile{}, false, nil
	}
	headMax := 0
	for version, hash := range head {
		got, ok := working[version]
		if !ok || got != hash {
			return migrationFile{}, false, nil
		}
		headMax = max(headMax, version)
	}
	added, found := 0, false
	for version := range working {
		if _, atHead := head[version]; !atHead {
			added, found = version, true
		}
	}
	if !found {
		return migrationFile{}, false, nil
	}
	var next migrationFile
	for _, mf := range series {
		if mf.version > headMax {
			next = mf
			break
		}
	}
	if next.name == "" || next.version != added {
		return migrationFile{}, false, nil
	}
	recorded := working[added]
	if !recorded.Valid {
		return migrationFile{}, false, nil
	}
	want, err := fileHash(next)
	if err != nil {
		return migrationFile{}, false, err
	}
	if recorded.String != want {
		return migrationFile{}, false, nil
	}
	return next, true, nil
}

// fileContentHash is the content_hash runMigrations records for mf: the hex
// SHA-256 of its frozen file bytes.
func (m migrationSource) fileContentHash(mf migrationFile) (string, error) {
	data, err := m.files.ReadFile(m.dir + "/" + mf.name)
	if err != nil {
		return "", fmt.Errorf("reading migration %s: %w", mf.name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// stepTexts returns the SQL migration mf of m runs: its frozen file and, when
// a body plan is registered for it, the body runMigrations actually executes.
func (m migrationSource) stepTexts(mf migrationFile) ([]string, error) {
	data, err := m.files.ReadFile(m.dir + "/" + mf.name)
	if err != nil {
		return nil, fmt.Errorf("reading migration %s: %w", mf.name, err)
	}
	texts := []string{string(data)}
	if _, planned := migrationBodyPlans[repairKey{m.cursorTable, mf.version}]; !planned {
		return texts, nil
	}
	body, err := m.plannedMigrationBody(mf.version, mf.name, data)
	if err != nil {
		return nil, fmt.Errorf("planning migration %s: %w", mf.name, err)
	}
	return append(texts, body), nil
}

// stepTouchesTable reports whether migration mf of m writes table by its own
// statements (stepSQLTouchesTable over stepTexts). Registered pre-repairs
// need no list of their own; TestStrandedStepPreRepairsNeedNoTouchedTables
// keeps that true. strandedStepTables adds the tables a row delete reaches
// through a foreign key, which no statement names.
func (m migrationSource) stepTouchesTable(mf migrationFile, table string) (bool, error) {
	texts, err := m.stepTexts(mf)
	if err != nil {
		return false, err
	}
	for _, text := range texts {
		if stepSQLTouchesTable(text, table) {
			return true, nil
		}
	}
	return false, nil
}

// stepSQLTouchesTable is the dirty-table guard's text match
// (migrationSQLTouchesTable) widened by the two writes whose statements do
// not name the table they change: view, trigger and event DDL, which Dolt
// stores in dolt_schemas (0017, 0018, 0025, 0026, 0044, 0045), and a
// multi-table DELETE, whose targets are aliases (0035 deletes comments,
// events, dependencies and labels this way). Only the finish uses it; the
// guard keeps its own match. TestEmbeddedMigrateStrandedStepTablesCoverEveryStepCommit
// holds it, with the foreign-key reach, to every step commit an uninterrupted
// pass makes.
func stepSQLTouchesTable(sqlText, table string) bool {
	if migrationSQLTouchesTable(sqlText, table) || multiTableDeleteNamesTable(sqlText, table) {
		return true
	}
	return table == doltSchemasTable && schemaObjectDDLRe.MatchString(sqlText)
}

// doltSchemasTable is the Dolt system table that stores views, triggers and
// events.
const doltSchemasTable = "dolt_schemas"

// schemaObjectDDLRe matches DDL for an object Dolt stores in dolt_schemas.
var schemaObjectDDLRe = regexp.MustCompile(`(?i)\b(?:create\s+(?:or\s+replace\s+)?|alter\s+|drop\s+)(?:view|trigger|event)\b`)

// multiTableDeleteRe captures the targets and the table references of a
// multi-table DELETE (DELETE <target>[, <target>...] FROM <references>, or a
// single-table DELETE with a modifier such as IGNORE), up to its WHERE, the
// statement's end, or the end of the string literal a PREPARE runs it from.
var multiTableDeleteRe = regexp.MustCompile(`(?is)\bdelete\s+(\w+(?:\s*,\s*\w+)*)\s+from\s+(.+?)(?:\bwhere\b|;|'|$)`)

// tableReferenceRe matches a table reference in a FROM clause: the clause's
// first word and the word after each JOIN or comma.
var tableReferenceRe = regexp.MustCompile("(?i)(?:^|\\bjoin\\s+|,\\s*)`?(\\w+)`?")

// referenceAliasRe matches the alias that follows a table reference.
var referenceAliasRe = regexp.MustCompile("(?i)^\\s+(?:as\\s+)?`?(\\w+)`?")

// notAnAlias holds the words that can follow a table reference without
// naming an alias for it.
var notAnAlias = map[string]bool{
	"join": true, "left": true, "right": true, "inner": true, "outer": true, "cross": true,
	"natural": true, "straight_join": true, "on": true, "using": true, "where": true,
}

// deleteModifiers are the words a single-table DELETE may carry before FROM.
var deleteModifiers = map[string]bool{"low_priority": true, "quick": true, "ignore": true}

// multiTableDeleteTargets returns the tables the multi-table DELETEs in
// sqlText delete rows from: each target resolved through its FROM clause to
// the table it aliases, or taken as a table name. A table a statement only
// joins is not a target (0058 deletes wisp_dependencies rows LEFT JOINed to
// issues).
func multiTableDeleteTargets(sqlText string) []string {
	var tables []string
	for _, m := range multiTableDeleteRe.FindAllStringSubmatch(sqlText, -1) {
		from := strings.TrimSpace(m[2])
		names := fromClauseNames(from)
		for _, target := range strings.Split(m[1], ",") {
			target = strings.ToLower(strings.TrimSpace(target))
			if deleteModifiers[target] {
				// DELETE IGNORE FROM t: a single-table DELETE, of its first
				// (only) table.
				if first := tableReferenceRe.FindStringSubmatch(from); first != nil {
					tables = append(tables, first[1])
				}
			} else if table, ok := names[target]; ok {
				tables = append(tables, table)
			}
		}
	}
	return tables
}

// fromClauseNames maps each name a DELETE target can give a table of FROM
// clause from, the table's own or its alias, lowercased, to the table. The
// first definition of a name wins: a later "table" match is a comma in an ON
// condition.
func fromClauseNames(from string) map[string]string {
	names := make(map[string]string)
	define := func(name, table string) {
		if _, seen := names[strings.ToLower(name)]; !seen {
			names[strings.ToLower(name)] = table
		}
	}
	for _, loc := range tableReferenceRe.FindAllStringSubmatchIndex(from, -1) {
		table := from[loc[2]:loc[3]]
		define(table, table)
		if alias := referenceAliasRe.FindStringSubmatch(from[loc[1]:]); alias != nil && !notAnAlias[strings.ToLower(alias[1])] {
			define(alias[1], table)
		}
	}
	return names
}

// multiTableDeleteNamesTable reports whether a multi-table DELETE in sqlText
// deletes rows from table.
func multiTableDeleteNamesTable(sqlText, table string) bool {
	return slices.ContainsFunc(multiTableDeleteTargets(sqlText), func(target string) bool {
		return strings.EqualFold(target, table)
	})
}

// sqlDeletesRowsFrom reports whether sqlText deletes rows of table: DELETE
// FROM table, a multi-table DELETE that targets it, or REPLACE INTO table,
// which deletes the row it replaces.
func sqlDeletesRowsFrom(sqlText, table string) bool {
	tableRef := "`?" + regexp.QuoteMeta(table) + "`?"
	return regexp.MustCompile(`(?i)\b(?:delete\s+from|replace\s+into)\s+`+tableRef+`\b`).MatchString(sqlText) ||
		multiTableDeleteNamesTable(sqlText, table)
}

// foreignKeyEdge is a foreign key of child onto parent whose ON DELETE rule
// changes child rows.
type foreignKeyEdge struct{ child, parent string }

// rowChangingForeignKeys reads the working schema's foreign keys whose
// ON DELETE rule changes child rows (CASCADE, SET NULL, SET DEFAULT). It runs
// only while a stranded step is finished, and only for a step that deletes
// rows beside dirt its statements do not name, so the INFORMATION_SCHEMA read
// costs a steady-state open nothing.
func rowChangingForeignKeys(ctx context.Context, db DBConn) ([]foreignKeyEdge, error) {
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, REFERENCED_TABLE_NAME
		FROM INFORMATION_SCHEMA.REFERENTIAL_CONSTRAINTS
		WHERE CONSTRAINT_SCHEMA = DATABASE() AND DELETE_RULE IN ('CASCADE', 'SET NULL', 'SET DEFAULT')`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var edges []foreignKeyEdge
	for rows.Next() {
		var edge foreignKeyEdge
		if err := rows.Scan(&edge.child, &edge.parent); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// cascadeReach returns every table whose rows a row delete in a table
// deletesFrom reports can change through edges, transitively: the child of a
// reached table is reached too.
func cascadeReach(edges []foreignKeyEdge, deletesFrom func(table string) bool) map[string]bool {
	reached := make(map[string]bool)
	var frontier []string
	for _, edge := range edges {
		if deletesFrom(edge.parent) && !reached[edge.child] {
			reached[edge.child] = true
			frontier = append(frontier, edge.child)
		}
	}
	for len(frontier) > 0 {
		parent := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		for _, edge := range edges {
			if edge.parent == parent && !reached[edge.child] {
				reached[edge.child] = true
				frontier = append(frontier, edge.child)
			}
		}
	}
	return reached
}

// strandedStepTables splits tables, the dirty tables beside the cursor table,
// into the ones stranded step mf changed (staged, sorted) and the rest. A
// table is the step's when one of its statements writes it
// (stepTouchesTable) or when a row it deletes reaches the table through a
// foreign key: 0035 deletes issues rows, and ON DELETE CASCADE removes their
// child_counters and snapshot rows, tables 0035 never names. The foreign keys
// are read from db's working schema, the schema the step left.
func (m migrationSource) strandedStepTables(ctx context.Context, db DBConn, mf migrationFile, tables []string) (staged, rest []string, err error) {
	for _, table := range tables {
		touched, err := m.stepTouchesTable(mf, table)
		if err != nil {
			return nil, nil, err
		}
		if touched {
			staged = append(staged, table)
		} else {
			rest = append(rest, table)
		}
	}
	if len(rest) > 0 {
		var cascaded []string
		cascaded, rest, err = m.cascadedTables(ctx, db, mf, rest)
		if err != nil {
			return nil, nil, err
		}
		staged = append(staged, cascaded...)
	}
	sort.Strings(staged)
	return staged, rest, nil
}

// cascadedTables splits tables into the ones a row delete of step mf reaches
// through a foreign key and the rest. A step that deletes no rows reads
// nothing.
func (m migrationSource) cascadedTables(ctx context.Context, db DBConn, mf migrationFile, tables []string) (reached, rest []string, err error) {
	texts, err := m.stepTexts(mf)
	if err != nil {
		return nil, nil, err
	}
	if !slices.ContainsFunc(texts, anyRowDeleteRe.MatchString) {
		return nil, tables, nil
	}
	edges, err := rowChangingForeignKeys(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("reading foreign keys for migration %s: %w", mf.name, err)
	}
	reach := cascadeReach(edges, func(table string) bool {
		return slices.ContainsFunc(texts, func(text string) bool { return sqlDeletesRowsFrom(text, table) })
	})
	for _, table := range tables {
		if reach[table] {
			reached = append(reached, table)
		} else {
			rest = append(rest, table)
		}
	}
	return reached, rest, nil
}

// anyRowDeleteRe matches a statement in any form sqlDeletesRowsFrom
// recognizes.
var anyRowDeleteRe = regexp.MustCompile(`(?i)\b(?:delete\s+(?:\w+(?:\s*,\s*\w+)*\s+)?from|replace\s+into)\s`)

// StrandedStepTablesForTest returns which of tables a finish of stranded main
// step version commits with it, reading foreign keys from db's working
// schema. Test-only: the embedded tier checks it against every step commit
// an uninterrupted pass makes, right after that commit.
func StrandedStepTablesForTest(ctx context.Context, db DBConn, version int, tables []string) ([]string, error) {
	for _, mf := range mainSource.list() {
		if mf.version == version {
			staged, _, err := mainSource.strandedStepTables(ctx, db, mf, tables)
			return staged, err
		}
	}
	return nil, fmt.Errorf("no main migration %d", version)
}

// finishStrandedMainStep commits the step strandedMainStep recognizes, if
// any, through commitMigrationStep under the step's own message: the cursor
// table and every committable dirty table the step changed
// (strandedStepTables) are staged by name, and every other dirty table stays
// in the working set for the guard to judge as it would at any other open.
// Recognition is by table, not by row, so an uncommitted write that landed in
// one of the step's tables after the kill is committed with the step.
//
// cursorDirty is whether the main cursor table was in the pass's dirty set;
// when it was not, nothing is read. A commit error carrying an encoding-drift
// signature comes back as a *SchemaEncodingDriftError naming the step, as a
// step commit's does in runMigrations; any other error aborts the pass.
func finishStrandedMainStep(ctx context.Context, db DBConn, cursorDirty bool) (finished bool, err error) {
	if !cursorDirty {
		return false, nil
	}
	mf, ok, err := strandedMainStep(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading stranded migration state: %w", err)
	}
	if !ok {
		return false, nil
	}
	defer func() { err = classifyMigrationDrift(err, mainSource, mf) }()

	dirty, err := dirtyTables(ctx, db, true)
	if err != nil {
		return false, fmt.Errorf("reading dirty tables before finishing migration %s: %w", mf.name, err)
	}
	delete(dirty, mainSource.cursorTable)
	stepTables, rest, err := mainSource.strandedStepTables(ctx, db, mf, sortedDirtyTableNames(dirty))
	if err != nil {
		return false, err
	}
	keep := make(map[string]dirtyTableState, len(rest))
	for _, table := range rest {
		keep[table] = dirty[table]
	}
	staged := append([]string{mainSource.cursorTable}, stepTables...)
	sort.Strings(staged)
	if err := commitMigrationStep(ctx, db, mainSource.cursorTable, mf.name, keep); err != nil {
		return false, fmt.Errorf("committing stranded migration %s: %w", mf.name, err)
	}
	log.Printf("schema migration: committed %s, which an interrupted migration pass applied but did not commit (tables: %s)",
		mf.name, strings.Join(staged, ", "))
	return true, nil
}

// cursorTableModifiedOnly reports whether table appears in dolt_status only
// as modified.
func cursorTableModifiedOnly(ctx context.Context, db DBConn, table string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT status FROM dolt_status WHERE table_name = ?", table)
	if err != nil {
		return false, fmt.Errorf("reading %s status: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	seen := false
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return false, fmt.Errorf("reading %s status: %w", table, err)
		}
		if status != "modified" {
			return false, nil
		}
		seen = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("reading %s status: %w", table, err)
	}
	return seen, nil
}

// headTableHasColumn reports whether table at HEAD has column. The caller
// knows table exists at HEAD (it is modified in the working set), so the
// statement cannot fail on a missing table.
func headTableHasColumn(ctx context.Context, db DBConn, table, column string) (bool, error) {
	var name, ddl string
	//nolint:gosec // G202: table is a migration-series constant.
	if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE "+table+" AS OF 'HEAD'").Scan(&name, &ddl); err != nil {
		return false, fmt.Errorf("reading %s schema at HEAD: %w", table, err)
	}
	return strings.Contains(strings.ToLower(ddl), "`"+strings.ToLower(column)+"`"), nil
}

func readCursorRows(ctx context.Context, db DBConn, query string) (map[int]sql.NullString, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int]sql.NullString)
	for rows.Next() {
		var version int
		var hash sql.NullString
		if err := rows.Scan(&version, &hash); err != nil {
			return nil, err
		}
		out[version] = hash
	}
	return out, rows.Err()
}
