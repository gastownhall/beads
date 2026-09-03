package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournal_EmbeddedPlumbing drives mutations through the DoltStore
// (the DoltStorage write plumbing, which bottoms out in the issueops *InTx
// functions) against real Dolt and asserts the journal at the issueops seam
// records each op with a counter-assigned monotonic seq. This is the second of
// the two plumbings; the first is exercised in domain/db.
func TestEventsJournal_EmbeddedPlumbing(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	enableJournalForTest(t, store)
	clearJournal(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	must(store.CreateIssue(ctx, mk("bd-emb-1"), "actor"), "create 1")
	must(store.CreateIssue(ctx, mk("bd-emb-2"), "actor"), "create 2")
	must(store.UpdateIssue(ctx, "bd-emb-1", map[string]interface{}{"title": "renamed"}, "actor"), "update")
	must(store.AddLabel(ctx, "bd-emb-1", "urgent", "actor"), "add label")
	must(store.ClaimIssue(ctx, "bd-emb-1", "worker"), "claim")
	must(store.AddDependency(ctx, &types.Dependency{IssueID: "bd-emb-1", DependsOnID: "bd-emb-2", Type: types.DepBlocks}, "actor"), "add dep")
	must(store.RemoveDependency(ctx, "bd-emb-1", "bd-emb-2", "actor"), "remove dep")
	must(store.CloseIssue(ctx, "bd-emb-1", "done", "actor", ""), "close")
	must(store.DeleteIssue(ctx, "bd-emb-2"), "delete")

	got := readJournalRows(t, store)
	wantOps := []string{
		"create", "create", "update", "update", "update", "dep_add",
		"update", // derived is_blocked flip after dependency removal
		"dep_remove", "close", "delete",
	}
	if len(got) != len(wantOps) {
		t.Fatalf("expected %d journal rows, got %d: %+v", len(wantOps), len(got), got)
	}
	var prev int64
	for i, want := range wantOps {
		if got[i].op != want {
			t.Fatalf("row %d: op %q, want %q (%+v)", i, got[i].op, want, got)
		}
		if got[i].seq <= prev {
			t.Fatalf("row %d: seq %d not strictly greater than prev %d", i, got[i].seq, prev)
		}
		prev = got[i].seq
	}
}

// TestEventsJournal_NoPhantomDeletes asserts the bulk delete journals a delete
// only for ids that actually removed a row — never a phantom for an id that
// matched nothing.
func TestEventsJournal_NoPhantomDeletes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	if err := store.CreateIssue(ctx, mk("bd-pd-1"), "actor"); err != nil {
		t.Fatalf("create 1: %v", err)
	}
	if err := store.CreateIssue(ctx, mk("bd-pd-2"), "actor"); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	clearJournal(t, store)

	// Delete a mix of present and absent ids; force avoids the dependent gate.
	if _, err := store.DeleteIssues(ctx, []string{"bd-pd-1", "bd-pd-missing-a", "bd-pd-2", "bd-pd-missing-b"}, false, true, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	deleted := map[string]bool{}
	for _, r := range readJournalRows(t, store) {
		if r.op != "delete" {
			t.Fatalf("unexpected op %q for %s", r.op, r.id)
		}
		deleted[r.id] = true
	}
	if len(deleted) != 2 || !deleted["bd-pd-1"] || !deleted["bd-pd-2"] {
		t.Fatalf("journal must record deletes only for present ids, got %v", deleted)
	}
}

// TestEventsJournal_RunInTransactionMixedBuckets proves one public
// RunInTransaction callback can journal both a durable issue and a wisp. That
// plumbing normally uses separate regular and ignored SQL transactions, so both
// mutations must still share one ordered journal without contending with each
// other on bd_events_seq.
func TestEventsJournal_RunInTransactionMixedBuckets(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	enableJournalForTest(t, store)
	clearJournal(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	regular := &types.Issue{
		ID: "bd-jtx-regular", Title: "regular", IssueType: types.TypeTask, Status: types.StatusOpen,
	}
	wisp := &types.Issue{
		ID: "bd-jtx-wisp", Title: "wisp", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: journal mixed buckets", func(tx storage.Transaction) error {
		return tx.CreateIssues(ctx, []*types.Issue{regular, wisp}, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction mixed journaled create: %v", err)
	}

	rows := readJournalRows(t, store)
	if len(rows) != 2 {
		t.Fatalf("mixed journal rows = %+v, want two creates", rows)
	}
	if rows[0].op != "create" || rows[0].id != regular.ID {
		t.Fatalf("mixed journal row 0 = %+v, want create for %s", rows[0], regular.ID)
	}
	if rows[1].op != "create" || rows[1].id != wisp.ID {
		t.Fatalf("mixed journal row 1 = %+v, want create for %s", rows[1], wisp.ID)
	}
	if rows[1].seq != rows[0].seq+1 {
		t.Fatalf("mixed journal seqs = %d then %d, want consecutive", rows[0].seq, rows[1].seq)
	}
}

// TestEventsJournal_RunInTransactionWispOnly guards the no-versioned-tables
// case. The journal-enabled transaction still has to persist its ignored wisp
// and journal row when there is nothing for the following Dolt commit to stage.
func TestEventsJournal_RunInTransactionWispOnly(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	enableJournalForTest(t, store)
	clearJournal(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wisp := &types.Issue{
		ID: "bd-jtx-wisp-only", Title: "wisp", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true,
	}
	if err := store.RunInTransaction(ctx, "test: journal wisp only", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, wisp, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction journaled wisp create: %v", err)
	}

	if got, err := store.GetIssue(ctx, wisp.ID); err != nil || !got.Ephemeral {
		t.Fatalf("journaled wisp persisted = (%+v, %v), want active wisp", got, err)
	}
	rows := readJournalRows(t, store)
	if len(rows) != 1 || rows[0].op != "create" || rows[0].id != wisp.ID {
		t.Fatalf("wisp-only journal rows = %+v, want one create", rows)
	}
}

// TestEventsJournal_StaleSeqCounterSelfHeals proves an instance cannot be
// permanently wedged by a counter that has fallen BEHIND the journal — the
// state a restored, hand-edited, or cross-workspace-copied bd_events_seq row
// leaves. Left unhealed, every later mutation re-mints an already-taken seq,
// the primary key rejects it, and because the journal row shares the mutation's
// transaction the user's write fails with it: the store stops accepting writes
// entirely until someone repairs the counter by hand.
func TestEventsJournal_StaleSeqCounterSelfHeals(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)
	clearJournal(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	for _, id := range []string{"bd-heal-1", "bd-heal-2", "bd-heal-3"} {
		if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	before := readJournalRows(t, store)
	if len(before) != 3 {
		t.Fatalf("setup journal rows = %d, want 3", len(before))
	}

	// Drive the counter back to zero: the next allocation now collides with an
	// existing seq instead of extending past it.
	if _, err := store.db.ExecContext(ctx, "UPDATE bd_events_seq SET next_seq = 0 WHERE id = 0"); err != nil {
		t.Fatalf("corrupt seq counter: %v", err)
	}

	// The next mutation must succeed, not wedge.
	if err := store.CreateIssue(ctx, mk("bd-heal-4"), "actor"); err != nil {
		t.Fatalf("mutation after counter corruption must self-heal, got: %v", err)
	}

	after := readJournalRows(t, store)
	if len(after) != 4 {
		t.Fatalf("journal rows after heal = %d, want 4: %+v", len(after), after)
	}
	// The healed row extends past the high-water mark rather than colliding,
	// and the journal stays strictly ordered with no duplicates.
	seen := map[int64]bool{}
	var prev int64
	for i, r := range after {
		if seen[r.seq] {
			t.Fatalf("duplicate seq %d after heal: %+v", r.seq, after)
		}
		seen[r.seq] = true
		if i > 0 && r.seq <= prev {
			t.Fatalf("seq %d not strictly greater than %d after heal: %+v", r.seq, prev, after)
		}
		prev = r.seq
	}
	if after[3].id != "bd-heal-4" {
		t.Fatalf("healed row = %+v, want the create for bd-heal-4", after[3])
	}

	// And the instance keeps working afterwards — the heal is durable, not a
	// one-shot that re-wedges on the following write.
	if err := store.CreateIssue(ctx, mk("bd-heal-5"), "actor"); err != nil {
		t.Fatalf("mutation after heal must succeed, got: %v", err)
	}
	if got := readJournalRows(t, store); len(got) != 5 {
		t.Fatalf("journal rows after follow-up = %d, want 5: %+v", len(got), got)
	}
}

// TestEventsJournal_TxAtomicity proves the journal insert and the mutation
// share the transaction even though the journal table is dolt-ignored: a
// rollback drops both, a commit keeps both.
func TestEventsJournal_TxAtomicity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	issue := &types.Issue{ID: "bd-atom-1", Title: "atomic", IssueType: types.TypeTask, Status: types.StatusOpen}
	if err := store.CreateIssue(ctx, issue, "actor"); err != nil {
		t.Fatalf("create: %v", err)
	}
	clearJournal(t, store)

	// The transaction rolls back when the callback fails: neither the mutation
	// nor its journal row survives.
	wantErr := errRollbackProbe
	if err := store.RunInTransaction(ctx, "test: journal rollback", func(tx storage.Transaction) error {
		if err := tx.UpdateIssue(ctx, "bd-atom-1", map[string]interface{}{"title": "rolled back"}, "actor"); err != nil {
			return err
		}
		return wantErr
	}); err == nil {
		t.Fatal("RunInTransaction must surface the callback error")
	}
	if rows := readJournalRows(t, store); len(rows) != 0 {
		t.Fatalf("rolled-back journal rows = %+v, want none", rows)
	}
	got, err := store.GetIssue(ctx, "bd-atom-1")
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if got.Title != "atomic" {
		t.Fatalf("rolled-back title = %q, want the original", got.Title)
	}

	// The same mutation, committed: both land.
	if err := store.RunInTransaction(ctx, "test: journal commit", func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, "bd-atom-1", map[string]interface{}{"title": "committed"}, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction commit: %v", err)
	}
	rows := readJournalRows(t, store)
	if len(rows) != 1 || rows[0].op != "update" || rows[0].id != "bd-atom-1" {
		t.Fatalf("committed journal rows = %+v, want one update", rows)
	}
	got, err = store.GetIssue(ctx, "bd-atom-1")
	if err != nil {
		t.Fatalf("get after commit: %v", err)
	}
	if got.Title != "committed" {
		t.Fatalf("committed title = %q, want %q", got.Title, "committed")
	}
}
