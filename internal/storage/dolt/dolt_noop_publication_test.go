package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	rootissueops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The GH#5752 no-op and early-return contract (AC-7): a dry run, an
// early-return case, or a mutation that captures nothing to publish issues
// no publication statements and preserves its current result; a path that
// reaches staging with an empty staged set mints no empty history commit
// (pinned by TestHasStagedChanges and the empty_commit suite). The
// sweeper/deleter dry-run facet is pinned in
// dolt_commit_ordering_lifecycle_test.go; these cover the replay-capture
// leak, the no-ready-issue dequeue, the metadata compare loss, and the
// empty lifecycle dirty set.

// serializationConflictOnce returns a commit-error knob that fails the first
// driver-level COMMIT with Dolt's rollback-guaranteed 1213 serialization
// conflict, then succeeds — the conflict withRetryTx replays by design.
func serializationConflictOnce() func() error {
	failed := false
	return func() error {
		if failed {
			return nil
		}
		failed = true
		return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	}
}

// TestRunIssueOperationTxReplayStagesOnlyTheCommittedAttemptTables pins the
// capture-reset contract: a serialization conflict rolls the first attempt
// back, the replayed body's table set REPLACES the lost attempt's, and the
// publication stages exactly what the committed attempt reported — never the
// union with a rolled-back attempt.
func TestRunIssueOperationTxReplayStagesOnlyTheCommittedAttemptTables(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec, commitErr: serializationConflictOnce()}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	attempts := 0
	err := s.runIssueOperationTx(context.Background(), "bd: replay test", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		attempts++
		if attempts == 1 {
			return storageissueops.ChangedTables{"issues": true}, nil
		}
		return storageissueops.ChangedTables{"events": true}, nil
	})
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v; events: %v", err, rec.snapshot())
	}
	if attempts != 2 {
		t.Fatalf("body attempts = %d, want 2 (the 1213 conflict replays)", attempts)
	}
	assertPostTxPublication(t, rec.snapshot(), []string{"events"}, "")
}

// TestRunIssueOperationTxReplayToNoOpPublishesNothing: when the replayed
// attempt captures nothing to publish — the concurrent winner took the work —
// the rolled-back attempt's tables must not leak into a publication.
func TestRunIssueOperationTxReplayToNoOpPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec, commitErr: serializationConflictOnce()}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	attempts := 0
	err := s.runIssueOperationTx(context.Background(), "bd: replay test", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		attempts++
		if attempts == 1 {
			return storageissueops.ChangedTables{"issues": true}, nil
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v; events: %v", err, rec.snapshot())
	}
	if attempts != 2 {
		t.Fatalf("body attempts = %d, want 2", attempts)
	}
	assertNoPublication(t, rec.snapshot())
}

// TestClaimReadyIssueNoReadyIssuePublishesNothing: a dequeue that finds no
// ready issue changes nothing and publishes nothing.
func TestClaimReadyIssueNoReadyIssuePublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	issue, err := s.ClaimReadyIssue(context.Background(), types.WorkFilter{}, "tester")
	if err == nil && issue != nil {
		t.Fatalf("ClaimReadyIssue claimed %q from an empty fixture", issue.ID)
	}
	assertNoPublication(t, rec.snapshot())
}

// TestMetadataCompareAndSetLossPublishesNothing: a compare-and-set that loses
// its race is an answer, not a write — Swapped=false, nothing written, no
// publication.
func TestMetadataCompareAndSetLossPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "SELECT metadata FROM issues WHERE id") {
			return &valRows{cols: []string{"metadata"}, vals: [][]driver.Value{[]driver.Value{[]byte(`{"lease":"held"}`)}}}, true
		}
		return nil, false
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	cas, err := s.MetadataCAS()
	if err != nil {
		t.Fatal(err)
	}
	// The row holds "held"; the plan expects "other" — a lost race.
	expected := json.RawMessage(`"other"`)
	value := json.RawMessage(`"mine"`)
	result, err := cas.CompareAndSetKey(context.Background(), rootissueops.CompareAndSetKeyRequest{
		Actor:    "tester",
		IssueID:  "test-1",
		Key:      "lease",
		Expected: &expected,
		Value:    &value,
	})
	if err != nil {
		t.Fatalf("CompareAndSetKey: %v; events: %v", err, rec.snapshot())
	}
	if result.Swapped {
		t.Fatal("CompareAndSetKey swapped against a mismatched Expected")
	}
	if result.Current == nil || string(*result.Current) != `"held"` {
		t.Fatalf("CompareAndSetKey Current = %v, want the row's held value", result.Current)
	}
	assertNoPublication(t, rec.snapshot())
}

// TestLifecycleEmptyDirtySetPublishesNothing: a lifecycle callback that
// dirtied no tables publishes nothing — publishCommittedTables' empty-list
// early return.
func TestLifecycleEmptyDirtySetPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	err := s.RunInIssueLifecycleTransaction(context.Background(), "bd: lifecycle no-op", func(tx storage.IssueLifecycleTransaction) error {
		return nil
	})
	if err != nil {
		t.Fatalf("RunInIssueLifecycleTransaction: %v; events: %v", err, rec.snapshot())
	}
	assertNoPublication(t, rec.snapshot())
}
