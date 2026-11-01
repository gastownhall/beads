package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlcount"
	"github.com/steveyegge/beads/internal/types"
)

// TestInsertUpsertWithHistoryOffIssuesThePreStackStatements pins the
// statements Insert's upsert (CreateOnly unset: the quick, todo, gate and
// batch proxied servers' create) issues while versioned history is off.
//
// The participation_generation write fence (#6943) gave this path an
// existence probe (Exists) before the upsert, so the mint can tell a new row
// from one the upsert resolved to. With history off neither mint entry reads
// anything, so the probe is skipped and the create costs the round trips it
// did before the fence. Those are exactly the three statements below, the
// same three the code before the versioned-history stack (4a67d0d028)
// issues: the upsert, the created event's same-content scan, and its insert.
// The events journal is off too (the default); it adds its own statements
// whatever history does.
//
// Nothing else is scripted, and the mock is reached through sqlcount, which
// counts every statement Insert sends. So a probe, or any other read added
// ahead of the upsert or after it while history is off, fails the pin even
// when Insert swallows the error sqlmock answers it with: sqlmock's own
// checks see only an error the caller returns and expectations left unmet,
// and the count sees the rest. The history-on run of the same script is the
// control: there Insert reads (the probe, then the mint), so the pin has to
// refuse it, which shows that the script is exhaustive and that Insert reads
// the activation this transaction is scoped with.
func TestInsertUpsertWithHistoryOffIssuesThePreStackStatements(t *testing.T) {
	for _, tc := range []struct {
		name          string
		wisp          bool
		table, events string
	}{
		{name: "issues table", table: "issues", events: "events"},
		{name: "wisps table", wisp: true, table: "wisps", events: "wisp_events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := insertUpsertAgainstScript(t, tc.wisp, tc.table, tc.events, false); err != nil {
				t.Fatalf("history off: %v", err)
			}
		})
	}
	t.Run("history on is refused by the same script", func(t *testing.T) {
		err := insertUpsertAgainstScript(t, false, "issues", "events", true)
		if err == nil {
			t.Fatal("Insert with history on passed the history-off script; " +
				"the script no longer sees the reads history adds, so the history-off cases above prove nothing")
		}
		t.Logf("history on, as expected: %v", err)
	})
}

// upsertStatements is how many statements the script below holds, and so
// how many the history-off upsert may send.
const upsertStatements = 3

// insertUpsertAgainstScript runs one upsert Insert into table on a mocked
// transaction scoped with versioned history as asked, against the script of
// the three statements the history-off upsert issues. It returns why the
// Insert broke the script, or nil if it sent exactly the scripted statements.
func insertUpsertAgainstScript(t *testing.T, wisp bool, table, events string, history bool) error {
	t.Helper()
	mock, tx, counts := countedMockTx(t)
	// Every store and provider scopes each transaction with its own setting;
	// a store whose switch is off scopes false.
	t.Cleanup(issueops.ScopeVersionedHistoryTransaction(tx, history))

	mock.ExpectExec(`^INSERT INTO ` + table + ` \( id, content_hash, .* ON DUPLICATE KEY UPDATE .* row_lock = VALUES\(row_lock\)$`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`^SELECT id FROM `+events+` WHERE issue_id = \? AND event_type = \? AND actor = \?`).
		WithArgs("bd-1", string(types.EventCreated), "creator", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`^INSERT INTO ` + events + ` \(id, issue_id, event_type, actor, old_value, new_value, comment, created_at\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := NewIssueSQLRepository(tx).Insert(t.Context(), &types.Issue{
		ID: "bd-1", Title: "upserted", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: at, UpdatedAt: at,
	}, "creator", domain.InsertIssueOpts{UseWispsTable: wisp}); err != nil {
		return fmt.Errorf("the upsert failed against the script: %w", err)
	}
	if n := counts.Total(); n != upsertStatements {
		return fmt.Errorf("the upsert sent %d statements, want exactly the %d scripted; "+
			"sqlmock refused each extra one with an error Insert dropped", n, upsertStatements)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		return fmt.Errorf("the upsert did not send the scripted statements: %w", err)
	}
	return nil
}

var sqlmockSeq atomic.Int64

// countedMockTx begins a transaction on a fresh sqlmock connection that is
// reached through sqlcount, so a pin can count every statement the code
// under test sends, including one whose error it swallowed.
func countedMockTx(t *testing.T) (sqlmock.Sqlmock, *sql.Tx, *sqlcount.Counts) {
	t.Helper()
	dsn := fmt.Sprintf("domain_db_counted_%d", sqlmockSeq.Add(1))
	mockDB, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatalf("sqlmock.NewWithDSN: %v", err)
	}
	counts := &sqlcount.Counts{}
	db := sql.OpenDB(sqlcount.WrapConnector(sqlmockConnector{dsn: dsn, driver: mockDB.Driver()}, counts))
	t.Cleanup(func() {
		_ = db.Close()
		_ = mockDB.Close()
	})
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return mock, tx, counts
}

// sqlmockConnector opens the sqlmock connection registered under dsn, for
// sql.OpenDB through a wrapping connector.
type sqlmockConnector struct {
	dsn    string
	driver driver.Driver
}

func (c sqlmockConnector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }
func (c sqlmockConnector) Driver() driver.Driver                        { return c.driver }
