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
// items names) is skipped like a held one, without the occupancy read, and
// the counter advanced again.
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

// expectCounterBump expects one NextCounterIDTx increment that hands back next.
func expectCounterBump(mock sqlmock.Sqlmock, prefix string, next int) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?")).
		WithArgs(prefix).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
		WithArgs(prefix).WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(next))
}

// expectIDTaken expects the one occupancy read IDTakenInTx makes for id.
func expectIDTaken(mock sqlmock.Sqlmock, id string, taken bool) {
	n := 0
	if taken {
		n = 1
	}
	mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(id, id, id, id).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(n))
}

// TestGenerateIssueIDCounterModeJumpsPastHeldIDs pins the counter's collision
// rule (bd-t4h0n7): a candidate a live row of either plane or a tombstone holds
// is never handed out; on the first one the counter jumps, once, to one past
// the highest numeric suffix the prefix holds in issues, wisps and both
// tombstone tables, and a free candidate is taken as it comes.
func TestGenerateIssueIDCounterModeJumpsPastHeldIDs(t *testing.T) {
	held := func(ids ...string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"id"})
		for _, id := range ids {
			rows.AddRow(id)
		}
		return rows
	}
	pattern := `bd-%`
	expectScan := func(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
		mock.ExpectQuery(regexp.QuoteMeta(heldCounterIDsSQL)).
			WithArgs(pattern, pattern, pattern, pattern).WillReturnRows(rows)
	}
	expectRaise := func(mock sqlmock.Sqlmock, to int) {
		mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = ? WHERE prefix = ? AND last_id < ?")).
			WithArgs(to, "bd", to).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	cases := []struct {
		name    string
		claimed map[string]bool
		expect  func(mock sqlmock.Sqlmock)
		want    string
		wantErr string
	}{
		{
			name: "a free candidate is taken as it comes, with 100 held",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", false)
			},
			want: "bd-2",
		},
		{
			name: "explicit 2 and 100 ahead of the counter",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				// Child, non-numeric, signed, zero and unrepresentable
				// suffixes hold no counter value.
				expectScan(mock, held("bd-2", "bd-100", "bd-100.1", "bd-7x", "bd-+900", "bd-0", "bd-99999999999", "bd-2147483647"))
				expectRaise(mock, 100)
				expectCounterBump(mock, "bd", 101)
				expectIDTaken(mock, "bd-101", false)
			},
			want: "bd-101",
		},
		{
			name: "a long retired run costs one read",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				expectScan(mock, held("bd-2", "bd-3", "bd-4", "bd-5", "bd-6"))
				expectRaise(mock, 6)
				expectCounterBump(mock, "bd", 7)
				expectIDTaken(mock, "bd-7", false)
			},
			want: "bd-7",
		},
		{
			name: "an all-digit hash suffix holds its value",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				expectScan(mock, held("bd-2", "bd-4821"))
				expectRaise(mock, 4821)
				expectCounterBump(mock, "bd", 4822)
				expectIDTaken(mock, "bd-4822", false)
			},
			want: "bd-4822",
		},
		{
			name:    "the jump reads once, then claimed and held values are walked",
			claimed: map[string]bool{"bd-101": true},
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				expectScan(mock, held("bd-2", "bd-100"))
				expectRaise(mock, 100)
				expectCounterBump(mock, "bd", 101)
				expectCounterBump(mock, "bd", 102)
				expectIDTaken(mock, "bd-102", true)
				expectCounterBump(mock, "bd", 103)
				expectIDTaken(mock, "bd-103", false)
			},
			want: "bd-103",
		},
		{
			name: "no numeric suffix held leaves the counter alone",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				expectScan(mock, held())
				expectCounterBump(mock, "bd", 3)
				expectIDTaken(mock, "bd-3", false)
			},
			want: "bd-3",
		},
		{
			name: "a read error fails the mint",
			expect: func(mock sqlmock.Sqlmock) {
				expectCounterBump(mock, "bd", 2)
				expectIDTaken(mock, "bd-2", true)
				mock.ExpectQuery(regexp.QuoteMeta(heldCounterIDsSQL)).WillReturnError(errors.New("scan failed"))
			},
			wantErr: "scan failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newIDRetentionMock(t)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
				WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
			tc.expect(mock)
			id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", tc.claimed)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("GenerateIssueIDInTable(counter) = %q, %v; want an error naming %q", id, err, tc.wantErr)
				}
			} else if err != nil || id != tc.want {
				t.Fatalf("GenerateIssueIDInTable(counter) = %q, %v; want %q", id, err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCounterJumpEscapesLikeWildcardsInThePrefix pins that the held-id read
// matches the prefix literally: a prefix holding '_' or '%' must not read
// every id one character away from it.
func TestCounterJumpEscapesLikeWildcardsInThePrefix(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	pattern := `a\_b\%-%`
	mock.ExpectQuery(regexp.QuoteMeta(heldCounterIDsSQL)).
		WithArgs(pattern, pattern, pattern, pattern).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("a_b%-9").AddRow("axb%-50"))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = ? WHERE prefix = ? AND last_id < ?")).
		WithArgs(9, "a_b%", 9).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := raiseCounterPastHeldIDsTx(context.Background(), db, "a_b%"); err != nil {
		t.Fatalf("raiseCounterPastHeldIDsTx() = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
