package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// The GH#5752 replay-capture contract: withRetryTx replays the issue-operation
// body after a rollback-guaranteed serialization conflict, and the trailing
// publication must stage only what the COMMITTED attempt reported, never the
// union with (or a leftover of) the rolled-back attempt.

// serializationConflictOnce fails the first SQL COMMIT with Dolt's
// rollback-guaranteed 1213 serialization conflict, the error withRetryTx
// replays by design, then lets every later COMMIT succeed.
func serializationConflictOnce(n int) error {
	if n == 1 {
		return &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	}
	return nil
}

// replayBody returns an issue-operation body that writes one marked row per
// attempt and reports perAttempt[attempt-1] as its changed tables.
func replayBody(attempts *int, perAttempt ...storageissueops.ChangedTables) func(*sql.Tx) (storageissueops.ChangedTables, error) {
	return func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		*attempts++
		if _, err := tx.ExecContext(context.Background(), "UPDATE issues SET title = ? WHERE id = ?", replayAttemptMark(*attempts), "pub-1"); err != nil {
			return nil, err
		}
		return perAttempt[*attempts-1], nil
	}
}

func replayAttemptMark(n int) string { return fmt.Sprintf("replay-attempt-%d", n) }

// writesReplayAttempt matches the given attempt's marked write.
func writesReplayAttempt(n int) func(pubEvent) bool {
	return func(ev pubEvent) bool {
		return ev.kind == "EXEC" && slices.Contains(ev.args, any(replayAttemptMark(n)))
	}
}

// TestRunIssueOperationTxReplayStagesOnlyTheCommittedAttemptTables: a
// serialization conflict rolls the first attempt back; the replayed body's
// table set replaces the lost attempt's, and the publication stages exactly
// what the committed attempt reported.
func TestRunIssueOperationTxReplayStagesOnlyTheCommittedAttemptTables(t *testing.T) {
	d := newPublicationOrderDriver()
	d.commitErr = serializationConflictOnce
	s := d.store()
	defer func() { _ = s.db.Close() }()

	attempts := 0
	err := s.runIssueOperationTx(context.Background(), "bd: replay test", replayBody(&attempts,
		storageissueops.ChangedTables{"issues": true},
		storageissueops.ChangedTables{"events": true},
	))
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v; events:%s", err, formatPubEvents(d.snapshot()))
	}
	if attempts != 2 {
		t.Fatalf("body attempts = %d, want 2 (the 1213 conflict replays)", attempts)
	}
	pub := requirePublishedAfterCommit(t, "replay", d.snapshot(), writesReplayAttempt(2))
	if !slices.Equal(pub.tables, []string{"events"}) {
		t.Errorf("staged tables = %v, want only the committed attempt's [events]; events:%s", pub.tables, formatPubEvents(d.snapshot()))
	}
	if pub.message != "bd: replay test" {
		t.Errorf("DOLT_COMMIT message = %q, want %q", pub.message, "bd: replay test")
	}
}

// TestRunIssueOperationTxReplayToNoOpPublishesNothing: when the replayed
// attempt captures nothing to publish (a concurrent winner took the work),
// the rolled-back attempt's tables must not leak into a publication.
func TestRunIssueOperationTxReplayToNoOpPublishesNothing(t *testing.T) {
	d := newPublicationOrderDriver()
	d.commitErr = serializationConflictOnce
	s := d.store()
	defer func() { _ = s.db.Close() }()

	attempts := 0
	err := s.runIssueOperationTx(context.Background(), "bd: replay test", replayBody(&attempts,
		storageissueops.ChangedTables{"issues": true},
		nil,
	))
	if err != nil {
		t.Fatalf("runIssueOperationTx: %v; events:%s", err, formatPubEvents(d.snapshot()))
	}
	if attempts != 2 {
		t.Fatalf("body attempts = %d, want 2 (the 1213 conflict replays)", attempts)
	}
	requireNoPublication(t, "replay to no-op", d.snapshot())
}
