package versioncontrolops

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// These tests use sqlmock, matching the convention already established in
// this package (see clone_test.go, backup_test.go) rather than a live
// embedded Dolt fixture: the package has no existing helper that spins up a
// real multi-commit Dolt history, and building one just for this file would
// invent a new harness the task instructions say to avoid. sqlmock lets us
// exercise the ahead/behind branching logic exhaustively (including a
// genuine "behind" case and a diverged/ahead case) by controlling the
// COUNT(*) results directly, without needing real divergent commits.
// Deferred to piece-2 integration tests: driving these against a REAL
// embedded Dolt database with actual multi-commit ancestry/divergence and a
// genuine fast-forward merge outcome.

func TestLocalIsStrictAncestorOf(t *testing.T) {
	tests := []struct {
		name   string
		ahead  int
		behind int
		want   bool
	}{
		{name: "equal refs (ahead=0, behind=0) is not a strict ancestor", ahead: 0, behind: 0, want: false},
		{name: "strictly behind (ahead=0, behind>=1) is a strict ancestor", ahead: 0, behind: 3, want: true},
		{name: "diverged (ahead>0, behind>0) is not a strict ancestor", ahead: 2, behind: 1, want: false},
		{name: "purely ahead (ahead>0, behind=0) is not a strict ancestor", ahead: 1, behind: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			ref := "origin/main"
			expectedQuery := fmt.Sprintf(`
		SELECT
			(SELECT COUNT(*) FROM dolt_log WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log AS OF '%s')) AS ahead,
			(SELECT COUNT(*) FROM dolt_log AS OF '%s' WHERE commit_hash NOT IN
				(SELECT commit_hash FROM dolt_log)) AS behind
	`, ref, ref)
			rows := sqlmock.NewRows([]string{"ahead", "behind"}).AddRow(tt.ahead, tt.behind)
			mock.ExpectQuery(regexp.QuoteMeta(expectedQuery)).WillReturnRows(rows)

			got, err := LocalIsStrictAncestorOf(context.Background(), db, ref)
			if err != nil {
				t.Fatalf("LocalIsStrictAncestorOf: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalIsStrictAncestorOfInvalidRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No query expectations set: an invalid ref must be rejected before any
	// SQL runs.
	_, err = LocalIsStrictAncestorOf(context.Background(), db, "bad ref; drop table issues")
	if err == nil {
		t.Fatal("expected error for invalid ref, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingSetClean(t *testing.T) {
	tests := []struct {
		name   string
		tables []string
		want   bool
	}{
		{name: "no rows is clean", tables: nil, want: true},
		{name: "non-wisp dirty table is not clean", tables: []string{"issues"}, want: false},
		{name: "wisps table alone is ignored and still clean", tables: []string{"wisps"}, want: true},
		{name: "wisp_ prefixed table alone is ignored and still clean", tables: []string{"wisp_foo"}, want: true},
		{name: "wisp rows plus a real dirty table is not clean", tables: []string{"wisps", "wisp_foo", "issues"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			rows := sqlmock.NewRows([]string{"table_name"})
			for _, tbl := range tt.tables {
				rows.AddRow(tbl)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT table_name FROM dolt_status")).WillReturnRows(rows)

			got, err := WorkingSetClean(context.Background(), db)
			if err != nil {
				t.Fatalf("WorkingSetClean: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFastForwardAdopt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	ref := "origin/main"
	mock.ExpectExec(regexp.QuoteMeta("CALL DOLT_MERGE('--ff-only', ?)")).
		WithArgs(ref).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := FastForwardAdopt(context.Background(), db, ref); err != nil {
		t.Fatalf("FastForwardAdopt: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFastForwardAdoptInvalidRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// No exec expectations set: an invalid ref must be rejected before any
	// SQL runs.
	err = FastForwardAdopt(context.Background(), db, "bad ref; drop table issues")
	if err == nil {
		t.Fatal("expected error for invalid ref, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
