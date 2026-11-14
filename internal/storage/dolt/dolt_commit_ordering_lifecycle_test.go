package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	rootissueops "github.com/steveyegge/beads/issueops"
)

// Ordering contracts for the transaction-runner, delete, and purge surfaces
// GH#5752 migrates to the post-tx publisher: RunInIssueLifecycleTransaction,
// DeleteIssue, the role deleter (inside the delete fence and out), and the
// sweeper must run their DOLT_ADD/DOLT_COMMIT strictly AFTER the driver-level
// COMMIT of the SQL transaction that applied the write — never inside it — and
// a fenced delete must publish only after the fence's RELEASE_LOCK, so the
// next racer can never observe a Dolt commit minted while the fence is still
// held.

// serveDeleteCounts serves the COUNT aggregates the bulk-delete paths read
// (dependencies/labels/events emptiness probes). The dolt_status staged-set
// probe is deliberately NOT matched: it must fall through to the connector's
// default so the publisher sees a non-empty staged set and issues its
// DOLT_COMMIT — serving it 0 here would make the empty-staged-set guard
// correctly skip the commit and fail every ordering assertion.
func serveDeleteCounts(c *seqConnector) {
	prev := c.rows
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "SELECT COUNT(*) FROM") && !strings.Contains(q, "dolt_status") {
			return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
		}
		if prev != nil {
			return prev(q)
		}
		return nil, false
	}
}

// serveWispPlaneLookups serves the wisps-plane membership reads the sweep's
// deletion body uses: the table probe (non-empty) and the per-id partition.
func serveWispPlaneLookups(c *seqConnector, id string) {
	prev := c.rows
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "SELECT 1 FROM wisps LIMIT 1") {
			return &valRows{cols: []string{"1"}, vals: [][]driver.Value{{int64(1)}}}, true
		}
		if strings.Contains(q, "SELECT id FROM wisps WHERE id IN") {
			return &valRows{cols: []string{"id"}, vals: [][]driver.Value{{id}}}, true
		}
		if prev != nil {
			return prev(q)
		}
		return nil, false
	}
}

// serveSweepCandidate serves the wisps-plane candidate search one closed row,
// the minimum FilterSweepCandidates keeps (a candidate with no closed_at is
// skipped, and a sweep that sweeps nothing publishes nothing).
func serveSweepCandidate(c *seqConnector, id string) {
	c.rows = func(q string) (driver.Rows, bool) {
		if strings.Contains(q, "FROM wisps") && strings.Contains(q, "LEFT JOIN leases") {
			vals := claimBoundaryIssueValues(id)
			for i, column := range claimBoundaryIssueColumns() {
				switch column {
				case "status":
					vals[i] = "closed"
				case "closed_at":
					vals[i] = time.Unix(1700000000, 0).UTC()
				}
			}
			return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{vals}}, true
		}
		return nil, false
	}
}

// sweptTableNames is the staging list the deleter and sweeper publish, spelled
// out here so a change to sweptTables itself fails these tests rather than
// silently re-blessing a different table set.
var sweptTableNames = []string{
	"issues", "dependencies", "labels", "comments", "events", "provenance_events",
	"child_counters", "issue_snapshots", "compaction_snapshots",
	"id_tombstones", "wisp_id_tombstones",
}

// TestLifecycleTransactionDoltCommitRunsAfterTxCommit pins the lifecycle
// runner's ordering (GH#5752): the callback's dirty tables are committed by
// SQL first, and the Dolt history commit for them runs only afterwards. Inside
// the transaction, DOLT_ADD stages from the BEGIN-time root and the minted
// commit writes every concurrently-committed row in those tables back to its
// BEGIN-time value.
func TestLifecycleTransactionDoltCommitRunsAfterTxCommit(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	err := s.RunInIssueLifecycleTransaction(context.Background(), "bd: lifecycle test-1", func(tx storage.IssueLifecycleTransaction) error {
		return tx.SetMetadata(context.Background(), "lifecycle", "yes")
	})
	if err != nil {
		t.Fatalf("RunInIssueLifecycleTransaction: %v; events: %v", err, rec.snapshot())
	}
	assertPostTxPublication(t, rec.snapshot(), []string{"metadata"}, "bd: lifecycle test-1")
}

// TestDeleteIssueDoltCommitRunsAfterTxCommit pins the single durable delete's
// ordering: its Dolt history commit runs after the SQL transaction that
// deleted the row (and its cascades) committed.
func TestDeleteIssueDoltCommitRunsAfterTxCommit(t *testing.T) {
	rec := &seqRecorder{}
	db := sql.OpenDB(&seqConnector{rec: rec})
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	if err := s.DeleteIssue(context.Background(), "legacy-1"); err != nil {
		t.Fatalf("DeleteIssue: %v; events: %v", err, rec.snapshot())
	}
	assertPostTxPublication(t, rec.snapshot(), []string{
		"issues", "dependencies", "labels", "comments", "events", "provenance_events",
		"child_counters", "issue_snapshots", "compaction_snapshots",
	}, "bd: delete legacy-1")
}

// TestSweepDoltCommitRunsAfterTxCommit pins the sweeper's ordering: a sweep's
// Dolt history commit runs after the SQL transaction that swept the rows
// committed, staging the fixed swept-tables list.
func TestSweepDoltCommitRunsAfterTxCommit(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	serveSweepCandidate(connector, "wisp-1")
	serveWispPlaneLookups(connector, "wisp-1")
	serveDeleteCounts(connector)
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	sweeper, err := s.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	result, err := sweeper.Sweep(context.Background(), rootissueops.SweepRequest{
		Tier:      rootissueops.SweepWispsPlane,
		IDPattern: "*",
	})
	if err != nil {
		t.Fatalf("Sweep: %v; events: %v", err, rec.snapshot())
	}
	if result.Swept != 1 {
		t.Fatalf("Swept = %d, want 1 (the fixture serves one closed wisp); events: %v", result.Swept, rec.snapshot())
	}
	assertPostTxPublication(t, rec.snapshot(), sweptTableNames,
		fmt.Sprintf("bd: sweep 1 %s bead(s)", rootissueops.SweepWispsPlane))
}

// TestDeleterFencedDeletePublishesAfterFenceRelease pins the fence/publication
// ordering: a guarded (ExpectedVersion) delete on a real sql-server runs under
// the per-id delete fence, and its Dolt history commit may run only after the
// fence's RELEASE_LOCK — never while the fence is held inside the transaction,
// where the in-tx DOLT_ADD both carries the lost-update hazard and lets the
// next racer observe a minted commit against a row whose SQL commit has not
// even been attempted yet.
func TestDeleterFencedDeletePublishesAfterFenceRelease(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.rows = func(q string) (driver.Rows, bool) {
		switch {
		case strings.Contains(q, "SELECT GET_LOCK"):
			return &valRows{cols: []string{"locked"}, vals: [][]driver.Value{{int64(1)}}}, true
		case strings.Contains(q, "SELECT RELEASE_LOCK"):
			return &valRows{cols: []string{"released"}, vals: [][]driver.Value{{int64(1)}}}, true
		case strings.Contains(q, "SELECT row_lock FROM issues WHERE id = ?"):
			return &valRows{cols: []string{"row_lock"}, vals: [][]driver.Value{{int64(0)}}}, true
		case strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") && strings.Contains(q, "WHERE id IN"):
			return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
		case strings.Contains(q, "SELECT id FROM issues WHERE id IN"):
			return &valRows{cols: []string{"id"}, vals: [][]driver.Value{{"legacy-1"}}}, true
		case strings.Contains(q, "SELECT COUNT(*) FROM") && !strings.Contains(q, "dolt_status"):
			// The cascade emptiness probes read 0; the dolt_status staged-set
			// probe falls through to the connector default (count=1) so the
			// publisher issues its DOLT_COMMIT.
			return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
		}
		return nil, false
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db, serverMode: true}
	deleter, err := s.Deleter()
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	result, err := deleter.Delete(context.Background(), rootissueops.DeleteRequest{
		IDs:            []string{"legacy-1"},
		ExpectedVersion: &zero,
	})
	if err != nil {
		t.Fatalf("Delete: %v; events: %v", err, rec.snapshot())
	}
	if result.Deleted != 1 {
		t.Fatalf("Deleted = %d, want 1; events: %v", result.Deleted, rec.snapshot())
	}

	events := rec.snapshot()
	releaseIdx, addIdx := -1, -1
	for i, ev := range events {
		if strings.Contains(ev, "RELEASE_LOCK") && releaseIdx == -1 {
			releaseIdx = i
		}
		if strings.Contains(ev, "DOLT_ADD") && addIdx == -1 {
			addIdx = i
		}
	}
	if releaseIdx == -1 {
		t.Fatalf("fence RELEASE_LOCK not recorded; events: %v", events)
	}
	if addIdx == -1 {
		t.Fatalf("DOLT_ADD not recorded; events: %v", events)
	}
	if addIdx < releaseIdx {
		t.Fatalf("DOLT_ADD (index %d) ran before the fence's RELEASE_LOCK (index %d): the publication must wait until the fence is down; events: %v",
			addIdx, releaseIdx, events)
	}
	assertPostTxPublication(t, events, sweptTableNames, "bd: delete 1 issue(s)")
}

// TestLifecycleCallbackRunsOnceAndMintsNoDoltCommitOnAmbiguousCommit pins the
// lifecycle runner's at-most-once boundary at the ambiguous-commit seam: a
// connection loss during the SQL COMMIT surfaces ErrCommitIndeterminate, the
// callback is never replayed, and NO Dolt commit is minted off the ambiguous
// outcome — in-tx staging would have committed a Dolt revision for a
// transaction the server may have rolled back (phantom audit evidence).
func TestLifecycleCallbackRunsOnceAndMintsNoDoltCommitOnAmbiguousCommit(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	connector.commitErr = func() error { return testConnectionLoss }
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	calls := 0
	err := s.RunInIssueLifecycleTransaction(context.Background(), "bd: lifecycle test-1", func(tx storage.IssueLifecycleTransaction) error {
		calls++
		return tx.SetMetadata(context.Background(), "lifecycle", "yes")
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("RunInIssueLifecycleTransaction error = %v, want ErrCommitIndeterminate", err)
	}
	if calls != 1 {
		t.Fatalf("callback ran %d times, want exactly 1 (never replayed)", calls)
	}
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
			t.Fatalf("no Dolt publication may be minted off an ambiguous SQL commit; events: %v", rec.snapshot())
		}
	}
}

// TestSweeperAndDeleterDryRunsPublishNothing pins the preview contract: a dry
// run takes a read transaction, writes nothing, and therefore mints no Dolt
// history commit on either role.
func TestSweeperAndDeleterDryRunsPublishNothing(t *testing.T) {
	t.Run("sweeper", func(t *testing.T) {
		rec := &seqRecorder{}
		connector := &seqConnector{rec: rec}
		serveSweepCandidate(connector, "wisp-1")
		serveWispPlaneLookups(connector, "wisp-1")
		serveDeleteCounts(connector)
		db := sql.OpenDB(connector)
		defer func() { _ = db.Close() }()

		s := &DoltStore{db: db}
		sweeper, err := s.Sweeper()
		if err != nil {
			t.Fatal(err)
		}
		result, err := sweeper.Sweep(context.Background(), rootissueops.SweepRequest{
			Tier:      rootissueops.SweepWispsPlane,
			IDPattern: "*",
			DryRun:    true,
		})
		if err != nil {
			t.Fatalf("dry-run Sweep: %v; events: %v", err, rec.snapshot())
		}
		if result.Swept != 1 {
			t.Fatalf("dry-run Swept = %d, want 1 (a preview reports what it would delete)", result.Swept)
		}
		for _, ev := range rec.snapshot() {
			if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
				t.Fatalf("dry-run sweep minted Dolt history; events: %v", rec.snapshot())
			}
		}
	})
	t.Run("deleter", func(t *testing.T) {
		rec := &seqRecorder{}
		connector := &seqConnector{rec: rec}
		connector.rows = func(q string) (driver.Rows, bool) {
			switch {
			case strings.Contains(q, "FROM issues") && strings.Contains(q, "LEFT JOIN leases") && strings.Contains(q, "WHERE id IN"):
				return &valRows{cols: claimBoundaryIssueColumns(), vals: [][]driver.Value{claimBoundaryIssueValues("legacy-1")}}, true
			case strings.Contains(q, "SELECT COUNT(*) FROM"):
				return &valRows{cols: []string{"count"}, vals: [][]driver.Value{{int64(0)}}}, true
			}
			return nil, false
		}
		db := sql.OpenDB(connector)
		defer func() { _ = db.Close() }()

		s := &DoltStore{db: db}
		deleter, err := s.Deleter()
		if err != nil {
			t.Fatal(err)
		}
		result, err := deleter.Delete(context.Background(), rootissueops.DeleteRequest{
			IDs:    []string{"legacy-1"},
			DryRun: true,
		})
		if err != nil {
			t.Fatalf("dry-run Delete: %v; events: %v", err, rec.snapshot())
		}
		if result.Deleted != 1 {
			t.Fatalf("dry-run Deleted = %d, want 1 (a preview reports what it would delete)", result.Deleted)
		}
		for _, ev := range rec.snapshot() {
			if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
				t.Fatalf("dry-run delete minted Dolt history; events: %v", rec.snapshot())
			}
		}
	})
}

// TestSweepHonorsDeferredVersionCommit pins the sweeper's deferral contract
// (bd-4wamg): a context carrying WithDeferredVersionCommit leaves the sweep's
// rows in the Dolt working set for a later explicit commit point, minting no
// per-sweep version commit. The in-tx staging this path used never checked the
// deferral — this is the intentional correction GH#5752's funnel brings.
func TestSweepHonorsDeferredVersionCommit(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	serveSweepCandidate(connector, "wisp-1")
	serveWispPlaneLookups(connector, "wisp-1")
	serveDeleteCounts(connector)
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	s := &DoltStore{db: db}
	sweeper, err := s.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	ctx := storeissueops.WithDeferredVersionCommit(context.Background())
	result, err := sweeper.Sweep(ctx, rootissueops.SweepRequest{
		Tier:      rootissueops.SweepWispsPlane,
		IDPattern: "*",
	})
	if err != nil {
		t.Fatalf("deferred Sweep: %v; events: %v", err, rec.snapshot())
	}
	if result.Swept != 1 {
		t.Fatalf("Swept = %d, want 1 (deferral defers the version commit, not the delete)", result.Swept)
	}
	sawCommit := false
	for _, ev := range rec.snapshot() {
		if ev == "COMMIT" {
			sawCommit = true
		}
		if strings.Contains(ev, "DOLT_ADD") || strings.Contains(ev, "DOLT_COMMIT") {
			t.Fatalf("deferred sweep minted Dolt history; events: %v", rec.snapshot())
		}
	}
	if !sawCommit {
		t.Fatalf("sweep's SQL transaction never committed; events: %v", rec.snapshot())
	}
}
