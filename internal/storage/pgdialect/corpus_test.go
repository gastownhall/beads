package pgdialect_test

// This file is the PREPARE gate the package doc promises. It enumerates every
// distinct statement family bd's shared layer (sqlbuild + issueops) emits on
// the read/search path, runs each through pgdialect.Translate, and — when a
// live Postgres is configured via BEADS_PG_TEST_URL — PREPAREs the translated
// text against a scratch schema. An untranslatable construct fails the test
// loudly (Translate error, surviving MySQL residue, or a PREPARE-time Postgres
// error) instead of silently mistranslating a row set.
//
// It is an external test package (pgdialect_test) so it can live alongside the
// white-box translate_test.go while only touching pgdialect's exported surface.
//
// The scratch schema is built here rather than via postgres.InitSchema on
// purpose: pgdialect is a lower layer than the postgres backend (postgres
// imports pgdialect), so importing postgres into pgdialect's tests would invert
// the dependency and couple this gate to that package's build state. The tables
// are generated from sqlbuild.IssueSelectColumns so they cannot drift out of
// sync with the column list the queries project. Only the shapes the corpus
// exercises are created; types are faithful where an operator depends on them
// (jsonb metadata, integer priority, smallint flags, timestamp columns) and
// text elsewhere.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/pgdialect"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

type corpusStmt struct {
	name string
	sql  string
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// searchSelectSQL mirrors issueops.searchTableInTx Pattern A: the full-column
// filtered scan for one table family (incl. the label-driven JOIN plan).
func searchSelectSQL(tables sqlbuild.FilterTables, query string, filter types.IssueFilter) (string, error) {
	plan := sqlbuild.BuildLabelDrivenSearch(filter, tables)
	where, _, err := sqlbuild.BuildIssueFilterClauses(query, plan.Filter, tables)
	if err != nil {
		return "", err
	}
	where, _ = plan.MergeInto(where, nil)
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	selectKw := "SELECT "
	if plan.Distinct {
		selectKw = "SELECT DISTINCT "
	}
	return fmt.Sprintf("%s%s FROM %s %s %s",
		selectKw, sqlbuild.IssueSelectColumns, plan.FromSQL, whereSQL,
		sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "")), nil
}

// searchCountsSQL mirrors issueops.runFilterSearchQueryInTx: the counts
// mega-query (the DATE_FORMAT / JSON_OBJECT / CAST / JSON_ARRAYAGG surface).
func searchCountsSQL(tables sqlbuild.FilterTables, query string, filter types.IssueFilter, includeWispReverseDeps bool) (string, error) {
	where, _, err := sqlbuild.BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return "", err
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	sql, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i"), "", includeWispReverseDeps, filter.SkipLabels)
	return sql, nil
}

// readySelectSQL mirrors issueops.GetReadyWorkInTx: SELECT id FROM <main> with
// the ready-work WHERE/ORDER.
func readySelectSQL(tables sqlbuild.FilterTables, filter types.WorkFilter) (string, error) {
	whereSQL, _, err := sqlbuild.BuildReadyWorkWhere(filter, tables, sqlbuild.ReadyWorkWhereInputs{})
	if err != nil {
		return "", err
	}
	order := sqlbuild.BuildReadyWorkOrder(filter.SortPolicy, "created_at", "priority")
	return fmt.Sprintf("SELECT id FROM %s %s %s", tables.Main, whereSQL, order.SQL), nil
}

// readyCountsSQL mirrors issueops.GetReadyWorkWithCountsInTx: the counts
// mega-query fed the ready-work predicates.
func readyCountsSQL(tables sqlbuild.FilterTables, filter types.WorkFilter, includeWispReverseDeps bool) (string, error) {
	whereSQL, _, err := sqlbuild.BuildReadyWorkWhere(filter, tables, sqlbuild.ReadyWorkWhereInputs{})
	if err != nil {
		return "", err
	}
	order := sqlbuild.BuildReadyWorkOrder(filter.SortPolicy, "created_at", "priority")
	sql, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, order.SQL, "", includeWispReverseDeps, false)
	return sql, nil
}

// adaptiveIDInstrSQL is issueops.GetAdaptiveIDLengthTx's create-path query
// (CONCAT + INSTR + SUBSTRING). Reproduced verbatim so the corpus covers the
// non-search statement family the smoke test's explicit-ID creates never hit.
const adaptiveIDInstrSQL = `SELECT COUNT(*)
	FROM issues
	WHERE id LIKE CONCAT(?, '-%')
	  AND INSTR(SUBSTRING(id, LENGTH(?) + 2), '.') = 0`

func buildCorpus(t *testing.T) []corpusStmt {
	t.Helper()

	// Representative filters: priority, an AND label, metadata has-key + eq,
	// and a parent filter (each drives a distinct clause shape).
	richIssue := types.IssueFilter{
		Priority:       intp(1),
		Labels:         []string{"backlog"},
		HasMetadataKey: "sprint",
		MetadataFields: map[string]string{"team": "core"},
		ParentID:       strp("bd-1"),
		SortBy:         "priority",
	}
	richWork := types.WorkFilter{
		Status:         types.Status("open"),
		Priority:       intp(1),
		Labels:         []string{"backlog"},
		HasMetadataKey: "sprint",
		MetadataFields: map[string]string{"team": "core"},
		ParentID:       strp("bd-1"),
	}
	metaOnly := types.IssueFilter{HasMetadataKey: "sprint", MetadataFields: map[string]string{"team": "core"}}

	var corpus []corpusStmt
	add := func(name, sqlText string, err error) {
		if err != nil {
			t.Fatalf("build corpus %q: %v", name, err)
		}
		corpus = append(corpus, corpusStmt{name: name, sql: sqlText})
	}

	s, err := searchSelectSQL(sqlbuild.IssuesFilterTables, "", types.IssueFilter{})
	add("search/issues/plain", s, err)
	s, err = searchSelectSQL(sqlbuild.IssuesFilterTables, "bd-1", richIssue)
	add("search/issues/rich", s, err)
	s, err = searchSelectSQL(sqlbuild.WispsFilterTables, "", richIssue)
	add("search/wisps/rich", s, err)
	s, err = searchSelectSQL(sqlbuild.IssuesFilterTables, "", metaOnly)
	add("search/issues/metadata-only", s, err)

	s, err = searchCountsSQL(sqlbuild.IssuesFilterTables, "", types.IssueFilter{}, false)
	add("counts/issues/plain", s, err)
	s, err = searchCountsSQL(sqlbuild.IssuesFilterTables, "", richIssue, true)
	add("counts/issues/rich", s, err)
	s, err = searchCountsSQL(sqlbuild.WispsFilterTables, "", richIssue, true)
	add("counts/wisps/rich", s, err)

	s, err = readySelectSQL(sqlbuild.IssuesFilterTables, richWork)
	add("ready/issues", s, err)
	s, err = readySelectSQL(sqlbuild.WispsFilterTables, richWork)
	add("ready/wisps", s, err)
	s, err = readyCountsSQL(sqlbuild.IssuesFilterTables, richWork, true)
	add("ready-counts/issues", s, err)
	s, err = readyCountsSQL(sqlbuild.WispsFilterTables, richWork, true)
	add("ready-counts/wisps", s, err)

	corpus = append(corpus,
		corpusStmt{"adaptive-id/instr", adaptiveIDInstrSQL},
		// Literal-path gate check (issueops/blocked_state.go): keeps the literal
		// JSON rule covered alongside the dynamic one.
		corpusStmt{"metadata/literal-gate", `SELECT id FROM issues WHERE JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.gate')) = ?`},
		// Dep JSON aggregation in isolation (DATE_FORMAT + JSON_OBJECT + CAST).
		corpusStmt{"dep-json/arrayagg", fmt.Sprintf(`SELECT issue_id, JSON_ARRAYAGG(%s) AS deps_json FROM dependencies GROUP BY issue_id`, sqlbuild.DepJSONObject)},
	)
	return corpus
}

// forbiddenResidue are lowercased markers of MySQL constructs that must never
// survive translation. JSON_ARRAYAGG is intentionally absent: Postgres 16
// supports SQL-standard JSON_ARRAYAGG natively, so the wedge leaves it in place.
var forbiddenResidue = []string{
	"date_format(", "json_object(", "json_unquote(", "json_extract(",
	" as char", "cast(", "insert ignore", "replace into", "on duplicate",
	"utc_timestamp", "now()", "`", "?",
}

func assertNoResidue(t *testing.T, name, translated string) {
	t.Helper()
	low := strings.ToLower(translated)
	for _, marker := range forbiddenResidue {
		if strings.Contains(low, marker) {
			t.Errorf("%s: untranslated MySQL residue %q survived:\n%s", name, strings.TrimSpace(marker), translated)
		}
	}
}

// TestCorpusTranslates runs with no database: it proves every statement family
// translates without error and leaves no MySQL residue. The fail-loud property
// is a build-time guarantee, so this half of the gate always runs.
func TestCorpusTranslates(t *testing.T) {
	for _, c := range buildCorpus(t) {
		translated, err := pgdialect.Translate(c.sql)
		if err != nil {
			t.Errorf("%s: Translate error: %v\n-- sql:\n%s", c.name, err, c.sql)
			continue
		}
		assertNoResidue(t, c.name, translated)
	}
}

// TestCorpusPrepare is the live half: PREPARE every translated statement against
// a real Postgres so a construct that is syntactically valid but references a
// missing column/function/operator still fails loudly. Gated on BEADS_PG_TEST_URL.
func TestCorpusPrepare(t *testing.T) {
	dsn := os.Getenv("BEADS_PG_TEST_URL")
	if dsn == "" {
		t.Skip("BEADS_PG_TEST_URL not set; skipping live Postgres PREPARE gate")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("corpus_%d", time.Now().UnixNano())

	db, err := pgdialect.Open(dsn, schema)
	if err != nil {
		t.Fatalf("pgdialect.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
		_ = db.Close()
	})
	if err := createScratchSchema(ctx, db, schema); err != nil {
		t.Fatalf("create scratch schema: %v", err)
	}

	for _, c := range buildCorpus(t) {
		t.Run(c.name, func(t *testing.T) {
			translated, err := pgdialect.Translate(c.sql)
			if err != nil {
				t.Fatalf("Translate error: %v\n-- sql:\n%s", err, c.sql)
			}
			assertNoResidue(t, c.name, translated)
			// PREPARE the RAW statement through the translating driver — the exact
			// production path (driver.Translate then server Parse).
			stmt, err := db.PrepareContext(ctx, c.sql)
			if err != nil {
				t.Fatalf("PREPARE failed: %v\n-- translated:\n%s", err, translated)
			}
			_ = stmt.Close()
		})
	}
}

// createScratchSchema builds just enough of the wedge schema for the corpus to
// PREPARE against: the issues/wisps families (generated from IssueSelectColumns
// so they track the projected column list), their label/dependency/comment
// side tables, and the bd_mysql_jsonkey helper the dynamic-path rewrite calls
// (in production schema.go/InitSchema owns this function).
func createScratchSchema(ctx context.Context, db *sql.DB, schema string) error {
	stmts := []string{
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schema),
		issueTableDDL("issues"),
		issueTableDDL("wisps"),
		`CREATE TABLE labels (issue_id text, label text)`,
		`CREATE TABLE wisp_labels (issue_id text, label text)`,
		depTableDDL("dependencies"),
		depTableDDL("wisp_dependencies"),
		`CREATE TABLE comments (issue_id text)`,
		`CREATE TABLE wisp_comments (issue_id text)`,
		`CREATE OR REPLACE FUNCTION bd_mysql_jsonkey(p text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
			SELECT CASE WHEN p LIKE '$."%"' THEN substring(p from 4 for length(p)-4) ELSE substring(p from 3) END
		$$`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(s), err)
		}
	}
	return nil
}

// issueTableDDL renders a CREATE TABLE for the issues/wisps family from
// IssueSelectColumns plus the derived is_blocked column the ready-work WHERE
// reads. Column types are faithful where an operator depends on them.
func issueTableDDL(table string) string {
	var defs []string
	for _, name := range splitColumnList(sqlbuild.IssueSelectColumns) {
		defs = append(defs, name+" "+pgColumnType(name))
	}
	defs = append(defs, "is_blocked smallint NOT NULL DEFAULT 0")
	return fmt.Sprintf("CREATE TABLE %s (%s)", table, strings.Join(defs, ", "))
}

func depTableDDL(table string) string {
	return fmt.Sprintf(`CREATE TABLE %s (
		issue_id text, depends_on_issue_id text, depends_on_wisp_id text,
		depends_on_external text, type text, created_by text,
		created_at timestamp(0), thread_id text, metadata jsonb)`, table)
}

func pgColumnType(name string) string {
	switch name {
	case "metadata":
		return "jsonb"
	case "created_at", "updated_at", "started_at", "closed_at", "compacted_at", "due_at", "defer_until":
		return "timestamp(0)"
	case "priority", "estimated_minutes", "compaction_level", "original_size", "timeout_ns":
		return "integer"
	case "pinned", "ephemeral", "is_template", "no_history":
		return "smallint"
	default:
		return "text"
	}
}

// splitColumnList splits a select-column constant into bare column names.
func splitColumnList(cols string) []string {
	raw := strings.NewReplacer("\n", " ", "\t", " ").Replace(cols)
	var names []string
	for _, p := range strings.Split(raw, ",") {
		if n := strings.TrimSpace(p); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestTranslateNewRules pins the four red-team translator rules (DATE_FORMAT,
// JSON_OBJECT comma form, CAST AS CHAR, dynamic ?-path JSON metadata), including
// their fail-loud (left-untranslated) branches.
func TestTranslateNewRules(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "DATE_FORMAT ISO8601 -> to_char",
			in:   `SELECT DATE_FORMAT(created_at, '%Y-%m-%dT%H:%i:%sZ') FROM issues`,
			want: `SELECT to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM issues`,
		},
		{
			name: "DATE_FORMAT unknown format left loud",
			in:   `SELECT DATE_FORMAT(created_at, '%Y-%m-%d') FROM issues`,
			want: `SELECT DATE_FORMAT(created_at, '%Y-%m-%d') FROM issues`,
		},
		{
			name: "JSON_OBJECT even -> jsonb_build_object",
			in:   `SELECT JSON_OBJECT('a', x, 'b', y) FROM t`,
			want: `SELECT jsonb_build_object('a', x, 'b', y) FROM t`,
		},
		{
			name: "JSON_OBJECT odd arg count left loud",
			in:   `SELECT JSON_OBJECT('a', x, 'b') FROM t`,
			want: `SELECT JSON_OBJECT('a', x, 'b') FROM t`,
		},
		{
			name: "CAST AS CHAR -> ::text",
			in:   `SELECT CAST(metadata AS CHAR) FROM t`,
			want: `SELECT (metadata)::text FROM t`,
		},
		{
			name: "CAST AS CHAR(n) -> ::text",
			in:   `SELECT CAST(metadata AS CHAR(64)) FROM t`,
			want: `SELECT (metadata)::text FROM t`,
		},
		{
			name: "CAST AS SIGNED left loud",
			in:   `SELECT CAST(x AS SIGNED) FROM t`,
			want: `SELECT CAST(x AS SIGNED) FROM t`,
		},
		{
			name: "dynamic has-key JSON_EXTRACT -> -> bd_mysql_jsonkey",
			in:   `SELECT id FROM issues WHERE JSON_EXTRACT(metadata, ?) IS NOT NULL`,
			want: `SELECT id FROM issues WHERE (metadata -> bd_mysql_jsonkey($1)) IS NOT NULL`,
		},
		{
			name: "dynamic eq JSON_UNQUOTE(JSON_EXTRACT) -> ->> bd_mysql_jsonkey",
			in:   `SELECT id FROM issues WHERE JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?`,
			want: `SELECT id FROM issues WHERE (metadata ->> bd_mysql_jsonkey($1)) = $2`,
		},
		{
			name: "literal path rule still applies (regression)",
			in:   `SELECT 1 WHERE JSON_UNQUOTE(JSON_EXTRACT(d.metadata, '$.gate')) = 'any-children'`,
			want: `SELECT 1 WHERE (d.metadata #>> '{gate}') = 'any-children'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pgdialect.Translate(tc.in)
			if err != nil {
				t.Fatalf("Translate error: %v", err)
			}
			if got != tc.want {
				t.Errorf("mismatch\n in:   %s\n got:  %s\n want: %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestTranslateDepJSONObject exercises the real counts.go DepJSONObject constant
// (DATE_FORMAT + JSON_OBJECT + CAST composed) end to end.
func TestTranslateDepJSONObject(t *testing.T) {
	got, err := pgdialect.Translate(sqlbuild.DepJSONObject)
	if err != nil {
		t.Fatalf("Translate error: %v", err)
	}
	for _, want := range []string{
		"jsonb_build_object(",
		`to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`,
		"(metadata)::text",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DepJSONObject translation missing %q:\n%s", want, got)
		}
	}
	assertNoResidue(t, "DepJSONObject", got)
}
