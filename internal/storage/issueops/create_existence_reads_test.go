package issueops

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
	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateOnlyCreateReadsEachPlaneOnce pins the existence reads of a
// create-only create with an explicit id: the create-only guard reads both
// planes (and both id tombstone tables) in its one occupancy statement, and
// neither the cross-plane collision check nor the insert re-reads a plane the
// guard just found empty. sqlmock refuses any statement not expected here, so
// a re-read fails the test.
func TestCreateOnlyCreateReadsEachPlaneOnce(t *testing.T) {
	mock, db := newBlockedStateResultMock(t)
	mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).
		WithArgs("bd-wisp-1", "bd-wisp-1", "bd-wisp-1", "bd-wisp-1").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO wisps")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.Background()
	opts := storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true}
	bc := &BatchContext{ConfigPrefix: "bd", Opts: opts}
	issue := &types.Issue{
		ID: "bd-wisp-1", Title: "op", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, NoHistory: true,
	}
	issueTable, _, outcome, err := prepareCreateIssueInTx(ctx, db, bc, issue, "tester")
	if err != nil || outcome != prepareWrite {
		t.Fatalf("prepareCreateIssueInTx = outcome %v, err %v; want prepareWrite, no error", outcome, err)
	}
	if issueTable != "wisps" {
		t.Fatalf("issueTable = %q, want wisps", issueTable)
	}
	isNew, staleRejected, err := insertIssueIfNewCached(ctx, db, issueTable, issue, opts, nil)
	if err != nil || !isNew || staleRejected {
		t.Fatalf("insertIssueIfNewCached = new %v, stale %v, err %v; want new", isNew, staleRejected, err)
	}
}

// TestConflictSkipCreateStillReadsExistingRow pins the read the create-only
// shortcut must keep: a ConflictSkip insert decides from the existing row's
// presence, so it still reads it.
func TestConflictSkipCreateStillReadsExistingRow(t *testing.T) {
	mock, db := newBlockedStateResultMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).
		WithArgs("bd-wisp-1").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	issue := &types.Issue{ID: "bd-wisp-1", Title: "op", Status: types.StatusOpen, NoHistory: true}
	opts := storage.BatchCreateOptions{CreateOnly: true, ConflictSkip: true}
	isNew, _, err := insertIssueIfNewCached(context.Background(), db, "wisps", issue, opts, nil)
	if err != nil || isNew {
		t.Fatalf("insertIssueIfNewCached = new %v, err %v; want a skipped existing row", isNew, err)
	}
}

// TestMintedIDCreateInsertsCreateOnly pins bd-t4h0n7 (PR #6754): a create that
// names no id inserts the id it was minted create-only, whatever the caller's
// options, so an occupant the mint did not see is never upserted over (default
// options) or silently treated as this create (ConflictSkip). The minted id
// takes the guard (coordination row, one occupancy read) and a strict INSERT;
// the matcher refuses any ON DUPLICATE KEY UPDATE outright.
func TestMintedIDCreateInsertsCreateOnly(t *testing.T) {
	at := time.Date(2025, 4, 5, 6, 7, 8, 0, time.UTC)
	minted := idgen.GenerateHashID("bd", "minted", "", "tester", at, 6, 0)
	noUpsert := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(actualSQL, "ON DUPLICATE KEY UPDATE") {
			return fmt.Errorf("an auto-minted create upserted: %s", actualSQL)
		}
		return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
	})
	for _, opts := range []struct {
		name string
		opts storage.BatchCreateOptions
	}{
		{"default options", storage.BatchCreateOptions{SkipPrefixValidation: true}},
		{"ConflictSkip", storage.BatchCreateOptions{SkipPrefixValidation: true, ConflictSkip: true}},
	} {
		for _, occupied := range []string{"at the guard", "at the insert"} {
			t.Run(opts.name+" occupied "+occupied, func(t *testing.T) {
				db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(noUpsert))
				if err != nil {
					t.Fatalf("sqlmock.New: %v", err)
				}
				t.Cleanup(func() { _ = db.Close() })
				// The mint: hash mode, length 6, and the candidate is free in
				// the snapshot the mint read.
				mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
					WithArgs("issue_id_mode").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery("SELECT").WillReturnError(errors.New("adaptive length unavailable"))
				mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(minted, minted, minted, minted).
					WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
				// The create-only guard: coordination row, one occupancy read.
				mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).
					WillReturnResult(sqlmock.NewResult(0, 1))
				occupancy := sqlmock.NewRows([]string{"src", "closed_at"})
				if occupied == "at the guard" {
					occupancy.AddRow("issues", nil)
				}
				mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs(minted, minted, minted, minted).
					WillReturnRows(occupancy)
				if occupied == "at the insert" {
					mock.ExpectExec(regexp.QuoteMeta("INSERT INTO issues")).
						WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate primary key"})
				}

				issue := &types.Issue{Title: "minted", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: at}
				bc := &BatchContext{ConfigPrefix: "bd", Opts: opts.opts}
				result, err := CreateIssueInTxWithResult(context.Background(), db, bc, issue, "tester")
				if !errors.Is(err, storage.ErrAlreadyExists) {
					t.Fatalf("CreateIssueInTxWithResult(minted %s) = %+v, %v; want ErrAlreadyExists", minted, result, err)
				}
				if bc.Opts.CreateOnly || bc.Opts.ConflictSkip != opts.opts.ConflictSkip {
					t.Fatalf("the caller's options changed to %+v", bc.Opts)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
