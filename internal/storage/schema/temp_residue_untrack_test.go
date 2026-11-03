package schema

import (
	"bytes"
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

// expectTempListing mocks one listTempTables read of revision ('HEAD' or
// 'STAGED'), returning tables as Dolt would — LIKE's underscores are
// wildcards, so a listing may carry names the literal prefix check rejects.
func expectTempListing(mock sqlmock.Sqlmock, qualifier, revision string, tables ...string) {
	from := ""
	if qualifier != "" {
		from = " FROM " + qualifier
	}
	rows := sqlmock.NewRows([]string{"Tables_in_testdb"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES" + from + " AS OF '" + revision + "' LIKE '__temp__%'")).
		WillReturnRows(rows)
}

// expectTempResidueGate mocks the committed-__temp__ gate on a healthy
// database: one HEAD listing that finds nothing, and nothing else.
func expectTempResidueGate(mock sqlmock.Sqlmock, qualifier string) {
	expectTempListing(mock, qualifier, "HEAD")
}

// expectOpenReconcilesNoop mocks both open-time reconciles MigrateUp runs
// before its migration pass, on a healthy database.
func expectOpenReconcilesNoop(mock sqlmock.Sqlmock) {
	expectIgnoredCursorHealNoop(mock)
	expectTempResidueGate(mock, "")
}

// captureStdLog routes the standard logger, which carries the repair's
// announcement and warnings, into a buffer for the test's duration.
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// TestTempResidueUntrackSkipsAHealthyDatabaseInOneRead is the healthy-path
// contract every writable open pays: one HEAD listing that finds nothing, no
// write, no log line, and nothing counted.
func TestTempResidueUntrackSkipsAHealthyDatabaseInOneRead(t *testing.T) {
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempResidueGate(mock, "")

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair on a healthy database")
	}
	if logged.Len() > 0 {
		t.Errorf("untrackTempResidue() logged on a healthy database: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueGateIgnoresLikeWildcardLookalikes: LIKE '__temp__%' reads
// each underscore as any character, so the HEAD listing can carry a table
// that merely looks like an intermediate. It is no residue: no dolt_ignore
// read, no write.
func TestTempResidueGateIgnoresLikeWildcardLookalikes(t *testing.T) {
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempListing(mock, "", "HEAD", "abtempcdcounters")

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair for a LIKE lookalike")
	}
	if logged.Len() > 0 {
		t.Errorf("untrackTempResidue() logged for a LIKE lookalike: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// expectTempResidueUntrackStart mocks the reconcile up to, not including,
// DOLT_RM for the eligible tables, given sorted: the gate (the HEAD listing,
// then each table's dolt_ignore resolution, the seeded wildcard alone), an
// unstage that finds nothing staged, the publishing COMMIT and the staged
// listing.
func expectTempResidueUntrackStart(mock sqlmock.Sqlmock, tables ...string) {
	expectTempListing(mock, "", "HEAD", tables...)
	for _, table := range tables {
		expectIgnoreResolution(mock, "", table, []doltIgnoreRow{{pattern: "__temp__%", ignored: true}})
	}
	expectIgnoredCursorUnstage(mock)
	mock.ExpectExec(regexp.QuoteMeta("COMMIT")).WillReturnResult(sqlmock.NewResult(0, 0))
	expectTempListing(mock, "", "STAGED", tables...)
}

func expectTempResidueRM(mock sqlmock.Sqlmock, table string) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RM('--cached', ?)")).WithArgs(table)
}

func expectTempResidueCommit(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('--skip-empty', '-m', ?)")).
		WithArgs(tempResidueUntrackCommitMessage)
}

// TestTempResidueUntrackDeclinesWhenTheUnstageIsDenied: a client fenced from
// DOLT_RESET cannot scope the commit, so the reconcile declines before
// anything can move HEAD, counts nothing and lets the open go on.
func TestTempResidueUntrackDeclinesWhenTheUnstageIsDenied(t *testing.T) {
	db, mock := newMockDB(t)
	advisory := allowSwallowedAdvisory(t)
	const table = "__temp__wisp_child_counters"

	expectTempListing(mock, "", "HEAD", table)
	expectIgnoreResolution(mock, "", table, []doltIgnoreRow{{pattern: "__temp__%", ignored: true}})
	mock.ExpectQuery("(?s)SELECT s\\.table_name, s\\.staged\\s+FROM dolt_status s").
		WillReturnRows(sqlmock.NewRows([]string{"table_name", "staged"}).AddRow("config", true))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RESET(?)")).WithArgs("config").
		WillReturnError(&mysql.MySQLError{Number: 1227, Message: "Access denied; you need the SUPER privilege"})

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil: the repair is optional", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a repair that never reached DOLT_RM")
	}
	if !strings.Contains(*advisory, "cannot clear the staging area") {
		t.Errorf("advisory = %q, want it to name the declined step", *advisory)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// tempResidueAnnouncement is the stderr line a repair that ran prints.
const tempResidueAnnouncement = "untracking and keeping the rows"

// TestTempResidueUntrackDeclinesWhenDoltRMIsDenied: a client that can see the
// residue but may not CALL procedures is refused at DOLT_RM's authorization,
// before any root changes. That is a decline like any before DOLT_RM: nothing
// counted (no pool rebuild, no "Applied" from bd migrate), no repair
// announced, and the next permitted open converges.
func TestTempResidueUntrackDeclinesWhenDoltRMIsDenied(t *testing.T) {
	const table = "__temp__wisp_child_counters"
	db, mock := newMockDB(t)
	advisory := allowSwallowedAdvisory(t)
	logged := captureStdLog(t)

	expectTempResidueUntrackStart(mock, table)
	expectTempResidueRM(mock, table).WillReturnError(
		&mysql.MySQLError{Number: 1142, Message: "EXECUTE command denied to user 'fenced'@'%' for routine 'dolt_rm'"})

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil: the repair is optional", err)
	}
	if untracked {
		t.Fatal("untrackTempResidue() counted a DOLT_RM refused before any root changed")
	}
	if !strings.Contains(*advisory, "not permitted to untrack") {
		t.Errorf("advisory = %q, want it to say the client may not untrack", *advisory)
	}
	if strings.Contains(logged.String(), tempResidueAnnouncement) {
		t.Errorf("a refused DOLT_RM announced a repair: %q", logged.String())
	}

	// The next, permitted open finds the residue again and converges.
	expectTempResidueUntrackStart(mock, table)
	expectTempResidueRM(mock, table).WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectTempResidueCommit(mock).WillReturnRows(sqlmock.NewRows([]string{"hash"}))
	untracked, err = untrackTempResidue(context.Background(), db)
	if err != nil || !untracked {
		t.Fatalf("retry untrackTempResidue() = %v, %v; want true, nil", untracked, err)
	}
	if !strings.Contains(logged.String(), tempResidueAnnouncement) {
		t.Errorf("the permitted open did not announce its repair: %q", logged.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueUntrackFailuresAfterTheRemovalAreCountedNotFatal covers a
// failure once DOLT_RM was issued and not refused: failed, or removed but not
// committed. None may fail the open, each must count as possible mutation
// (HEAD may have moved, so the caller must not keep a stale root), and a
// later permitted open must converge. Only a DOLT_RM that ran announces the
// repair.
func TestTempResidueUntrackFailuresAfterTheRemovalAreCountedNotFatal(t *testing.T) {
	const table = "__temp__wisp_child_counters"
	for _, tc := range []struct {
		name      string
		fail      func(mock sqlmock.Sqlmock)
		warns     string
		announced bool
	}{
		{
			name: "rm failed",
			fail: func(mock sqlmock.Sqlmock) {
				expectTempResidueRM(mock, table).WillReturnError(errors.New("injected DOLT_RM failure"))
			},
			warns: "untracking committed " + table + " failed",
		},
		{
			name: "commit failed",
			fail: func(mock sqlmock.Sqlmock) {
				expectTempResidueRM(mock, table).WillReturnRows(sqlmock.NewRows([]string{"status"}))
				expectTempResidueCommit(mock).WillReturnError(errors.New("injected DOLT_COMMIT failure"))
			},
			warns:     "committing the untrack of committed " + table + " failed",
			announced: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			logged := captureStdLog(t)

			expectTempResidueUntrackStart(mock, table)
			tc.fail(mock)
			untracked, err := untrackTempResidue(context.Background(), db)
			if err != nil {
				t.Fatalf("untrackTempResidue() error = %v, want nil: the repair is optional", err)
			}
			if !untracked {
				t.Fatal("untrackTempResidue() = false after DOLT_RM was issued; possible mutation must be counted")
			}
			if !strings.Contains(logged.String(), tc.warns) {
				t.Errorf("log = %q, want %q", logged.String(), tc.warns)
			}
			if got := strings.Contains(logged.String(), tempResidueAnnouncement); got != tc.announced {
				t.Errorf("repair announced = %v, want %v: log %q", got, tc.announced, logged.String())
			}

			// The next, permitted open finds the residue again and converges.
			expectTempResidueUntrackStart(mock, table)
			expectTempResidueRM(mock, table).WillReturnRows(sqlmock.NewRows([]string{"status"}))
			expectTempResidueCommit(mock).WillReturnRows(sqlmock.NewRows([]string{"hash"}))
			untracked, err = untrackTempResidue(context.Background(), db)
			if err != nil || !untracked {
				t.Fatalf("retry untrackTempResidue() = %v, %v; want true, nil", untracked, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestTempResidueUntrackReachesAHeadOnlyCursorScratch covers statements, not
// row restoration: the #4356 cursor heal's scratch table
// (__temp__ignored_schema_migrations_untrack), committed at HEAD by an older
// blanket commit while the working set no longer carries it, is a shape the
// heal's gate cannot see, so it declines. The residue reconcile is then the
// only repair that reaches it, and it must do so with a CACHED removal only:
// a DROP TABLE or a forced DOLT_ADD staging a delete would destroy whatever
// working rows the scratch still holds for the heal. Ordered sqlmock fails
// any statement nobody primed and failOnSwallowedAdvisory makes the
// resulting advisory decline fatal, so the expectations below pin the whole
// repair to the two procedure calls and nothing else.
func TestTempResidueUntrackReachesAHeadOnlyCursorScratch(t *testing.T) {
	db, mock := newMockDB(t)
	failOnSwallowedAdvisory(t)

	// The heal's read-only gate: cursor table not tracked at HEAD, no stray
	// scratch in the working set, so the heal touches nothing.
	expectIgnoredCursorGate(mock, "", false, nil, false)

	expectTempResidueUntrackStart(mock, ignoredCursorUntrackTempTable)
	expectTempResidueRM(mock, ignoredCursorUntrackTempTable).WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectTempResidueCommit(mock).WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	ctx := context.Background()
	healed, err := healTrackedIgnoredCursorTable(ctx, db)
	if err != nil {
		t.Fatalf("healTrackedIgnoredCursorTable() error = %v, want nil", err)
	}
	if healed {
		t.Fatal("healTrackedIgnoredCursorTable() reported a heal on a database whose gate it cannot see")
	}

	untracked, err := untrackTempResidue(ctx, db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if !untracked {
		t.Fatal("untrackTempResidue() reported no repair on a HEAD-only cursor scratch")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueUntrackRemovesEveryIntermediateInOneCall pins the field
// shape: one blanket commit catches several intermediates (ignored/0001 alone
// stages seven), and the reconcile must take every eligible one out of HEAD
// in a single DOLT_RM, each name its own bound argument, and announce them
// all, rather than converge one table per open.
func TestTempResidueUntrackRemovesEveryIntermediateInOneCall(t *testing.T) {
	const first, second = "__temp__local_metadata", "__temp__wisp_child_counters"
	db, mock := newMockDB(t)
	logged := captureStdLog(t)

	expectTempResidueUntrackStart(mock, first, second)
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_RM('--cached', ?, ?)")).
		WithArgs(first, second).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	expectTempResidueCommit(mock).WillReturnRows(sqlmock.NewRows([]string{"hash"}))

	untracked, err := untrackTempResidue(context.Background(), db)
	if err != nil {
		t.Fatalf("untrackTempResidue() error = %v, want nil", err)
	}
	if !untracked {
		t.Fatal("untrackTempResidue() reported no repair for two committed intermediates")
	}
	if want := first + ", " + second; !strings.Contains(logged.String(), want) {
		t.Errorf("announcement = %q, want it to name both intermediates (%s)", logged.String(), want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestTempResidueGateHonorsDoltGlobVetoes is the #7073 data-loss guard for a
// wildcard veto. Dolt reads dolt_ignore patterns as globs and keeps the more
// specific of two conflicting ones, so beside the seeded ('__temp__%', 1) an
// operator's ('__temp__keep*', 0) keeps __temp__keep_rows TRACKED. SQL LIKE
// reads '*' literally and never matches that row; believing it would commit
// the table out of HEAD, and a push would publish the deletion.
func TestTempResidueGateHonorsDoltGlobVetoes(t *testing.T) {
	const table = "__temp__keep_rows"
	seeded := doltIgnoreRow{pattern: "__temp__%", ignored: true}
	for _, tt := range []struct {
		name string
		veto []doltIgnoreRow
		want bool
		why  string
	}{
		{
			name: "star veto",
			veto: []doltIgnoreRow{{pattern: "__temp__keep*", ignored: false}},
			want: false,
			why:  "Dolt keeps the table tracked under the more specific glob",
		},
		{
			name: "question-mark veto",
			veto: []doltIgnoreRow{{pattern: "__temp__keep_?ows", ignored: false}},
			want: false,
			why:  "'?' is one character to Dolt",
		},
		{
			name: "vetoes for other names",
			veto: []doltIgnoreRow{{pattern: "__temp__other*", ignored: false}, {pattern: "audit_*", ignored: false}},
			want: true,
			why:  "a veto that cannot match must not block the repair",
		},
		{
			name: "exact name beside a star veto",
			veto: []doltIgnoreRow{{pattern: "__temp__keep*", ignored: false}, {pattern: table, ignored: true}},
			want: true,
			why:  "an exact row is more specific than any glob, in Dolt too",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			expectIgnoreResolution(mock, "", table, append([]doltIgnoreRow{seeded}, tt.veto...))

			got, err := tableActivelyIgnored(context.Background(), db, "", table)
			if err != nil {
				t.Fatalf("tableActivelyIgnored() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("tableActivelyIgnored(%s) = %v, want %v (%s)", table, got, tt.want, tt.why)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// doltReads is Dolt's own reading of a dolt_ignore pattern, restated from
// doltdb.compilePattern (libraries/doltcore/doltdb/table_name_patterns.go at
// the pinned Dolt): the oracle ignorePatternMayMatch must cover.
func doltReads(pattern, table string) bool {
	expr := "^" + regexp.QuoteMeta(pattern) + "$"
	expr = strings.ReplaceAll(expr, `\?`, ".")
	expr = strings.ReplaceAll(expr, `\*`, ".*")
	expr = strings.ReplaceAll(expr, "%", ".*")
	re, err := regexp.Compile(expr)
	return err == nil && re.MatchString(table)
}

// TestIgnorePatternMayMatchCoversDoltsReading pins the veto test's direction
// against Dolt's own reading: every name Dolt's glob matches must match here,
// for every pattern three fragments build, wildcards, '_' and '\' included.
// The looser readings may only add matches, as the examples show.
func TestIgnorePatternMayMatchCoversDoltsReading(t *testing.T) {
	for _, tt := range []struct {
		pattern string
		want    bool
		why     string
	}{
		{"__temp__%", true, "the seeded wildcard"},
		{"__temp__keep*", true, "Dolt's '*' is any run"},
		{"__temp__keep_?ows", true, "Dolt's '?' is one character"},
		{"__temp__keep_rows", true, "the exact name"},
		{"__TEMP__KEEP*", true, "Dolt compares case-sensitively; a veto errs towards yes"},
		{"__t_mp__keep*", true, "Dolt's '_' is literal; a veto errs towards yes"},
		{`__temp__keep\*`, true, `Dolt's '\' is literal; a veto errs towards yes`},
		{"__temp__other*", false, "another name's veto"},
		{"keep*", false, "anchored at the start"},
		{"__temp__keep", false, "anchored at the end"},
		{"__temp__keep_rows?", false, "'?' needs a character"},
		{"__temp__kee[p]*", false, "brackets are literal, not a class"},
	} {
		if got := ignorePatternMayMatch(tt.pattern, "__temp__keep_rows"); got != tt.want {
			t.Errorf("ignorePatternMayMatch(%q, __temp__keep_rows) = %v, want %v (%s)", tt.pattern, got, tt.want, tt.why)
		}
	}

	fragments := []string{"__temp__", "keep", "_", "%", "*", "?", `\`, "K", "rows"}
	names := []string{"__temp__keep_rows", "__temp__wisp_child_counters", "__temp__", "keep", "_", "x", "",
		`__temp__keep\rows`, "__TEMP__KEEP_ROWS", "K?", "%*"}
	var patterns []string
	level := []string{""}
	for range 3 {
		var next []string
		for _, prefix := range level {
			for _, fragment := range fragments {
				next = append(next, prefix+fragment)
			}
		}
		patterns = append(patterns, next...)
		level = next
	}
	covered := 0
	for _, pattern := range patterns {
		for _, name := range names {
			if !doltReads(pattern, name) {
				continue
			}
			covered++
			if !ignorePatternMayMatch(pattern, name) {
				t.Errorf("Dolt reads %q as matching %q but ignorePatternMayMatch does not: a veto it would miss", pattern, name)
			}
		}
	}
	if covered == 0 {
		t.Fatal("no corpus pattern matched a name under Dolt's reading; the cross-check checked nothing")
	}
}
