package uow

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/cenkalti/backoff/v4"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestClassifyInitSchemaErrorKeepsBootstrapPreparationErrorsDistinct(t *testing.T) {
	t.Run("permanent preparation remains permanent when cleanup fails", func(t *testing.T) {
		preparationErr := errors.New("create database failed")
		err := classifyInitSchemaError(errors.Join(
			&bootstrapPreparationError{err: preparationErr},
			schema.ErrMigrationLockRelease,
		))

		var permanentErr *backoff.PermanentError
		if !errors.As(err, &permanentErr) {
			t.Fatalf("classifyInitSchemaError() error = %T %v, want permanent", err, err)
		}
		if !errors.Is(err, preparationErr) || !errors.Is(err, schema.ErrMigrationLockRelease) {
			t.Fatalf("classifyInitSchemaError() error = %v, want preparation and release errors", err)
		}
	})

	for _, tt := range []struct {
		name      string
		retryable bool
		permanent bool
	}{
		{name: "initial bare create serialization retries", retryable: true},
		{name: "use serialization remains permanent", permanent: true},
		{name: "capture serialization remains permanent", permanent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serializationErr := &mysql.MySQLError{Number: 1213}
			err := classifyInitSchemaError(&bootstrapPreparationError{
				err:       serializationErr,
				retryable: tt.retryable,
			})

			var permanentErr *backoff.PermanentError
			if got := errors.As(err, &permanentErr); got != tt.permanent {
				t.Fatalf("classifyInitSchemaError() permanent = %t, want %t (error = %v)", got, tt.permanent, err)
			}
			if !errors.Is(err, serializationErr) {
				t.Fatalf("classifyInitSchemaError() error = %v, want serialization error", err)
			}
			if !tt.permanent && (!strings.Contains(err.Error(), "bootstrap preparation") || strings.Contains(err.Error(), "uow: migrate:")) {
				t.Fatalf("classifyInitSchemaError() error = %v, want retryable bootstrap preparation error", err)
			}
		})
	}
}

func TestInitSchemaAcquiresMigrationLockBeforeBootstrapDDL(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	lockName := schema.MigrationLockName("beads")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SHOW DATABASES")).
		WillReturnRows(sqlmock.NewRows([]string{"Database"}))
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DATABASE(), @@server_uuid, DOLT_HASHOF('HEAD')")).
		WillReturnRows(sqlmock.NewRows([]string{"database", "server_uuid", "head"}).AddRow("beads", "server-uuid", "initial-head"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_log")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO dolt_ignore VALUES (?, true)")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("first migration statement failed"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	p := &doltSQLProvider{
		defaultBranch:   defaultBranch,
		db:              db,
		serverEndpoint:  "tcp:127.0.0.1:3306",
		createIfMissing: true,
	}
	err = p.initSchema(context.Background(), "beads")
	if err == nil || !strings.Contains(err.Error(), "first migration statement failed") {
		t.Fatalf("initSchema() error = %v, want first migration sentinel", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ordered bootstrap SQL expectations: %v", err)
	}
}
