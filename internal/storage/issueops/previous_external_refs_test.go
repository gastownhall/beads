package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// These pin the shape of PreviousExternalRefsInTx's SQL traffic: how many
// history queries a request costs, what each binds, and how rows fold into
// the answer. Whether the answer matches PreviousExternalRefInTx on a real
// history table is the storagecontract suite's job, run by both Dolt stores.

// recordingHistoryDB returns a sqlmock whose matcher accepts any statement
// and appends it to *seen, so a test can count history queries and check
// that nothing else ran.
func recordingHistoryDB(t *testing.T) (DBTX, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	seen := new([]string)
	matcher := sqlmock.QueryMatcherFunc(func(_, actual string) error {
		*seen = append(*seen, actual)
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock, seen
}

// cutoffArg matches a bound time equal to want and in UTC.
type cutoffArg struct{ want time.Time }

func (c cutoffArg) Match(v driver.Value) bool {
	got, ok := v.(time.Time)
	return ok && got.Equal(c.want) && got.Location() == time.UTC
}

func historyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%04d", i)
	}
	return ids
}

func TestPreviousExternalRefsInTxRunsNoQueryForNoIDs(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"", ""}} {
		db, mock, seen := recordingHistoryDB(t)
		refs, err := PreviousExternalRefsInTx(context.Background(), db, ids, time.Now())
		if err != nil {
			t.Fatalf("PreviousExternalRefsInTx(%q): %v", ids, err)
		}
		if refs == nil || len(refs) != 0 {
			t.Errorf("PreviousExternalRefsInTx(%q) = %v, want an empty non-nil map", ids, refs)
		}
		if len(*seen) != 0 {
			t.Errorf("PreviousExternalRefsInTx(%q) ran %d statements, want 0", ids, len(*seen))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	}
}

// TestPreviousExternalRefsInTxQueriesOncePerChunk is the GH#6605 cost
// regression: U unique ids cost ceil(U/200) history queries, never one per
// id, and every id's answer comes back across the chunk boundaries.
func TestPreviousExternalRefsInTxQueriesOncePerChunk(t *testing.T) {
	plus5 := time.FixedZone("UTC+5", 5*60*60)
	asOf := time.Date(2026, time.January, 2, 8, 4, 5, 0, plus5)
	for _, n := range []int{1, 199, 200, 201, 450} {
		t.Run(fmt.Sprintf("%d ids", n), func(t *testing.T) {
			db, mock, seen := recordingHistoryDB(t)
			ids := historyIDs(n)
			// Repeats and blanks must not inflate a chunk or the count.
			asked := append(append([]string{""}, ids...), ids[:n/2]...)

			wantQueries := (n + 199) / 200
			for q := 0; q < wantQueries; q++ {
				chunk := ids[q*200 : min((q+1)*200, n)]
				args := make([]driver.Value, 0, len(chunk)+1)
				rows := sqlmock.NewRows([]string{"id", "external_ref"})
				for _, id := range chunk {
					args = append(args, id)
					rows.AddRow(id, "https://tracker.test/"+id)
				}
				args = append(args, cutoffArg{want: asOf})
				mock.ExpectQuery("").WithArgs(args...).WillReturnRows(rows)
			}

			refs, err := PreviousExternalRefsInTx(context.Background(), db, asked, asOf)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if len(*seen) != wantQueries {
				t.Fatalf("%d ids ran %d statements, want %d", n, len(*seen), wantQueries)
			}
			for _, stmt := range *seen {
				if !strings.Contains(stmt, "dolt_history_issues") {
					t.Errorf("statement %q is not a dolt_history_issues query", stmt)
				}
				if strings.Contains(stmt, "dolt_log") || strings.Contains(strings.ToUpper(stmt), "AS OF") {
					t.Errorf("statement %q resolves a snapshot; the batch reads history directly", stmt)
				}
			}
			if len(refs) != n {
				t.Fatalf("got %d refs, want %d", len(refs), n)
			}
			for _, id := range ids {
				if refs[id] != "https://tracker.test/"+id {
					t.Errorf("refs[%s] = %q, want its own ref", id, refs[id])
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestPreviousExternalRefsInTxFoldsNewestRowPerID: rows arrive newest first
// per id, so the first one decides; a NULL ref is present as "", and an id
// with no row stays absent.
func TestPreviousExternalRefsInTxFoldsNewestRowPerID(t *testing.T) {
	db, mock, _ := recordingHistoryDB(t)
	mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
		AddRow("bd-a", "https://tracker.test/NEW").
		AddRow("bd-a", "https://tracker.test/OLD").
		AddRow("bd-b", nil).
		AddRow("bd-b", "https://tracker.test/B-OLD"))

	refs, err := PreviousExternalRefsInTx(context.Background(), db, []string{"bd-a", "bd-b", "bd-c"}, time.Now())
	if err != nil {
		t.Fatalf("PreviousExternalRefsInTx: %v", err)
	}
	if ref, ok := refs["bd-a"]; !ok || ref != "https://tracker.test/NEW" {
		t.Errorf("bd-a = (%q, %v), want the newest row's ref", ref, ok)
	}
	if ref, ok := refs["bd-b"]; !ok || ref != "" {
		t.Errorf("bd-b = (%q, %v), want a present NULL (\"\", true)", ref, ok)
	}
	if ref, ok := refs["bd-c"]; ok {
		t.Errorf("bd-c = %q, want absent: it has no history row", ref)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestPreviousExternalRefsInTxFailuresReturnNoPartialMap: a failure in any
// chunk, including a later one after an earlier chunk answered, returns an
// error and no map.
func TestPreviousExternalRefsInTxFailuresReturnNoPartialMap(t *testing.T) {
	boom := errors.New("boom")
	ids := historyIDs(201)
	goodFirstChunk := func(mock sqlmock.Sqlmock) {
		rows := sqlmock.NewRows([]string{"id", "external_ref"})
		for _, id := range ids[:200] {
			rows.AddRow(id, "ref")
		}
		mock.ExpectQuery("").WillReturnRows(rows)
	}
	for _, test := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{"query", func(mock sqlmock.Sqlmock) {
			goodFirstChunk(mock)
			mock.ExpectQuery("").WillReturnError(boom)
		}},
		{"scan", func(mock sqlmock.Sqlmock) {
			goodFirstChunk(mock)
			mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(ids[200]))
		}},
		{"iteration", func(mock sqlmock.Sqlmock) {
			goodFirstChunk(mock)
			mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"id", "external_ref"}).
				AddRow(ids[200], "ref").RowError(0, boom))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, _ := recordingHistoryDB(t)
			test.expect(mock)
			refs, err := PreviousExternalRefsInTx(context.Background(), db, ids, time.Now())
			if err == nil {
				t.Fatal("PreviousExternalRefsInTx succeeded, want the failure")
			}
			if refs != nil {
				t.Errorf("PreviousExternalRefsInTx returned %d refs beside %v, want none", len(refs), err)
			}
			if test.name != "scan" && !errors.Is(err, boom) {
				t.Errorf("error %v does not wrap the cause", err)
			}
		})
	}
}
