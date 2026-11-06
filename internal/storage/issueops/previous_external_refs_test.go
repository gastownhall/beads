package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// These pin the shape of PreviousExternalRefsInTx's SQL without a database:
// how many history queries a batch issues, how its ids are bound into both
// sides of the latest-date join, and that the cutoff is bound as UTC. The
// answers themselves are compared against PreviousExternalRefInTx on real
// stores in the dolt and embeddeddolt packages.

var previousRefsCutoff = time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("x", 3*3600))

// newPreviousRefsTx opens a sqlmock transaction whose expectations are
// matched in order, so a test can tell one chunk's query from the next.
func newPreviousRefsTx(t *testing.T) (*sql.Tx, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx, mock
}

// previousRefsChunkArgs is the argument list one chunk's query must bind:
// the ids and cutoff for the latest-date subquery, then again for the outer
// history read.
func previousRefsChunkArgs(chunk []string, asOf time.Time) []driver.Value {
	args := make([]driver.Value, 0, 2*len(chunk)+2)
	for _, id := range chunk {
		args = append(args, id)
	}
	args = append(args, asOf.UTC())
	for _, id := range chunk {
		args = append(args, id)
	}
	return append(args, asOf.UTC())
}

func previousRefsIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%d", i)
	}
	return ids
}

// TestPreviousExternalRefsChunksHistoryQueries covers AC-7: N unique ids with
// unambiguous latest rows cost ceil(N/16) history queries, each binding at
// most 16 ids into both IN-lists, and no other SQL.
func TestPreviousExternalRefsChunksHistoryQueries(t *testing.T) {
	for _, n := range []int{1, 16, 17, 33} {
		t.Run(fmt.Sprintf("%d ids", n), func(t *testing.T) {
			ids := previousRefsIDs(n)
			// Duplicates and empty ids must not widen a chunk or add a query.
			request := append([]string{"", ids[0]}, ids...)
			request = append(request, ids[n-1], "")

			tx, mock := newPreviousRefsTx(t)
			want := make(map[string]string, n)
			queries := 0
			for start := 0; start < n; start += previousExternalRefsChunk {
				end := min(start+previousExternalRefsChunk, n)
				chunk := ids[start:end]
				rows := sqlmock.NewRows([]string{"id", "external_ref"})
				for i, id := range chunk {
					if i%5 == 4 {
						// SQL NULL: present in the map with an empty ref.
						rows.AddRow(id, nil)
						want[id] = ""
						continue
					}
					if i%5 == 3 {
						// No qualifying history row: absent from the map.
						continue
					}
					ref := "https://tracker.test/" + id
					rows.AddRow(id, ref)
					want[id] = ref
				}
				inList := strings.TrimSuffix(strings.Repeat(`\?,`, len(chunk)), ",")
				mock.ExpectQuery(`(?s)m\.id IN \(` + inList + `\) AND m\.commit_date <= \?.*h\.id IN \(` + inList + `\) AND h\.commit_date <= \?`).
					WithArgs(previousRefsChunkArgs(chunk, previousRefsCutoff)...).
					WillReturnRows(rows)
				queries++
			}
			if wantQueries := (n + 15) / 16; queries != wantQueries {
				t.Fatalf("test setup: %d expected queries, want %d", queries, wantQueries)
			}

			got, err := PreviousExternalRefsInTx(context.Background(), tx, request, previousRefsCutoff)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("history queries: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d refs %v, want %d %v", len(got), got, len(want), want)
			}
			for id, ref := range want {
				if g, ok := got[id]; !ok || g != ref {
					t.Errorf("refs[%q] = (%q, %t), want (%q, true)", id, g, ok, ref)
				}
			}
		})
	}
}

// TestPreviousExternalRefsEmptyRequestRunsNoSQL: nothing to resolve means no
// history scan at all.
func TestPreviousExternalRefsEmptyRequestRunsNoSQL(t *testing.T) {
	for _, request := range [][]string{nil, {}, {"", ""}} {
		tx, mock := newPreviousRefsTx(t)
		got, err := PreviousExternalRefsInTx(context.Background(), tx, request, previousRefsCutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefsInTx(%q): %v", request, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("PreviousExternalRefsInTx(%q) = %v, want an empty non-nil map", request, got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("PreviousExternalRefsInTx(%q) ran SQL: %v", request, err)
		}
	}
}

// TestPreviousExternalRefsDiscardsPartialResultsOnError: a failure in any
// chunk, at query, scan or iteration time, returns no map, so a caller cannot
// mistake the chunks that did resolve for the whole answer.
func TestPreviousExternalRefsDiscardsPartialResultsOnError(t *testing.T) {
	ids := previousRefsIDs(17)
	first, second := ids[:16], ids[16:]
	firstRows := func() *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{"id", "external_ref"})
		for _, id := range first {
			rows.AddRow(id, "ref-"+id)
		}
		return rows
	}
	for _, test := range []struct {
		name   string
		second func(*sqlmock.ExpectedQuery)
	}{
		{"query", func(q *sqlmock.ExpectedQuery) { q.WillReturnError(fmt.Errorf("boom")) }},
		{"scan", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(second[0]))
		}},
		{"iteration", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
				AddRow(second[0], "ref").RowError(0, fmt.Errorf("row boom")))
		}},
		{"close", func(q *sqlmock.ExpectedQuery) {
			q.WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
				AddRow(second[0], "ref").CloseError(fmt.Errorf("close boom")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, mock := newPreviousRefsTx(t)
			mock.ExpectQuery(`dolt_history_issues`).
				WithArgs(previousRefsChunkArgs(first, previousRefsCutoff)...).
				WillReturnRows(firstRows())
			test.second(mock.ExpectQuery(`dolt_history_issues`).
				WithArgs(previousRefsChunkArgs(second, previousRefsCutoff)...))

			got, err := PreviousExternalRefsInTx(context.Background(), tx, ids, previousRefsCutoff)
			if err == nil {
				t.Fatalf("PreviousExternalRefsInTx = %v, want an error", got)
			}
			if got != nil {
				t.Fatalf("PreviousExternalRefsInTx returned partial map %v with error %v", got, err)
			}
		})
	}
}

// TestPreviousExternalRefsResolvesTiesThroughPerIssueLookup covers AC-5's
// tie rule: when an id has more than one row at its latest qualifying date,
// the batch takes neither row but asks the unchanged PreviousExternalRefInTx,
// after the chunk's rows are consumed, so a tie resolves exactly as the
// per-issue lookup resolves it.
func TestPreviousExternalRefsResolvesTiesThroughPerIssueLookup(t *testing.T) {
	ids := []string{"bd-tied", "bd-plain"}
	tx, mock := newPreviousRefsTx(t)
	mock.ExpectQuery(`(?s)MAX\(m\.commit_date\)`).
		WithArgs(previousRefsChunkArgs(ids, previousRefsCutoff)...).
		WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
			AddRow("bd-tied", "ref-a").
			AddRow("bd-plain", "ref-plain").
			AddRow("bd-tied", "ref-b"))
	mock.ExpectQuery(`(?s)WHERE h\.id = \? AND h\.commit_date <= \?\s+ORDER BY h\.commit_date DESC\s+LIMIT 1`).
		WithArgs("bd-tied", previousRefsCutoff.UTC()).
		WillReturnRows(sqlmock.NewRows([]string{"external_ref"}).AddRow("ref-b"))

	got, err := PreviousExternalRefsInTx(context.Background(), tx, ids, previousRefsCutoff)
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("tie lookup: %v", err)
	}
	want := map[string]string{"bd-tied": "ref-b", "bd-plain": "ref-plain"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("PreviousExternalRefsInTx = %v, want %v", got, want)
	}
}
