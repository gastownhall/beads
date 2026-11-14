package issueops

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// The counter mint's jump (bd-t4h0n7, PR #6754): the counter can lag behind
// ids the id space already holds — an explicit create need not advance it —
// so a counter candidate is probed like a hash one (IDTakenInTx, one read for
// both planes and both tombstone tables), and the first held candidate jumps
// the counter to the highest numeric suffix the id space holds for the prefix,
// one scan, at most once per mint.

// expectCounterMode expects the config read that routes the mint to counter
// mode.
func expectCounterMode(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
}

// bumpCounter expects one NextCounterIDTx round trip: the increment and the
// read.
func bumpCounter(mock sqlmock.Sqlmock, next int) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?")).
		WithArgs("bd").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
		WithArgs("bd").WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(next))
}

// probeTaken expects the one taken read of a counter candidate.
func probeTaken(mock sqlmock.Sqlmock, id string, count int) {
	mock.ExpectQuery(regexp.QuoteMeta(idTakenSQL)).WithArgs(id, id, id, id).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(count))
}

// expectJump expects the one-scan jump: every held id for the prefix, then
// the raise-only update to the highest numeric suffix those rows hold.
func expectJump(mock sqlmock.Sqlmock, held ...string) {
	rows := sqlmock.NewRows([]string{"id"})
	maxNum := 0
	for _, id := range held {
		rows.AddRow(id)
		if n, ok := counterNumericSuffix(id, "bd"); ok && n > maxNum {
			maxNum = n
		}
	}
	mock.ExpectQuery(regexp.QuoteMeta(counterHeldIDsScanSQL)).WithArgs("bd", "bd", "bd", "bd").
		WillReturnRows(rows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = ? WHERE prefix = ? AND last_id < ?")).
		WithArgs(maxNum, "bd", maxNum).WillReturnResult(sqlmock.NewResult(0, 1))
}

// TestCounterMintJumpsToPastTheHighestHeldSuffix: the counter at 1 hands out
// bd-2 next, which an explicit create holds in the issues plane; the jump
// scans both planes and both tombstone tables, counts only exact-prefix
// numeric suffixes (a dotted child and a hash-shaped id hold nothing back),
// raises the counter to the highest one held — bd-100, in the wisps plane —
// and the mint returns bd-101.
func TestCounterMintJumpsToPastTheHighestHeldSuffix(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCounterMode(mock)
	bumpCounter(mock, 2)
	probeTaken(mock, "bd-2", 1)
	expectJump(mock, "bd-2", "bd-100", "bd-2.3", "bd-wisp-7")
	bumpCounter(mock, 101)
	probeTaken(mock, "bd-101", 0)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter over the lag) = %v", err)
	}
	if id != "bd-101" {
		t.Fatalf("GenerateIssueIDInTable(counter over the lag) = %q, want bd-101, one past the highest held suffix", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCounterMintReturnsAFreeCandidateWithoutScanning: a candidate nothing
// holds is returned as-is, however many higher suffixes the id space holds —
// the scan runs only when a candidate is actually held, so the common case
// costs nothing beyond the one taken read it always paid.
func TestCounterMintReturnsAFreeCandidateWithoutScanning(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCounterMode(mock)
	bumpCounter(mock, 2)
	probeTaken(mock, "bd-2", 0)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter, free candidate) = %v", err)
	}
	if id != "bd-2" {
		t.Fatalf("GenerateIssueIDInTable(counter, free candidate) = %q, want bd-2", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCounterMintJumpsAtMostOnce: a writer that lands ON the jumped-to value
// after the scan (the race the scan cannot see) is walked past one increment
// at a time — the scan does not run a second time.
func TestCounterMintJumpsAtMostOnce(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCounterMode(mock)
	bumpCounter(mock, 2)
	probeTaken(mock, "bd-2", 1)
	expectJump(mock, "bd-2", "bd-100")
	bumpCounter(mock, 101)
	probeTaken(mock, "bd-101", 1) // the race: held after the scan saw it free
	bumpCounter(mock, 102)
	probeTaken(mock, "bd-102", 0)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter, held past the jump) = %v", err)
	}
	if id != "bd-102" {
		t.Fatalf("GenerateIssueIDInTable(counter, held past the jump) = %q, want bd-102", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCounterMintJumpsPastALongHeldRun: a sweep that retired a run of a
// thousand ids costs the mint ONE scan and one raise, not a thousand skips —
// the walk bound never comes into play for a contiguous held range.
func TestCounterMintJumpsPastALongHeldRun(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCounterMode(mock)
	bumpCounter(mock, 2)
	probeTaken(mock, "bd-2", 1)
	held := make([]string, 0, 1000)
	for n := 2; n <= 1001; n++ {
		held = append(held, fmt.Sprintf("bd-%d", n))
	}
	expectJump(mock, held...)
	bumpCounter(mock, 1002)
	probeTaken(mock, "bd-1002", 0)

	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor", nil)
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter, long retired run) = %v", err)
	}
	if id != "bd-1002" {
		t.Fatalf("GenerateIssueIDInTable(counter, long retired run) = %q, want bd-1002 after one jump", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCounterNumericSuffix pins the parser the seed and the jump share: an
// exact-prefix id with a numeric suffix and no child extension holds a
// counter value back; every other shape does not.
func TestCounterNumericSuffix(t *testing.T) {
	cases := []struct {
		id    string
		n     int
		valid bool
	}{
		{"bd-2", 2, true},
		{"bd-100", 100, true},
		{"bd-2.3", 0, false},                  // dotted child
		{"bd-wisp-7", 0, false},               // hash/wisp shaped
		{"bdw-2", 0, false},                   // another prefix
		{"bd-", 0, false},                     // no suffix
		{"bd-99999999999999999999", 0, false}, // not representable
	}
	for _, tc := range cases {
		n, ok := counterNumericSuffix(tc.id, "bd")
		if ok != tc.valid || n != tc.n {
			t.Errorf("counterNumericSuffix(%q, \"bd\") = (%d, %v), want (%d, %v)", tc.id, n, ok, tc.n, tc.valid)
		}
	}
}
