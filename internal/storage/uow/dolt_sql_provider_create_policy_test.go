package uow

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/schema"
)

const schemataProbeSQL = "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?"

func TestInitSchemaRefusesMissingDatabaseWithCreateDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	lockName := schema.MigrationLockName("beads")
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, "beads", false)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(schemataProbeSQL)).
		WithArgs("beads").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"released"}).AddRow(1))

	p := &doltSQLProvider{
		defaultBranch:  defaultBranch,
		db:             db,
		serverEndpoint: "tcp:127.0.0.1:3306",
	}
	err = p.initSchema(context.Background(), "beads")
	if err == nil || !strings.Contains(err.Error(), `database "beads" not found on Dolt server; check dolt_database in .beads/metadata.json (or BEADS_DOLT_SERVER_DATABASE, --database, --db)`) {
		t.Fatalf("initSchema() error = %v, want the not-found refusal", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a create-disabled open of a missing database must issue no CREATE: %v", err)
	}
}

func TestInitSchemaLosingTheCreateRaceCapturesNoHeal(t *testing.T) {
	schema.SetSharedMigrateConsent(true)
	defer schema.SetSharedMigrateConsent(false)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer db.Close()

	lockName := schema.MigrationLockName("beads")
	expectNoSessionDatabase(mock)
	expectDatabaseExistsProbe(mock, "beads", false)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, 5).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(schemataProbeSQL)).
		WithArgs("beads").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `beads`")).
		WillReturnError(&mysql.MySQLError{Number: 1007, Message: "can't create database beads; database exists"})
	mock.ExpectExec(regexp.QuoteMeta("USE `beads`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectSharedGateProbe(mock, 1, 0)
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
		t.Fatalf("a lost create race must open the database without capturing heal identity: %v", err)
	}
}
