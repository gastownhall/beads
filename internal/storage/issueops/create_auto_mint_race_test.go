package issueops

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssueInTxWithResultRacingAutoMintDoesNotSilentlyOverwrite covers
// be-tl5v2: two auto-minted creates that race the identical candidate ID,
// forced deterministically rather than left to real goroutine timing and
// natural hash-space collision odds — same Title/Description/actor/CreatedAt
// are idgen.GenerateHashID's only inputs besides length/nonce, so two issues
// sharing them collide on the very first candidate.
//
// Before the fix, CreateIssueInTxWithResult only forced CreateOnly (and the
// EnsureIssueIDAvailableInTx coordination lock that CreateOnly gates) when a
// caller explicitly opted in via bc.Opts.CreateOnly. A plain auto-minted
// create — modeled here with a zero-value BatchContext.Opts, the common
// caller shape — took the unconditional ON DUPLICATE KEY UPDATE upsert path,
// so a second writer racing the same candidate silently clobbered the first
// row instead of erroring or minting a distinct ID.
//
// This test's mock script encodes the FIXED call sequence: CreateOnly forced
// whenever the ID was auto-minted, with a bounded retry-and-remint on
// storage.ErrAlreadyExists. Pre-fix, a plain auto-minted create never calls
// EnsureIssueIDAvailableInTx at all, so the script mismatches — a scripted
// Exec where the unfixed code instead issues a Query next — at tx1 already,
// before the race even comes into play. That is still the correct RED
// signal: it proves CreateOnly-forcing is unconditionally absent today,
// which is the actual root cause.
//
// TestCrossProject_ConcurrentWrites (internal/storage/dolt) keeps exercising
// the same fix against a real concurrent Dolt backend; that coverage is
// non-deterministic (real goroutine timing) where this one is deterministic
// by construction, per this bead's own requirement.
func TestCreateIssueInTxWithResultRacingAutoMintDoesNotSilentlyOverwrite(t *testing.T) {
	ctx := context.Background()
	createdAt := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	bc := &BatchContext{Opts: storage.BatchCreateOptions{}, SkipChildCounterReconcile: true}

	newRacingIssue := func() *types.Issue {
		return &types.Issue{
			Title:       "racing auto-mint fixture",
			Description: "racing auto-mint collision fixture",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			CreatedAt:   createdAt,
			Ephemeral:   true,
		}
	}

	mockCountRows := func(n int) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"count"}).AddRow(n)
	}

	// expectMint scripts one full assignCreateIssueIDInTx call routed to the
	// wisps table: GetAdaptiveIDLengthTx's own count, the three (unconfigured
	// — each errors and falls back to its default) adaptive-config lookups,
	// then the candidate probe loop. nonceCounts is the COUNT(*) answer for
	// each candidate tried in order — every value before the last must be >0
	// (occupied, so GenerateIssueIDInTable advances to the next nonce) and
	// the last must be 0 (free, accepted).
	expectMint := func(mock sqlmock.Sqlmock, nonceCounts ...int) {
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		for _, n := range nonceCounts {
			mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(n))
		}
	}
	// expectCoordinationLock scripts one EnsureIssueIDAvailableInTx call that
	// finds the freshly minted candidate free in both tables.
	expectCoordinationLock := func(mock sqlmock.Sqlmock) {
		mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
	}
	expectCrossTableCheck := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
	}
	expectExistingCount := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
	}
	expectInsertSucceeds := func(mock sqlmock.Sqlmock) {
		mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	expectInsertFailsDuplicate := func(mock sqlmock.Sqlmock) {
		mock.ExpectExec("(?s).*").WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	}
	expectEventRecorded := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
	}

	// --- tx1: first writer, uncontested. ---
	_, mock1, tx1 := beginMockTx(t)
	expectMint(mock1, 0)
	expectCoordinationLock(mock1)
	expectCrossTableCheck(mock1)
	expectExistingCount(mock1)
	expectInsertSucceeds(mock1)
	expectEventRecorded(mock1)
	mock1.ExpectCommit()

	issue1 := newRacingIssue()
	result1, err := CreateIssueInTxWithResult(ctx, tx1, bc, issue1, "tester")
	if err != nil {
		t.Fatalf("tx1 CreateIssueInTxWithResult: %v", err)
	}
	if issue1.ID == "" {
		t.Fatal("tx1: issue1.ID was not minted")
	}
	if !result1.ChangedTables["wisps"] {
		t.Fatalf("tx1: result.ChangedTables = %+v, want wisps marked changed", result1.ChangedTables)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1.Commit: %v", err)
	}
	if err := mock1.ExpectationsWereMet(); err != nil {
		t.Fatalf("tx1: unmet SQL expectations: %v", err)
	}

	// --- tx2: second writer racing the identical candidate (same Title,
	// Description, actor, CreatedAt — GenerateHashID's only inputs besides
	// nonce/length — so its first candidate probe computes the same
	// candidate as issue1's, run before issue1's commit was visible to it).
	// Must not silently overwrite issue1's row: the fix retries with a fresh
	// candidate — the real unique constraint on the now-committed row, not
	// the racy pre-insert probes, is what actually catches the collision —
	// and lands on a second, distinct ID. ---
	_, mock2, tx2 := beginMockTx(t)
	expectMint(mock2, 0) // same candidate as issue1's: minted before issue1's commit was visible.
	expectCoordinationLock(mock2)
	expectCrossTableCheck(mock2)
	expectExistingCount(mock2)
	expectInsertFailsDuplicate(mock2)
	expectMint(mock2, 1, 0) // retry: re-mint sees the now-committed candidate occupied (count=1), advances to the next nonce.
	expectCoordinationLock(mock2)
	expectCrossTableCheck(mock2)
	expectExistingCount(mock2)
	expectInsertSucceeds(mock2)
	expectEventRecorded(mock2)
	mock2.ExpectCommit()

	issue2 := newRacingIssue()
	result2, err := CreateIssueInTxWithResult(ctx, tx2, bc, issue2, "tester")
	if err != nil {
		t.Fatalf("tx2 CreateIssueInTxWithResult: %v", err)
	}
	if issue2.ID == "" {
		t.Fatal("tx2: issue2.ID was not minted")
	}
	if issue2.ID == issue1.ID {
		t.Fatalf("tx2 silently reused issue1's ID %q instead of minting a distinct one", issue1.ID)
	}
	if !result2.ChangedTables["wisps"] {
		t.Fatalf("tx2: result.ChangedTables = %+v, want wisps marked changed", result2.ChangedTables)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2.Commit: %v", err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatalf("tx2: unmet SQL expectations: %v", err)
	}
}
