package main

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCleanupCandidateIndexesDropsByDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, idx := range indexes {
		mock.ExpectExec(regexp.QuoteMeta(idx.Drop)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}

	if err := cleanupCandidateIndexes(context.Background(), db, false); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupCandidateIndexesHonorsKeepIndexes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := cleanupCandidateIndexes(context.Background(), db, true); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCleansUpCandidateIndexesAfterCreateFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}

	oldSQLOpen := sqlOpen
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) {
		if driverName != "mysql" {
			t.Fatalf("driverName = %q, want mysql", driverName)
		}
		if dataSourceName != "sqlmock-dsn" {
			t.Fatalf("dataSourceName = %q, want sqlmock-dsn", dataSourceName)
		}
		return db, nil
	}
	t.Cleanup(func() {
		sqlOpen = oldSQLOpen
	})

	mock.ExpectPing()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM issues WHERE status IN ('open','in_progress') ORDER BY priority ASC, created_at DESC, id ASC LIMIT 100`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-a"))

	expectDropAll(mock) // baseline
	for _, idx := range indexes {
		expectDropAll(mock)
		mock.ExpectExec(regexp.QuoteMeta(idx.SQL)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
	expectDropAll(mock) // all-indexes state starts from a clean slate
	mock.ExpectExec(regexp.QuoteMeta(indexes[0].SQL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(indexes[1].SQL)).
		WillReturnError(errors.New("create failed"))
	expectDropAll(mock) // deferred cleanup still runs after the create error
	mock.ExpectClose()

	err = runWithArgs(context.Background(), []string{
		"--dsn", "sqlmock-dsn",
		"--concurrency", "0",
		"--iterations", "1",
	})
	if err == nil {
		t.Fatal("expected create-index error")
	}
	if !strings.Contains(err.Error(), "create "+indexes[1].Name) {
		t.Fatalf("error = %v, want create failure for %s", err, indexes[1].Name)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildQueriesUsesTypedDependencyTargetProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM issues WHERE status IN ('open','in_progress') ORDER BY priority ASC, created_at DESC, id ASC LIMIT 100`)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-a"))

	queries, err := buildQueries(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, qc := range queries {
		if qc.Name != "candidate_blocking_deps" {
			continue
		}
		found = true
		if !strings.Contains(qc.Query, dependencyTargetExpr) {
			t.Fatalf("candidate query = %q, want typed target projection", qc.Query)
		}
		if strings.Contains(qc.Query, "depends_on"+"_id") {
			t.Fatalf("candidate query still references generated column: %q", qc.Query)
		}
	}
	if !found {
		t.Fatal("candidate_blocking_deps query not found")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectDropAll(mock sqlmock.Sqlmock) {
	for _, idx := range indexes {
		mock.ExpectExec(regexp.QuoteMeta(idx.Drop)).
			WillReturnResult(sqlmock.NewResult(0, 0))
	}
}
