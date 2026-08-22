package bench_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/suite"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
	"github.com/steveyegge/beads/internal/testutil"
)

// harnessSuite seeds a real Dolt database once per suite and resets to a
// baseline commit between tests, mirroring internal/storage/domain/db's
// suite_test.go pattern rather than reinventing DB lifecycle management.
//
// KNOWN ENVIRONMENT GAP: this suite is gated by testutil.RequireDoltContainer
// and skips cleanly wherever Docker/testcontainers cannot pull the pinned
// Dolt image (as in this sandbox). It has not been run end-to-end against a
// live server; see be-qm6fb's handoff notes.
type harnessSuite struct {
	suite.Suite
	db             *sql.DB
	dbName         string
	baselineCommit string
	eventsDDL      string
}

func (s *harnessSuite) SetupSuite() {
	testutil.RequireDoltContainer(s.T())

	port := testutil.DoltContainerPortInt()
	s.Require().NotZero(port, "test container port must be set")

	ctx := context.Background()

	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	root, err := sql.Open("mysql", rootDSN)
	s.Require().NoError(err)
	defer root.Close()

	s.dbName = "beads_sqlbuild_bench_" + randomSuffix(8)
	_, err = root.ExecContext(ctx, "CREATE DATABASE `"+s.dbName+"`")
	s.Require().NoError(err)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root", Database: s.dbName}.String()
	db, err := sql.Open("mysql", dsn)
	s.Require().NoError(err)
	s.Require().NoError(db.PingContext(ctx))
	s.db = db

	var version string
	s.Require().NoError(db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version), "capture served Dolt version")
	s.T().Logf("served Dolt version: %s (pinned image: %s)", version, testutil.DoltDockerImage)

	_, err = schema.MigrateUp(ctx, db)
	s.Require().NoError(err, "applying beads schema")

	_, err = db.ExecContext(ctx, "CALL DOLT_ADD('-A')")
	s.Require().NoError(err, "dolt add baseline")
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--allow-empty')", "sqlbuild bench harness baseline")
	s.Require().NoError(err, "dolt commit baseline")
	s.Require().NoError(
		db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&s.baselineCommit),
		"capture baseline commit hash",
	)

	var tblName string
	s.Require().NoError(
		db.QueryRowContext(ctx, "SHOW CREATE TABLE events").Scan(&tblName, &s.eventsDDL),
		"capture events DDL (events is dolt_ignored, must be recreated after DOLT_RESET)",
	)

	seedPlaneCorpus(ctx, s.T(), db, issuesPlane)
	seedPlaneCorpus(ctx, s.T(), db, wispsPlane)

	_, err = db.ExecContext(ctx, "CALL DOLT_ADD('-A')")
	s.Require().NoError(err, "dolt add seeded corpus")
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--allow-empty')", "sqlbuild bench harness seeded corpus")
	s.Require().NoError(err, "dolt commit seeded corpus")
	s.Require().NoError(
		db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&s.baselineCommit),
		"capture seeded baseline commit hash",
	)
}

func (s *harnessSuite) TearDownSuite() {
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if s.dbName == "" {
		return
	}
	port := testutil.DoltContainerPortInt()
	if port == 0 {
		return
	}
	rootDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	root, err := sql.Open("mysql", rootDSN)
	if err != nil {
		return
	}
	defer root.Close()
	_, _ = root.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+s.dbName+"`")
}

func (s *harnessSuite) SetupTest() {
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, "CALL DOLT_RESET('--hard', ?)", s.baselineCommit)
	s.Require().NoError(err, "reset to baseline %s", s.baselineCommit)
	_, err = s.db.ExecContext(ctx, "DROP TABLE IF EXISTS events")
	s.Require().NoError(err, "drop events after reset")
	_, err = s.db.ExecContext(ctx, s.eventsDDL)
	s.Require().NoError(err, "recreate events after reset")
}

func TestSearchCountsHarness(t *testing.T) {
	suite.Run(t, &harnessSuite{})
}

// TestNullSelfCheck_IssuesPlane and TestNullSelfCheck_WispsPlane register
// MainShape() under two different names and assert CompareRowSets reports no
// diffs between them — proof the harness's comparison machinery is sound
// (two runs of the *same* shape must always agree) without requiring a
// second real candidate SQL shape, which is out of scope for this bead (see
// be-qm6fb's no-CTE-revival note).
func (s *harnessSuite) TestNullSelfCheck_IssuesPlane() {
	s.runNullSelfCheck(issuesPlane)
}

func (s *harnessSuite) TestNullSelfCheck_WispsPlane() {
	s.runNullSelfCheck(wispsPlane)
}

func (s *harnessSuite) runNullSelfCheck(plane planeConfig) {
	ctx := context.Background()
	t := s.T()

	shapeA := bench.Shape{Name: "main", Render: bench.MainShape().Render}
	shapeB := bench.Shape{Name: "main-again", Render: bench.MainShape().Render}

	whereSQL := fmt.Sprintf("WHERE i.assignee = %s", quoteLiteral(seedNarrowAssignee))
	hyd := sqlbuild.CountsHydration{Lite: true}

	rowsA := s.runShape(ctx, plane, shapeA, whereSQL, hyd)
	rowsB := s.runShape(ctx, plane, shapeB, whereSQL, hyd)

	s.Require().Len(rowsA, seedNarrowCount, "narrow WHERE clause should isolate exactly the tagged subset")

	diffs := bench.CompareRowSets(rowsA, rowsB)
	if len(diffs) != 0 {
		t.Fatalf("null self-check found diffs between two runs of the same shape (%s plane): %v", plane.name, diffs)
	}

	// 8 alternating rounds matches the scale the PR #5339 review used to
	// trust its own measurement (see be-qm6fb).
	results, err := bench.RunAlternating(8, []bench.Shape{shapeA, shapeB}, func(shape bench.Shape, round int) (time.Duration, error) {
		start := time.Now()
		s.runShape(ctx, plane, shape, whereSQL, hyd)
		return time.Since(start), nil
	})
	s.Require().NoError(err)
	for _, rr := range results {
		t.Logf("%s plane, shape %s, round %d: %v", plane.name, rr.Shape, rr.Round, rr.Duration)
	}
	stats := bench.Summarize(results)
	for _, st := range stats {
		t.Logf("%s plane, shape %s summary: n=%d min=%v max=%v mean=%v spread=%v", plane.name, st.Shape, st.N, st.Min, st.Max, st.Mean, st.Spread)
	}
}

// TestExplainReferenceCount_Reported exercises PlanReferenceCount against a
// real EXPLAIN plan from the live server, confirming the plumbing from
// rendered SQL through to a reference count actually works end-to-end.
func (s *harnessSuite) TestExplainReferenceCount_Reported() {
	ctx := context.Background()
	t := s.T()

	whereSQL := fmt.Sprintf("WHERE i.assignee = %s", quoteLiteral(seedNarrowAssignee))
	sqlText, args := bench.MainShape().Render(issuesPlane.tables, nil, whereSQL, "", "", true, sqlbuild.CountsHydration{Lite: true})

	// FORMAT=TREE (not the classic tabular EXPLAIN): the tabular form's
	// numeric "rows" estimate column comes back from this server as the
	// literal text "NULL" for this query shape, rather than a real SQL NULL,
	// which the driver's row decoder rejects (strconv.ParseUint) before
	// Scan ever runs. TREE format returns a single text "plan" column, no
	// numeric columns at all, sidestepping that decode failure entirely.
	rows, err := s.db.QueryContext(ctx, "EXPLAIN FORMAT=TREE "+sqlText, args...)
	s.Require().NoError(err, "EXPLAIN main shape")
	defer rows.Close()

	var plan string
	for rows.Next() {
		var line string
		s.Require().NoError(rows.Scan(&line))
		plan += line + "\n"
	}
	s.Require().NoError(rows.Err())

	count := bench.PlanReferenceCount(plan, issuesPlane.tables.Dependencies)
	t.Logf("EXPLAIN references to %q: %d", issuesPlane.tables.Dependencies, count)
	if count < 1 {
		t.Errorf("expected at least one EXPLAIN reference to %q, got %d\nplan:\n%s", issuesPlane.tables.Dependencies, count, plan)
	}
}

// runShape renders and executes shape against plane, scanning results into
// bench.Row. It intentionally reads only the id plus the six "extra"
// mega-query columns (labels/dep/rdep/comment/parent/deps) — the rest of the
// hydration payload is shape-invariant and irrelevant to an A/B comparison.
func (s *harnessSuite) runShape(ctx context.Context, plane planeConfig, shape bench.Shape, whereSQL string, hyd sqlbuild.CountsHydration) []bench.Row {
	t := s.T()
	t.Helper()

	sqlText, args := shape.Render(plane.tables, nil, whereSQL, "ORDER BY i.id", "", plane.name == "issues", hyd)

	fullSQL, fullArgs := sqlbuild.SearchCountsSQL(plane.tables, nil, whereSQL, "ORDER BY i.id", "", plane.name == "issues", hyd)
	s.Require().Equal(fullSQL, sqlText, "shape %s must render the full mega-query so extra columns are present", shape.Name)

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	s.Require().NoError(err, "query shape %s", shape.Name)
	defer rows.Close()

	cols, err := rows.Columns()
	s.Require().NoError(err)

	var out []bench.Row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		s.Require().NoError(rows.Scan(ptrs...))

		byName := make(map[string]any, len(cols))
		for i, c := range cols {
			byName[c] = vals[i]
		}
		out = append(out, rowFromColumns(byName))
	}
	s.Require().NoError(rows.Err())
	_ = fullArgs
	return out
}

func rowFromColumns(byName map[string]any) bench.Row {
	return bench.Row{
		IssueID:      asString(byName["id"]),
		LabelsJSON:   asString(byName["labels_json"]),
		DepCount:     asInt64(byName["dep_count"]),
		RDepCount:    asInt64(byName["rdep_count"]),
		CommentCount: asInt64(byName["comment_count"]),
		ParentID:     asString(byName["parent_id"]),
		DepsJSON:     asString(byName["deps_json"]),
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int64:
		return t
	case []byte:
		var n int64
		_, _ = fmt.Sscanf(string(t), "%d", &n)
		return n
	default:
		return 0
	}
}

// quoteLiteral renders s as a single-quoted SQL string literal for embedding
// directly into whereSQL text (see ShapeFunc — whereSQL must be real SQL, not
// a placeholder, so EXPLAIN has something to plan against). It is for
// trusted, in-test literals only: doubling embedded single quotes is
// sufficient to keep the literal well-formed, but this is not a general
// escaping routine and must never be used with untrusted input.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// TestQuoteLiteral guards quoteLiteral's SQL-literal escaping. quoteLiteral
// embeds its argument directly into whereSQL text passed to ShapeFunc.Render
// (required so EXPLAIN gets real SQL, not a placeholder) rather than binding
// it as a driver arg, so an unescaped embedded quote would corrupt the
// rendered statement. Both call sites currently pass the fixed constant
// seedNarrowAssignee, which contains no quote character, so this is not
// exploitable today — this test guards against that changing silently.
func TestQuoteLiteral(t *testing.T) {
	got := quoteLiteral("bench's-target")
	want := "'bench''s-target'"
	if got != want {
		t.Errorf("quoteLiteral(%q) = %q, want %q (embedded single quotes must be doubled per SQL literal-escaping convention)", "bench's-target", got, want)
	}
}

var suffixLetters = []rune("abcdefghijklmnopqrstuvwxyz")

func randomSuffix(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = suffixLetters[rand.Intn(len(suffixLetters))]
	}
	return string(b)
}
