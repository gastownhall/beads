package versioncontrolops

import (
	"context"
	"regexp"
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
