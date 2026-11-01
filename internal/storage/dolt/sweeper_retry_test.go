package dolt

import (
	"context"
	"database/sql"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/issueops"
)

// TestSweepRetriesARollbackGuaranteedConflict pins that a sweep that loses
// Dolt's commit-time merge is replayed, as every other write through
// withRetryTx is. A retaining sweep writes one coordination cell per shard it
// retires, so on a sql-server any create committing in one of those shards
// first conflicts it; single-attempt, it failed whenever that happened. The
// fake driver refuses the first SQL COMMIT with 1213, which guarantees the
// server rolled the attempt back.
func TestSweepRetriesARollbackGuaranteedConflict(t *testing.T) {
	rec := &seqRecorder{}
	connector := &seqConnector{rec: rec}
	failures := 0
	connector.commitErr = func() error {
		if failures == 0 {
			failures++
			return &mysql.MySQLError{Number: 1213, Message: "serialization failure"}
		}
		return nil
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{db: db}

	sweeper, err := store.Sweeper()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sweeper.Sweep(context.Background(), issueops.SweepRequest{Tier: issueops.SweepEphemeral, RetainIDs: true}); err != nil {
		t.Fatalf("Sweep() = %v; a 1213 at commit must be replayed, not surfaced", err)
	}
	commits := 0
	for _, event := range rec.snapshot() {
		if event == "COMMIT" {
			commits++
		}
	}
	if commits != 2 {
		t.Fatalf("saw %d SQL COMMIT(s), want 2: the refused attempt and its replay", commits)
	}
}
