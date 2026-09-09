package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// failOnSecondEventInsertTx wraps a real *sql.Tx and fails the second
// "INSERT INTO events" statement with context.Canceled. In CreateIssueInTx's
// statement order the first such insert is always the issue-created event and
// the second is the first label's "Added label" event — so this deterministically
// reproduces a label-event write failing mid-transaction (ga-xcq1ph) without
// racing a wall-clock timeout against a real Dolt contention window.
type failOnSecondEventInsertTx struct {
	*sql.Tx
	eventInsertCount int
	injected         bool
}

func (f *failOnSecondEventInsertTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "INSERT INTO events") {
		f.eventInsertCount++
		if f.eventInsertCount == 2 {
			f.injected = true
			return nil, context.Canceled
		}
	}
	return f.Tx.ExecContext(ctx, query, args...)
}

// TestCreateIssueRollsBackFullyOnLabelEventFailure is a regression test for
// ga-xcq1ph: a `bd create` with multiple labels, observed under real Dolt
// write contention, printed an error naming a generated issue ID ("failed to
// record label event ... context canceled / transaction has already been
// committed or rolled back") and that ID never persisted (confirmed by a
// later `bd show`). The open question was whether that is silent partial
// persistence (a real bug) or a fully-rolled-back transaction whose error
// text merely mentions the doomed ID (confusing, but safe).
//
// This test injects the exact failure shape at the exact point PersistLabels
// hits it (a label's "Added label" event insert returning context.Canceled)
// against a real Dolt transaction, then rolls back by hand the way
// DoltStore.withWriteTx does on any callback error (store.go:1195:
// errors.Join(err, tx.Rollback())). That mirrors withWriteTx's rollback
// behavior; it does not exercise the wrapper itself — the wrapper the
// production `bd create` path (cmd/bd/create.go -> writeOps -> ops.Create ->
// issueOperations.Create -> runIssueOperationTx -> withRetryTx -> withWriteTx)
// actually runs CreateIssuesInTxWithResult/CreateIssueInTxWithResult under —
// and checks whether the issue or its first label leaked through anyway.
// withRetryTx never retries this failure: context.Canceled matches none of
// isDoltAutocommitRollbackError/isSerializationError/isRetryableError, so it
// falls to backoff.Permanent and surfaces to the CLI as a single failed
// attempt, exactly as reported.
//
// What this test does NOT cover: the injected context.Canceled never
// reaches the server, so the real transaction stays alive throughout and is
// rolled back manually above, once, by this test's own tx.Rollback() call.
// The original production report's error text named a second condition —
// "transaction has already been committed or rolled back" (sql.ErrTxDone) —
// which only occurs when database/sql's own background machinery has
// already torn the transaction down out from under the caller. See
// TestCreateIssueRollsBackFullyOnContextCancelMidTransaction below for that
// case.
func TestCreateIssueRollsBackFullyOnLabelEventFailure(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const issueID = "create-rollback-label-event"
	issue := &types.Issue{
		ID:        issueID,
		Title:     "phantom create under label-event failure",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"label-one", "label-two"},
	}

	realTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = realTx.Rollback() }() // no-op if already rolled back below

	faultyTx := &failOnSecondEventInsertTx{Tx: realTx}

	// CreateOnly matches issueops.ExecuteCreate's own BatchCreateOptions —
	// the facade cmd/bd/create.go actually calls (via writeOps/ops.Create) on
	// the production `bd create` path — so this test exercises the same
	// insert-vs-upsert branch in InsertIssueIfNew that production hits.
	bc, err := issueops.NewBatchContext(ctx, faultyTx, storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("new batch context: %v", err)
	}

	_, createErr := issueops.CreateIssueInTxWithResult(ctx, faultyTx, bc, issue, "tester")
	if createErr == nil {
		t.Fatal("expected CreateIssueInTxWithResult to fail when a label event insert is canceled, got nil error")
	}
	if !strings.Contains(createErr.Error(), "failed to record label event") {
		t.Fatalf("error = %v, want it to mention the failed label event (matches the production error text in issueops/create.go)", createErr)
	}
	if !errors.Is(createErr, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", createErr)
	}
	if !faultyTx.injected {
		t.Fatal("fault injector never fired (eventInsertCount never reached 2) — test setup is broken and proves nothing")
	}

	// This is exactly what DoltStore.withWriteTx does on any callback error:
	// unconditionally roll back (internal/storage/dolt/store.go).
	if err := realTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// The claim under test: does the issue, or its first label (inserted
	// successfully before the second event insert failed), leak through
	// despite the rollback?
	var issueCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&issueCount); err != nil {
		t.Fatalf("post-rollback issue count: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("issue %s persisted despite mid-transaction label-event failure and rollback: found %d rows (this would confirm ga-xcq1ph as a real data-loss bug)", issueID, issueCount)
	}
	var labelCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", issueID).Scan(&labelCount); err != nil {
		t.Fatalf("post-rollback label count: %v", err)
	}
	if labelCount != 0 {
		t.Fatalf("label rows for %s persisted despite rollback: found %d rows", issueID, labelCount)
	}
	var eventCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issueID).Scan(&eventCount); err != nil {
		t.Fatalf("post-rollback event count: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("event rows for %s persisted despite rollback: found %d rows", issueID, eventCount)
	}
}

// cancelOnSecondEventInsertTx wraps a real *sql.Tx and, right as the second
// "INSERT INTO events" statement is about to run (the first label's "Added
// label" event — see the statement-ordering note on failOnSecondEventInsertTx
// above), cancels the transaction's OWN context instead of faking an error.
// The call is then let through with that now-canceled context, so it fails
// the same way a real canceled request would.
type cancelOnSecondEventInsertTx struct {
	*sql.Tx
	cancel           context.CancelFunc
	eventInsertCount int
}

func (c *cancelOnSecondEventInsertTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "INSERT INTO events") {
		c.eventInsertCount++
		if c.eventInsertCount == 2 {
			c.cancel()
		}
	}
	return c.Tx.ExecContext(ctx, query, args...)
}

// TestCreateIssueRollsBackFullyOnContextCancelMidTransaction closes the gap
// the PR #5989 review (bee-ghosttrack, 2026-09-03) identified in
// TestCreateIssueRollsBackFullyOnLabelEventFailure above: that test's
// injected context.Canceled never reaches the real transaction, so the
// transaction stays alive throughout and is rolled back manually, once, by
// the test itself — it can't reach the state where partial persistence
// would actually be conceivable.
//
// That state is: database/sql's own background awaitDone goroutine (spawned
// by BeginTx whenever it's given a cancelable context) notices the context
// is done and tears the transaction down on its own, racing ahead of
// anything the caller does next. The original production report's error
// text — "...context canceled / transaction has already been committed or
// rolled back" — is two different errors from two different sources: the
// failing ExecContext call (context.Canceled), and a *later* tx.Rollback()
// call — withWriteTx's own errors.Join(err, tx.Rollback()) cleanup
// (store.go:1195) — returning sql.ErrTxDone because the background goroutine
// had, by then, already won the race and rolled back for real.
//
// This test reproduces that: it cancels a real, BeginTx-scoped context
// mid-transaction (not a fabricated per-call error), confirms sql.ErrTxDone
// on the follow-up Rollback() exactly as reported, and then re-queries on
// store.db — a fresh connection from the pool, never the now-dead tx — to
// prove this state, too, still rolls back cleanly with no partial
// persistence.
func TestCreateIssueRollsBackFullyOnContextCancelMidTransaction(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	parent, parentCancel := testContext(t)
	defer parentCancel()
	ctx, cancel := context.WithCancel(parent)
	defer cancel() // no-op if already canceled below

	const issueID = "create-rollback-ctx-cancel"
	issue := &types.Issue{
		ID:        issueID,
		Title:     "phantom create under real mid-transaction context cancel",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"label-one", "label-two"},
	}

	realTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	// Safety net for any early t.Fatal below (e.g. a future change to this
	// test, or an environment where the injector never fires): without this,
	// an unreached tx sits open against the shared test DB until this
	// process exits, which can stall store.Close() during cleanup. A
	// same-goroutine Rollback() after the transaction is already done (the
	// success path below calls it explicitly) is a harmless no-op returning
	// sql.ErrTxDone.
	defer func() { _ = realTx.Rollback() }()

	cancelingTx := &cancelOnSecondEventInsertTx{Tx: realTx, cancel: cancel}

	bc, err := issueops.NewBatchContext(ctx, cancelingTx, storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("new batch context: %v", err)
	}

	_, createErr := issueops.CreateIssueInTxWithResult(ctx, cancelingTx, bc, issue, "tester")
	if createErr == nil {
		t.Fatal("expected CreateIssueInTxWithResult to fail when its context is canceled mid-transaction, got nil error")
	}
	if !errors.Is(createErr, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", createErr)
	}
	if cancelingTx.eventInsertCount < 2 {
		t.Fatalf("cancel trigger never fired (eventInsertCount = %d) — test setup is broken and proves nothing", cancelingTx.eventInsertCount)
	}

	// Wait for database/sql's background awaitDone goroutine to actually
	// finish tearing the transaction down. It races cancel() above against
	// this goroutine, so there is no synchronous signal for "done" — poll a
	// read-only statement on the SAME tx, with a context that is NOT
	// canceled (so this specifically probes the tx's own done flag, not the
	// argument-context check), until it reports sql.ErrTxDone.
	deadline := time.Now().Add(3 * time.Second)
	var probeErr error
	for time.Now().Before(deadline) {
		var discard int
		probeErr = realTx.QueryRowContext(context.Background(), "SELECT 1").Scan(&discard)
		if errors.Is(probeErr, sql.ErrTxDone) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !errors.Is(probeErr, sql.ErrTxDone) {
		t.Fatalf("timed out waiting for database/sql to tear down the tx after context cancel; last probe error: %v", probeErr)
	}

	// This is exactly what withWriteTx's own errors.Join(err, tx.Rollback())
	// does on the callback error above (store.go:1195) — and, matching the
	// original production report verbatim, it returns sql.ErrTxDone here
	// because the background rollback just confirmed above already beat it
	// to the punch.
	rollbackErr := realTx.Rollback()
	if !errors.Is(rollbackErr, sql.ErrTxDone) {
		t.Fatalf("rollback error = %v, want sql.ErrTxDone (database/sql had already rolled back on its own after the context was canceled)", rollbackErr)
	}
	joined := errors.Join(createErr, rollbackErr)
	if !strings.Contains(joined.Error(), "transaction has already been committed or rolled back") {
		t.Fatalf("joined error = %q, want it to reproduce the original report's exact second half", joined.Error())
	}

	// The claim under test: does the issue, or its first label (inserted
	// successfully before the second event insert triggered the cancel),
	// leak through despite database/sql's own rollback? Query on store.db —
	// a fresh connection from the pool, never the now-dead realTx — so this
	// can't accidentally pass by reading stale state off a broken tx.
	verifyCtx := context.Background()
	var issueCount int
	if err := store.db.QueryRowContext(verifyCtx, "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&issueCount); err != nil {
		t.Fatalf("post-rollback issue count: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("issue %s persisted despite mid-transaction context cancel: found %d rows (this would confirm ga-xcq1ph as a real data-loss bug)", issueID, issueCount)
	}
	var labelCount int
	if err := store.db.QueryRowContext(verifyCtx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", issueID).Scan(&labelCount); err != nil {
		t.Fatalf("post-rollback label count: %v", err)
	}
	if labelCount != 0 {
		t.Fatalf("label rows for %s persisted despite context-cancel rollback: found %d rows", issueID, labelCount)
	}
	var eventCount int
	if err := store.db.QueryRowContext(verifyCtx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issueID).Scan(&eventCount); err != nil {
		t.Fatalf("post-rollback event count: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("event rows for %s persisted despite context-cancel rollback: found %d rows", issueID, eventCount)
	}
}
