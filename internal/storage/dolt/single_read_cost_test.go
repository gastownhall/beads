package dolt

import (
	"context"
	"errors"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// expectAutocommit mocks the once-per-store read of the server's autocommit mode.
func expectAutocommit(mock sqlmock.Sqlmock, value any) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT @@autocommit")).
		WillReturnRows(sqlmock.NewRows([]string{"@@autocommit"}).AddRow(value))
}

// TestSingleStatementReadsOpenNoTransaction pins what a one-statement settings
// read costs a server-mode store with autocommit on, counted at the driver: the
// statement, after the store's one read of the mode. A transaction around it
// added START TRANSACTION and ROLLBACK, two more round trips, for a consistency
// one statement already has.
//
// sqlmock refuses a Begin nothing expected, so a read that went back to a
// transaction fails here as well as in the counts below.
func TestSingleStatementReadsOpenNoTransaction(t *testing.T) {
	configKV := sqlmock.NewRows([]string{"key", "value"}).AddRow("a.b", "1")
	for _, tc := range []struct {
		name   string
		expect func(sqlmock.Sqlmock)
		read   func(*DoltStore) error
	}{
		{
			name: "GetConfig",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
					WithArgs("issue_prefix").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("bd"))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetConfig(context.Background(), "issue_prefix")
				if err == nil && got != "bd" {
					return errors.New("GetConfig returned " + got)
				}
				return err
			},
		},
		{
			name: "GetAllConfig",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config")).WillReturnRows(configKV)
			},
			read: func(s *DoltStore) error {
				got, err := s.GetAllConfig(context.Background())
				if err == nil && got["a.b"] != "1" {
					return errors.New("GetAllConfig lost the row")
				}
				return err
			},
		},
		{
			name: "GetConfigByPrefix",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` LIKE ?")).
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow("a.b", "1"))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetConfigByPrefix(context.Background(), "a.")
				if err == nil && got["a.b"] != "1" {
					return errors.New("GetConfigByPrefix lost the row")
				}
				return err
			},
		},
		{
			name: "GetConfigKeys",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")).
					WithArgs("a.b", "absent").
					WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow("a.b", "1"))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetConfigKeys(context.Background(), "a.b", "absent")
				if err != nil {
					return err
				}
				if _, present := got["absent"]; present || got["a.b"] != "1" || len(got) != 1 {
					return errors.New("GetConfigKeys returned the wrong rows")
				}
				return nil
			},
		},
		{
			name: "GetMetadata",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT value FROM metadata WHERE `key` = ?")).
					WithArgs("_project_id").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("p"))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetMetadata(context.Background(), "_project_id")
				if err == nil && got != "p" {
					return errors.New("GetMetadata returned " + got)
				}
				return err
			},
		},
		{
			name: "GetLocalMetadata",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT value FROM local_metadata WHERE `key` = ?")).
					WithArgs("k").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("v"))
			},
			read: func(s *DoltStore) error {
				got, err := s.GetLocalMetadata(context.Background(), "k")
				if err == nil && got != "v" {
					return errors.New("GetLocalMetadata returned " + got)
				}
				return err
			},
		},
		{
			name: "GetInfraTypes",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
					WithArgs("types.infra").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("agent"))
			},
			read: func(s *DoltStore) error {
				if !s.GetInfraTypes(context.Background())["agent"] {
					return errors.New("GetInfraTypes lost the configured type")
				}
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, counts := vhCountedStore(t)
			expectAutocommit(mock, 1)
			tc.expect(mock)
			if err := tc.read(store); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			got := counts.Snapshot()
			if got.Begin != 0 || got.Rollback != 0 {
				t.Errorf("read opened a transaction: %+v", got)
			}
			if got.Total() != 2 {
				t.Errorf("read sent %d statements, want the mode read and the read: %+v", got.Total(), got)
			}
		})
	}
}

// TestGetConfigKeysReportsTheSpellingAskedFor: on a case-insensitive collation the
// IN query can return ROUTING.MODE for routing.mode, as a read of that key would.
// A caller looks it up by the key it asked for, so routing must not lose it.
func TestGetConfigKeysReportsTheSpellingAskedFor(t *testing.T) {
	store, mock, _ := vhCountedStore(t)
	expectAutocommit(mock, 1)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `key`, value FROM config WHERE `key` IN (?,?)")).
		WithArgs("routing.mode", "routing.default").
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow("ROUTING.MODE", "auto").AddRow("routing.default", "x"))

	got, err := store.GetConfigKeys(context.Background(), "routing.mode", "routing.default")
	if err != nil {
		t.Fatal(err)
	}
	if got["routing.mode"] != "auto" || got["routing.default"] != "x" || len(got) != 2 {
		t.Fatalf("GetConfigKeys = %v", got)
	}
}

// TestSingleStatementReadRetriesALostConnection keeps the one thing the
// transaction wrapper did for these reads besides the transaction: a pooled
// connection the server reaped surfaces as a retryable error, not a failed read.
func TestSingleStatementReadRetriesALostConnection(t *testing.T) {
	store, mock, _ := vhCountedStore(t)
	expectAutocommit(mock, 1)
	query := regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnError(errVHReadLost)
	mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("bd"))

	got, err := store.GetConfig(context.Background(), "issue_prefix")
	if err != nil {
		t.Fatalf("GetConfig after a lost connection: %v", err)
	}
	if got != "bd" {
		t.Fatalf("GetConfig = %q, want the retried read's answer", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetInfraTypesRetriesALostConnection: GetInfraTypes caches what it resolves
// for the store's lifetime, so a lost connection must be retried rather than
// read as "no custom types". The configured type goes to the wisp tables, and a
// cached default would send it to the durable issues table instead.
func TestGetInfraTypesRetriesALostConnection(t *testing.T) {
	store, mock, _ := vhCountedStore(t)
	expectAutocommit(mock, 1)
	query := regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	mock.ExpectQuery(query).WithArgs("types.infra").WillReturnError(errVHReadLost)
	mock.ExpectQuery(query).WithArgs("types.infra").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("dispatch"))

	if !store.GetInfraTypes(context.Background())["dispatch"] {
		t.Fatal("GetInfraTypes lost the configured type to a retryable read error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestSingleStatementReadKeepsATransactionWithoutAutocommit: with autocommit off
// (dolt sql-server --no-auto-commit) a bare SELECT holds its session on that
// snapshot, which Dolt 2.6.0 and 2.7.2 both show, so the read keeps the
// transaction that refreshes it. A mode the server would not report gets the same
// safe answer, and is asked again.
func TestSingleStatementReadKeepsATransactionWithoutAutocommit(t *testing.T) {
	query := regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")
	row := func() *sqlmock.Rows { return sqlmock.NewRows([]string{"value"}).AddRow("bd") }

	t.Run("autocommit off is remembered", func(t *testing.T) {
		store, mock, _ := vhCountedStore(t)
		expectAutocommit(mock, 0)
		for range 2 {
			mock.ExpectBegin()
			mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(row())
			mock.ExpectRollback()
		}
		for range 2 {
			if got, err := store.GetConfig(context.Background(), "issue_prefix"); err != nil || got != "bd" {
				t.Fatalf("GetConfig = %q, %v", got, err)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a failed mode read takes the transaction and asks again", func(t *testing.T) {
		store, mock, _ := vhCountedStore(t)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT @@autocommit")).WillReturnError(errVHReadLost)
		mock.ExpectBegin()
		mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(row())
		mock.ExpectRollback()
		expectAutocommit(mock, 1)
		mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(row())
		for range 2 {
			if got, err := store.GetConfig(context.Background(), "issue_prefix"); err != nil || got != "bd" {
				t.Fatalf("GetConfig = %q, %v", got, err)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("autocommit on is read once", func(t *testing.T) {
		store, mock, counts := vhCountedStore(t)
		expectAutocommit(mock, 1)
		mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(row())
		mock.ExpectQuery(query).WithArgs("issue_prefix").WillReturnRows(row())
		for range 2 {
			if _, err := store.GetConfig(context.Background(), "issue_prefix"); err != nil {
				t.Fatal(err)
			}
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		if got := counts.Snapshot().Total(); got != 3 {
			t.Fatalf("two reads sent %d statements, want 3", got)
		}
	})
}

// TestSingleStatementReadOnClosedStore keeps the closed-store refusal the
// transaction wrapper gave these reads.
func TestSingleStatementReadOnClosedStore(t *testing.T) {
	store, _, counts := vhCountedStore(t)
	store.closed.Store(true)
	if _, err := store.GetConfig(context.Background(), "issue_prefix"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("GetConfig on a closed store = %v, want ErrStoreClosed", err)
	}
	if got := counts.Snapshot().Total(); got != 0 {
		t.Fatalf("closed store sent %d statements", got)
	}
}
