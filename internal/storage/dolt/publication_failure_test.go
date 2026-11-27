package dolt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// migratedWrite is one representative write per migrated family, for the
// failure-contract tests: it runs the real store method against the
// publication-order driver and names the statement that is its mutation.
type migratedWrite struct {
	name     string
	setup    func(*publicationOrderDriver, *DoltStore)
	run      func(context.Context, *DoltStore) error
	mutation func(pubEvent) bool
}

func migratedWrites() []migratedWrite {
	return []migratedWrite{
		{
			name: "UpdateIssue",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UpdateIssue(ctx, "pub-1", map[string]interface{}{"title": "retitled"}, "alice")
			},
			mutation: writesTo("issues"),
		},
		{
			name: "ClaimIssue",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.ClaimIssue(ctx, "pub-1", "alice")
			},
			mutation: writesTo("issues"),
		},
		{
			name: "UnclaimIssue",
			setup: func(d *publicationOrderDriver, _ *DoltStore) {
				d.issueStatus = types.StatusInProgress
				d.issueAssignee = "alice"
			},
			run: func(ctx context.Context, s *DoltStore) error {
				return s.UnclaimIssue(ctx, "pub-1", "alice", false)
			},
			mutation: writesTo("issues"),
		},
		{
			name: "CloseIssueChecked",
			run: func(ctx context.Context, s *DoltStore) error {
				res, err := s.CloseIssueChecked(ctx, "pub-1", "alice", storage.CloseIssueOptions{Reason: "done", Force: true})
				if err == nil && res.Unchanged {
					return errors.New("CloseIssueChecked reported the close unchanged")
				}
				return err
			},
			mutation: writesTo("issues"),
		},
		{
			name: "MergeMetadata",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.MergeMetadata(ctx, "pub-1", "gc.k", json.RawMessage(`"v"`), "alice")
			},
			mutation: writesTo("issues"),
		},
		{
			name: "MetadataCompareAndSetKey",
			run: func(ctx context.Context, s *DoltStore) error {
				cas, err := s.MetadataCAS()
				if err != nil {
					return err
				}
				value := json.RawMessage(`"holder"`)
				res, err := cas.CompareAndSetKey(ctx, publicops.CompareAndSetKeyRequest{
					Actor: "alice", IssueID: "pub-1", Key: "gc.lease", Value: &value,
				})
				if err == nil && !res.Swapped {
					return errors.New("CompareAndSetKey did not swap")
				}
				return err
			},
			mutation: writesTo("issues"),
		},
		{
			name: "DemoteToWisp",
			run: func(ctx context.Context, s *DoltStore) error {
				return s.DemoteToWisp(ctx, "pub-1", map[string]interface{}{"wisp": true}, "alice")
			},
			mutation: writesTo("issues"),
		},
		{
			name: "RunInIssueLifecycleTransaction",
			run: func(ctx context.Context, s *DoltStore) error {
				calls := 0
				err := s.RunInIssueLifecycleTransaction(ctx, "bd: lifecycle pub-1", func(tx storage.IssueLifecycleTransaction) error {
					calls++
					dtx := tx.(*doltTransaction)
					if _, err := dtx.regularTx.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "t", "pub-1"); err != nil {
						return err
					}
					dtx.markIssuesChanged("issues")
					return nil
				})
				if err == nil && calls != 1 {
					return errors.New("lifecycle callback did not run exactly once")
				}
				return err
			},
			mutation: writesTo("issues"),
		},
		{
			name:  "DeleteIssues",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = existingIssueRows },
			run: func(ctx context.Context, s *DoltStore) error {
				res, err := s.DeleteIssues(ctx, []string{"pub-1"}, false, true, false)
				if err == nil && (res == nil || res.DeletedCount != 1) {
					return errors.New("DeleteIssues did not report its deletion")
				}
				return err
			},
			mutation: deletesFrom("issues"),
		},
		{
			name:  "DeleterDelete",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = existingIssueRows },
			run: func(ctx context.Context, s *DoltStore) error {
				del, err := s.Deleter()
				if err != nil {
					return err
				}
				res, err := del.Delete(ctx, publicops.DeleteRequest{Actor: "alice", IDs: []string{"pub-1"}, Force: true})
				if err == nil && res.Deleted != 1 {
					return errors.New("Delete did not report its deletion")
				}
				return err
			},
			mutation: deletesFrom("issues"),
		},
		{
			name:  "SweeperSweep",
			setup: func(d *publicationOrderDriver, _ *DoltStore) { d.rows = sweepCandidateRows },
			run: func(ctx context.Context, s *DoltStore) error {
				sw, err := s.Sweeper()
				if err != nil {
					return err
				}
				res, err := sw.Sweep(ctx, publicops.SweepRequest{Actor: "alice", Tier: publicops.SweepDurable, IDPattern: "*"})
				if err == nil && res.Swept != 1 {
					return errors.New("Sweep did not report its sweep")
				}
				return err
			},
			mutation: deletesFrom("issues"),
		},
	}
}

// droppedPublications swaps the bd.db.post_tx_commit_dropped counter and the
// standard logger for test-local ones, restoring both on cleanup, and returns
// readers for the counter's total and the dropped-publication log lines. The
// swap is package state, so a test using it must not run in parallel.
func droppedPublications(t *testing.T) (count func() int64, logLines func() int) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	counter, err := provider.Meter("test").Int64Counter("bd.db.post_tx_commit_dropped")
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	prevCounter := doltMetrics.postTxCommitDropped
	doltMetrics.postTxCommitDropped = metric.Int64Counter(counter)

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		doltMetrics.postTxCommitDropped = prevCounter
		_ = provider.Shutdown(context.Background())
	})

	count = func() int64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		var total int64
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == "bd.db.post_tx_commit_dropped" {
					for _, dp := range sum.DataPoints {
						total += dp.Value
					}
				}
			}
		}
		return total
	}
	logLines = func() int {
		return strings.Count(buf.String(), "post-tx dolt commit failed")
	}
	return count, logLines
}

// TestMigratedPublicationFailure pins the failure contract every migrated
// write shares with the issue-operation funnel (GH#5752): once the SQL
// transaction committed, a failing trailing DOLT_ADD or DOLT_COMMIT cannot
// unwind it, so the method returns its normal successful result, the SQL
// mutation is not replayed, and the dropped publication is counted and logged
// exactly once.
func TestMigratedPublicationFailure(t *testing.T) {
	failures := []struct {
		name      string
		statement string
	}{
		{name: "DoltAdd", statement: "DOLT_ADD"},
		{name: "DoltCommit", statement: "DOLT_COMMIT"},
	}
	for _, failure := range failures {
		for _, w := range migratedWrites() {
			t.Run(failure.name+"/"+w.name, func(t *testing.T) {
				count, logLines := droppedPublications(t)
				d := newPublicationOrderDriver()
				// A non-retryable refusal: the post-commit publisher retries
				// only Dolt's rollback-guaranteed conflicts.
				refused := errors.New("publication refused")
				d.queryErr = func(query string) error {
					if strings.Contains(query, "CALL "+failure.statement) {
						return refused
					}
					return nil
				}
				s := d.store()
				t.Cleanup(func() { _ = s.db.Close() })
				if w.setup != nil {
					w.setup(d, s)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := w.run(ctx, s); err != nil {
					t.Fatalf("%s: error = %v, want its normal success: the SQL transaction committed before the failing %s; events:%s",
						w.name, err, failure.statement, formatPubEvents(d.snapshot()))
				}
				evs := d.snapshot()
				if mutationCommitIndex(evs, w.mutation) < 0 {
					t.Fatalf("%s: no committed SQL transaction carried the mutation; events:%s", w.name, formatPubEvents(evs))
				}
				if n := mutationTxCount(evs, w.mutation); n != 1 {
					t.Errorf("%s: mutation transactions = %d, want 1 (no replay); events:%s", w.name, n, formatPubEvents(evs))
				}
				if n := countEvents(evs, func(ev pubEvent) bool { return strings.Contains(ev.query, "CALL "+failure.statement) }); n != 1 {
					t.Errorf("%s: %s attempts = %d, want 1; events:%s", w.name, failure.statement, n, formatPubEvents(evs))
				}
				if got := count(); got != 1 {
					t.Errorf("%s: bd.db.post_tx_commit_dropped = %d, want 1", w.name, got)
				}
				if got := logLines(); got != 1 {
					t.Errorf("%s: dropped-publication log lines = %d, want 1", w.name, got)
				}
			})
		}
	}
}

// mutationTxCount counts the SQL transactions — committed or rolled back —
// that carried a statement matching mutation: the number of times the
// mutation was attempted, however many matching statements each attempt ran.
func mutationTxCount(evs []pubEvent, mutation func(pubEvent) bool) int {
	carried := map[int]bool{}
	n := 0
	for _, ev := range evs {
		switch {
		case ev.kind == "BEGIN":
			carried[ev.conn] = false
		case ev.kind == "COMMIT" || ev.kind == "ROLLBACK":
			if carried[ev.conn] {
				n++
			}
			carried[ev.conn] = false
		case ev.inTx && mutation(ev):
			carried[ev.conn] = true
		}
	}
	return n
}
