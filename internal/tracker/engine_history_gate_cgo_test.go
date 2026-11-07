//go:build cgo

// Cgo-tagged regression tests for the externalRefChangedAfter fast-path gate
// (GH#4549), exercised against the real Dolt-backed stores. Pure-Go tests
// using fakes live in engine_external_ref_history_test.go.

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

// assertBatchHistoryReachesConcreteStore covers AC-6: whether the engine is
// handed the concrete store, the NewStore adapter around it, or a storage
// decorator (as bd's tracker commands hand it a HookFiringStore), capability
// discovery must arrive at the concrete store's PreviousExternalRefs. A
// decorator promotes only storage.DoltStorage, so a plain type assertion on
// it would miss the batch method and silently fall back to one history query
// per issue.
func assertBatchHistoryReachesConcreteStore(t *testing.T, concrete storage.DoltStorage, issueID, wantRef string, asOf time.Time) {
	t.Helper()
	decorated := storage.NewHookFiringStore(concrete, nil)
	if _, ok := storage.DoltStorage(decorated).(storage.ExternalRefHistoryBatchQuerier); ok {
		t.Fatal("test setup invalid: the decorator itself exposes PreviousExternalRefs")
	}
	for name, store := range map[string]Store{
		"direct":              concrete,
		"NewStore":            NewStore(concrete),
		"decorator":           decorated,
		"NewStore(decorator)": NewStore(decorated),
	} {
		q, ok := externalRefHistoryBatchQuerier(store)
		if !ok {
			t.Errorf("%s: no batch external_ref history capability found", name)
			continue
		}
		if concreteQ, _ := concrete.(ExternalRefHistoryBatchStore); q != concreteQ {
			t.Errorf("%s: capability resolved to %T, want the concrete %T", name, q, concrete)
		}
		refs, err := q.PreviousExternalRefs(context.Background(), []string{issueID}, asOf)
		if err != nil {
			t.Fatalf("%s: PreviousExternalRefs: %v", name, err)
		}
		if got, ok := refs[issueID]; !ok || got != wantRef {
			t.Errorf("%s: PreviousExternalRefs[%s] = (%q, %t), want (%q, true)", name, issueID, got, ok, wantRef)
		}
	}
}

func TestEmbeddedDoltPreviousExternalRefsCapabilityReachesConcreteStore(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "batchcap", "main")
	if err != nil {
		t.Fatalf("Open embedded store: %v", err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "batchcap"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	issue := &types.Issue{ID: "batchcap-1", Title: "batch capability", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"external_ref": "batch-ref"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if err := store.Commit(ctx, "set external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	assertBatchHistoryReachesConcreteStore(t, store, issue.ID, "batch-ref", time.Now().Add(time.Hour))
}

func TestDoltStorePreviousExternalRefsCapabilityReachesConcreteStore(t *testing.T) {
	store := newTestStore(t) // skips if the shared test Dolt server isn't available
	defer store.Close()
	ctx := context.Background()
	issue := &types.Issue{ID: "batch-cap-1", Title: "batch capability", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"external_ref": "batch-ref"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if err := store.Commit(ctx, "set external_ref"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	assertBatchHistoryReachesConcreteStore(t, store, issue.ID, "batch-ref", time.Now().Add(time.Hour))
}
