package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestSQLFetchCallPassesUserFlag pins the fetch half of the server-mode SQL
// pull (bd-e2q8zs): the Hosted Dolt route authenticates with --user exactly as
// embedded mode's PullWithStrategy does, and the default route does not.
func TestSQLFetchCallPassesUserFlag(t *testing.T) {
	for _, tc := range []struct {
		user      string
		wantQuery string
		wantArgs  []any
	}{
		{user: "u", wantQuery: "CALL DOLT_FETCH('--user', ?, ?, ?)", wantArgs: []any{"u", "origin", "main"}},
		{user: "", wantQuery: "CALL DOLT_FETCH(?, ?)", wantArgs: []any{"origin", "main"}},
	} {
		query, args := sqlFetchCall(tc.user, "origin", "main")
		if query != tc.wantQuery || !reflect.DeepEqual(args, tc.wantArgs) {
			t.Errorf("sqlFetchCall(%q) = (%q, %v), want (%q, %v)", tc.user, query, args, tc.wantQuery, tc.wantArgs)
		}
	}
}

// TestFetchAndMergeTrackingRefInTx pins the statements the SQL pull issues in
// its transaction: it fetches (with --user when the route has one) and then
// merges the tracking ref with CALL DOLT_MERGE, never CALL DOLT_PULL, whose
// merge message copies the stored remote URL, credentials included. It also
// pins how the DOLT_MERGE outcome is reported: a no-op merges nothing, a
// conflicted row sets the flag, Dolt's "Already up to date." error is
// swallowed, and a failed fetch comes back as fetchErr naming the tracking
// ref, before any merge runs.
func TestFetchAndMergeTrackingRefInTx(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       string
		fetchQuery string
		fetchArgs  []any
		// fetchFails makes the fetch statement error instead of return a row.
		fetchFails bool
		// mergeErr, when non-nil, is returned by the merge statement instead
		// of a row; mergeMessage/mergeConflicts build the row otherwise.
		mergeErr       error
		mergeMessage   string
		mergeConflicts int
		wantReported   bool
		wantMerged     bool
		wantConflicted bool
		wantFetchErr   string // non-empty: want a fetchErr containing this
	}{
		{name: "remote_user", user: "e2q8zs-user", fetchQuery: "CALL DOLT_FETCH('--user', ?, ?, ?)", fetchArgs: []any{"e2q8zs-user", "origin", "main"},
			mergeMessage: doltMergeSucceededMessage, wantReported: true, wantMerged: true},
		{name: "default", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: doltMergeSucceededMessage, wantReported: true, wantMerged: true},
		{name: "no-op merges nothing", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: "Everything up-to-date", wantReported: true},
		{name: "conflicted merge reports the flag", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: "conflicts found", mergeConflicts: 1, wantReported: true, wantConflicted: true},
		{name: "already up to date error is swallowed", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeErr: errors.New("Already up to date.")},
		{name: "fetch failure names the tracking ref", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			fetchFails: true, wantFetchErr: "fetch from origin/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			fetch := mock.ExpectQuery(tc.fetchQuery).WithArgs(toDriverValues(tc.fetchArgs)...)
			if tc.fetchFails {
				fetch.WillReturnError(errors.New("no such remote"))
			} else {
				fetch.WillReturnRows(sqlmock.NewRows([]string{"success"}).AddRow(1))
				merge := mock.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main")
				if tc.mergeErr != nil {
					merge.WillReturnError(tc.mergeErr)
				} else {
					merge.WillReturnRows(sqlmock.NewRows([]string{"hash", "fast_forward", "conflicts", "message"}).
						AddRow("abc", 0, tc.mergeConflicts, tc.mergeMessage))
				}
			}

			store := &DoltStore{branch: "main"}
			ctx := context.Background()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			report, mergeErr, fetchErr := store.fetchAndMergeTrackingRefInTx(ctx, tx, "origin", tc.user)
			if mergeErr != nil {
				t.Fatalf("mergeErr = %v, want nil", mergeErr)
			}
			if tc.wantFetchErr != "" {
				if fetchErr == nil || !strings.Contains(fetchErr.Error(), tc.wantFetchErr) {
					t.Fatalf("fetchErr = %v, want an error containing %q", fetchErr, tc.wantFetchErr)
				}
			} else if fetchErr != nil {
				t.Fatalf("fetchErr = %v, want nil", fetchErr)
			}
			if report.Reported != tc.wantReported || report.Merged != tc.wantMerged || report.Conflicted != tc.wantConflicted {
				t.Errorf("report = %+v, want Reported=%v Merged=%v Conflicted=%v",
					report, tc.wantReported, tc.wantMerged, tc.wantConflicted)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("statements: %v", err)
			}
		})
	}
}

func toDriverValues(args []any) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}
