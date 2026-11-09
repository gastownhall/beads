package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// These tests pin the catch-up step's two exits (compact.go): the quiet case,
// which adds exactly one statement to the compact sequence — the read of
// main's head, unchanged — and the fallback case, which leaves a range the
// replay cannot carry to the staged follow-up without replaying it: no
// checkout of compact-tmp, no cherry-pick, straight to the soft reset.

// expectCompactAfterVerify queues the replay check Compact runs on main
// after the rebuilt chain was read back (compact_verify_test.go queues the
// steps before it).
func expectCompactAfterVerify(mock sqlmock.Sqlmock, head string) {
	mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs(head).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
}

// expectCompactTail queues the end of a compact run whose landed range needs
// no replay: the soft reset, a clean staged check, and the temp branch
// deleted.
func expectCompactTail(mock sqlmock.Sqlmock) {
	ok := sqlmock.NewResult(0, 0)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', 'compact-tmp')")).WillReturnResult(ok)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).WillReturnResult(ok)
}

// expectCatchUpHead queues the read of main's head answering with head.
func expectCatchUpHead(mock sqlmock.Sqlmock, head string) {
	mock.ExpectQuery(regexp.QuoteMeta(catchUpHeadQuery)).
		WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow(head))
}

// catchUpLogRows builds dolt_log(range, '--parents') rows, newest first, with
// each commit's parents (nil for a root).
func catchUpLogRows(commits []compactCommit, parents map[string]string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "author", "author_email", "author_date", "message", "commit_order", "parents"})
	for _, c := range commits {
		rows.AddRow(c.Hash, c.Committer, c.CommitterEmail, c.CommitterDate,
			c.Author, c.AuthorEmail, c.AuthorDate, c.Message, c.CommitOrder, parents[c.Hash])
	}
	return rows
}

// caughtCommit returns a commit fixture for the landed range: hash h, parent
// p, by an identity the replay can restore, dated after the kept window.
func caughtCommit(h, p, name string, order int64) compactCommit {
	date := time.Date(2026, 11, 1, 12, 0, int(order)*30, 0, time.UTC)
	return compactCommit{
		Hash: h, Committer: name, CommitterEmail: name + "@example.com", CommitterDate: date,
		Author: name, AuthorEmail: name + "@example.com", AuthorDate: date,
		Message: "landed " + name, CommitOrder: order,
	}
}

// TestCompactCatchUpQuietAddsOnlyHeadRead: with main still at the planned
// head, the whole catch-up is one read of main's head. The ordered mock
// proves nothing else ran: no dolt_log range read, no second checkout of
// compact-tmp, no cherry-pick.
func TestCompactCatchUpQuietAddsOnlyHeadRead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	initial, boundary, kept := compactTestCommits()
	expectCompactBeforeVerify(mock, initial, boundary, kept)
	expectCompactAfterVerify(mock, plannedHead(kept))
	expectCatchUpHead(mock, plannedHead(kept))
	expectCompactTail(mock)

	if err := Compact(context.Background(), db, initial.Hash, boundary.Hash, plannedHead(kept), 2, keptHashes(kept)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactSkipsCatchUpOnUnreplayableRange: a landed range the replay
// cannot carry must skip catch-up entirely — main's head read, the range
// read, then the staged follow-up path exactly as before catch-up existed —
// and Compact must still succeed: those commits are another writer's work,
// and the soft reset keeps their rows.
func TestCompactSkipsCatchUpOnUnreplayableRange(t *testing.T) {
	_, _, keptFixtures := compactTestCommits()
	head, landed, first, second := plannedHead(keptFixtures), "landed2hash", "landed1hash", "landed0hash"

	cases := []struct {
		name   string
		rows   []compactCommit
		parent map[string]string
	}{
		{"merge in range",
			[]compactCommit{caughtCommit(landed, "", "Merger", 9)},
			map[string]string{landed: first + ", branchhash"}},
		{"gap below the landed commit",
			[]compactCommit{caughtCommit(landed, "unknownhash", "Wanda", 9)},
			map[string]string{landed: "unknownhash"}},
		{"duplicate rows",
			[]compactCommit{caughtCommit(landed, head, "Wanda", 9), caughtCommit(landed, head, "Wanda", 9)},
			map[string]string{landed: head}},
		{"cycle in the chain",
			[]compactCommit{caughtCommit(landed, second, "Wanda", 9), caughtCommit(second, landed, "Wanda", 8)},
			map[string]string{landed: second, second: landed}},
		{"root short of the planned head",
			[]compactCommit{caughtCommit(landed, head, "Wanda", 9)},
			map[string]string{landed: ""}},
		{"empty range, main not a descendant",
			nil,
			nil},
		{"unrestorable identity",
			func() []compactCommit {
				c := caughtCommit(landed, head, "Wanda", 9)
				c.Committer = ""
				return []compactCommit{c}
			}(),
			map[string]string{landed: head}},
		{"row outside the walked chain",
			[]compactCommit{caughtCommit(landed, head, "Wanda", 9), caughtCommit(second, first, "Stray", 8), caughtCommit(first, head, "Stray", 7)},
			map[string]string{landed: head, second: first, first: head}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			initial, boundary, kept := compactTestCommits()
			expectCompactBeforeVerify(mock, initial, boundary, kept)
			expectCompactAfterVerify(mock, head)
			expectCatchUpHead(mock, landed)
			mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed).
				WillReturnRows(catchUpLogRows(tc.rows, tc.parent))
			expectCompactTail(mock)

			err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
			if err != nil {
				t.Fatalf("Compact: %v (an unreplayable range must fall back, not refuse)", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err) // no checkout of compact-tmp, no cherry-pick ran
			}
		})
	}
}

// expectCatchUpReplay queues a catch-up that replays a two-commit landed
// chain — caught1 (Victor) below caught2 (Wanda) — onto the temp branch:
// checkout, both picks with the session's committer restored per commit,
// and the committer cleared when the replay ends.
func expectCatchUpReplay(mock sqlmock.Sqlmock, caught1, caught2 compactCommit) {
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectReplayOf(mock, caught1)
	expectReplayOf(mock, caught2)
	mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// caughtRebuiltLog extends the rebuilt log of the kept replay
// (compact_test.go) with the caught-up commits replayed above the kept ones.
func caughtRebuiltLog(initial, boundary compactCommit, kept, caught []compactCommit, msg string) []compactCommit {
	replayed := compactRebuiltLog(initial, boundary, kept, msg)
	caughtReplayed := make([]compactCommit, len(caught))
	for i, c := range caught {
		k := c
		k.Hash = fmt.Sprintf("rebuilt-caught-%d", i+1)
		k.CommitOrder = int64(3 + len(kept) + i)
		caughtReplayed[i] = k
	}
	return append(newestFirst(caughtReplayed...), replayed...)
}

// TestCompactCatchUpFailureAbortsBeforeReset: every failure inside the
// catch-up — reading main's head, reading the landed range, a scan error in
// it, the replay of a caught commit, the read-back of the rebuilt chain, and
// the tree comparison against the landed head — errors out before the soft
// reset: main's HEAD and pending set never move, and the deferred cleanup
// returns to main and deletes compact-tmp. The ordered mock pins that no
// reset ran.
func TestCompactCatchUpFailureAbortsBeforeReset(t *testing.T) {
	_, _, keptFixtures := compactTestCommits()
	head, landed1, landed2 := plannedHead(keptFixtures), "landed1hash", "landed2hash"
	caught1 := caughtCommit(landed1, head, "Victor", 8)
	caught2 := caughtCommit(landed2, landed1, "Wanda", 9)
	// A sqlmock rows object serves one iteration only, so every subtest
	// takes a fresh copy of the landed chain.
	chain := func() *sqlmock.Rows {
		return catchUpLogRows([]compactCommit{caught2, caught1},
			map[string]string{landed2: landed1, landed1: head})
	}

	// badScanLog has a date dolt_log could not have produced, so reading the
	// range fails mid-iteration.
	badScanLog := func() *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "author", "author_email", "author_date", "message", "commit_order", "parents"})
		rows.AddRow(landed2, caught2.Committer, caught2.CommitterEmail, "not a date",
			caught2.Author, caught2.AuthorEmail, "not a date", caught2.Message, caught2.CommitOrder, landed1)
		return rows
	}

	t.Run("read of main's head fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		injected := errors.New("no such function")
		mock.ExpectQuery(regexp.QuoteMeta(catchUpHeadQuery)).WillReturnError(injected)
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !errors.Is(err, injected) {
			t.Fatalf("Compact error = %v, want the injected head-read failure", err)
		}
		if !strings.Contains(err.Error(), `compact step "read main's head"`) {
			t.Fatalf("Compact error = %v, want it to name the failed head read", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err) // the reset never ran
		}
	})

	t.Run("read of the landed range fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		injected := errors.New("dolt_log refused the range")
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnError(injected)
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !errors.Is(err, injected) {
			t.Fatalf("Compact error = %v, want the injected range-read failure", err)
		}
		if !strings.Contains(err.Error(), `compact step "read commits that reached main"`) {
			t.Fatalf("Compact error = %v, want it to name the failed range read", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("scan error in the landed range", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(badScanLog())
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), `compact step "read commits that reached main"`) {
			t.Fatalf("Compact error = %v, want the failed range read wrapped", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("caught cherry-pick fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(chain())
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).WillReturnResult(sqlmock.NewResult(0, 1))
		expectReplayOf(mock, caught1)
		injected := errors.New("cherry-pick conflict")
		mock.ExpectExec(regexp.QuoteMeta(setCommitterQuery)).
			WithArgs(caught2.Committer, caught2.CommitterEmail, commitDateArg(caught2.CommitterDate)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).
			WithArgs(caught2.Hash).WillReturnError(injected)
		mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).WillReturnResult(sqlmock.NewResult(0, 0))
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !errors.Is(err, injected) {
			t.Fatalf("Compact error = %v, want the injected cherry-pick failure", err)
		}
		if !strings.Contains(err.Error(), `compact step "cherry-pick landed2h"`) {
			t.Fatalf("Compact error = %v, want it to name the failed caught cherry-pick", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("caught read-back does not preserve the original", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		msg := expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(chain())
		expectCatchUpReplay(mock, caught1, caught2)
		// The rebuilt chain reads back with the newest caught commit committed
		// by the session, which the read-back must refuse.
		rebuilt := caughtRebuiltLog(initial, boundary, kept, []compactCommit{caught1, caught2}, msg)
		rebuilt[0].Committer, rebuilt[0].CommitterEmail = "root", "root@localhost"
		mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).WillReturnRows(compactLogRows(rebuilt))
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), "rebuilt commit for landed2h does not preserve the original") {
			t.Fatalf("Compact error = %v, want the caught read-back refusal", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("caught-up tree differs from main's head", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		msg := expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(chain())
		expectCatchUpReplay(mock, caught1, caught2)
		mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
			WillReturnRows(compactLogRows(caughtRebuiltLog(initial, boundary, kept, []compactCommit{caught1, caught2}, msg)))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs(landed2).
			WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(2))
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(),
			"caught-up history differs from main's head landed2h in 2 table(s); main was not reset") {
			t.Fatalf("Compact error = %v, want the caught-up tree refusal", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("catch-up verify query fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		msg := expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(chain())
		expectCatchUpReplay(mock, caught1, caught2)
		mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
			WillReturnRows(compactLogRows(caughtRebuiltLog(initial, boundary, kept, []compactCommit{caught1, caught2}, msg)))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).WillReturnResult(sqlmock.NewResult(0, 1))
		injected := errors.New("diff summary refused")
		mock.ExpectQuery(regexp.QuoteMeta(compactVerifySQL)).WithArgs(landed2).WillReturnError(injected)
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !errors.Is(err, injected) {
			t.Fatalf("Compact error = %v, want the injected verify failure", err)
		}
		if !strings.Contains(err.Error(), `compact step "verify catch-up"`) {
			t.Fatalf("Compact error = %v, want it to name the failed catch-up verify", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	// The failed caught cherry-pick leaves the session's committer set to the
	// last replayed one; a clear that also fails gets the same cleanup and
	// then discards the pinned connection rather than returning it to its
	// pool stamping later commits with Wanda's identity at Wanda's date.
	t.Run("committer cannot be cleared after a failed catch-up", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("db.Conn: %v", err)
		}
		defer conn.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactBeforeVerify(mock, initial, boundary, kept)
		expectCompactAfterVerify(mock, head)
		expectCatchUpHead(mock, landed2)
		mock.ExpectQuery(regexp.QuoteMeta(catchUpLogQuery)).WithArgs(head + ".." + landed2).WillReturnRows(chain())
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).WillReturnResult(sqlmock.NewResult(0, 1))
		expectReplayOf(mock, caught1)
		expectReplayOf(mock, caught2)
		mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).WillReturnError(errors.New("SET refused"))
		expectAbandon(mock)
		mock.ExpectClose()

		err = Compact(context.Background(), conn, initial.Hash, boundary.Hash, head, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), `compact step "clear committer"`) {
			t.Fatalf("Compact error = %v, want the failed committer reset", err)
		}
		if err := conn.PingContext(context.Background()); !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("conn.PingContext after a failed committer reset = %v, want %v (connection discarded)", err, sql.ErrConnDone)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
