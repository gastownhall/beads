package issueops

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestMintedIDCreateInsertsCreateOnly pins the row write of a create whose id
// was minted, not supplied (bd-t4h0n7, PR #6754): a minted row meets the
// create-only guard — the shard coordination write, then the one occupancy
// probe over both planes and both tombstone tables — and is inserted STRICT,
// on an options copy, whatever the caller asked for. An id taken between the
// mint's probe and the insert is refused ErrAlreadyExists; it never upserts
// over its occupant (default options) and never silently skips it
// (ConflictSkip). A caller that named its id keeps the caller's options.
func TestMintedIDCreateInsertsCreateOnly(t *testing.T) {
	const actor = "minter"
	createdAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	newIssue := func() *types.Issue {
		return &types.Issue{
			Title: "minted row", Description: "d", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, CreatedAt: createdAt,
		}
	}
	// minted is the nonce-0 length-6 candidate the hash mint draws first once
	// the adaptive length read fails (the generator then starts at 6).
	minted := idgen.GenerateHashID("bd", "minted row", "d", actor, createdAt, 6, 0)

	// expectMint expects the hash mint's reads: hash mode (no config row), the
	// adaptive length unavailable (start at 6), and the one idTaken probe that
	// finds the candidate free in both planes and both tombstone tables.
	expectMint := func(mock sqlmock.Sqlmock) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
			WithArgs("issue_id_mode").WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("SELECT").WillReturnError(errors.New("adaptive length unavailable"))
		mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(minted, minted, minted, minted).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	}
	// expectGuard expects the create-only guard a minted row now meets: the
	// coordination write, then the occupancy probe. live reports what the
	// probe finds.
	expectGuard := func(mock sqlmock.Sqlmock, live bool) {
		mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		rows := sqlmock.NewRows([]string{"src", "closed_at"})
		if live {
			rows.AddRow("issues", nil)
		}
		mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).
			WithArgs(minted, minted, minted, minted).WillReturnRows(rows)
	}

	t.Run("occupied at the guard is refused, not upserted", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectMint(mock)
		expectGuard(mock, true)

		bc := &BatchContext{ConfigPrefix: "bd", Opts: storage.BatchCreateOptions{}}
		issue := newIssue()
		_, err := CreateIssueInTxWithResult(context.Background(), db, bc, issue, actor)
		if !errors.Is(err, storage.ErrAlreadyExists) {
			t.Fatalf("CreateIssueInTxWithResult(minted id occupied) = %v, want ErrAlreadyExists", err)
		}
		if issue.ID != minted {
			t.Fatalf("issue.ID = %q, want the minted %q", issue.ID, minted)
		}
	})

	t.Run("conflict-skip options do not silently skip a minted row", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectMint(mock)
		expectGuard(mock, true)

		bc := &BatchContext{ConfigPrefix: "bd", Opts: storage.BatchCreateOptions{ConflictSkip: true}}
		issue := newIssue()
		_, err := CreateIssueInTxWithResult(context.Background(), db, bc, issue, actor)
		if !errors.Is(err, storage.ErrAlreadyExists) {
			t.Fatalf("CreateIssueInTxWithResult(minted id occupied, ConflictSkip) = %v, want ErrAlreadyExists — a minted row is never the caller's to skip", err)
		}
	})

	t.Run("occupied at the insert is refused, not upserted", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectMint(mock)
		expectGuard(mock, false)
		// The strict insert itself meets the occupant a concurrent writer
		// slipped in after the probe: a primary-key violation is translated to
		// ErrAlreadyExists. The expectation is the exact strict statement, so
		// an ON DUPLICATE KEY UPDATE variant would not match it.
		mock.ExpectExec(regexp.QuoteMeta(issueInsertSQL("issues", 1, ""))).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry '" + minted + "' for key 'issues.PRIMARY'"})

		bc := &BatchContext{ConfigPrefix: "bd", Opts: storage.BatchCreateOptions{}}
		issue := newIssue()
		_, err := CreateIssueInTxWithResult(context.Background(), db, bc, issue, actor)
		if !errors.Is(err, storage.ErrAlreadyExists) {
			t.Fatalf("CreateIssueInTxWithResult(strict insert hit a duplicate) = %v, want ErrAlreadyExists", err)
		}
	})
}
