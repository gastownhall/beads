package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// stompRefusal is the pinned engine's refusal as the embedded pull returns it.
var stompRefusal = errors.New("merge origin/main: Error 1105: error: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge.")

const (
	mergeBaseQuery = "SELECT DOLT_MERGE_BASE('HEAD', ?), DOLT_HASHOF(?)"
	mergingQuery   = "SELECT is_merging FROM dolt_merge_status"
	ignoredQuery   = "SELECT table_name FROM dolt_status_ignored WHERE ignored"
)

// newIgnoredMergeMock is a single-session DBConn that matches queries exactly.
func newIgnoredMergeMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// TestSettleIgnoredTableStompEscalatesWhenContainmentIsUnknown pins that an
// unavailable containment proof only declines the no-op success: positive
// evidence that ignored working rows block the merge still classifies it, and
// the original refusal stays reachable.
func TestSettleIgnoredTableStompEscalatesWhenContainmentIsUnknown(t *testing.T) {
	cases := map[string]func(sqlmock.Sqlmock){
		"merge base unreadable": func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnError(errors.New("connection reset"))
		},
		"remote tip not contained": func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow("aaaa", "bbbb"))
		},
	}
	for name, expectProof := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock := newIgnoredMergeMock(t)
			expectProof(mock)
			mock.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events").AddRow("wisps"))

			err := settleIgnoredTableStomp(context.Background(), db, "origin/main", stompRefusal)
			var ignoredErr *IgnoredTablesMergeError
			if !errors.As(err, &ignoredErr) {
				t.Fatalf("settle = %v; want an IgnoredTablesMergeError", err)
			}
			if !slices.Equal(ignoredErr.Tables, []string{"events"}) {
				t.Fatalf("Tables = %v, want [events]", ignoredErr.Tables)
			}
			if !errors.Is(err, stompRefusal) || err.Error() != stompRefusal.Error() {
				t.Fatalf("typed error does not preserve the original refusal: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStompedTables(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{name: "pinned multiline form", err: errors.New("Error 1105: error: local changes would be stomped by merge:\n\twisps\n\tevents\n\twisps\n Please commit your changes before you merge."), want: []string{"events", "wisps"}},
		{name: "reported inline form", err: errors.New("merge origin/main: Error 1105: error: local changes would be stomped by merge: events\n Please commit your changes before you merge."), want: []string{"events"}},
		{name: "CLI wrapper around the diagnostic", err: errors.New("dolt pull failed: Fetching...\nerror: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge.: exit status 1"), want: []string{"events"}},
		{name: "nil", err: nil},
		{name: "unrelated error", err: errors.New("fetch from origin/main: connection refused")},
		{name: "no terminator", err: errors.New("error: local changes would be stomped by merge:\n\tevents\n")},
		{name: "no table named", err: errors.New("error: local changes would be stomped by merge:\n\t\n Please commit your changes before you merge.")},
		{name: "diagnostic repeated", err: errors.New("error: local changes would be stomped by merge:\n\tevents\n Please commit your changes before you merge. error: local changes would be stomped by merge:\n\tissues\n Please commit your changes before you merge.")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := StompedTables(tc.err)
			if ok != (tc.want != nil) || !slices.Equal(got, tc.want) {
				t.Fatalf("StompedTables = %v, %v; want %v, %v", got, ok, tc.want, tc.want != nil)
			}
		})
	}
}

// TestClassifyIgnoredTableStompDeclinesWithoutPositiveEvidence pins that an
// ignored-table diagnosis is never invented: a named table the engine does not
// report as ignored, or ignore evidence that cannot be read completely, leaves
// the original error untouched.
func TestClassifyIgnoredTableStompDeclinesWithoutPositiveEvidence(t *testing.T) {
	mixed := errors.New("error: local changes would be stomped by merge:\n\tevents\n\tissues\n Please commit your changes before you merge.")
	cases := []struct {
		name   string
		err    error
		expect func(sqlmock.Sqlmock)
	}{
		{name: "unrelated error is not queried", err: errors.New("push to origin/main: permission denied"), expect: func(sqlmock.Sqlmock) {}},
		{name: "mixed tracked and ignored tables", err: mixed, expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events"))
		}},
		{name: "named table not ignored", err: stompRefusal, expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("wisps"))
		}},
		{name: "system table missing", err: stompRefusal, expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(ignoredQuery).WillReturnError(errors.New("Error 1146: table not found: dolt_status_ignored"))
		}},
		{name: "iteration fails after the table was seen", err: stompRefusal, expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events").RowError(0, errors.New("connection reset")))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newIgnoredMergeMock(t)
			tc.expect(mock)
			got := ClassifyIgnoredTableStomp(context.Background(), db, tc.err)
			if got != tc.err {
				t.Fatalf("ClassifyIgnoredTableStomp = %#v; want the original error unchanged", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestProveMergeNoopRequiresCompleteEvidence pins that a no-op is reported
// only on an explicit containment AND an explicit closed merge state.
func TestProveMergeNoopRequiresCompleteEvidence(t *testing.T) {
	const hash = "0123456789abcdefghijklmnopqrstuv"
	contained := func(m sqlmock.Sqlmock) {
		m.ExpectQuery(mergeBaseQuery).WithArgs("origin/main", "origin/main").
			WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(hash, hash))
	}
	cases := []struct {
		name    string
		expect  func(sqlmock.Sqlmock)
		want    bool
		wantErr bool
	}{
		{name: "contained, no open merge", want: true, expect: func(m sqlmock.Sqlmock) {
			contained(m)
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		}},
		{name: "merge base NULL", expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(nil, hash))
		}},
		{name: "remote ref missing", expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(nil, nil))
		}},
		{name: "empty hashes", expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow("", ""))
		}},
		{name: "merge base query fails", wantErr: true, expect: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnError(errors.New("branch not found: origin/main"))
		}},
		{name: "merge open", expect: func(m sqlmock.Sqlmock) {
			contained(m)
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(true))
		}},
		{name: "merge state NULL", expect: func(m sqlmock.Sqlmock) {
			contained(m)
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(nil))
		}},
		{name: "merge state has no row", wantErr: true, expect: func(m sqlmock.Sqlmock) {
			contained(m)
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}))
		}},
		{name: "merge state table missing", wantErr: true, expect: func(m sqlmock.Sqlmock) {
			contained(m)
			m.ExpectQuery(mergingQuery).WillReturnError(errors.New("table not found: dolt_merge_status"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newIgnoredMergeMock(t)
			tc.expect(mock)
			got, err := ProveMergeNoop(context.Background(), db, "origin/main")
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("ProveMergeNoop = %v, %v; want %v, error %v", got, err, tc.want, tc.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSettleIgnoredTableStompPreservesUnconfirmedFailures pins the pull
// wrapper end to end: without a complete no-op proof and without positive
// ignore evidence the original failure is returned, never success.
func TestSettleIgnoredTableStompPreservesUnconfirmedFailures(t *testing.T) {
	t.Run("unrelated error", func(t *testing.T) {
		db, mock := newIgnoredMergeMock(t)
		orig := errors.New("merge origin/main: constraint violations")
		if got := settleIgnoredTableStomp(context.Background(), db, "origin/main", orig); got != orig {
			t.Fatalf("settle = %v; want the original error", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("open merge and tracked table", func(t *testing.T) {
		db, mock := newIgnoredMergeMock(t)
		mock.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow("h", "h"))
		mock.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(true))
		mock.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
		if got := settleIgnoredTableStomp(context.Background(), db, "origin/main", stompRefusal); got != stompRefusal {
			t.Fatalf("settle = %v; want the original refusal", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestSettleIgnoredTableStompContributorScenarios carries the ahead/behind
// scenarios of the contributor's skip-the-merge fix (#8104) over to the
// post-refusal proof: the merge still runs, and only after the engine refused
// it does the wrapper decide. A remote with nothing this clone lacks (the
// reported 430 ahead / 0 behind, and level) is the no-op pull; a remote with
// commits this clone lacks (one ahead, diverged) or unreadable evidence keeps
// the refusal, as the typed ignored-table error, never success.
func TestSettleIgnoredTableStompContributorScenarios(t *testing.T) {
	const (
		head   = "hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh"
		remote = "rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"
		base   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	cases := []struct {
		name   string
		proof  func(sqlmock.Sqlmock)
		wantOK bool
	}{
		{name: "remote is an ancestor (430 ahead, 0 behind)", wantOK: true, proof: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(remote, remote))
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		}},
		{name: "level (0 ahead, 0 behind)", wantOK: true, proof: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(head, head))
			m.ExpectQuery(mergingQuery).WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(false))
		}},
		{name: "remote one ahead (0 ahead, 1 behind)", proof: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(head, remote))
		}},
		{name: "diverged (2 ahead, 1 behind)", proof: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnRows(sqlmock.NewRows([]string{"base", "tip"}).AddRow(base, remote))
		}},
		{name: "evidence unavailable", proof: func(m sqlmock.Sqlmock) {
			m.ExpectQuery(mergeBaseQuery).WillReturnError(errors.New("count failed"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newIgnoredMergeMock(t)
			tc.proof(mock)
			if !tc.wantOK {
				mock.ExpectQuery(ignoredQuery).WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("events"))
			}

			err := settleIgnoredTableStomp(context.Background(), db, "origin/main", stompRefusal)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("settle = %v; want the no-op pull to succeed", err)
				}
			} else {
				var ignoredErr *IgnoredTablesMergeError
				if !errors.As(err, &ignoredErr) || !errors.Is(err, stompRefusal) {
					t.Fatalf("settle = %v; want the typed refusal wrapping the original", err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
