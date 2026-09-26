package issueops

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateIssueInTxWithResultCoordinationLockCollisionRetries covers be-p1gl1
// round 2: a collision surfaced by EnsureIssueIDAvailableInTx's coordination
// lock — not by InsertIssueIfNew's duplicate-key error, which
// create_auto_mint_race_test.go's TestCreateIssueInTxWithResultRacingAutoMintDoesNotSilentlyOverwrite
// (be-tl5v2) already covers — must also retry with a freshly minted ID rather
// than hard-fail, for an auto-minted create with attempts remaining.
//
// Before this fix, CreateIssueInTxWithResult checked
// wasAutoMinted/attempt/storage.ErrAlreadyExists only after InsertIssueIfNew;
// the EnsureIssueIDAvailableInTx branch a few lines above it returned any
// error immediately, with no equivalent check. A collision caught by the
// coordination lock — plausible whenever a second writer's commit becomes
// visible in the window between this attempt's own mint probe and its
// (later, separate) coordination-lock read, even though the two racing
// writers picked the same candidate because neither commit was visible to the
// other's mint probe — hard-failed the create even though the identical
// "auto-minted, attempts remain" situation already succeeds via remint one
// branch down.
//
// Forced deterministically via a scripted mock, not real concurrency or
// goroutine timing: the first minted candidate's coordination-lock check is
// scripted to find the ID already occupied (count=1), reproducing that
// visibility window without needing an actual second writer. The script only
// has enough calls staged for ONE remint-and-retry; pre-fix code returns
// before consuming any of it, so the unambiguous RED signal is
// CreateIssueInTxWithResult returning a non-nil error where the fixed code
// returns nil.
func TestCreateIssueInTxWithResultCoordinationLockCollisionRetries(t *testing.T) {
	ctx := context.Background()
	createdAt := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	bc := &BatchContext{Opts: storage.BatchCreateOptions{}, SkipChildCounterReconcile: true}

	issue := &types.Issue{
		Title:       "coordination lock collision fixture",
		Description: "coordination lock collision fixture",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   createdAt,
		Ephemeral:   true,
	}

	mockCountRows := func(n int) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"count"}).AddRow(n)
	}

	// expectMint scripts one full assignCreateIssueIDInTx call routed to the
	// wisps table — identical in shape to create_auto_mint_race_test.go's
	// helper of the same name: GetAdaptiveIDLengthTx's own count, the three
	// (unconfigured — each errors and falls back to its default)
	// adaptive-config lookups, then the candidate probe loop. nonceCounts is
	// the COUNT(*) answer for each candidate tried in order — every value
	// before the last must be >0 (occupied, so GenerateIssueIDInTable advances
	// to the next nonce) and the last must be 0 (free, accepted).
	expectMint := func(mock sqlmock.Sqlmock, nonceCounts ...int) {
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(0))
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("(?s).*").WillReturnError(sql.ErrNoRows)
		for _, n := range nonceCounts {
			mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(n))
		}
	}
	// expectCoordinationLockCollision scripts one EnsureIssueIDAvailableInTx
	// call that finds the freshly minted candidate already occupied in
	// "issues" — the loop returns as soon as one table's count is >0, so
	// "wisps" is never queried.
	expectCoordinationLockCollision := func(mock sqlmock.Sqlmock) {
		mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("(?s).*").WillReturnRows(mockCountRows(1))
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
	expectEventRecorded := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery("(?s).*").WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectExec("(?s).*").WillReturnResult(sqlmock.NewResult(0, 1))
	}

	_, mock, tx := beginMockTx(t)

	// Attempt 1: mint succeeds immediately, but by the time the coordination
	// lock reads "issues" a moment later, the candidate is occupied.
	expectMint(mock, 0)
	expectCoordinationLockCollision(mock)

	// Attempt 2 (only reached by the fixed code): remint a fresh candidate,
	// coordination lock finds it free this time, and the rest of the create
	// proceeds and succeeds normally.
	expectMint(mock, 0)
	expectCoordinationLock(mock)
	expectCrossTableCheck(mock)
	expectExistingCount(mock)
	expectInsertSucceeds(mock)
	expectEventRecorded(mock)
	mock.ExpectCommit()

	result, err := CreateIssueInTxWithResult(ctx, tx, bc, issue, "tester")
	if err != nil {
		t.Fatalf("CreateIssueInTxWithResult: %v (a coordination-lock collision on an auto-minted create must retry with a fresh ID, not hard-fail)", err)
	}
	if issue.ID == "" {
		t.Fatal("issue.ID was not minted")
	}
	if !result.ChangedTables["wisps"] {
		t.Fatalf("result.ChangedTables = %+v, want wisps marked changed", result.ChangedTables)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("tx.Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
