package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	rootissueops "github.com/steveyegge/beads/issueops"
)

// The GH#5752 indeterminate/failure boundary (AC-5): when a migrated
// operation's SQL transaction fails, or its driver-level COMMIT returns an
// ambiguous (connection-loss) response, the runner returns that error —
// never a trailing DOLT_ADD/DOLT_COMMIT, never a replay of the SQL body, and
// never a conversion of ErrCommitIndeterminate into success. The claim,
// ready-claim, update, dependency, comment, slot and lifecycle facets are
// pinned by the boundary suites (claim_commit_boundary_test.go,
// slot_commit_boundary_test.go, dolt_commit_ordering_lifecycle_test.go);
// these extend the contract to the unclaim variant, the metadata merge and
// the single durable delete, and pin the plain body-failure side of it.

// serveDurableIssueRow serves one in_progress issue row assigned to alice
// for the single-row reads the unclaim and metadata-merge bodies do: the
// metadata merge reads the issue's metadata column alone first, then the
// full combined-plane row (FROM wisps LEFT JOIN leases, by exact id); the
// unclaim body reads the combined-plane row directly.
func serveDurableIssueRow(c *seqConnector, id string) {
	prev := c.rows
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "SELECT metadata FROM issues WHERE id") {
			return &valRows{cols: []string{"metadata"}, vals: [][]driver.Value{{nil}}}, true
		}
		if strings.Contains(q, "WHERE id = ?") &&
			(strings.Contains(q, "FROM wisps LEFT JOIN leases") || strings.Contains(q, "FROM issues WHERE id")) {
			vals := claimBoundaryIssueValues(id)
			for i, column := range claimBoundaryIssueColumns() {
				switch column {
				case "status":
					vals[i] = "in_progress"
				case "assignee":
					vals[i] = "alice"
				}
			}
			return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{vals}}, true
		}
		if prev != nil {
			return prev(q)
		}
		return nil, false
	}
}

// assertNoPublication asserts not one DOLT_ADD or DOLT_COMMIT statement ran:
// an operation whose SQL transaction failed or committed ambiguously must
// not touch the versioned working set (staging off an ambiguous commit
// mints phantom audit evidence; staging a failed one stages nothing of
// this operation at all).
func assertNoPublication(t *testing.T, events []string) {
	t.Helper()
	for _, ev := range events {
		if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
			t.Fatalf("publication statement ran on a failed or ambiguous transaction: %q; events: %v", ev, events)
		}
	}
}

// assertSingleAttempt asserts the SQL body began exactly once.
func assertSingleAttempt(t *testing.T, events []string) {
	t.Helper()
	if got := countExact(events, "BEGIN"); got != 1 {
		t.Fatalf("transaction attempts = %d, want 1 (an ambiguous commit is never replayed); events: %v", got, events)
	}
}

// TestUnclaimIssueTxCommitLossIsIndeterminateAndPublishesNothing: the
// unclaim variant's driver-level COMMIT loses its response — the error is
// ErrCommitIndeterminate (never converted to success), the body ran once,
// and no publication statement ran.
func TestUnclaimIssueTxCommitLossIsIndeterminateAndPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec, commitErr: func() error { return testConnectionLoss }}
	serveDurableIssueRow(connector, "test-1")
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	err := s.UnclaimIssue(context.Background(), "test-1", "alice", false)
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("UnclaimIssue: %v, want ErrCommitIndeterminate (no conversion to success); events: %v", err, rec.snapshot())
	}
	assertSingleAttempt(t, rec.snapshot())
	assertNoPublication(t, rec.snapshot())
}

// TestMergeMetadataTxCommitLossIsIndeterminateAndPublishesNothing: the
// metadata merge's driver-level COMMIT loses its response — ErrCommitIndeterminate,
// one attempt, no publication.
func TestMergeMetadataTxCommitLossIsIndeterminateAndPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec, commitErr: func() error { return testConnectionLoss }}
	serveDurableIssueRow(connector, "test-1")
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	err := s.MergeMetadata(context.Background(), "test-1", "note", json.RawMessage(`"merged"`), "alice")
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("MergeMetadata: %v, want ErrCommitIndeterminate (no conversion to success); events: %v", err, rec.snapshot())
	}
	assertSingleAttempt(t, rec.snapshot())
	assertNoPublication(t, rec.snapshot())
}

// TestDeleteIssueTxCommitLossIsIndeterminateAndPublishesNothing: the single
// durable delete's driver-level COMMIT loses its response — the deletion
// error stays ErrCommitIndeterminate (recordDoltPublicationFailure's breaker
// accounting does not convert it), the body ran once, and no publication
// statement ran.
func TestDeleteIssueTxCommitLossIsIndeterminateAndPublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec, commitErr: func() error { return testConnectionLoss }}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	err := s.DeleteIssue(context.Background(), "legacy-1")
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("DeleteIssue: %v, want ErrCommitIndeterminate (no conversion to success); events: %v", err, rec.snapshot())
	}
	assertSingleAttempt(t, rec.snapshot())
	assertNoPublication(t, rec.snapshot())
}

// TestSweepBodyFailurePublishesNothing pins the plain-failure side: a
// mid-transaction body error (not a commit ambiguity) fails the sweep with
// that error, rolls the transaction back once, and publishes nothing. This
// also held before the migration; it is here so moving publication into the
// failure path, or retrying a failed body into a second attempt, fails a
// named test.
func TestSweepBodyFailurePublishesNothing(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	serveSweepCandidate(connector, "wisp-1")
	serveWispPlaneLookups(connector, "wisp-1")
	serveDeleteCounts(connector)
	sweepErr := errors.New("wisp delete failed")
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "DELETE FROM wisps") {
			return sweepErr
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	sweeper, err := s.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sweeper.Sweep(context.Background(), rootissueops.SweepRequest{
		Tier:      rootissueops.SweepWispsPlane,
		IDPattern: "*",
	}); !errors.Is(err, sweepErr) {
		t.Fatalf("Sweep: %v, want the body failure %v; events: %v", err, sweepErr, rec.snapshot())
	}
	assertSingleAttempt(t, rec.snapshot())
	if got := countExact(rec.snapshot(), "ROLLBACK"); got != 1 {
		t.Fatalf("transaction rollbacks = %d, want 1; events: %v", got, rec.snapshot())
	}
	assertNoPublication(t, rec.snapshot())
}
