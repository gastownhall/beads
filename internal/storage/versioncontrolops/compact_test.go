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

// compactLogQuery is the full-log read Compact makes before touching
// history and again to verify the rebuilt chain: both identities and both
// dates of every commit (#7294).
const compactLogQuery = "SELECT commit_hash, committer, email, date, author, author_email, author_date, message, commit_order FROM dolt_log"

// The replay's session-committer statements.
const (
	setCommitterQuery   = "SET @@dolt_committer_name = ?, @@dolt_committer_email = ?, @@dolt_committer_date = ?"
	clearCommitterQuery = "SET @@dolt_committer_name = '', @@dolt_committer_email = '', @@dolt_committer_date = ''"
)

// compactLogRows builds dolt_log rows for one full log read.
func compactLogRows(commits []compactCommit) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "author", "author_email", "author_date", "message", "commit_order"})
	for _, c := range commits {
		rows.AddRow(c.Hash, c.Committer, c.CommitterEmail, c.CommitterDate, c.Author, c.AuthorEmail, c.AuthorDate, c.Message, c.CommitOrder)
	}
	return rows
}

// newestFirst lists commits newest first, as dolt_log does.
func newestFirst(commits ...compactCommit) []compactCommit {
	out := make([]compactCommit, 0, len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		out = append(out, commits[i])
	}
	return out
}

// expectCompactSetup programs the sqlmock expectations for a compact run up
// to the replay: the preflight log read, the branch setup, and the
// squashed base, dated as the boundary. It returns the squashed-base
// message.
func expectCompactSetup(mock sqlmock.Sqlmock, initial, boundary compactCommit, kept []compactCommit) string {
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(append(newestFirst(kept...), boundary, initial)))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('compact-tmp', ?)")).
		WithArgs(boundary.Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs(initial.Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", 2)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?, '--date', ?)")).
		WithArgs(msg, commitDateArg(boundary.CommitterDate)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return msg
}

// expectReplayOf programs one kept commit's replay: the session's committer
// set to the original's, then the cherry-pick.
func expectReplayOf(mock sqlmock.Sqlmock, k compactCommit) *sqlmock.ExpectedExec {
	mock.ExpectExec(regexp.QuoteMeta(setCommitterQuery)).
		WithArgs(k.Committer, k.CommitterEmail, commitDateArg(k.CommitterDate)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).
		WithArgs(k.Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectCompactReplay programs a compact run that gets all the way through
// the replay: the setup, each kept commit's replay, and the committer
// cleared once the replay ends. It returns the squashed-base message.
func expectCompactReplay(mock sqlmock.Sqlmock, initial, boundary compactCommit, kept []compactCommit) string {
	msg := expectCompactSetup(mock, initial, boundary, kept)
	for _, k := range kept {
		expectReplayOf(mock, k)
	}
	mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	return msg
}

// expectAbandon programs the deferred cleanup of a run that refused after
// the temp branch exists: back to main, temp branch deleted, and no
// DOLT_RESET('--hard') onto the rebuilt chain.
func expectAbandon(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// compactRebuiltLog builds the log rows Compact expects to read back after
// the replay: the replayed commits carrying their originals' metadata
// (newest date first, as dolt_log orders), then the squashed base dated as
// the boundary, then the initial commit.
func compactRebuiltLog(initial, boundary compactCommit, kept []compactCommit, msg string) []compactCommit {
	rebuilt := make([]compactCommit, len(kept))
	for i, k := range kept {
		k.Hash, k.CommitOrder = fmt.Sprintf("rebuilt-%d", i+1), int64(i)+3
		rebuilt[i] = k
	}
	base := compactCommit{
		Hash: "rebuilt-base", Committer: "beads", CommitterEmail: "beads@local", CommitterDate: boundary.CommitterDate,
		Author: "beads", AuthorEmail: "beads@local", AuthorDate: boundary.CommitterDate, Message: msg, CommitOrder: 2,
	}
	return append(newestFirst(rebuilt...), base, initial)
}

// sameIdentity returns a commit fixture whose author and committer are one
// identity at one date, as bd's own writes leave them.
func sameIdentity(hash, name, email string, date time.Time, msg string, order int64) compactCommit {
	return compactCommit{
		Hash: hash, Committer: name, CommitterEmail: email, CommitterDate: date,
		Author: name, AuthorEmail: email, AuthorDate: date, Message: msg, CommitOrder: order,
	}
}

// compactTestCommits returns the commit fixtures: an initial commit, a
// boundary commit, and three kept commits whose hashes differ within their
// first 8 characters, so a refusal naming one cannot pass for another. Two
// kept commits sit in the same second 333 ms apart; the empty one of them
// was committed by someone other than its author, ten minutes after it was
// authored (the shape `dolt cherry-pick` and an earlier bd compact leave),
// under a name with a ')' and an email with '<>' that a `Name <email>`
// argument could not carry. The third is dated in a non-UTC fixed zone and
// its message starts with a dash.
func compactTestCommits() (initial, boundary compactCommit, kept []compactCommit) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tz := time.FixedZone("Non-UTC", 9*3600+1800)
	initial = sameIdentity("initial0hash", "Bees", "bees@example.com", base.Add(-72*time.Hour), "bd init", 1)
	boundary = sameIdentity("boundary-hash", "Bees", "bees@example.com", base.Add(-24*time.Hour), "old edit", 2)
	kept = []compactCommit{
		sameIdentity("kept0001hash", "Original Author", "orig@example.com", base.Add(123*time.Millisecond), "edit A", 4),
		{
			Hash: "kept0002hash", Committer: "Replayer (ci)", CommitterEmail: "replayer<ci>@example.com",
			CommitterDate: base.Add(456 * time.Millisecond),
			Author:        "Original Author", AuthorEmail: "orig@example.com",
			AuthorDate: base.Add(-10 * time.Minute).Add(456 * time.Millisecond),
			Message:    "an empty commit", CommitOrder: 5,
		},
		sameIdentity("kept0003hash", "Original Author", "orig@example.com",
			base.Add(time.Hour).Add(789*time.Millisecond).In(tz), "- leading dash edit B", 6),
	}
	return initial, boundary, kept
}

// keptHashes returns the kept commits' hashes in ascending commit_order.
func keptHashes(kept []compactCommit) []string {
	out := make([]string, len(kept))
	for i, k := range kept {
		out[i] = k.Hash
	}
	return out
}

// TestCompactRestoresCommitMetadata covers #7294 at the statement level:
// Compact reads the kept commits' original metadata before creating
// anything; each cherry-pick runs with the session's committer set to the
// original's, so the replayed commit keeps its committer and email (AC-2)
// and its commit date to the millisecond (AC-1), whatever characters the
// identity holds; the committer is cleared once the replay ends; the
// squashed base is dated as the boundary (AC-3); and the rebuilt chain is
// read back and verified before main moves onto it (AC-5).
func TestCompactRestoresCommitMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	initial, boundary, kept := compactTestCommits()
	msg := expectCompactReplay(mock, initial, boundary, kept)

	// Read-back verification, then the swap.
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(compactRebuiltLog(initial, boundary, kept, msg)))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--hard', 'compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactRefusesUnverifiableReplay covers the refusals that keep
// compaction from destroying history it cannot faithfully replay (#7294):
// a hash the log does not know, and an identity Dolt cannot write back,
// refuse before anything is created; a replay that fails part way, and a
// rebuilt chain that does not read back as its original, refuse after the
// replay, with the committer cleared and the deferred cleanup returning to
// main and deleting the temp branch, never resetting main onto the
// unverifiable chain; a committer that cannot be cleared gets the same
// cleanup before its connection is discarded.
func TestCompactRefusesUnverifiableReplay(t *testing.T) {
	logQuery := regexp.QuoteMeta(compactLogQuery)

	t.Run("recent hash not in the log", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		mock.ExpectQuery(logQuery).
			WillReturnRows(compactLogRows([]compactCommit{kept[2], kept[0], boundary, initial})) // kept[1] unknown
		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), "commit kept0002hash is not in the log") {
			t.Fatalf("Compact error = %v, want a refusal naming kept0002hash", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err) // nothing else ran: no temp branch was created
		}
	})

	t.Run("boundary not in the log", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		mock.ExpectQuery(logQuery).
			WillReturnRows(compactLogRows(append(newestFirst(kept...), initial))) // boundary unknown
		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), "commit boundary-hash is not in the log") {
			t.Fatalf("Compact error = %v, want a refusal naming boundary-hash", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*compactCommit)
		want   string
	}{
		{"committer name is empty", func(c *compactCommit) { c.Committer = "" }, "committer name is empty"},
		{"committer email is empty", func(c *compactCommit) { c.CommitterEmail = "" }, "committer email is empty"},
		{"author name is empty", func(c *compactCommit) { c.Author = "" }, "author name is empty"},
		{"author email is empty", func(c *compactCommit) { c.AuthorEmail = "" }, "author email is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			initial, boundary, kept := compactTestCommits()
			tc.mutate(&kept[1])
			mock.ExpectQuery(logQuery).
				WillReturnRows(compactLogRows(append(newestFirst(kept...), boundary, initial)))
			err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept))
			want := "commit kept0002: " + tc.want + "; compact a smaller window with --days"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Compact error = %v, want a refusal containing %q", err, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err) // nothing else ran: no temp branch was created
			}
		})
	}

	t.Run("cherry-pick fails part way", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		expectCompactSetup(mock, initial, boundary, kept)
		expectReplayOf(mock, kept[0])
		expectReplayOf(mock, kept[1]).WillReturnError(errors.New("cherry-pick conflict"))
		// The session's committer is cleared before the cleanup, so the
		// connection does not go back to its pool stamping later commits
		// as kept[1]'s committer at kept[1]'s date.
		mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		expectAbandon(mock)

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept))
		if err == nil || !strings.Contains(err.Error(), `compact step "cherry-pick kept0002"`) {
			t.Fatalf("Compact error = %v, want the failed cherry-pick of kept0002", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("clearing the committer fails", func(t *testing.T) {
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
		expectCompactSetup(mock, initial, boundary, kept)
		for _, k := range kept {
			expectReplayOf(mock, k)
		}
		mock.ExpectExec(regexp.QuoteMeta(clearCommitterQuery)).
			WillReturnError(errors.New("SET refused"))
		// The cleanup still reaches the server on the live session, so no
		// compact-tmp is left to block the next compaction; only then is the
		// pinned connection discarded rather than returned to its pool with
		// a frozen committer and date.
		expectAbandon(mock)
		mock.ExpectClose()

		err = Compact(context.Background(), conn, initial.Hash, boundary.Hash, 2, keptHashes(kept))
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

	// rebuilt lists the read-back newest first: kept[2], kept[1], kept[0],
	// the squashed base, the initial commit.
	for _, tc := range []struct {
		name   string
		mutate func(rebuilt []compactCommit)
		want   string
	}{
		{"replayed commit dated 1 ms late",
			func(r []compactCommit) { r[1].CommitterDate = r[1].CommitterDate.Add(time.Millisecond) },
			"rebuilt commit for kept0002 does not preserve the original"},
		{"replayed commit committed by the session",
			func(r []compactCommit) { r[1].Committer, r[1].CommitterEmail = "root", "root@localhost" },
			"rebuilt commit for kept0002 does not preserve the original"},
		{"replayed author rewritten to the committer",
			func(r []compactCommit) {
				r[1].Author, r[1].AuthorEmail, r[1].AuthorDate = r[1].Committer, r[1].CommitterEmail, r[1].CommitterDate
			},
			"rebuilt commit for kept0002 does not preserve the original"},
		{"replayed author date lost",
			func(r []compactCommit) { r[1].AuthorDate = r[1].CommitterDate },
			"rebuilt commit for kept0002 does not preserve the original"},
		{"squashed base dated off the boundary",
			func(r []compactCommit) { r[3].CommitterDate = r[3].CommitterDate.Add(time.Second) },
			"rebuilt squashed base is dated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()
			initial, boundary, kept := compactTestCommits()
			msg := expectCompactReplay(mock, initial, boundary, kept)

			rebuilt := compactRebuiltLog(initial, boundary, kept, msg)
			tc.mutate(rebuilt)
			mock.ExpectQuery(logQuery).WillReturnRows(compactLogRows(rebuilt))
			expectAbandon(mock)

			err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, keptHashes(kept))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compact error = %v, want a read-back refusal containing %q", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err) // main was never reset onto the rebuilt chain
			}
		})
	}
}
