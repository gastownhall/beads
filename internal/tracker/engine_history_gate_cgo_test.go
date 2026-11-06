//go:build cgo

// Cgo-tagged regression tests for the externalRefChangedAfter fast-path gate
// (GH#4549), exercised against the real Dolt-backed stores. Pure-Go tests
// using fakes live in engine_external_ref_history_test.go.
//
// TestBatchExternalRefHistoryCapabilityOnRealStores below extends the same
// gate to the batch form (GH#6605): both real backends must expose the batch
// capability directly, through tracker.NewStore's adapter, and beneath a
// decorator, because the pull path reaches the store through any of those
// three shapes.

package tracker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedDoltExternalRefChangedAfterUsesHistoryFastPath is a regression
// test for GH#4549: EmbeddedDoltStore supports the dolt_history_issues query
// (it implements storage.HistoryViewer) but does not expose a raw *sql.DB,
// so the old dbProvider{DB() *sql.DB}-gated fast path never engaged for it —
// it silently fell back to the coarse CreatedAt/UpdatedAt heuristic even
// though the precise history table was available. This test constructs a
// case where the two heuristics disagree (local's timestamps predate asOf,
// so the fallback would wrongly say "unchanged") and asserts the fast,
// correct answer is produced instead.
func TestEmbeddedDoltExternalRefChangedAfterUsesHistoryFastPath(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "gh4549", "main")
	if err != nil {
		t.Fatalf("Open embedded store: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "gh4549"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	issue := &types.Issue{
		ID:        "gh4549-1",
		Title:     "regression for GH#4549",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "old-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=old-ref): %v", err)
	}
	if err := store.Commit(ctx, "set old external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// asOf must land strictly after the "old-ref" commit and strictly
	// before the next one. Sleep on both sides to clear commit_date
	// timestamp granularity, since the query filters on commit_date <= asOf.
	time.Sleep(1200 * time.Millisecond)
	asOf := time.Now().UTC()
	time.Sleep(1200 * time.Millisecond)

	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "new-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=new-ref): %v", err)
	}
	if err := store.Commit(ctx, "set new external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Sanity check: EmbeddedDoltStore must be recognized as history-capable
	// directly (no decorator involved here).
	if _, ok := externalRefHistoryQuerier(store); !ok {
		t.Fatal("expected EmbeddedDoltStore to satisfy storage.ExternalRefHistoryQuerier")
	}

	e := &Engine{Store: store}

	// local's own timestamps predate asOf, so the coarse fallback heuristic
	// would (wrongly) say "unchanged" here.
	local := &types.Issue{
		ID:        issue.ID,
		CreatedAt: asOf.Add(-time.Hour),
		UpdatedAt: asOf.Add(-time.Hour),
	}

	changed, err := e.externalRefChangedAfter(ctx, local, "new-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true: external_ref differs from its value as of asOf, even though local's timestamps predate asOf")
	}

	// Complementary check: currentRef matching the historical value as of
	// asOf should report unchanged.
	changed, err = e.externalRefChangedAfter(ctx, local, "old-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false: currentRef matches the historical value as of asOf")
	}
}

// TestDoltStoreExternalRefChangedAfterUsesHistoryFastPath confirms the
// GH#4549 fix leaves the Dolt server store's fast-path behavior unchanged:
// DoltStore already implements storage.ExternalRefHistoryQuerier (it always
// had DB() access), so this should behave exactly as the old dbProvider gate
// did for this backend.
func TestDoltStoreExternalRefChangedAfterUsesHistoryFastPath(t *testing.T) {
	store := newTestStore(t) // skips if the shared test Dolt server isn't available
	defer store.Close()

	ctx := context.Background()
	issue := &types.Issue{
		ID:        "history-gate-1",
		Title:     "regression for GH#4549",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "old-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=old-ref): %v", err)
	}
	if err := store.Commit(ctx, "set old external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	asOf := time.Now().UTC()
	time.Sleep(1200 * time.Millisecond)

	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"external_ref": "new-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=new-ref): %v", err)
	}
	if err := store.Commit(ctx, "set new external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if _, ok := externalRefHistoryQuerier(store); !ok {
		t.Fatal("expected DoltStore to satisfy storage.ExternalRefHistoryQuerier")
	}

	e := &Engine{Store: store}
	local := &types.Issue{
		ID:        issue.ID,
		CreatedAt: asOf.Add(-time.Hour),
		UpdatedAt: asOf.Add(-time.Hour),
	}

	changed, err := e.externalRefChangedAfter(ctx, local, "new-ref", asOf)
	if err != nil {
		t.Fatalf("externalRefChangedAfter: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true via the (behaviorally unchanged) Dolt server fast path")
	}
}

// batchCapabilityGate holds the seeded history one gate leg needs: an issue
// whose external_ref was old-ref at asOf and new-ref after it, plus the
// never-existed id every leg must see absent from the batch answer.
type batchCapabilityGate struct {
	store   storage.DoltStorage
	issueID string
	asOf    time.Time
}

// seedBatchCapabilityGate writes the two dated external_ref states the gate
// legs read. It mirrors the GH#4549 gates' seeding: commit old-ref, record
// asOf between the commits (sleeping past commit_date granularity), then
// commit new-ref.
func seedBatchCapabilityGate(t *testing.T, ctx context.Context, store storage.DoltStorage, issueID string) batchCapabilityGate {
	t.Helper()
	issue := &types.Issue{
		ID:        issueID,
		Title:     "batch capability gate for GH#6605",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issueID, map[string]interface{}{
		"external_ref": "old-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=old-ref): %v", err)
	}
	if err := store.Commit(ctx, "set old external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// asOf must land strictly after the old-ref commit and strictly before
	// the new-ref one; the history query filters on commit_date <= asOf.
	time.Sleep(1200 * time.Millisecond)
	asOf := time.Now().UTC()
	time.Sleep(1200 * time.Millisecond)

	if err := store.UpdateIssue(ctx, issueID, map[string]interface{}{
		"external_ref": "new-ref",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue(external_ref=new-ref): %v", err)
	}
	if err := store.Commit(ctx, "set new external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return batchCapabilityGate{store: store, issueID: issueID, asOf: asOf}
}

// assertBatchCapabilityGate drives one discovered capability through the
// parity question the pull path asks: the batch answer must equal the
// single-id answer for the seeded id (old-ref at asOf, found) and must omit
// an id with no history.
func assertBatchCapabilityGate(t *testing.T, ctx context.Context, q ExternalRefHistoryBatchStore, gate batchCapabilityGate, shape string) {
	t.Helper()
	ids := []string{gate.issueID, "gh6605-never-existed"}
	refs, err := q.PreviousExternalRefs(ctx, ids, gate.asOf)
	if err != nil {
		t.Fatalf("%s: PreviousExternalRefs: %v", shape, err)
	}
	if ref, present := refs[gate.issueID]; !present || ref != "old-ref" {
		t.Errorf("%s: batch ref of %s = (%q, %v), want (old-ref, true)", shape, gate.issueID, ref, present)
	}
	if _, present := refs["gh6605-never-existed"]; present {
		t.Errorf("%s: batch answer contains gh6605-never-existed, which has no history", shape)
	}
	single, found, err := q.PreviousExternalRef(ctx, gate.issueID, gate.asOf)
	if err != nil {
		t.Fatalf("%s: PreviousExternalRef: %v", shape, err)
	}
	if !found || single != "old-ref" {
		t.Errorf("%s: single-id answer = (%q, %v), want (old-ref, true)", shape, single, found)
	}
}

// TestBatchExternalRefHistoryCapabilityOnRealStores pins the batch
// capability (GH#6605) on both real backends in every shape the pull path
// can hold the store: directly, through tracker.NewStore's directStore
// adapter, and beneath a decorator that only promotes DoltStorage and
// unwraps — the HookFiringStore shape. A backend that answers single-id
// history but not the batch form would silently put the pull back on one
// history query per linked issue, which is exactly the cost #6605 exists to
// remove.
func TestBatchExternalRefHistoryCapabilityOnRealStores(t *testing.T) {
	runGate := func(t *testing.T, ctx context.Context, store storage.DoltStorage) {
		gate := seedBatchCapabilityGate(t, ctx, store, "gh6605-1")

		// Direct: the store itself.
		direct, ok := externalRefHistoryBatchQuerier(store)
		if !ok {
			t.Fatal("expected the store itself to satisfy the batch capability")
		}
		assertBatchCapabilityGate(t, ctx, direct, gate, "direct")

		// Through NewStore: the directStore adapter the engine builds.
		adapted, ok := externalRefHistoryBatchQuerier(NewStore(store))
		if !ok {
			t.Fatal("expected NewStore(store) to preserve the batch capability")
		}
		assertBatchCapabilityGate(t, ctx, adapted, gate, "NewStore")

		// Beneath a decorator: only DoltStorage promotion and Unwrap, like
		// storage.HookFiringStore.
		decorated := &fakeStoreDecorator{inner: store}
		throughDecorator, ok := externalRefHistoryBatchQuerier(NewStore(decorated))
		if !ok {
			t.Fatal("expected the batch capability to be discovered beneath a decorator")
		}
		assertBatchCapabilityGate(t, ctx, throughDecorator, gate, "decorator")
	}

	t.Run("embedded", func(t *testing.T) {
		ctx := context.Background()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		store, err := embeddeddolt.Open(ctx, beadsDir, "gh6605", "main")
		if err != nil {
			t.Fatalf("Open embedded store: %v", err)
		}
		defer store.Close()
		if err := store.SetConfig(ctx, "issue_prefix", "gh6605"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		if err := store.Commit(ctx, "bd init"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		runGate(t, ctx, store)
	})

	t.Run("dolt-server", func(t *testing.T) {
		store := newTestStore(t) // skips if the shared test Dolt server isn't available
		defer store.Close()
		runGate(t, context.Background(), store)
	})
}
