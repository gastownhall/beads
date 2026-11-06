// Regression tests for #4549: externalRefChangedAfter's fast path must be
// gated on the storage.ExternalRefHistoryQuerier capability, not on whether
// the store happens to expose a raw *sql.DB. See engine.go's
// externalRefHistoryQuerier and externalRefChangedAfter for the rationale.
//
// This file MUST NOT carry a `//go:build cgo` tag (see test_helpers_pure_test.go):
// the mocks below are pure Go and exercise the capability-detection logic
// directly, without a real Dolt store.

package tracker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// historyQuerierStore implements storage.ExternalRefHistoryQuerier directly,
// with no DB() method at all -- mirroring embeddeddolt.EmbeddedDoltStore,
// which executes SQL through a transaction-scoped connection helper rather
// than exposing a pooled *sql.DB. Embeds storage.Storage (nil) purely so the
// type satisfies storage.Storage for assignment to Engine.Store; tests never
// call the promoted methods.
type historyQuerierStore struct {
	storage.Storage
	storage.IssueLifecycleStore

	prevRef   string
	prevFound bool
	prevErr   error
	calls     int
}

var _ storage.ExternalRefHistoryQuerier = (*historyQuerierStore)(nil)

func (h *historyQuerierStore) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return nil, nil
}

func (h *historyQuerierStore) AsOf(_ context.Context, _ string, _ string) (*types.Issue, error) {
	return nil, nil
}

func (h *historyQuerierStore) Diff(_ context.Context, _, _ string) ([]*storage.DiffEntry, error) {
	return nil, nil
}

func (h *historyQuerierStore) PreviousExternalRef(_ context.Context, _ string, _ time.Time) (string, bool, error) {
	h.calls++
	return h.prevRef, h.prevFound, h.prevErr
}

// doltLikeStore is a full storage.DoltStorage that also implements
// ExternalRefHistoryQuerier, standing in for dolt.DoltStore. It embeds
// storage.DoltStorage (nil) for the rest of the (huge) interface's methods,
// which these tests never exercise.
type doltLikeStore struct {
	storage.DoltStorage

	prevRef   string
	prevFound bool
	calls     int
}

var _ storage.DoltStorage = (*doltLikeStore)(nil)
var _ storage.ExternalRefHistoryQuerier = (*doltLikeStore)(nil)

func (d *doltLikeStore) History(_ context.Context, _ string) ([]*storage.HistoryEntry, error) {
	return nil, nil
}

func (d *doltLikeStore) AsOf(_ context.Context, _ string, _ string) (*types.Issue, error) {
	return nil, nil
}

func (d *doltLikeStore) Diff(_ context.Context, _, _ string) ([]*storage.DiffEntry, error) {
	return nil, nil
}

func (d *doltLikeStore) PreviousExternalRef(_ context.Context, _ string, _ time.Time) (string, bool, error) {
	d.calls++
	return d.prevRef, d.prevFound, nil
}

// fakeStoreDecorator stands in for storage.HookFiringStore (or any other
// storage.Unwrapper decorator): it embeds storage.DoltStorage for passthrough
// but does NOT itself implement ExternalRefHistoryQuerier, exactly like the
// real decorator, which only promotes DoltStorage's methods.
type fakeStoreDecorator struct {
	storage.DoltStorage
	inner storage.DoltStorage
}

var _ storage.DoltStorage = (*fakeStoreDecorator)(nil)
var _ storage.Unwrapper = (*fakeStoreDecorator)(nil)

func (f *fakeStoreDecorator) Unwrap() storage.DoltStorage { return f.inner }

func newTestIssue(id string, createdAt, updatedAt time.Time) *types.Issue {
	return &types.Issue{ID: id, CreatedAt: createdAt, UpdatedAt: updatedAt}
}

// (a) Embedded-Dolt-shaped store: takes the history fast path even though it
// has no DB() method, because it satisfies ExternalRefHistoryQuerier directly.
func TestExternalRefHistoryQuerier_DirectCapability(t *testing.T) {
	store := &historyQuerierStore{}
	q, ok := externalRefHistoryQuerier(store)
	if !ok {
		t.Fatal("expected historyQuerierStore to be detected as ExternalRefHistoryQuerier")
	}
	if q == nil {
		t.Fatal("expected non-nil querier")
	}
}

// (b) A store with no history capability at all must not be mistaken for one.
func TestExternalRefHistoryQuerier_NoCapability(t *testing.T) {
	store := newPureTestStore()
	if _, ok := externalRefHistoryQuerier(store); ok {
		t.Fatal("expected pureTestStore (no history capability) to report ok=false")
	}
}

// (c) A decorated Dolt-server-shaped store (e.g. HookFiringStore wrapping
// dolt.DoltStore) must still be detected via storage.UnwrapStore, even
// though the decorator itself doesn't directly satisfy the interface.
func TestExternalRefHistoryQuerier_UnwrapsDecorator(t *testing.T) {
	inner := &doltLikeStore{prevRef: "ref-1", prevFound: true}
	var dec storage.DoltStorage = &fakeStoreDecorator{DoltStorage: inner, inner: inner}

	// Sanity check mirroring the real HookFiringStore: the decorator itself
	// does not directly satisfy ExternalRefHistoryQuerier (PreviousExternalRef
	// is not part of storage.DoltStorage, so it isn't promoted).
	if _, ok := dec.(storage.ExternalRefHistoryQuerier); ok {
		t.Fatal("test setup invalid: decorator should not directly expose PreviousExternalRef")
	}

	q, ok := externalRefHistoryQuerier(dec)
	if !ok {
		t.Fatal("expected decorator wrapping a history-capable store to be detected via UnwrapStore")
	}
	ref, found, err := q.PreviousExternalRef(context.Background(), "bd-1", time.Now())
	if err != nil || !found || ref != "ref-1" {
		t.Fatalf("PreviousExternalRef via unwrapped decorator = (%q, %v, %v), want (\"ref-1\", true, nil)", ref, found, err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected inner store to be called once, got %d", inner.calls)
	}
}

func TestEngineExternalRefChangedAfter_NilLocalIssue(t *testing.T) {
	e := &Engine{Store: newPureTestStore()}
	changed, err := e.externalRefChangedAfter(context.Background(), nil, "ref", time.Now())
	if err != nil || changed {
		t.Fatalf("externalRefChangedAfter(nil local) = (%v, %v), want (false, nil)", changed, err)
	}
}

func TestEngineExternalRefChangedAfter_FastPathSameRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, err := e.externalRefChangedAfter(context.Background(), local, "https://tracker.test/EXT-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false when previous ref matches current ref")
	}
	if querier.calls != 1 {
		t.Fatalf("expected fast path to call PreviousExternalRef once, got %d calls", querier.calls)
	}
}

func TestEngineExternalRefChangedAfter_FastPathDifferentRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "https://tracker.test/EXT-OLD", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, err := e.externalRefChangedAfter(context.Background(), local, "https://tracker.test/EXT-NEW", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when previous ref differs from current ref")
	}
	if querier.calls != 1 {
		t.Fatalf("expected fast path to call PreviousExternalRef once, got %d calls", querier.calls)
	}
}

func TestEngineExternalRefChangedAfter_FastPathNotFound(t *testing.T) {
	// No history entry at or before lastSync: treat as changed, per the
	// documented contract on storage.ExternalRefHistoryQuerier.PreviousExternalRef.
	querier := &historyQuerierStore{prevFound: false}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, err := e.externalRefChangedAfter(context.Background(), local, "https://tracker.test/EXT-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when no prior history entry is found")
	}
}

// Locks the NULL-historical contract: a NULL external_ref column surfaces
// from PreviousExternalRefInTx as ("", found=true) (see
// issueops.PreviousExternalRefInTx, internal/storage/issueops/history.go),
// not as "not found". The fast path must therefore compare it against
// currentRef like any other value, rather than treating it as an
// unconditional "changed" the way the old inline !previousRef.Valid check did.
func TestEngineExternalRefChangedAfter_FastPathNullHistoricalRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, err := e.externalRefChangedAfter(context.Background(), local, "", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false: NULL historical ref (\"\", found=true) compared equal to an empty currentRef")
	}
}

func TestEngineExternalRefChangedAfter_FastPathPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	querier := &historyQuerierStore{prevErr: wantErr}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	_, err := e.externalRefChangedAfter(context.Background(), local, "ref", time.Now())
	if !errors.Is(err, wantErr) {
		t.Fatalf("externalRefChangedAfter error = %v, want %v", err, wantErr)
	}
}

// This is the core regression: before the fix, an embedded-Dolt store (which
// has no DB() method) fell through to the coarse timestamp heuristic even
// though it is fully capable of answering the history query precisely.
func TestEngineExternalRefChangedAfter_EmbeddedDoltTakesFastPath(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}
	e := &Engine{Store: querier}

	// Local issue's timestamps would make the old DB()-gated fallback report
	// "changed" (both before lastSync -> false) or "unchanged" incorrectly;
	// what matters is that the fast path -- not the heuristic -- decides.
	lastSync := time.Now()
	local := newTestIssue("bd-1", lastSync.Add(-time.Hour), lastSync.Add(time.Hour))

	changed, err := e.externalRefChangedAfter(context.Background(), local, "https://tracker.test/EXT-1", lastSync)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false: fast path found matching ref, heuristic would have said true")
	}
	if querier.calls != 1 {
		t.Fatal("expected embedded-Dolt-shaped store to take the history fast path")
	}
}

// (b) A store with no history capability (e.g. a plain non-Dolt backend)
// must fall back to the timestamp heuristic safely -- no panic, correct result.
func TestEngineExternalRefChangedAfter_NoHistoryCapabilityFallsBack(t *testing.T) {
	store := newPureTestStore()
	e := &Engine{Store: store}
	lastSync := time.Now()

	t.Run("touched after lastSync reports changed", func(t *testing.T) {
		local := newTestIssue("bd-1", lastSync.Add(-time.Hour), lastSync.Add(time.Hour))
		changed, err := e.externalRefChangedAfter(context.Background(), local, "ref", lastSync)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true when UpdatedAt is after lastSync (heuristic fallback)")
		}
	})

	t.Run("untouched since lastSync reports unchanged", func(t *testing.T) {
		local := newTestIssue("bd-2", lastSync.Add(-2*time.Hour), lastSync.Add(-time.Hour))
		changed, err := e.externalRefChangedAfter(context.Background(), local, "ref", lastSync)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when created/updated both before lastSync (heuristic fallback)")
		}
	})
}

// (c) A Dolt-server store wrapped in a decorator (e.g. HookFiringStore)
// behaves exactly as an undecorated Dolt-server store would: fast path,
// unchanged via UnwrapStore.
func TestEngineExternalRefChangedAfter_DecoratedDoltServerStoreUnchanged(t *testing.T) {
	inner := &doltLikeStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}
	dec := &fakeStoreDecorator{DoltStorage: inner, inner: inner}
	e := &Engine{Store: dec}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, err := e.externalRefChangedAfter(context.Background(), local, "https://tracker.test/EXT-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false: decorated Dolt-server store should take the fast path via UnwrapStore")
	}
	if inner.calls != 1 {
		t.Fatalf("expected inner Dolt-server store to be queried once via unwrap, got %d calls", inner.calls)
	}
}

// batchOverlapStore is an overlapStore that additionally answers the whole
// pull's external_ref history in one PreviousExternalRefs call, the way the
// Dolt stores do through issueops.PreviousExternalRefsInTx. singleCalls counts
// the per-issue fallback reads so a test can prove the batch path decided
// every candidate on its own.
type batchOverlapStore struct {
	*overlapStore
	batchCalls  int
	batchIDs    [][]string
	singleCalls int
}

func (s *batchOverlapStore) PreviousExternalRef(ctx context.Context, id string, asOf time.Time) (string, bool, error) {
	s.singleCalls++
	return s.overlapStore.PreviousExternalRef(ctx, id, asOf)
}

func (s *batchOverlapStore) PreviousExternalRefs(_ context.Context, issueIDs []string, _ time.Time) (map[string]string, error) {
	s.batchCalls++
	s.batchIDs = append(s.batchIDs, issueIDs)
	refs := make(map[string]string, len(issueIDs))
	for _, id := range issueIDs {
		if ref, ok := s.previousRefs[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

func newBatchOverlapLocal(id, ref string) *types.Issue {
	linked := ref
	return &types.Issue{
		ID:          id,
		Title:       "linked " + id,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &linked,
		CreatedAt:   overlapLastSync.Add(-time.Hour),
		UpdatedAt:   overlapLastSync.Add(-time.Hour),
	}
}

// TestFetchPrelinkedIssuesMakesOneBatchCall pins #6605's engine half: when the
// store can answer external_ref history for many issues in one call, an
// incremental pull reads that history once per pull — not once per pre-linked
// local issue — and still decides every issue exactly as the per-issue path
// would (AC-1).
func TestFetchPrelinkedIssuesMakesOneBatchCall(t *testing.T) {
	ctx := context.Background()
	unchanged := newBatchOverlapLocal("bd-0", "https://test.test/EXT-0")
	relinked := newBatchOverlapLocal("bd-1", "https://test.test/EXT-1")
	unproven := newBatchOverlapLocal("bd-2", "https://test.test/EXT-2")
	noRef := &types.Issue{ID: "bd-3", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	emptyRef := newBatchOverlapLocal("bd-4", " ")
	locals := []*types.Issue{unchanged, relinked, unproven, noRef, emptyRef}

	base := newOverlapStore(nil)
	base.issues = locals
	base.previousRefs[unchanged.ID] = "https://test.test/EXT-0" // same ref at last_sync: unchanged
	base.previousRefs[relinked.ID] = "https://test.test/EXT-OLD" // different ref at last_sync: changed
	// bd-2 has no history entry at or before last_sync: changed, per the
	// PreviousExternalRef contract.
	store := &batchOverlapStore{overlapStore: base}

	mock, _ := newOverlapTracker(
		TrackerIssue{ID: "EXT-1", Identifier: "EXT-1", Title: "remote one", UpdatedAt: overlapLastSync.Add(-time.Minute)},
		TrackerIssue{ID: "EXT-2", Identifier: "EXT-2", Title: "remote two", UpdatedAt: overlapLastSync.Add(-time.Minute)},
	)
	engine := NewEngine(mock, store, "sync")

	hydrated, hydratedLocalIDs, err := engine.fetchPrelinkedIssues(ctx, nil, locals, &overlapLastSync)
	if err != nil {
		t.Fatalf("fetchPrelinkedIssues: %v", err)
	}
	if store.batchCalls != 1 {
		t.Fatalf("PreviousExternalRefs calls = %d, want exactly 1 for the whole pull", store.batchCalls)
	}
	if store.singleCalls != 0 {
		t.Fatalf("PreviousExternalRef calls = %d, want 0: the batch result must decide every candidate", store.singleCalls)
	}
	if len(store.batchIDs) != 1 || len(store.batchIDs[0]) != 3 {
		t.Fatalf("batch request ids = %v, want the 3 eligible locals (nil/blank refs are not candidates)", store.batchIDs)
	}
	wantIDs := []string{"bd-0", "bd-1", "bd-2"}
	for i, id := range wantIDs {
		if store.batchIDs[0][i] != id {
			t.Fatalf("batch request ids = %v, want %v in local order", store.batchIDs[0], wantIDs)
		}
	}
	if len(hydrated) != 2 || hydratedLocalIDs["bd-1"] != true || hydratedLocalIDs["bd-2"] != true {
		t.Fatalf("hydrated = %+v, hydratedLocalIDs = %v, want only bd-1 (relinked) and bd-2 (no history) hydrated", hydrated, hydratedLocalIDs)
	}
	if hydratedLocalIDs["bd-0"] {
		t.Error("bd-0's ref is unchanged since last_sync; it must not be hydrated")
	}
}
