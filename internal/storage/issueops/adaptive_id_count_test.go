package issueops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestAdaptiveIDCountReadsBothPlanesInOneStatement pins the shared sizing
// count's shape (bd-u9cfx0): ONE statement summing the destination table's
// loose prefix count and the sibling plane's exact-namespace count — the
// sibling's second INSTR excluding sub-prefixed rows — so both halves read
// one snapshot, with the prefix bound five times and only the two validated
// table names interpolated.
func TestAdaptiveIDCountReadsBothPlanesInOneStatement(t *testing.T) {
	for _, tc := range []struct{ table, sibling string }{
		{"issues", "wisps"},
		{"wisps", "issues"},
	} {
		db, mock := newIDRetentionMock(t)
		query := fmt.Sprintf(`(?s)SELECT\s+\(SELECT COUNT\(\*\) FROM %s .*INSTR\(SUBSTRING\(id, LENGTH\(\?\) \+ 2\), '\.'\) = 0\)\s+\+\s+\(SELECT COUNT\(\*\) FROM %s .*INSTR\(SUBSTRING\(id, LENGTH\(\?\) \+ 2\), '-'\) = 0\)`,
			tc.table, tc.sibling)
		mock.ExpectQuery(query).
			WithArgs("bd", "bd", "bd", "bd", "bd").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))

		got, err := AdaptiveIDCountInTx(context.Background(), db, tc.table, "bd")
		if err != nil {
			t.Fatalf("AdaptiveIDCountInTx(%s): %v", tc.table, err)
		}
		if got != 7 {
			t.Fatalf("AdaptiveIDCountInTx(%s) = %d, want 7", tc.table, got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("AdaptiveIDCountInTx(%s): %v", tc.table, err)
		}
	}
}

// TestAdaptiveIDCountRejectsUnknownTable pins that a table name outside the
// issues/wisps pair is refused before any SQL is built, so no unvalidated
// name can reach a statement.
func TestAdaptiveIDCountRejectsUnknownTable(t *testing.T) {
	db, _ := newIDRetentionMock(t)
	_, err := AdaptiveIDCountInTx(context.Background(), db, "events", "bd")
	if err == nil || !strings.Contains(err.Error(), "unknown issue table") {
		t.Fatalf("AdaptiveIDCountInTx(events) = %v, want unknown issue table error", err)
	}
}
