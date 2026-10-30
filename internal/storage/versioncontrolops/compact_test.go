package versioncontrolops

import (
	"context"
	"fmt"
	"regexp"
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
			Date: base.Add(time.Hour).Add(789 * time.Millisecond).In(tz),
			Message: "- leading dash edit B", CommitOrder: 6,
		},
	}
	return initial, boundary, kept
}

// TestCompactRestoresCommitMetadata covers #7294 at the statement level:
// Compact reads the kept commits' original metadata before creating
// anything, and each cherry-pick is followed by a metadata restore so the
// replayed commit keeps its original date to the millisecond (AC-1) and its
// original committer and email (AC-2).
func TestCompactRestoresCommitMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	initial, boundary, kept := compactTestCommits()
	recentHashes := []string{kept[0].Hash, kept[1].Hash, kept[2].Hash} // ascending commit_order

	// Preflight log read, newest date first.
	mock.ExpectQuery(regexp.QuoteMeta(compactLogQuery)).
		WillReturnRows(compactLogRows(t, []storage.CommitInfo{kept[2], kept[1], kept[0], boundary, initial}))

	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_BRANCH('compact-tmp', ?)")).
		WithArgs(boundary.Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHECKOUT('compact-tmp')")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_RESET('--soft', ?)")).
		WithArgs(initial.Hash).
		WillReturnResult(sqlmock.NewResult(0, 1))
	msg := fmt.Sprintf("compact: squash %d commits into base snapshot", 2)
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('-Am', ?)")).
		WithArgs(msg).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, k := range kept {
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_CHERRY_PICK('--allow-empty', ?)")).
			WithArgs(k.Hash).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_COMMIT('--amend', '--allow-empty', '--date', ?, '--author', ?)")).
			WithArgs(commitDateArg(k.Date), k.Author+" <"+k.Email+">").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
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
