package dolt

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	rootissueops "github.com/steveyegge/beads/issueops"
)

// The GH#5752 trailing-publication contract (AC-4): once a mutation's SQL
// transaction has committed, a failure in the trailing DOLT_ADD or
// DOLT_COMMIT cannot fail the method — the data change is durable and rides
// the next Dolt commit on the branch. The publisher drops it: one counter
// increment and one log line at the single accounting site, never a replay
// of the SQL mutation. The claim/slot facets of this contract are pinned in
// claim_commit_boundary_test.go and slot_commit_boundary_test.go; these
// tests add the AC-3 migration family (delete and sweep) and make the
// counted-and-logged-ONCE facet observable by capturing the standard
// logger's output across one call.

// captureDropLogs swaps the standard logger's output into a buffer for the
// rest of the test and returns a reader over the post-tx drop lines the
// publisher emitted. This package's tests do not run in parallel, so the
// global swap is safe; cleanup restores the original writer and flags
// regardless of the test's outcome.
func captureDropLogs(t *testing.T) func() (dropLines []string) {
	t.Helper()
	var buf bytes.Buffer
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})
	return func() []string {
		var drops []string
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.Contains(line, "post-tx dolt commit failed") {
				drops = append(drops, line)
			}
		}
		return drops
	}
}

// countEvents returns how many recorded events contain needle.
func countEvents(events []string, needle string) int {
	n := 0
	for _, ev := range events {
		if strings.Contains(ev, needle) {
			n++
		}
	}
	return n
}

// countExact returns how many recorded events equal want — for the
// driver-level "COMMIT", which a contains-match would conflate with the
// trailing "CALL DOLT_COMMIT" publication event.
func countExact(events []string, want string) int {
	n := 0
	for _, ev := range events {
		if ev == want {
			n++
		}
	}
	return n
}

// reopenDropFixture wires the connector for a successful reopen of a closed
// issue (the TestReopenIssueDoltCommitRunsAfterTxCommit fixture) plus a
// trailing-failure injection on the Dolt publication statements.
func reopenDropFixture(c *seqConnector, failQuery func(query string) error) {
	c.rows = func(query string) (driver.Rows, bool) {
		if strings.Contains(query, "SELECT status FROM issues WHERE id") {
			return &valRows{cols: []string{"status"}, vals: [][]driver.Value{{"closed"}}}, true
		}
		return nil, false
	}
	c.execRows = func(query string) (int64, bool) {
		// Derived blocked-state maintenance is a fixpoint loop: report no
		// change so it converges.
		if strings.Contains(query, "is_blocked") {
			return 0, true
		}
		return 0, false
	}
	c.queryErr = failQuery
}

// TestReopenIssueTrailingDoltCommitFailureDropsOnce pins the drop-through on
// a legacy issue mutation: an untyped trailing DOLT_COMMIT failure (response
// loss — the class the circuit breaker counts, deliberately NOT retried by
// the post-tx publisher, so exactly one attempt is made) resolves as a
// successful reopen, with the SQL mutation applied once and the drop
// counted and logged exactly once.
func TestReopenIssueTrailingDoltCommitFailureDropsOnce(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	reopenDropFixture(connector, func(query string) error {
		if strings.Contains(query, "CALL DOLT_COMMIT") {
			return testConnectionLoss
		}
		return nil
	})
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	drops := captureDropLogs(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.ReopenIssue(ctx, "test-1", "", "tester"); err != nil {
		t.Fatalf("ReopenIssue: %v (want nil: the reopen is durable; the trailing dolt commit failure is dropped); events: %v", err, rec.snapshot())
	}

	events := rec.snapshot()
	if got := countEvents(events, "CALL DOLT_COMMIT"); got != 1 {
		t.Fatalf("DOLT_COMMIT attempts = %d, want 1 (attempted once, then dropped); events: %v", got, events)
	}
	if got := countExact(events, "COMMIT"); got != 1 {
		t.Fatalf("driver-level COMMITs = %d, want 1 (a dropped publication never replays the SQL mutation); events: %v", got, events)
	}
	got := drops()
	if len(got) != 1 {
		t.Fatalf("post-tx drop log lines = %d, want exactly 1; lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "bd: reopen test-1") {
		t.Fatalf("drop log line %q does not name the reopen publication", got[0])
	}
}

// TestDeleteIssueTrailingDoltAddFailureDropsOnce pins the stage-failure
// facet on the single durable delete: a trailing DOLT_ADD failure also
// cannot fail the deletion, attempts no DOLT_COMMIT after staging failed,
// and is dropped once.
func TestDeleteIssueTrailingDoltAddFailureDropsOnce(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	stageErr := errors.New("stage failed")
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "CALL DOLT_ADD") {
			return stageErr
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	drops := captureDropLogs(t)

	if err := s.DeleteIssue(context.Background(), "legacy-1"); err != nil {
		t.Fatalf("DeleteIssue: %v (want nil: the deletion is durable; the trailing dolt add failure is dropped); events: %v", err, rec.snapshot())
	}

	events := rec.snapshot()
	if got := countEvents(events, "CALL DOLT_ADD"); got != 1 {
		t.Fatalf("DOLT_ADD attempts = %d, want 1 (staging stops at the first failure); events: %v", got, events)
	}
	if got := countEvents(events, "CALL DOLT_COMMIT"); got != 0 {
		t.Fatalf("DOLT_COMMIT attempts = %d, want 0 after a staging failure; events: %v", got, events)
	}
	if got := countExact(events, "COMMIT"); got != 1 {
		t.Fatalf("driver-level COMMITs = %d, want 1 (no replay of the deletion); events: %v", got, events)
	}
	got := drops()
	if len(got) != 1 {
		t.Fatalf("post-tx drop log lines = %d, want exactly 1; lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "bd: delete legacy-1") {
		t.Fatalf("drop log line %q does not name the delete publication", got[0])
	}
}

// TestSweepTrailingDoltCommitFailureDropsOnce pins the drop-through on the
// sweeper: a trailing DOLT_COMMIT failure after the sweep's SQL commit
// resolves as a successful sweep reporting its swept count, with the drop
// counted and logged once.
func TestSweepTrailingDoltCommitFailureDropsOnce(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	serveSweepCandidate(connector, "wisp-1")
	serveWispPlaneLookups(connector, "wisp-1")
	serveDeleteCounts(connector)
	connector.queryErr = func(query string) error {
		if strings.Contains(query, "CALL DOLT_COMMIT") {
			return testConnectionLoss
		}
		return nil
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	drops := captureDropLogs(t)

	sweeper, err := s.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	result, err := sweeper.Sweep(context.Background(), rootissueops.SweepRequest{
		Tier:      rootissueops.SweepWispsPlane,
		IDPattern: "*",
	})
	if err != nil {
		t.Fatalf("Sweep: %v (want nil: the sweep is durable; the trailing dolt commit failure is dropped); events: %v", err, rec.snapshot())
	}
	if result.Swept != 1 {
		t.Fatalf("Swept = %d, want 1 (the fixture serves one closed wisp); events: %v", result.Swept, rec.snapshot())
	}

	events := rec.snapshot()
	if got := countEvents(events, "CALL DOLT_COMMIT"); got != 1 {
		t.Fatalf("DOLT_COMMIT attempts = %d, want 1 (attempted once, then dropped); events: %v", got, events)
	}
	if got := countExact(events, "COMMIT"); got != 1 {
		t.Fatalf("driver-level COMMITs = %d, want 1 (no replay of the sweep); events: %v", got, events)
	}
	got := drops()
	if len(got) != 1 {
		t.Fatalf("post-tx drop log lines = %d, want exactly 1; lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "bd: sweep 1 wisps-plane bead(s)") {
		t.Fatalf("drop log line %q does not name the sweep publication", got[0])
	}
}
