package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
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
// DoltStore.commitWriteTx does on any callback error
// (errors.Join(err, tx.Rollback()), reached via withWriteTx). That mirrors
// the rollback half only; it does not exercise the wrapper itself — the
// wrapper the production `bd create` path (cmd/bd/create.go -> writeOps ->
// ops.Create -> issueOperations.Create -> runIssueOperationTx -> withRetryTx
// -> withWriteTx) actually runs
// CreateIssuesInTxWithResult/CreateIssueInTxWithResult under — and checks
// whether the issue or its first label leaked through anyway.
// withRetryTx never retries this failure: context.Canceled matches none of
// isDoltAutocommitRollbackError/isSerializationError/isRetryableError, so it
// falls to backoff.Permanent and surfaces to the CLI as a single failed
// attempt, exactly as reported.
//
// Exactly what is and is not mirrored: before calling its callback,
// commitWriteTx installs three transaction-scoped activations —
// scopeEventsJournalTransaction, scopeVersionedHistoryTransaction and
// issueops.ScopeBlockedRecheckTransaction — and on the success path runs a
// post-commit blocked recheck. The bare BeginTx below registers none of them,
// so this transaction cannot emit a bd_events_journal or issue_versions row
// at all (journalEnabled falls back to the unregistered-tx default, false).
// The three counts asserted below — issues, labels, events — are therefore
// the only ones this shape can meaningfully test: adding a journal or
// issue_versions cleanliness assertion here, or moving the injection point
// downstream of those seams, would pass vacuously.
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

	// The rollback half of what DoltStore.commitWriteTx does on any callback
	// error: unconditionally roll back (errors.Join(err, tx.Rollback()),
	// reached via withWriteTx). The transaction scopes this hand-rolled tx
	// does not install are named in the doc comment above.
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
// call — the errors.Join(err, tx.Rollback()) cleanup in commitWriteTx,
// reached via withWriteTx — returning sql.ErrTxDone because the background
// goroutine had, by then, already won the race and rolled back for real.
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

	// The same rollback half commitWriteTx's errors.Join(err, tx.Rollback())
	// performs on a callback error, reached via withWriteTx — and, matching
	// the original production report verbatim, it returns sql.ErrTxDone here
	// because the background rollback just confirmed above already beat it to
	// the punch. As in test 1 this hand-rolled tx installs none of
	// commitWriteTx's three transaction scopes; driving the real wrapper is
	// deliberately out of scope here, because the behavior under test is
	// database/sql tearing the transaction down independently of any wrapper.
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

// TestFailedAutoMintedCreateRestoresEmptyID reproduces bd-wm5ahf (GH#8457):
// a singular CreateIssue with an empty ID that fails permanently (a
// non-retryable ErrFieldTooLong on a 256-character label, raised after the ID
// was minted) must leave the caller's struct with the empty ID it passed in.
// Otherwise a caller retrying the same struct takes the explicit-ID path
// instead of minting a fresh ID.
func TestFailedAutoMintedCreateRestoresEmptyID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	issue := &types.Issue{
		Title:     "auto-minted create that fails permanently",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", 256)},
	}

	err := store.CreateIssue(ctx, issue, "tester")
	if err == nil {
		t.Fatal("expected CreateIssue to fail on a 256-character label, got nil error")
	}
	if !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("error = %v, want types.ErrFieldTooLong", err)
	}
	if issue.ID != "" {
		t.Fatalf("issue.ID = %q after a permanently failed auto-minted create, want \"\" (minted ID leaked onto the caller's struct)", issue.ID)
	}
}

// TestCreateIssueFailureRestoresAutoMintedID pins the singular-create half of
// the GH#8457 fix. A permanently failed auto-minted create leaves the
// caller's ID empty and no row behind; an explicit-ID create failing the same
// way keeps the caller's ID, which was never the store's to clear.
func TestCreateIssueFailureRestoresAutoMintedID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	countIssues := func() int {
		var n int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&n); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		return n
	}
	before := countIssues()

	auto := &types.Issue{
		Title:     "auto-minted create that fails permanently",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", types.MaxFieldLen+1)},
	}
	if err := store.CreateIssue(ctx, auto, "tester"); !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("auto-minted CreateIssue err = %v, want types.ErrFieldTooLong", err)
	}
	if auto.ID != "" {
		t.Fatalf("auto issue ID = %q after a rolled-back create, want empty", auto.ID)
	}
	if n := countIssues(); n != before {
		t.Fatalf("issues row count = %d after a rolled-back create, want %d", n, before)
	}

	explicit := &types.Issue{
		ID:        "test-explicit",
		Title:     "explicit-ID create failing the same way",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", types.MaxFieldLen+1)},
	}
	if err := store.CreateIssue(ctx, explicit, "tester"); !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("explicit-ID CreateIssue err = %v, want types.ErrFieldTooLong", err)
	}
	if explicit.ID != "test-explicit" {
		t.Fatalf("explicit issue ID = %q, want test-explicit (a caller-supplied ID is never cleared)", explicit.ID)
	}
	if n := countIssues(); n != before {
		t.Fatalf("issues row count = %d after two rolled-back creates, want %d", n, before)
	}
}

// TestCreateIssueRetryAfterFailedAutoMintKeepsPeerRow pins the damage the
// leaked ID caused in counter mode (GH#8457): the failed create's counter
// bump rolled back with its transaction, so a peer create receives the same
// counter value. Retrying the caller's struct must mint a fresh ID and leave
// the peer's row alone — before the fix the leaked ID routed the retry
// through the explicit-ID upsert path and overwrote the peer's issue.
func TestCreateIssueRetryAfterFailedAutoMintKeepsPeerRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set issue_id_mode: %v", err)
	}

	seed := &types.Issue{Title: "seed", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, seed, "tester"); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	retry := &types.Issue{
		Title:     "failed create the caller retries",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{strings.Repeat("x", types.MaxFieldLen+1)},
	}
	if err := store.CreateIssue(ctx, retry, "tester"); !errors.Is(err, types.ErrFieldTooLong) {
		t.Fatalf("first create err = %v, want types.ErrFieldTooLong", err)
	}

	// The failed transaction's counter bump rolled back, so the peer create
	// receives the counter value the failed create minted and lost.
	peer := &types.Issue{Title: "peer", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, peer, "tester"); err != nil {
		t.Fatalf("peer create: %v", err)
	}

	// The caller retries the SAME struct with the invalid label fixed.
	retry.Labels = []string{"ok"}
	if err := store.CreateIssue(ctx, retry, "tester"); err != nil {
		t.Fatalf("retry create: %v", err)
	}
	if retry.ID == peer.ID {
		t.Fatalf("retry reused the rolled-back ID %q that the peer now holds", retry.ID)
	}

	got, err := store.GetIssue(ctx, peer.ID)
	if err != nil {
		t.Fatalf("get peer %s: %v", peer.ID, err)
	}
	if got.Title != "peer" {
		t.Fatalf("peer %s title = %q, want %q (the retry must not overwrite the peer's row)", peer.ID, got.Title, "peer")
	}
}

// TestCreateIssuesFailureRestoresFilledIDs pins the batch half of the GH#8457
// fix: when a CreateIssues transaction fails after issueops minted IDs and
// backfilled dependency IssueIDs and comment IDs from them, every ID field
// the caller left empty is empty again and every caller-supplied ID is
// unchanged. Three arms: the durable batch failing on a label after a
// sibling's comment was backfilled, the durable batch failing on a hierarchy
// conflict after both dependency IssueIDs were backfilled, and the
// all-wisps fast path failing on a label.
func TestCreateIssuesFailureRestoresFilledIDs(t *testing.T) {
	tooLong := strings.Repeat("x", types.MaxFieldLen+1)

	t.Run("durable/label", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		ctx, cancel := testContext(t)
		defer cancel()

		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}

		// An auto-minted issue is never deferred (deferrable needs a
		// non-empty ID), so the comment-bearing issue is fully written —
		// comment ID included — before the over-length label fails the batch.
		explicit := &types.Issue{
			ID: "test-explicit", Title: "explicit", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}
		withComment := &types.Issue{
			Title:     "auto with a fresh comment",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Labels:    []string{"ok"},
			Comments:  []*types.Comment{{Author: "tester", Text: "fresh comment"}},
		}
		badLabel := &types.Issue{
			Title:     "auto with an over-length label",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Labels:    []string{tooLong},
		}
		err := store.CreateIssues(ctx, []*types.Issue{explicit, withComment, badLabel}, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssues err = %v, want types.ErrFieldTooLong", err)
		}
		if explicit.ID != "test-explicit" {
			t.Fatalf("explicit ID = %q, want test-explicit (a caller-supplied ID is never cleared)", explicit.ID)
		}
		if withComment.ID != "" {
			t.Fatalf("auto issue ID = %q after a rolled-back batch, want empty", withComment.ID)
		}
		if withComment.Comments[0].ID != "" {
			t.Fatalf("fresh comment ID = %q after a rolled-back batch, want empty", withComment.Comments[0].ID)
		}
		if badLabel.ID != "" {
			t.Fatalf("failed issue ID = %q after a rolled-back batch, want empty", badLabel.ID)
		}
		var rows int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&rows); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if rows != 0 {
			t.Fatalf("issues holds %d rows after a rolled-back batch, want 0", rows)
		}
	})

	t.Run("durable/dependency", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		ctx, cancel := testContext(t)
		defer cancel()

		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}

		// Seed G, and P as G's child. The seed must go through the batch
		// create: only it persists creation-time dependencies.
		g := &types.Issue{ID: "test-g", Title: "parent G", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		p := &types.Issue{
			ID: "test-p", Title: "child P", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{DependsOnID: "test-g", Type: types.DepParentChild}},
		}
		if err := store.CreateIssues(ctx, []*types.Issue{g, p}, "tester"); err != nil {
			t.Fatalf("seed G and P: %v", err)
		}

		// X as a child of P — whose ancestors include G — that also blocks G:
		// an ancestor depending on its own descendant. The conflict is raised
		// by the batch dependency pass, after both empty dependency IssueIDs
		// were backfilled from X's minted ID.
		x := &types.Issue{
			Title: "auto X", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: "test-p", Type: types.DepParentChild},
				{DependsOnID: "test-g", Type: types.DepBlocks},
			},
		}
		err := store.CreateIssues(ctx, []*types.Issue{x}, "tester")
		var hierarchy *domain.DependencyHierarchyConflictError
		if !errors.As(err, &hierarchy) {
			t.Fatalf("CreateIssues err = %v, want *DependencyHierarchyConflictError", err)
		}
		if x.ID != "" {
			t.Fatalf("auto issue ID = %q after a rolled-back batch, want empty", x.ID)
		}
		for i, dep := range x.Dependencies {
			if dep.IssueID != "" {
				t.Fatalf("dependencies[%d].IssueID = %q after a rolled-back batch, want empty", i, dep.IssueID)
			}
		}
	})

	t.Run("all-wisps", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		ctx, cancel := testContext(t)
		defer cancel()

		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}

		first := &types.Issue{
			Title: "wisp one", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}
		second := &types.Issue{
			Title: "wisp two", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
			Labels: []string{tooLong},
		}
		err := store.CreateIssues(ctx, []*types.Issue{first, second}, "tester")
		if !errors.Is(err, types.ErrFieldTooLong) {
			t.Fatalf("CreateIssues err = %v, want types.ErrFieldTooLong", err)
		}
		if first.ID != "" || second.ID != "" {
			t.Fatalf("failed all-wisps batch left IDs %q/%q, want both empty", first.ID, second.ID)
		}
		var rows int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&rows); err != nil {
			t.Fatalf("count wisps: %v", err)
		}
		if rows != 0 {
			t.Fatalf("wisps holds %d rows after a rolled-back batch, want 0", rows)
		}
	})
}
