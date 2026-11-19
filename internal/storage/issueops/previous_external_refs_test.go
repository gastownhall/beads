package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// Tests for PreviousExternalRefsInTx (bd-vcpyl7, GH#6605): the batched form
// of PreviousExternalRefInTx resolves every requested id's external_ref as
// of one cutoff, reading dolt_history_issues in bounded chunks instead of
// scanning it once per issue. sqlmock pins the query shape and count — no
// real engine, no timing — so the cost regression the reporter measured
// (issues x commits per pull) is caught as a query-count change.

// previousRefsQueryRegex matches the chunked history statement for a chunk
// of n ids: an IN-list of n placeholders followed by the cutoff. It pins
// both the batching and the bind-parameter shape (no interpolated ids, no
// AS OF, no dolt_log snapshot statements).
func previousRefsQueryRegex(n int) string {
	return `WHERE h\.id IN \(` + strings.Repeat(`\?,`, n-1) + `\?\) AND h\.commit_date <= \?`
}

type refRow struct {
	id  string
	ref *string // nil is a NULL external_ref column
}

// expectPreviousRefsChunk queues one chunked history query over ids at
// cutoff, answering with rows. A requested id with no row stays absent.
func expectPreviousRefsChunk(mock sqlmock.Sqlmock, ids []string, cutoff time.Time, rows []refRow) {
	expected := mock.ExpectQuery(previousRefsQueryRegex(len(ids)))
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, cutoff)
	driverArgs := make([]driver.Value, len(args))
	for i, arg := range args {
		driverArgs[i] = arg
	}
	expected.WithArgs(driverArgs...)
	result := sqlmock.NewRows([]string{"id", "external_ref"})
	for _, row := range rows {
		if row.ref == nil {
			result.AddRow(row.id, nil)
			continue
		}
		result.AddRow(row.id, *row.ref)
	}
	expected.WillReturnRows(result)
}

func strptr(s string) *string { return &s }

// TestPreviousExternalRefsInTxEmptyInputQueriesNothing pins that empty and
// empty-only input opens no history query at all.
func TestPreviousExternalRefsInTxEmptyInputQueriesNothing(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, ids := range [][]string{nil, {}, {""}, {"", ""}} {
		refs, err := PreviousExternalRefsInTx(ctx, tx, ids, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefsInTx(%q): %v", ids, err)
		}
		if len(refs) != 0 {
			t.Errorf("PreviousExternalRefsInTx(%q) = %v, want an empty map", ids, refs)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no history query may run for empty input: %v", err)
	}
}

// TestPreviousExternalRefsInTxDedupesAndDropsEmptyIDs pins the request
// normalization: one query whose IN-list holds each requested id exactly
// once, with no empty ids.
func TestPreviousExternalRefsInTxDedupesAndDropsEmptyIDs(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expectPreviousRefsChunk(mock, []string{"bd-1", "bd-2"}, cutoff, []refRow{
		{"bd-1", strptr("gh-1")},
		{"bd-2", strptr("gh-2")},
	})

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-1", "", "bd-2", "bd-1", ""}, cutoff)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	want := map[string]string{"bd-1": "gh-1", "bd-2": "gh-2"}
	if len(refs) != len(want) || refs["bd-1"] != want["bd-1"] || refs["bd-2"] != want["bd-2"] {
		t.Fatalf("PreviousExternalRefsInTx = %v, want %v", refs, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestPreviousExternalRefsInTxBindsCutoffAsUTC pins the cutoff binding: the
// cutoff is bound as its UTC instant even when the caller passes it in
// another zone, matching the single-id oracle PreviousExternalRefInTx.
func TestPreviousExternalRefsInTxBindsCutoffAsUTC(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	zone := time.FixedZone("UTC+5:30", 5*3600+30*60)
	cutoff := time.Date(2026, 1, 2, 8, 34, 5, 0, zone)
	expectPreviousRefsChunk(mock, []string{"bd-1"}, cutoff.UTC(), []refRow{
		{"bd-1", strptr("gh-1")},
	})

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-1"}, cutoff)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	if refs["bd-1"] != "gh-1" {
		t.Fatalf("PreviousExternalRefsInTx = %v, want bd-1 -> gh-1", refs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: the cutoff must bind as its UTC instant: %v", err)
	}
}

// TestPreviousExternalRefsInTxChunkBoundaries pins the cost contract: U
// unique non-empty ids cost exactly ceil(U/200) history queries, at and
// across the 200-id chunk boundary, and the answer is complete across
// chunk boundaries with no per-id fallback query.
func TestPreviousExternalRefsInTxChunkBoundaries(t *testing.T) {
	for _, test := range []struct {
		ids       int
		wantQuery int
	}{
		{ids: 199, wantQuery: 1},
		{ids: 200, wantQuery: 1},
		{ids: 201, wantQuery: 2},
		{ids: 450, wantQuery: 3},
	} {
		t.Run(strconv.Itoa(test.ids)+" ids", func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			ids := make([]string, test.ids)
			for i := range ids {
				ids[i] = "bd-" + strconv.Itoa(i)
			}
			for start := 0; start < len(ids); start += 200 {
				end := start + 200
				if end > len(ids) {
					end = len(ids)
				}
				rows := make([]refRow, 0, end-start)
				for _, id := range ids[start:end] {
					rows = append(rows, refRow{id: id, ref: strptr("gh-" + id)})
				}
				expectPreviousRefsChunk(mock, ids[start:end], cutoff, rows)
			}

			refs, err := PreviousExternalRefsInTx(context.Background(), tx, ids, cutoff)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx(%d ids): %v", test.ids, err)
			}
			if len(refs) != test.ids {
				t.Fatalf("result holds %d ids, want %d: a chunk boundary must not drop answers", len(refs), test.ids)
			}
			for _, id := range ids {
				if refs[id] != "gh-"+id {
					t.Fatalf("refs[%s] = %q, want %q", id, refs[id], "gh-"+id)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// TestPreviousExternalRefsInTxFoldsNewestRowPerID pins the row-to-answer
// fold: rows arrive ordered id then commit_date descending, and the first
// row per id is its newest committed value at or before the cutoff. A NULL
// external_ref is a present answer holding the empty string, never an
// absence; an id with no row stays absent.
func TestPreviousExternalRefsInTxFoldsNewestRowPerID(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expectPreviousRefsChunk(mock, []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-absent"}, cutoff, []refRow{
		{"bd-1", strptr("new-ref")},
		{"bd-1", strptr("old-ref")},
		{"bd-2", nil},
		{"bd-3", strptr("only-ref")},
	})

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-absent"}, cutoff)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	want := map[string]string{"bd-1": "new-ref", "bd-2": "", "bd-3": "only-ref"}
	if len(refs) != len(want) {
		t.Fatalf("PreviousExternalRefsInTx = %v, want %v", refs, want)
	}
	for id, ref := range want {
		if refs[id] != ref {
			t.Errorf("refs[%s] = %q, want %q", id, refs[id], ref)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestPreviousExternalRefsInTxErrorsPinNoPartialAnswers pins the failure
// contract: a query, scan or iteration failure returns the error and no
// usable map, with no per-id fallback after the failure.
func TestPreviousExternalRefsInTxErrorsPinNoPartialAnswers(t *testing.T) {
	boom := errors.New("boom")
	cutoff := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	t.Run("query error mid-batch", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		ids := make([]string, 201)
		for i := range ids {
			ids[i] = "bd-" + strconv.Itoa(i)
		}
		expectPreviousRefsChunk(mock, ids[:200], cutoff, nil)
		mock.ExpectQuery(previousRefsQueryRegex(1)).
			WithArgs(ids[200], cutoff).
			WillReturnError(boom)

		refs, err := PreviousExternalRefsInTx(context.Background(), tx, ids, cutoff)
		if !errors.Is(err, boom) {
			t.Fatalf("PreviousExternalRefsInTx error = %v, want %v: no per-id fallback may mask the chunk failure", err, boom)
		}
		if refs != nil {
			t.Fatalf("PreviousExternalRefsInTx returned %v with the error, want no partial map", refs)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("scan error", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		// The id column is NOT NULL in the real table; a NULL there makes
		// Scan fail, standing in for any driver-level scan failure.
		mock.ExpectQuery(previousRefsQueryRegex(1)).
			WithArgs("bd-1", cutoff).
			WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).AddRow(nil, "gh-1"))

		refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-1"}, cutoff)
		if err == nil {
			t.Fatalf("PreviousExternalRefsInTx = %v, want a scan error", refs)
		}
		if refs != nil {
			t.Fatalf("PreviousExternalRefsInTx returned %v with the error, want no partial map", refs)
		}
	})

	t.Run("iteration error", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		rows := sqlmock.NewRows([]string{"id", "external_ref"}).AddRow("bd-1", "gh-1")
		rows.RowError(0, boom)
		mock.ExpectQuery(previousRefsQueryRegex(1)).
			WithArgs("bd-1", cutoff).
			WillReturnRows(rows)

		refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-1"}, cutoff)
		if !errors.Is(err, boom) {
			t.Fatalf("PreviousExternalRefsInTx error = %v, want %v", err, boom)
		}
		if refs != nil {
			t.Fatalf("PreviousExternalRefsInTx returned %v with the error, want no partial map", refs)
		}
	})
}
