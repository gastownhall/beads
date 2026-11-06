package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// chunkQueryPattern builds the sqlmock regexp for one chunk of
// previousExternalRefsForIDsInTx: both IN lists must carry exactly n
// placeholder marks, so a query built with any other arity — one giant
// query, or chunks of a different size — does not match its expectation.
func chunkQueryPattern(n int) string {
	marks := regexp.QuoteMeta(strings.TrimSuffix(strings.Repeat("?,", n), ","))
	return `SELECT h\.id, h\.external_ref\s+FROM \(SELECT id, external_ref, commit_date FROM dolt_history_issues\) h` +
		`\s+JOIN \(\s+SELECT m\.id, MAX\(m\.commit_date\) AS latest_date\s+FROM \(SELECT id, commit_date FROM dolt_history_issues\) m` +
		`\s+WHERE m\.id IN \(` + marks + `\) AND m\.commit_date <= \?\s+GROUP BY m\.id` +
		`\s+\) latest ON latest\.id = h\.id AND latest\.latest_date = h\.commit_date` +
		`\s+WHERE h\.id IN \(` + marks + `\) AND h\.commit_date <= \?`
}

// singleRefQueryPattern is PreviousExternalRefInTx's query, used by the tie
// resolution test below.
func singleRefQueryPattern() string {
	return `SELECT external_ref\s+FROM \(\s+SELECT id, external_ref, commit_date FROM dolt_history_issues\s+\) h` +
		`\s+WHERE h\.id = \? AND h\.commit_date <= \?\s+ORDER BY h\.commit_date DESC\s+LIMIT 1`
}

// batchCutoff returns the cutoff in a non-UTC zone, so WithArgs comparing the
// bound values against asOf.UTC() catches a raw asOf sneaking through.
func batchCutoff() time.Time {
	zone := time.FixedZone("test-offset", -4*60*60)
	return time.Date(2026, 3, 5, 12, 0, 0, 0, zone)
}

// TestPreviousExternalRefsInTxReadsIDsInChunkQueries pins the query shape
// (GH#6605, AC-7): the deduplicated ids are read through
// ceil(len/externalRefHistoryChunk) queries, each carrying at most that many
// ids, with the cutoff bound as UTC in both IN lists. All chunk queries run
// on the caller's one transaction — they arrive on the same mock tx in order.
func TestPreviousExternalRefsInTxReadsIDsInChunkQueries(t *testing.T) {
	cutoff := batchCutoff()

	for _, tt := range []struct {
		name string
		// raw input; may contain duplicates and empty ids
		rawIDs []string
		// wantChunks is the expected placeholder arity per chunk query, in
		// call order
		wantChunks []int
	}{
		{name: "one id", rawIDs: idsPrefixed(1, "bd"), wantChunks: []int{1}},
		{name: "exactly one full chunk", rawIDs: idsPrefixed(16, "bd"), wantChunks: []int{16}},
		{name: "one over a full chunk", rawIDs: idsPrefixed(17, "bd"), wantChunks: []int{16, 1}},
		{name: "33 ids", rawIDs: idsPrefixed(33, "bd"), wantChunks: []int{16, 16, 1}},
		{
			name: "duplicates and empty ids do not inflate chunks",
			rawIDs: append(
				append(idsPrefixed(33, "dup"), idsPrefixed(8, "dup")...),
				"", ""),
			wantChunks: []int{16, 16, 1},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)

			// uniqueIDs recomputes the dedup the implementation must do, in
			// input order, so each chunk's WithArgs pins exactly which ids
			// that query carries.
			uniqueIDs := dedupeNonEmpty(tt.rawIDs)
			wantFirsts := make([]string, 0, len(tt.wantChunks))
			start := 0
			for i, wantArity := range tt.wantChunks {
				chunkIDs := uniqueIDs[start : start+wantArity]
				start += wantArity
				wantFirsts = append(wantFirsts, chunkIDs[0])
				args := make([]driver.Value, 0, 2*wantArity+2)
				for _, id := range chunkIDs {
					args = append(args, id)
				}
				args = append(args, cutoff.UTC())
				for _, id := range chunkIDs {
					args = append(args, id)
				}
				args = append(args, cutoff.UTC())
				mock.ExpectQuery(chunkQueryPattern(wantArity)).
					WithArgs(args...).
					WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
						AddRow(chunkIDs[0], fmt.Sprintf("ref-%d", i)))
			}

			refs, err := PreviousExternalRefsInTx(context.Background(), tx, tt.rawIDs, cutoff)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if len(refs) != len(tt.wantChunks) {
				t.Fatalf("refs = %v, want one entry per chunk query", refs)
			}
			for i, id := range wantFirsts {
				if refs[id] != fmt.Sprintf("ref-%d", i) {
					t.Fatalf("refs[%s] = %q, want ref-%d", id, refs[id], i)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

// TestPreviousExternalRefsInTxEmptyInputMakesNoQuery keeps the trivial cases
// query-free: no candidate ids, nothing to ask the database.
func TestPreviousExternalRefsInTxEmptyInputMakesNoQuery(t *testing.T) {
	for _, name := range []string{"nil", "empty", "only empty ids"} {
		t.Run(name, func(t *testing.T) {
			var ids []string
			switch name {
			case "empty", "only empty ids":
				ids = []string{}
				if name == "only empty ids" {
					ids = []string{"", ""}
				}
			}
			_, mock, tx := beginMockTx(t)
			refs, err := PreviousExternalRefsInTx(context.Background(), tx, ids, batchCutoff())
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if refs == nil || len(refs) != 0 {
				t.Fatalf("refs = %v, want an initialized empty map", refs)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expected no queries, got: %v", err)
			}
		})
	}
}

// TestPreviousExternalRefsInTxStopsAtFirstChunkError checks the fail-fast and
// discard contract across the chunk loop: a chunk query error aborts the
// batch — later chunks never run, and the partial map is discarded.
func TestPreviousExternalRefsInTxStopsAtFirstChunkError(t *testing.T) {
	_, mock, tx := beginMockTx(t)
	ids := idsPrefixed(33, "bd") // three chunks; only the first is expected

	mock.ExpectQuery(chunkQueryPattern(16)).
		WillReturnError(errors.New("chunk query failed"))

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, ids, batchCutoff())
	if err == nil {
		t.Fatal("expected an error from the failed chunk query")
	}
	if refs != nil {
		t.Fatalf("refs = %v, want nil: a failed batch read must discard partial results", refs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("later chunks must not run after a chunk error: %v", err)
	}
}

// TestPreviousExternalRefsInTxRowFailureDiscardsResults drives a row-consumption
// failure inside one chunk: it must abort the batch with a nil map, not a
// partially-filled answer.
//
// The row-consumption seams cannot be simulated separately with this mock:
// go's database/sql converts every scalar driver.Value into the string and
// NullString destinations this scan uses (even time.Time and bool), so a
// distinct scan failure is unreachable, and a driver close error reaches the
// caller through rows.Err(), landing on the same iteration branch the RowError
// below takes. The query-failure seam is pinned by
// TestPreviousExternalRefsInTxStopsAtFirstChunkError.
func TestPreviousExternalRefsInTxRowFailureDiscardsResults(t *testing.T) {
	cutoff := batchCutoff()
	_, mock, tx := beginMockTx(t)
	rows := sqlmock.NewRows([]string{"id", "external_ref"}).
		AddRow("bd-0", "ref-0").
		AddRow("bd-1", "ref-1")
	rows.RowError(1, errors.New("connection died mid-iteration"))
	mock.ExpectQuery(chunkQueryPattern(2)).WillReturnRows(rows)

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-0", "bd-1"}, cutoff)
	if err == nil {
		t.Fatal("expected an iteration error")
	}
	if refs != nil {
		t.Fatalf("refs = %v, want nil: a failed iteration must discard partial results", refs)
	}
}

// TestPreviousExternalRefsInTxResolvesEqualDateTiesThroughSingleIDLookup pins
// the tie path's query order: a chunk returning two rows at one id's greatest
// qualifying date hands the id to PreviousExternalRefInTx on the same
// transaction, after the chunk's rows are consumed — sqlmock's ordered
// expectations only match in that order.
func TestPreviousExternalRefsInTxResolvesEqualDateTiesThroughSingleIDLookup(t *testing.T) {
	cutoff := batchCutoff()
	_, mock, tx := beginMockTx(t)

	mock.ExpectQuery(chunkQueryPattern(1)).
		WithArgs("bd-0", cutoff.UTC(), "bd-0", cutoff.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
			AddRow("bd-0", "ref-a").
			AddRow("bd-0", "ref-b"))
	mock.ExpectQuery(singleRefQueryPattern()).
		WithArgs("bd-0", cutoff.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"external_ref"}).AddRow("ref-tie"))

	refs, err := PreviousExternalRefsInTx(context.Background(), tx, []string{"bd-0"}, cutoff)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	if refs["bd-0"] != "ref-tie" {
		t.Fatalf("refs[bd-0] = %q, want the single-id lookup's ref-tie", refs["bd-0"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// idsPrefixed returns n ids, "prefix-0" .. "prefix-(n-1)".
func idsPrefixed(n int, prefix string) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return ids
}

// dedupeNonEmpty recomputes PreviousExternalRefsInTx's dedup: input order,
// empty ids and repeats dropped.
func dedupeNonEmpty(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
