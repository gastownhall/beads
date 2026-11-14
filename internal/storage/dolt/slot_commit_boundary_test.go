package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// slotCommitBoundaryDriver models permanent metadata writes through their SQL
// mutation, EventUpdated insert, DOLT_ADD, and DOLT_COMMIT phases. A failed
// transaction deliberately leaves its starting metadata unchanged so an unsafe
// retry is visible as a duplicate update and event attempt.
type slotCommitBoundaryDriver struct {
	mu sync.Mutex

	metadata        string
	stageErr        error
	commitErr       error
	sqlCommitErr    error
	nothingToCommit bool
	activeWisp      bool

	metadataUpdates int
	eventInserts    int
	stageCalls      int
	doltCommits     int
	txAttempts      int
	txCommits       int
	txRollbacks     int

	// stagedTables is every table a DOLT_ADD named, however many tables
	// each call bound: the staged set, not the call count, is the contract.
	stagedTables []string
}

func (d *slotCommitBoundaryDriver) Open(string) (driver.Conn, error) {
	return &slotCommitBoundaryConn{driver: d}, nil
}

func (d *slotCommitBoundaryDriver) Connect(context.Context) (driver.Conn, error) {
	return &slotCommitBoundaryConn{driver: d}, nil
}

func (d *slotCommitBoundaryDriver) Driver() driver.Driver { return d }

type slotCommitBoundaryConn struct {
	driver *slotCommitBoundaryDriver
}

func (c *slotCommitBoundaryConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("slot commit boundary driver does not prepare statements")
}

func (c *slotCommitBoundaryConn) Close() error { return nil }

func (c *slotCommitBoundaryConn) Begin() (driver.Tx, error) {
	c.driver.mu.Lock()
	c.driver.txAttempts++
	c.driver.mu.Unlock()
	return &slotCommitBoundaryTx{driver: c.driver}, nil
}

func (c *slotCommitBoundaryConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	switch {
	case strings.Contains(query, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1"):
		if c.driver.activeWisp {
			return &claimCommitBoundaryRows{columns: []string{"exists"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
		return &claimCommitBoundaryRows{columns: []string{"exists"}}, nil
	case strings.Contains(query, "SELECT metadata FROM issues WHERE id = ?"),
		strings.Contains(query, "SELECT metadata FROM wisps WHERE id = ?"):
		return &claimCommitBoundaryRows{
			columns: []string{"metadata"},
			values:  [][]driver.Value{{c.driver.metadata}},
		}, nil
	case strings.Contains(query, "FROM issues") && strings.Contains(query, "LEFT JOIN leases") && strings.Contains(query, "WHERE id = ?"):
		if c.driver.activeWisp {
			return &claimCommitBoundaryRows{columns: claimBoundaryIssueColumns()}, nil
		}
		return &claimCommitBoundaryRows{
			columns: claimBoundaryIssueColumns(),
			values:  [][]driver.Value{slotBoundaryIssueValues("slot-boundary", c.driver.metadata)},
		}, nil
	case strings.Contains(query, "FROM wisps") && strings.Contains(query, "LEFT JOIN leases") && strings.Contains(query, "WHERE id = ?"):
		return &claimCommitBoundaryRows{
			columns: claimBoundaryIssueColumns(),
			values:  [][]driver.Value{slotBoundaryIssueValues("slot-boundary", c.driver.metadata)},
		}, nil
	case strings.Contains(query, "SELECT label FROM labels"), strings.Contains(query, "SELECT label FROM wisp_labels"):
		return &claimCommitBoundaryRows{columns: []string{"label"}}, nil
	case strings.Contains(query, "SELECT id FROM events"), strings.Contains(query, "SELECT id FROM wisp_events"):
		return &claimCommitBoundaryRows{columns: []string{"id"}}, nil
	case strings.Contains(query, "FROM dolt_status"):
		// Empty-commit guard (GH#4288 re-port): report one staged row so the
		// commit paths under test still reach DOLT_COMMIT.
		return &claimCommitBoundaryRows{columns: []string{"count"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "CALL DOLT_ADD"):
		c.driver.stageCalls++
		for _, arg := range args {
			if table, ok := arg.Value.(string); ok {
				c.driver.stagedTables = append(c.driver.stagedTables, table)
			}
		}
		if c.driver.stageErr != nil {
			return nil, c.driver.stageErr
		}
		return &claimCommitBoundaryRows{columns: []string{"status"}}, nil
	case strings.Contains(query, "CALL DOLT_COMMIT"):
		c.driver.doltCommits++
		if c.driver.commitErr != nil && c.driver.doltCommits == 1 {
			return nil, c.driver.commitErr
		}
		if c.driver.nothingToCommit {
			return nil, errors.New("nothing to commit")
		}
		return &claimCommitBoundaryRows{columns: []string{"hash"}}, nil
	default:
		return &claimCommitBoundaryRows{columns: []string{"value"}}, nil
	}
}

func (c *slotCommitBoundaryConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	switch {
	case (strings.Contains(query, "UPDATE issues") || strings.Contains(query, "UPDATE wisps")) && strings.Contains(query, "`metadata`"):
		c.driver.metadataUpdates++
	case strings.Contains(query, "INSERT INTO events"), strings.Contains(query, "INSERT INTO wisp_events"):
		c.driver.eventInserts++
	}
	return driver.RowsAffected(1), nil
}

type slotCommitBoundaryTx struct {
	driver *slotCommitBoundaryDriver
}

func (t *slotCommitBoundaryTx) Commit() error {
	t.driver.mu.Lock()
	defer t.driver.mu.Unlock()
	t.driver.txCommits++
	return t.driver.sqlCommitErr
}

func (t *slotCommitBoundaryTx) Rollback() error {
	t.driver.mu.Lock()
	defer t.driver.mu.Unlock()
	t.driver.txRollbacks++
	return nil
}

func slotBoundaryIssueValues(id, metadata string) []driver.Value {
	values := claimBoundaryIssueValues(id)
	for i, column := range claimBoundaryIssueColumns() {
		if column == "metadata" {
			values[i] = metadata
			break
		}
	}
	return values
}

func newSlotCommitBoundaryStore(d *slotCommitBoundaryDriver) *DoltStore {
	return &DoltStore{db: sql.OpenDB(d)}
}

func metadataSlotWriteCases() []struct {
	name     string
	metadata string
	run      func(*DoltStore) error
} {
	return []struct {
		name     string
		metadata string
		run      func(*DoltStore) error
	}{
		{
			name:     "merge metadata",
			metadata: `{"seed":"kept"}`,
			run: func(store *DoltStore) error {
				return store.MergeMetadata(context.Background(), "slot-boundary", "new", json.RawMessage(`"value"`), "alice")
			},
		},
		{
			name:     "clear metadata",
			metadata: `{"clear":"value","keep":"yes"}`,
			run: func(store *DoltStore) error {
				return store.SlotClear(context.Background(), "slot-boundary", "clear", "alice")
			},
		},
	}
}

// TestMetadataSlotWritesDoltAddFailureAfterSQLCommitKeepsTheWrite pins the
// post-commit publication contract (GH#5752) on the metadata slot writes: the
// DOLT_ADD runs only after the SQL transaction committed, so a staging failure
// stops publication before DOLT_COMMIT but cannot unwind the committed write,
// which reports applied and is not replayed.
func TestMetadataSlotWritesDoltAddFailureAfterSQLCommitKeepsTheWrite(t *testing.T) {
	for _, tc := range metadataSlotWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			stageErr := errors.New("stage failed")
			driver := &slotCommitBoundaryDriver{
				metadata:        tc.metadata,
				stageErr:        stageErr,
				nothingToCommit: true,
			}
			store := newSlotCommitBoundaryStore(driver)
			t.Cleanup(func() { _ = store.db.Close() })

			if err := tc.run(store); err != nil {
				t.Fatalf("metadata slot write error = %v, want nil: the SQL transaction committed before DOLT_ADD", err)
			}

			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.stageCalls != 1 {
				t.Fatalf("DOLT_ADD calls = %d, want 1", driver.stageCalls)
			}
			if driver.doltCommits != 0 {
				t.Fatalf("DOLT_COMMIT calls = %d, want 0 after staging failure", driver.doltCommits)
			}
			if driver.metadataUpdates != 1 || driver.eventInserts != 1 {
				t.Fatalf("mutation attempts = updates:%d events:%d, want updates:1 events:1 (no replay)", driver.metadataUpdates, driver.eventInserts)
			}
			if driver.txAttempts != 1 || driver.txCommits != 1 || driver.txRollbacks != 0 {
				t.Fatalf("SQL transaction outcomes = attempts:%d commits:%d rollbacks:%d, want attempts:1 commits:1 rollbacks:0", driver.txAttempts, driver.txCommits, driver.txRollbacks)
			}
		})
	}
}

// TestMetadataSlotWritesDoltCommitResponseLossAfterSQLCommitIsAppliedAndNotReplayed
// is the DOLT_COMMIT twin: losing the history commit's response after the SQL
// commit leaves the write applied, so it reports success rather than an
// indeterminate outcome that would invite a caller retry.
func TestMetadataSlotWritesDoltCommitResponseLossAfterSQLCommitIsAppliedAndNotReplayed(t *testing.T) {
	for _, tc := range metadataSlotWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			driver := &slotCommitBoundaryDriver{
				metadata:  tc.metadata,
				commitErr: testConnectionLoss,
			}
			store := newSlotCommitBoundaryStore(driver)
			t.Cleanup(func() { _ = store.db.Close() })

			if err := tc.run(store); err != nil {
				t.Fatalf("metadata slot write error = %v, want nil: the SQL transaction committed before the Dolt commit", err)
			}

			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.metadataUpdates != 1 || driver.eventInserts != 1 {
				t.Fatalf("mutation attempts = updates:%d events:%d, want updates:1 events:1 (no replay)", driver.metadataUpdates, driver.eventInserts)
			}
			if got, want := sortedCopy(driver.stagedTables...), []string{"events", "issues"}; !slices.Equal(got, want) || driver.doltCommits != 1 {
				t.Fatalf("Dolt publication = staged %v commits:%d, want staged %v commits:1", got, driver.doltCommits, want)
			}
			if driver.txAttempts != 1 || driver.txCommits != 1 || driver.txRollbacks != 0 {
				t.Fatalf("SQL transaction outcomes = attempts:%d commits:%d rollbacks:%d, want attempts:1 commits:1 rollbacks:0", driver.txAttempts, driver.txCommits, driver.txRollbacks)
			}
		})
	}
}

func TestWispMetadataSlotWritesSQLCommitResponseLossIsIndeterminateAndNotReplayed(t *testing.T) {
	for _, tc := range metadataSlotWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "")
			breaker := newTestCircuitBreaker(t)
			driver := &slotCommitBoundaryDriver{
				metadata:     tc.metadata,
				activeWisp:   true,
				sqlCommitErr: testConnectionLoss,
			}
			store := newSlotCommitBoundaryStore(driver)
			store.breaker = breaker
			t.Cleanup(func() { _ = store.db.Close() })

			err := tc.run(store)
			if !errors.Is(err, ErrCommitIndeterminate) {
				t.Fatalf("wisp metadata slot write error = %v, want ErrCommitIndeterminate", err)
			}
			if !errors.Is(err, testConnectionLoss) {
				t.Fatalf("wisp metadata slot write error = %v, want cause %v", err, testConnectionLoss)
			}

			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.metadataUpdates != 1 || driver.eventInserts != 1 {
				t.Fatalf("mutation attempts = updates:%d events:%d, want updates:1 events:1 (no replay)", driver.metadataUpdates, driver.eventInserts)
			}
			if driver.stageCalls != 0 || driver.doltCommits != 0 {
				t.Fatalf("wisp write unexpectedly used Dolt versioning: adds:%d commits:%d", driver.stageCalls, driver.doltCommits)
			}
			if driver.txAttempts != 1 || driver.txCommits != 1 {
				t.Fatalf("SQL transaction attempts = %d, commit calls = %d, want 1 and 1", driver.txAttempts, driver.txCommits)
			}

			state := breaker.readState()
			if state.State != circuitClosed || state.Failures != 1 {
				t.Fatalf("circuit state after one lost response = %+v, want closed with one failure", state)
			}
		})
	}
}

var _ driver.Connector = (*slotCommitBoundaryDriver)(nil)
var _ driver.ExecerContext = (*slotCommitBoundaryConn)(nil)
var _ driver.QueryerContext = (*slotCommitBoundaryConn)(nil)
