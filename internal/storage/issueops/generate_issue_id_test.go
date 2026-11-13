package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/types"
)

// TestGenerateIssueIDPassesOverAClaimedCandidate pins the batch half of the
// hash mint: a candidate the caller's batch already claimed is passed over for
// the next nonce exactly like one a row holds, and without a probe. The rows
// the batch will write are in no table yet, so a probe would answer "free" and
// hand two items the same id.
func TestGenerateIssueIDPassesOverAClaimedCandidate(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	issue := &types.Issue{Title: "twin", Description: "d", CreatedAt: idRetentionClosedAt}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnError(sql.ErrNoRows)
	// The adaptive length is unavailable, so the generator starts at 6.
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("adaptive length unavailable"))
	claimed := idgen.GenerateHashID("bd", issue.Title, issue.Description, "actor", issue.CreatedAt, 6, 0)
	next := idgen.GenerateHashID("bd", issue.Title, issue.Description, "actor", issue.CreatedAt, 6, 1)
	mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(next, next, next, next).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", issue, "actor", map[string]bool{claimed: true})
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable() = %v", err)
	}
	if id != next {
		t.Fatalf("GenerateIssueIDInTable() = %q, want %q, the next nonce past the claimed %q", id, next, claimed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateIssueIDCounterModeWalksPastAClaimedValue pins the same rule in
// counter mode: a value the batch already claimed (an explicit id one of its
// items names) is skipped like a taken one, without the taken read, and the
// counter advanced again.
func TestGenerateIssueIDCounterModeWalksPastAClaimedValue(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
	bump := func(next int) {
		mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?")).
			WithArgs("bd").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
			WithArgs("bd").WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(next))
	}
	bump(5)
	bump(6)
	mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs("bd-6", "bd-6", "bd-6", "bd-6").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", map[string]bool{"bd-5": true})
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter) = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("GenerateIssueIDInTable(counter) = %q, want bd-6 past the claimed bd-5", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// expectCounterCandidate expects one counter-mode candidate: the bump, the
// read-back of next, and the one both-planes taken probe that decides it.
func expectCounterCandidate(mock sqlmock.Sqlmock, next int, taken bool) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?")).
		WithArgs("bd").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
		WithArgs("bd").WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(next))
	id := fmt.Sprintf("bd-%d", next)
	count := 0
	if taken {
		count = 1
	}
	mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(id, id, id, id).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(count))
}

// TestGenerateIssueIDCounterModeWalksPastALiveRow pins GH#8456: a counter
// value a live row already holds (an explicit, imported or wisp id, none of
// which move the counter) is skipped like a retired one, decided by the same
// one both-planes probe hash mode asks, instead of being handed out and then
// overwritten by an upsert create or refused by a create-only one.
func TestGenerateIssueIDCounterModeWalksPastALiveRow(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
	expectCounterCandidate(mock, 5, true)
	expectCounterCandidate(mock, 6, false)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter) = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("GenerateIssueIDInTable(counter) = %q, want bd-6 past the live bd-5", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// expectHeldCounterIDs expects the one-read skip of a held run: the counter's
// current value, then the one statement that reads every id under the prefix
// from both row tables and both tombstone tables. The statement is spelled
// out, so a read that drops a table fails here.
func expectHeldCounterIDs(mock sqlmock.Sqlmock, lastID int, ids ...string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
		WithArgs("bd").WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(lastID))
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues WHERE id LIKE CONCAT(?, '-%') "+
		"UNION ALL SELECT id FROM wisps WHERE id LIKE CONCAT(?, '-%') "+
		"UNION ALL SELECT id FROM id_tombstones WHERE id LIKE CONCAT(?, '-%') "+
		"UNION ALL SELECT id FROM wisp_id_tombstones WHERE id LIKE CONCAT(?, '-%')")).
		WithArgs("bd", "bd", "bd", "bd").WillReturnRows(rows)
}

// TestGenerateIssueIDCounterModeSkipsAHeldRunLongerThanTheWalk pins the long
// lag of GH#8456: more held values in a row than one walk probes, as a bulk
// import ahead of an existing counter leaves. Giving up at the bound failed
// every auto create for good, because the rollback undoes the bumps. Past the
// bound the counter skips the rest of the run in one read and lands on the
// first free value, the one an unbounded walk would reach: a claimed value
// extends the run, a child id or a zero-padded id holds nothing, and a
// far-ahead id does not drag the counter after it.
func TestGenerateIssueIDCounterModeSkipsAHeldRunLongerThanTheWalk(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
	for n := 1; n <= maxCounterSkips+1; n++ {
		expectCounterCandidate(mock, n, true)
	}
	expectHeldCounterIDs(mock, maxCounterSkips+1,
		"bd-7", "bd-1002", "bd-1003", "bd-1004", "bd-1006.1", "bd-01006", "bd-5000")
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = ? WHERE prefix = ?")).
		WithArgs(1005, "bd").WillReturnResult(sqlmock.NewResult(0, 1))
	expectCounterCandidate(mock, 1006, false)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", map[string]bool{"bd-1005": true})
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter) = %v", err)
	}
	if id != "bd-1006" {
		t.Fatalf("GenerateIssueIDInTable(counter) = %q, want bd-1006 past the held bd-1002..bd-1004 and the claimed bd-1005", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateIssueIDCounterModeGivesUpAfterTheSkipBound pins the bound that
// still ends the walk: a counter that keeps landing on held values the
// one-read skip does not see (here it reads none, so it moves nothing) walks
// one more bound and fails loudly instead of walking forever.
func TestGenerateIssueIDCounterModeGivesUpAfterTheSkipBound(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
	for n := 1; n <= maxCounterSkips+1; n++ {
		expectCounterCandidate(mock, n, true)
	}
	expectHeldCounterIDs(mock, maxCounterSkips+1)
	for n := maxCounterSkips + 2; n <= 2*(maxCounterSkips+1); n++ {
		expectCounterCandidate(mock, n, true)
	}

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err == nil || !strings.Contains(err.Error(), "taken or claimed") {
		t.Fatalf("GenerateIssueIDInTable(counter) = %q, %v; want the skip-bound error", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestIDTakenReadsBothPlanesInOneStatement pins the probe both minters ask: a
// live row of EITHER plane or a tombstone in either tombstone table takes the
// id, in one statement, whichever plane the id is being minted for.
func TestIDTakenReadsBothPlanesInOneStatement(t *testing.T) {
	for _, held := range []int{0, 1} {
		t.Run(fmt.Sprintf("count %d", held), func(t *testing.T) {
			db, mock := newIDRetentionMock(t)
			mock.ExpectQuery(regexp.QuoteMeta(
				"SELECT (SELECT COUNT(*) FROM issues WHERE id = ?) + (SELECT COUNT(*) FROM wisps WHERE id = ?) + "+
					"(SELECT COUNT(*) FROM id_tombstones WHERE id = ?) + (SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ?)")).
				WithArgs("bd-1", "bd-1", "bd-1", "bd-1").
				WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(held))
			taken, err := IDTakenInTx(context.Background(), db, "bd-1")
			if err != nil {
				t.Fatalf("IDTakenInTx() = %v", err)
			}
			if taken != (held > 0) {
				t.Fatalf("IDTakenInTx() = %v for a count of %d", taken, held)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
