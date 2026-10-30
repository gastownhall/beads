package versioncontrolops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
)

// compactLogQuery is the full-log read Compact calls before touching
// history (and, see the AC-5 version, again to verify the rebuilt chain).
const compactLogQuery = "SELECT commit_hash, committer, email, date, message, commit_order FROM dolt_log ORDER BY date DESC"

// compactLogRows builds dolt_log rows for one full log read.
func compactLogRows(t *testing.T, commits []storage.CommitInfo) *sqlmock.Rows {
	t.Helper()
	rows := sqlmock.NewRows([]string{"commit_hash", "committer", "email", "date", "message", "commit_order"})
	for _, c := range commits {
		rows.AddRow(c.Hash, c.Author, c.Email, c.Date, c.Message, c.CommitOrder)
	}
	return rows
}

// expectCompactReplay programs the sqlmock expectations for a compact run
// that gets all the way through the replay: the preflight log read, the
// branch setup, the squashed base (dated as the boundary), and the
// cherry-pick + metadata-restore pairs. It returns the squashed-base
// message.
func expectCompactReplay(t *testing.T, mock sqlmock.Sqlmock, initial, boundary storage.CommitInfo, kept []storage.CommitInfo) string {
	t.Helper()
	preflight := make([]storage.CommitInfo, 0, len(kept)+2)
	for i := len(kept) - 1; i >= 0; i-- { // newest date first, as Log orders
		preflight = append(preflight, kept[i])
	}
	preflight = append(preflight, boundary, initial)
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(t, preflight))

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
		WithArgs(msg, commitDateArg(boundary.Date)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, k := range kept {
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).
			WithArgs(k.Hash).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?, '--author', ?)")).
			WithArgs(commitDateArg(k.Date), k.Author+" <"+k.Email+">").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	return msg
}

// compactRebuiltLog builds the log rows Compact expects to read back after
// the replay: the replayed commits carrying their originals' metadata
// (newest date first, as Log orders), then the squashed base dated as the
// boundary, then the initial commit.
func compactRebuiltLog(initial, boundary storage.CommitInfo, kept []storage.CommitInfo, msg string) []storage.CommitInfo {
	rebuilt := make([]storage.CommitInfo, 0, len(kept)+2)
	for i := len(kept) - 1; i >= 0; i-- {
		rebuilt = append(rebuilt, storage.CommitInfo{
			Hash: fmt.Sprintf("rebuilt-%d", i+1), Author: kept[i].Author, Email: kept[i].Email,
			Date: kept[i].Date, Message: kept[i].Message, CommitOrder: int64(i) + 3,
		})
	}
	rebuilt = append(rebuilt,
		storage.CommitInfo{Hash: "rebuilt-base", Author: "beads", Email: "beads@local",
			Date: boundary.Date, Message: msg, CommitOrder: 2},
		storage.CommitInfo{Hash: initial.Hash, Author: initial.Author, Email: initial.Email,
			Date: initial.Date, Message: initial.Message, CommitOrder: initial.CommitOrder},
	)
	return rebuilt
}

// compactTestCommits returns the commit fixtures: an initial commit, a
// boundary commit, and three kept commits — two of them in the same second
// 333 ms apart (one of those empty), and one dated in a non-UTC fixed zone
// whose message starts with a dash.
func compactTestCommits() (initial, boundary storage.CommitInfo, kept []storage.CommitInfo) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tz := time.FixedZone("Non-UTC", 9*3600+1800)
	initial = storage.CommitInfo{
		Hash: "initial-hash", Author: "Bees", Email: "bees@example.com",
		Date: base.Add(-72 * time.Hour), Message: "bd init", CommitOrder: 1,
	}
	boundary = storage.CommitInfo{
		Hash: "boundary-hash", Author: "Bees", Email: "bees@example.com",
		Date: base.Add(-24 * time.Hour), Message: "old edit", CommitOrder: 2,
	}
	kept = []storage.CommitInfo{
		{
			Hash: "kept-hash-1", Author: "Original Author", Email: "orig@example.com",
			Date: base.Add(123 * time.Millisecond), Message: "edit A", CommitOrder: 4,
		},
		{
			Hash: "kept-hash-2", Author: "Original Author", Email: "orig@example.com",
			Date: base.Add(456 * time.Millisecond), Message: "an empty commit", CommitOrder: 5,
		},
		{
			Hash: "kept-hash-3", Author: "Original Author", Email: "orig@example.com",
			Date:    base.Add(time.Hour).Add(789 * time.Millisecond).In(tz),
			Message: "- leading dash edit B", CommitOrder: 6,
		},
	}
	return initial, boundary, kept
}

// TestCompactRestoresCommitMetadata covers #7294 at the statement level:
// Compact reads the kept commits' original metadata before creating
// anything, each cherry-pick is followed by a metadata restore so the
// replayed commit keeps its original date to the millisecond (AC-1) and
// its original committer and email (AC-2), the squashed base is dated as
// the boundary (AC-3), and the rebuilt chain is read back and verified
// before main moves onto it (AC-5).
func TestCompactRestoresCommitMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	initial, boundary, kept := compactTestCommits()
	recentHashes := []string{kept[0].Hash, kept[1].Hash, kept[2].Hash} // ascending commit_order

	msg := expectCompactReplay(t, mock, initial, boundary, kept)

	// Read-back verification, then the swap.
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(t, compactRebuiltLog(initial, boundary, kept, msg)))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--hard', 'compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, recentHashes); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCompactRefusesUnverifiableReplay covers the refusals that keep
// compaction from destroying history it cannot faithfully replay (#7294):
// a hash the log does not know, and a committer the --author argument
// cannot carry, refuse before anything is created; a rebuilt chain that
// does not read back as its original refuses after the replay, with the
// deferred cleanup returning to main and deleting the temp branch without
// ever resetting main onto the unverifiable chain.
func TestCompactRefusesUnverifiableReplay(t *testing.T) {
	logQuery := regexp.QuoteMeta(compactLogQuery)

	hashes := func(kept []storage.CommitInfo) []string {
		out := make([]string, len(kept))
		for i, k := range kept {
			out[i] = k.Hash
		}
		return out
	}

	t.Run("recent hash not in the log", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		mock.ExpectQuery(logQuery).
			WillReturnRows(compactLogRows(t, []storage.CommitInfo{kept[2], kept[0], boundary, initial})) // kept[1] unknown
		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, hashes(kept))
		if err == nil || !strings.Contains(err.Error(), "kept-hash-2 is not in the log") {
			t.Fatalf("Compact error = %v, want a refusal naming kept-hash-2", err)
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
			WillReturnRows(compactLogRows(t, []storage.CommitInfo{kept[2], kept[1], kept[0], initial})) // boundary unknown
		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, hashes(kept))
		if err == nil || !strings.Contains(err.Error(), "boundary-hash is not in the log") {
			t.Fatalf("Compact error = %v, want a refusal naming boundary-hash", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*storage.CommitInfo)
		want   string
	}{
		{"committer name is empty", func(c *storage.CommitInfo) { c.Author = "" }, "committer name is empty"},
		{"committer name has )", func(c *storage.CommitInfo) { c.Author = "Bad)Name" }, "contains ')'"},
		{"committer email has <", func(c *storage.CommitInfo) { c.Email = "a<b@example.com" }, "contains one of ')', '<', '>'"},
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
				WillReturnRows(compactLogRows(t, []storage.CommitInfo{kept[2], kept[1], kept[0], boundary, initial}))
			err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, hashes(kept))
			if err == nil || !strings.Contains(err.Error(), "kept-has") ||
				!strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--days") {
				t.Fatalf("Compact error = %v, want a refusal naming kept-hash-2 (%s) and --days", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err) // nothing else ran: no temp branch was created
			}
		})
	}

	t.Run("rebuilt commit differs on read-back", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer db.Close()
		initial, boundary, kept := compactTestCommits()
		msg := expectCompactReplay(t, mock, initial, boundary, kept)

		rebuilt := compactRebuiltLog(initial, boundary, kept, msg)
		rebuilt[1].Date = rebuilt[1].Date.Add(time.Millisecond) // kept[1]'s replay, 1 ms late
		mock.ExpectQuery(logQuery).WillReturnRows(compactLogRows(t, rebuilt))

		// The deferred cleanup returns to main and deletes the temp branch;
		// the hard reset onto the unverifiable chain never runs.
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('main')")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('-D', 'compact-tmp')")).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err = Compact(context.Background(), db, initial.Hash, boundary.Hash, 2, hashes(kept))
		if err == nil || !strings.Contains(err.Error(), "kept-has") {
			t.Fatalf("Compact error = %v, want a read-back refusal naming kept-hash-2", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err) // main was never reset onto the rebuilt chain
		}
	})
}

// TestAuthorArg pins which identities the DOLT_COMMIT --author argument can
// carry: any non-empty name without ')' and any email free of ')<>'; an
// empty email renders as 'Name <>'.
func TestAuthorArg(t *testing.T) {
	for _, tc := range []struct {
		name, email, want string
		wantErr           bool
	}{
		{"Bees", "bees@example.com", "Bees <bees@example.com>", false},
		{"Original Author", "", "Original Author <>", false},
		{"", "bees@example.com", "", true},
		{"Bad)Name", "bees@example.com", "", true},
		{"Bees", "a<b@example.com", "", true},
		{"Bees", "b>@example.com", "", true},
		{"Bees", "bees)@example.com", "", true},
	} {
		got, err := authorArg(tc.name, tc.email)
		if tc.wantErr {
			if err == nil {
				t.Errorf("authorArg(%q, %q) = %q, want an error", tc.name, tc.email, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("authorArg(%q, %q): %v", tc.name, tc.email, err)
			continue
		}
		if got != tc.want {
			t.Errorf("authorArg(%q, %q) = %q, want %q", tc.name, tc.email, got, tc.want)
		}
	}
}
