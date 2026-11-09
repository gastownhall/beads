package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// compactVerifySQL compares the replay with the planned head (bound as its
// argument), not with main's current HEAD.
const compactVerifySQL = "SELECT COUNT(*) FROM dolt_diff_summary(?, 'compact-tmp')"

// compactHeadSQL is the catch-up's read of main's head, made once after the
// replay check.
const compactHeadSQL = "SELECT HASHOF('main')"

// expectCompactBeforeVerify queues Compact's steps up to and including the
// checkout of main that precedes the replay check: the replay of kept and
// the read-back of the rebuilt chain (compact_test.go).
func expectCompactBeforeVerify(mock sqlmock.Sqlmock, initial, boundary compactCommit, kept []compactCommit) {
	msg := expectCompactReplay(mock, initial, boundary, kept)
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(compactRebuiltLog(initial, boundary, kept, msg)))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectCompactCleanup queues the deferred cleanup of a failed Compact.
func expectCompactCleanup(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(ok)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
}

func runCompactForVerify(t *testing.T, setup func(sqlmock.Sqlmock)) error {
	t.Helper()
	db, mock := newMockDB(t)
	initial, boundary, kept := compactTestCommits()
	expectCompactBeforeVerify(mock, initial, boundary, kept)
	setup(mock)
	err := Compact(context.Background(), db, initial.Hash, boundary.Hash, "headhash", 2, keptHashes(kept))
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("unmet expectations (Compact returned %v): %v", err, merr)
	}
	return err
}

// TestCompactVerifyDivergentReplayAborts: a replay whose committed tables
// differ from main must not reset main; the temp branch is cleaned up.
func TestCompactVerifyDivergentReplayAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), "replayed history differs from main's planned head headhash in 1 table(s); main was not reset") {
		t.Fatalf("Compact error = %v, want the divergence abort", err)
	}
}

func TestCompactVerifyQueryErrorAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnError(errors.New("boom"))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), `compact step "verify replay"`) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Compact error = %v, want a wrapped verify replay error", err)
	}
}

func TestCompactVerifyScanErrorAborts(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow("not a number"))
		expectCompactCleanup(mock)
	})
	if err == nil || !strings.Contains(err.Error(), `compact step "verify replay"`) {
		t.Fatalf("Compact error = %v, want a wrapped verify replay error", err)
	}
}

// TestCompactVerifyEqualReplayResets: an empty diff lets Compact swap main and
// delete the temp branch.
func TestCompactVerifyEqualReplayResets(t *testing.T) {
	err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
		ok := sqlmock.NewResult(0, 0)
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectQuery(regexp.QuoteMeta(compactHeadSQL)).WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow("headhash"))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'compact-tmp')")).WillReturnResult(ok)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
	})
	if err != nil {
		t.Fatalf("Compact error = %v, want nil", err)
	}
}

// TestCompactRequiresPlannedHead: without the planned head there is nothing
// to verify the replay against, so Compact refuses before it creates the
// temp branch or touches main.
func TestCompactRequiresPlannedHead(t *testing.T) {
	db, mock := newMockDB(t)
	err := Compact(context.Background(), db, "initial", "boundary", "", 3, []string{"recent"})
	if err == nil || !strings.Contains(err.Error(), "planned head") {
		t.Fatalf("Compact error = %v, want the missing planned head refused", err)
	}
	if merr := mock.ExpectationsWereMet(); merr != nil {
		t.Errorf("Compact ran statements without a planned head: %v", merr)
	}
}

// compactLandedSQL is the catch-up's read of the commits that reached main
// after the plan was read, bound to the range headhash..<main's head>.
const compactLandedSQL = "SELECT commit_hash, committer, email, date, author, author_email, author_date, message, commit_order, parents FROM dolt_log(?, '--parents')"

// landedRow is one row of a compactLandedSQL read: a commit and its
// parents' hashes.
type landedRow struct {
	compactCommit
	Parents []string
}

// landedRows builds the rows of one compactLandedSQL read; Parents renders
// as dolt_log does, comma-separated.
func landedRows(commits ...landedRow) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "author", "author_email", "author_date", "message", "commit_order", "parents"})
	for _, c := range commits {
		rows.AddRow(c.Hash, c.Committer, c.CommitterEmail, c.CommitterDate, c.Author, c.AuthorEmail, c.AuthorDate, c.Message, c.CommitOrder,
			strings.Join(c.Parents, ", "))
	}
	return rows
}

// landedTestChain returns three commits that reached main after the plan
// was read, oldest first, each the single parent of the next, the oldest's
// parent the planned head "headhash".
func landedTestChain() []landedRow {
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	return []landedRow{
		{sameIdentity("land0001hash", "Ada", "ada@example.com", base, "landed 1", 7), []string{"headhash"}},
		{sameIdentity("land0002hash", "Bo", "bo@example.com", base.Add(-time.Hour), "landed 2", 8), []string{"land0001hash"}},
		{sameIdentity("land0003hash", "Cy", "cy@example.com", base.Add(time.Minute), "landed 3", 9), []string{"land0002hash"}},
	}
}

// expectHeadMoved queues the catch-up's head read returning landed, and its
// range read returning rows.
func expectHeadMoved(mock sqlmock.Sqlmock, landed string, rows *sqlmock.Rows) *sqlmock.ExpectedQuery {
	mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta(compactHeadSQL)).WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow(landed))
	return mock.ExpectQuery(regexp.QuoteMeta(compactLandedSQL)).WithArgs("headhash.." + landed).WillReturnRows(rows)
}

// expectFollowUpPath queues today's path for commits that were not caught
// up: the soft reset, a staged check that finds them, the follow-up commit
// and the temp branch's deletion.
func expectFollowUpPath(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'compact-tmp')")).WillReturnResult(ok)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
		WithArgs("compact: commit changes that reached main during compaction").
		WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("followup"))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
}

// TestCompactCatchUpSkipsNonReplayableRange: a range that is not one line of
// single-parent commits from main's head down to the planned head, or holds
// a commit Dolt cannot write back, is not replayed: no second checkout of
// compact-tmp, no cherry-pick, and main takes today's soft reset and
// follow-up commit.
func TestCompactCatchUpSkipsNonReplayableRange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		landed string
		rows   func(c []landedRow) []landedRow
	}{
		{"gap", "land0003hash", func(c []landedRow) []landedRow { return []landedRow{c[2], c[0]} }},
		{"commit off the line", "land0003hash", func(c []landedRow) []landedRow {
			extra := landedRow{sameIdentity("side0001hash", "Ada", "ada@example.com", c[0].CommitterDate, "side", 7), []string{"headhash"}}
			return []landedRow{c[2], c[1], extra, c[0]}
		}},
		{"duplicate row", "land0003hash", func(c []landedRow) []landedRow { return []landedRow{c[2], c[1], c[1], c[0]} }},
		{"cycle", "land0003hash", func(c []landedRow) []landedRow {
			c[1].Parents = []string{"land0003hash"}
			return []landedRow{c[2], c[1], c[0]}
		}},
		{"root", "land0003hash", func(c []landedRow) []landedRow {
			c[0].Parents = nil
			return []landedRow{c[2], c[1], c[0]}
		}},
		{"merge", "land0003hash", func(c []landedRow) []landedRow {
			side := landedRow{sameIdentity("side0001hash", "Ada", "ada@example.com", c[0].CommitterDate, "side", 7), []string{"headhash"}}
			c[1].Parents = []string{"land0001hash", "side0001hash"}
			return []landedRow{c[2], c[1], side, c[0]}
		}},
		{"head not a descendant of the planned head", "elsewhere0hash", func([]landedRow) []landedRow { return nil }},
		{"empty committer email", "land0003hash", func(c []landedRow) []landedRow {
			c[1].CommitterEmail = ""
			return []landedRow{c[2], c[1], c[0]}
		}},
		{"empty author name", "land0003hash", func(c []landedRow) []landedRow {
			c[2].Author = ""
			return []landedRow{c[2], c[1], c[0]}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runCompactForVerify(t, func(mock sqlmock.Sqlmock) {
				expectHeadMoved(mock, tc.landed, landedRows(tc.rows(landedTestChain())...))
				expectFollowUpPath(mock)
			})
			if err != nil {
				t.Fatalf("Compact error = %v, want today's follow-up path", err)
			}
		})
	}
}

// errInjected is the failure a catch-up test injects into one statement.
var errInjected = errors.New("injected catch-up failure")

// TestCompactCatchUpFailureLeavesMainUnmoved: when any statement of the
// catch-up fails, or its read-back or tree check refuses, Compact returns
// that error before main's soft reset, so main's HEAD and its staged and
// working sets stay as the writers left them; the deferred cleanup returns
// to main and deletes compact-tmp, and a committer that could not be cleared
// discards the connection. "none" is the whole catch-up and the swap, at
// statement level.
func TestCompactCatchUpFailureLeavesMainUnmoved(t *testing.T) {
	for _, tc := range []struct {
		failAt string
		want   string
	}{
		{"none", ""},
		{"head read", `compact step "read main's head"`},
		{"range read", `compact step "read commits that reached main"`},
		{"range row", `compact step "read commits that reached main"`},
		{"checkout temp", `compact step "checkout temp for catch-up"`},
		{"set committer", `compact step "set committer land0001"`},
		{"cherry-pick", `compact step "cherry-pick land0002"`},
		{"clear committer", `compact step "clear committer"`},
		{"read back", "compact: read rebuilt log"},
		{"read back differs", "rebuilt commit for land0002 does not preserve the original"},
		{"checkout main", `compact step "checkout main after catch-up"`},
		{"verify", `compact step "verify catch-up"`},
		{"verify differs", "caught-up history differs from main's head land0003 in 2 table(s); main was not reset"},
	} {
		t.Run(tc.failAt, func(t *testing.T) {
			db, mock := newMockDB(t)
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatalf("db.Conn: %v", err)
			}
			defer conn.Close()
			ok := sqlmock.NewResult(0, 0)
			initial, boundary, kept := compactTestCommits()
			expectCompactBeforeVerify(mock, initial, boundary, kept)
			mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs("headhash").WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))

			chain := landedTestChain()
			landed := chain[2].Hash
			fails := func(step string) bool { return tc.failAt == step }
			// queue programs the catch-up up to the failing statement and
			// reports whether it got past it.
			queue := func() bool {
				head := mock.ExpectQuery(regexp.QuoteMeta(compactHeadSQL))
				if fails("head read") {
					head.WillReturnError(errInjected)
					return false
				}
				head.WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow(landed))
				rows := landedRows(chain[2], chain[1], chain[0])
				if fails("range row") {
					rows.RowError(1, errInjected)
				}
				rng := mock.ExpectQuery(regexp.QuoteMeta(compactLandedSQL)).WithArgs("headhash.." + landed)
				if fails("range read") {
					rng.WillReturnError(errInjected)
					return false
				}
				rng.WillReturnRows(rows)
				if fails("range row") {
					return false
				}
				co := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')"))
				if fails("checkout temp") {
					co.WillReturnError(errInjected)
					return false
				}
				co.WillReturnResult(ok)
				for i, c := range chain {
					set := mock.ExpectExec(regexp.QuoteMeta(setCommitterQuery)).
						WithArgs(c.Committer, c.CommitterEmail, commitDateArg(c.CommitterDate))
					if i == 0 && fails("set committer") {
						set.WillReturnError(errInjected)
						mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).WillReturnResult(ok)
						return false
					}
					set.WillReturnResult(ok)
					pick := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).WithArgs(c.Hash)
					if i == 1 && fails("cherry-pick") {
						pick.WillReturnError(errInjected)
						mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).WillReturnResult(ok)
						return false
					}
					pick.WillReturnResult(ok)
				}
				clr := mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery))
				if fails("clear committer") {
					clr.WillReturnError(errInjected)
					return false
				}
				clr.WillReturnResult(ok)
				all := append(append([]compactCommit{}, kept...), chain[0].compactCommit, chain[1].compactCommit, chain[2].compactCommit)
				rebuilt := compactRebuiltLog(initial, boundary, all, "compact: squash 2 commits into base snapshot")
				if fails("read back differs") {
					rebuilt[1].Message = "rewritten" // newest first: rebuilt[1] replays land0002
				}
				back := mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery))
				if fails("read back") {
					back.WillReturnError(errInjected)
					return false
				}
				back.WillReturnRows(compactLogRows(rebuilt))
				if fails("read back differs") {
					return false
				}
				main := mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')"))
				if fails("checkout main") {
					main.WillReturnError(errInjected)
					return false
				}
				main.WillReturnResult(ok)
				verify := mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs(landed)
				switch {
				case fails("verify"):
					verify.WillReturnError(errInjected)
					return false
				case fails("verify differs"):
					verify.WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(2))
					return false
				}
				verify.WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
				return true
			}
			if queue() {
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'compact-tmp')")).WillReturnResult(ok)
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
				mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
			} else {
				expectCompactCleanup(mock)
				if fails("clear committer") {
					mock.ExpectClose()
				}
			}

			err = Compact(context.Background(), conn, initial.Hash, boundary.Hash, "headhash", 2, keptHashes(kept))
			if merr := mock.ExpectationsWereMet(); merr != nil {
				t.Errorf("unmet expectations (Compact returned %v): %v", err, merr)
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Compact error = %v, want the catch-up to succeed", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compact error = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "reset main to compacted") {
				t.Fatalf("Compact error = %v: main was reset after the catch-up failed", err)
			}
			pingErr := conn.PingContext(context.Background())
			if discarded := errors.Is(pingErr, sql.ErrConnDone); discarded != fails("clear committer") {
				t.Fatalf("connection discarded = %t (ping: %v), want %t", discarded, pingErr, fails("clear committer"))
			}
		})
	}
}
