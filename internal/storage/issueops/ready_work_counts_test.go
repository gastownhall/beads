package issueops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkWithCountsUnboundedHydratesByIDs is the GH#4370 AC-2 pin on the
// ready seam: an unbounded (Limit 0) ready read on one plane with 205 matching
// rows must resolve the whole ready ID set with the cheap ID query first — no
// page bound, because there is no page — and then hydrate those IDs in
// sqlbuild.QueryBatchSize batches (200 and 5) through the by-IDs counts form.
// The pre-change shortcut ran the predicate-form counts mega-query over the
// full matching set, whose aggregate LEFT JOINs read every side table
// unconstrained — the shape that seized at ~100k issues. The bounded page
// already worked this way; this is the two arms sharing one shape.
func TestReadyWorkWithCountsUnboundedHydratesByIDs(t *testing.T) {
	t.Parallel()

	ids := make([]string, 205)
	idsRows := sqlmock.NewRows([]string{"id"})
	for i := range ids {
		ids[i] = rowFixtureID(i)
		idsRows.AddRow(ids[i])
	}

	var captured []string
	mock, tx := beginCapturingCountsMockTx(t, &captured)

	// The one-statement probe (IncludeDeferred: no deferred columns; unbounded
	// read: no collision column) answers wisps-absent, so the read is
	// issues-only and stays on one plane.
	mock.ExpectQuery(readyProbeSQL).WillReturnRows(readyProbeRows(false, true, false, false))
	// The ready ID query: no LIMIT for an unbounded read.
	mock.ExpectQuery(`(?s)^SELECT id FROM issues .*ORDER BY`).
		WillReturnRows(idsRows)
	mock.ExpectQuery(`WHERE i\.id IN \(\?(?:,\?){199}\)`).
		WillReturnRows(countsMockRows(ids[:200]))
	mock.ExpectQuery(`WHERE i\.id IN \(\?(?:,\?){4}\)`).
		WillReturnRows(countsMockRows(ids[200:]))

	got, err := GetReadyWorkWithCountsInTx(context.Background(), tx, types.WorkFilter{IncludeDeferred: true})
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsInTx: %v", err)
	}
	if len(got) != 205 {
		t.Fatalf("row count = %d, want 205", len(got))
	}
	for i, g := range got {
		if g.Issue.ID != ids[i] {
			t.Fatalf("row %d = %s, want %s (the ready ID query's order)", i, g.Issue.ID, ids[i])
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	if len(captured) != 4 {
		t.Fatalf("query count = %d, want 4 (probe, unbounded ID select, two hydration batches)", len(captured))
	}
	for _, q := range captured {
		if strings.Contains(q, "SELECT i.*") {
			t.Fatalf("a predicate-form counts query ran (derived driver SELECT i.*): %s", q)
		}
	}
}

// rowFixtureID renders the zero-padded fixture id "bd-000".., with the
// bd- prefix common to the row fixtures, so byte order and natural order agree
// across the 205-row batch boundary.
func rowFixtureID(i int) string {
	return fmt.Sprintf("bd-%03d", i)
}
