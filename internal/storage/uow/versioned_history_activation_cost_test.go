package uow_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// What versioned-history activation costs a unit-of-work provider whose
// history is off, counted at the driver.
//
// Activation used to open a unit of work of its own on every provider it
// activated, to read one config row and roll back: START TRANSACTION, SELECT,
// ROLLBACK, on a pinned session, at every proxied command's open. It now hands
// the provider the read, and BeginTx makes it inside the first unit of work,
// after that unit's own START TRANSACTION. A unit of work does not say whether
// it will write, so the first one carries the read whether it writes or not;
// every later one carries nothing.

var vhConfigRead = regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")

var vhMockSeq atomic.Int64

type vhMockConnector struct {
	dsn    string
	driver driver.Driver
}

func (c vhMockConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }
func (c vhMockConnector) Driver() driver.Driver                        { return c.driver }

func vhCountedProvider(t *testing.T) (uow.UnitOfWorkProvider, sqlmock.Sqlmock, *sqlcount.Counts) {
	t.Helper()
	dsn := fmt.Sprintf("uow_vh_activation_%d", vhMockSeq.Add(1))
	mockDB, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatalf("sqlmock.NewWithDSN: %v", err)
	}
	counts := &sqlcount.Counts{}
	db := sql.OpenDB(sqlcount.WrapConnector(vhMockConnector{dsn: dsn, driver: mockDB.Driver()}, counts))
	t.Cleanup(func() {
		_ = db.Close()
		_ = mockDB.Close()
	})
	return uow.NewDoltSQLProviderOverDBForTest(db), mock, counts
}

// vhUnit opens and rolls back one unit of work, scripting its statements, and
// reports whether it was scoped for minting.
func vhUnit(t *testing.T, p uow.UnitOfWorkProvider, mock sqlmock.Sqlmock, expectRead func()) bool {
	t.Helper()
	mock.ExpectExec(regexp.QuoteMeta("START TRANSACTION;")).WillReturnResult(sqlmock.NewResult(0, 0))
	if expectRead != nil {
		expectRead()
	}
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK;")).WillReturnResult(sqlmock.NewResult(0, 0))
	uw, err := p.NewUOW(context.Background())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	scoped := uow.VersionedHistoryScopedForTest(uw)
	uw.Close(context.Background())
	return scoped
}

func TestActivateProviderSendsNoStatementsToAHistoryOffProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows func() *sqlmock.Rows
		want bool
	}{
		{name: "no row", rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"value"}) }, want: false},
		{name: "row false", rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"value"}).AddRow("false") }, want: false},
		{name: "row true", rows: func() *sqlmock.Rows { return sqlmock.NewRows([]string{"value"}).AddRow("true") }, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, mock, counts := vhCountedProvider(t)
			if _, err := versionedhistory.ActivateProvider(context.Background(), p, nil); err != nil {
				t.Fatal(err)
			}
			if got := counts.Snapshot(); got != (sqlcount.Snapshot{}) {
				t.Fatalf("activation sent %+v to the provider; want nothing: the switch's row is the first unit of work's to read", got)
			}
			if got := vhUnit(t, p, mock, func() {
				mock.ExpectQuery(vhConfigRead).WithArgs(versionedhistory.ConfigKey).WillReturnRows(tc.rows())
			}); got != tc.want {
				t.Errorf("first unit of work scoped %v, want %v", got, tc.want)
			}
			if got := vhUnit(t, p, mock, nil); got != tc.want {
				t.Errorf("second unit of work scoped %v, want %v without reading the row again", got, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			// START TRANSACTION + ROLLBACK per unit, plus the one read.
			if got := counts.Snapshot().Total(); got != 2*2+1 {
				t.Errorf("two units of work sent %d statements, want 5", got)
			}
		})
	}
}

// TestDeferredProviderReadErrorFailsTheUnitOfWork: a unit of work whose switch
// read fails does not begin. Its transaction is rolled back and its session
// released, so it can write nothing scoped off, and the next unit of work reads
// the row again.
func TestDeferredProviderReadErrorFailsTheUnitOfWork(t *testing.T) {
	p, mock, _ := vhCountedProvider(t)
	if _, err := versionedhistory.ActivateProvider(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("connection reset")
	mock.ExpectExec(regexp.QuoteMeta("START TRANSACTION;")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(vhConfigRead).WithArgs(versionedhistory.ConfigKey).WillReturnError(readErr)
	mock.ExpectExec(regexp.QuoteMeta("ROLLBACK;")).WillReturnResult(sqlmock.NewResult(0, 0))
	if uw, err := p.NewUOW(context.Background()); !errors.Is(err, readErr) {
		if uw != nil {
			uw.Close(context.Background())
		}
		t.Fatalf("NewUOW = %v, want the switch read's error", err)
	}
	if !vhUnit(t, p, mock, func() {
		mock.ExpectQuery(vhConfigRead).WithArgs(versionedhistory.ConfigKey).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("true"))
	}) {
		t.Error("the unit of work after a failed switch read did not read the row again")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
