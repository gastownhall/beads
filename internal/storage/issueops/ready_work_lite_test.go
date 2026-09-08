package issueops

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// heavyTextColumns are the six columns the lite projection drops. Named here
// rather than inlined so a column added to HeavyDropList without being added to
// IssueBaseColumnsLite is a failure in this file too, not only in the
// schema-parity test.
var heavyTextColumns = []string{"description", "design", "acceptance_criteria", "notes", "payload", "waiters"}

// TestGetReadyWorkInTxLiteProjectsWithoutHeavyText is the control this car
// turns on: with filter.Lite the ready scan's HYDRATION query must not name a
// single heavy TEXT column. Before the change the scan projected `id` and then
// hydrated the page WHOLE regardless of Lite, so the flag was accepted and
// silently ignored — the exact shape ("accepted and dropped") the projection's
// own refusals elsewhere exist to prevent.
func TestGetReadyWorkInTxLiteProjectsWithoutHeavyText(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM issues`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	// The wisp-set probe GetIssuesByIDsInTx runs when it is handed a nil set.
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)
	// THE ASSERTION. sqlmock matches the expectation as a regex against the
	// query text, so a hydration that named `description` would not match this
	// and the test fails with the offending SQL printed.
	mock.ExpectQuery(`SELECT id, content_hash, title,\s+status,`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	if _, err := GetReadyWorkInTx(context.Background(), tx, types.WorkFilter{Status: types.StatusOpen, Lite: true}); err != nil {
		t.Fatalf("GetReadyWorkInTx(Lite): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetReadyWorkInTxDefaultStillHydratesFully is the NEGATIVE control, and it
// is the half that keeps the change honest: a filter that did NOT ask for the
// projection must still get every column. Without it, a mistake that made lite
// unconditional would leave the positive test above green.
func TestGetReadyWorkInTxDefaultStillHydratesFully(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM issues`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id, content_hash, title, description, design,`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	if _, err := GetReadyWorkInTx(context.Background(), tx, types.WorkFilter{Status: types.StatusOpen}); err != nil {
		t.Fatalf("GetReadyWorkInTx(default): %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestIssueSelectColumnsLiteNamesNoHeavyText pins the two column lists directly:
// the lite one names none of the six, the full one names all six. It is the
// cheap standing guard behind both SQL-shape tests above, which can only prove
// which CONSTANT was used, not what is in it.
func TestIssueSelectColumnsLiteNamesNoHeavyText(t *testing.T) {
	t.Parallel()

	for _, col := range heavyTextColumns {
		if strings.Contains(IssueSelectColumnsLite, col) {
			t.Errorf("IssueSelectColumnsLite names %q; the lite projection must drop every heavy TEXT column", col)
		}
		if !strings.Contains(IssueSelectColumns, col) {
			t.Errorf("IssueSelectColumns does NOT name %q; full hydration must still read every heavy TEXT column", col)
		}
	}
}

// TestClaimReadyIssueInTxScansLite proves the claim's own candidate scan is
// projected. It is the shape that matters most and the one no consumer can
// observe: ClaimReadyIssueInTx walks an UNBOUNDED ready front, reads exactly
// `issue.ID` off each row, and refetches the row it wins whole — so every heavy
// TEXT column that scan carried was read from the store and discarded.
//
// The mock returns one candidate id and then NO row for the hydration, which
// leaves the ready set empty and returns before the claim CAS. That keeps the
// test about the projection instead of about the compare-and-set.
func TestClaimReadyIssueInTxScansLite(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id FROM issues`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-1"))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT id, content_hash, title,\s+status,`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	// The caller's filter says nothing about hydration: the claim sets the
	// projection on its own copy, which is the point — ValidateClaimNextRequest
	// refuses a caller that tries to project the row it gets BACK, and this is
	// not that row.
	got, err := ClaimReadyIssueInTx(context.Background(), tx, types.WorkFilter{Status: types.StatusOpen}, "tester")
	if err != nil {
		t.Fatalf("ClaimReadyIssueInTx: %v", err)
	}
	if got != nil {
		t.Fatalf("ClaimReadyIssueInTx = %v, want nil (the hydration returned no rows)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
