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
	"reflect"
	"slices"
	"sort"
	"strings"
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

// recordingTracker records every FetchIssue identifier in call order, so a
// parity run can compare the batch and per-issue paths' single-issue fetches.
type recordingTracker struct {
	*mockTracker
	fetchedIDs []string
}

func (m *recordingTracker) FetchIssue(ctx context.Context, identifier string) (*TrackerIssue, error) {
	m.fetchedIDs = append(m.fetchedIDs, identifier)
	return m.mockTracker.FetchIssue(ctx, identifier)
}

// prelinkedOutcome is what one fetchPrelinkedIssues run produced, reduced to
// the observable surface the batch and per-issue paths must agree on.
type prelinkedOutcome struct {
	hydratedIDs []string
	localIDs    map[string]bool
	fetchedIDs  []string
	err         error
}

// runPrelinkedPull executes fetchPrelinkedIssues once over a fresh copy of
// the scenario, either through a batch-capable store or a per-issue-only one.
func runPrelinkedPull(t *testing.T, batched bool, build func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error)) (prelinkedOutcome, *recordingTracker, *batchOverlapStore) {
	t.Helper()
	locals, previous, fetched, remotes, fetchErr := build()
	base := newOverlapStore(nil)
	base.issues = locals
	base.previousRefs = previous
	store := &batchOverlapStore{overlapStore: base}
	var engine *Engine
	if batched {
		engine = NewEngine(newRecordingTracker(t, remotes, fetchErr), store, "sync")
	} else {
		// Without the batch method the store is exactly the legacy
		// per-issue capability overlapStore always was.
		engine = NewEngine(newRecordingTracker(t, remotes, fetchErr), base, "sync")
	}
	tracker := engine.Tracker.(*recordingTracker)
	hydrated, localIDs, err := engine.fetchPrelinkedIssues(context.Background(), fetched, locals, &overlapLastSync)
	outcome := prelinkedOutcome{localIDs: localIDs, err: err}
	for _, issue := range hydrated {
		outcome.hydratedIDs = append(outcome.hydratedIDs, issue.Identifier)
	}
	outcome.fetchedIDs = tracker.fetchedIDs
	return outcome, tracker, store
}

func newRecordingTracker(t *testing.T, remotes []TrackerIssue, fetchErr error) *recordingTracker {
	t.Helper()
	mock := newMockTracker("test")
	mock.issues = remotes
	mock.fetchIssueErr = fetchErr
	return &recordingTracker{mockTracker: mock}
}

func parityLocals(refs map[string]string) []*types.Issue {
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	locals := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		locals = append(locals, newBatchOverlapLocal(id, refs[id]))
	}
	return locals
}

// TestFetchPrelinkedIssuesBatchMatchesPerIssue pins the batch path's parity
// contract (AC-2): given identical locals, history, bulk rows and remotes,
// a batch-capable store and a per-issue-only store hydrate the same issues,
// mark the same hydratedLocalIDs, and make the same FetchIssue calls in the
// same order — across the decision shapes the pull actually meets.
func TestFetchPrelinkedIssuesBatchMatchesPerIssue(t *testing.T) {
	remote := func(identifier string, updated time.Duration) TrackerIssue {
		return TrackerIssue{ID: identifier, Identifier: identifier, Title: "remote " + identifier, UpdatedAt: overlapLastSync.Add(updated)}
	}
	for _, test := range []struct {
		name string
		// wantBatch is 1 when the scenario has at least one eligible linked
		// local, so the pull must make its one batch history read.
		wantBatch int
		// build returns locals, their previous refs (nil value = no history
		// entry), the pull's bulk rows, the tracker's remotes, and a
		// FetchIssue error.
		build func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error)
	}{
		{
			name: "unchanged ref is not hydrated", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-0": "https://test.test/EXT-0"}),
					map[string]string{"bd-0": "https://test.test/EXT-0"}, nil, nil, nil
			},
		},
		{
			name: "relinked ref is hydrated", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-1": "https://test.test/EXT-1"}),
					map[string]string{"bd-1": "https://test.test/EXT-OLD"},
					nil, []TrackerIssue{remote("EXT-1", -time.Minute)}, nil
			},
		},
		{
			name: "missing history is hydrated", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-2": "https://test.test/EXT-2"}),
					map[string]string{}, nil, []TrackerIssue{remote("EXT-2", -time.Minute)}, nil
			},
		},
		{
			name: "null historical ref is hydrated", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-3": "https://test.test/EXT-3"}),
					map[string]string{"bd-3": ""}, nil, []TrackerIssue{remote("EXT-3", -time.Minute)}, nil
			},
		},
		{
			name: "whitespace-only ref difference is not a change", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-4": "https://test.test/EXT-4"}),
					map[string]string{"bd-4": "  https://test.test/EXT-4\t"}, nil, nil, nil
			},
		},
		{
			name: "identifier already in the bulk fetch is not re-fetched", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-5": "https://test.test/EXT-5"}),
					map[string]string{"bd-5": "https://test.test/EXT-OLD"},
					[]TrackerIssue{remote("EXT-5", 10*time.Second)}, nil, nil
			},
		},
		{
			name: "overlap-only bulk row marks the local without a fetch", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-6": "https://test.test/EXT-6"}),
					map[string]string{"bd-6": "https://test.test/EXT-OLD"},
					[]TrackerIssue{remote("EXT-6", -3*time.Minute)}, nil, nil
			},
		},
		{
			name: "duplicate identifiers hydrate once", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{
					"bd-7": "https://test.test/EXT-7",
					"bd-8": "https://test.test/EXT-7",
				}), map[string]string{
					"bd-7": "https://test.test/EXT-OLD",
					"bd-8": "https://test.test/EXT-OLD",
				}, nil, []TrackerIssue{remote("EXT-7", -time.Minute)}, nil
			},
		},
		{
			name: "remote that no longer exists is skipped", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-9": "https://test.test/EXT-9"}),
					map[string]string{"bd-9": "https://test.test/EXT-OLD"}, nil, nil, nil
			},
		},
		{
			name: "fetch error aborts the pull", wantBatch: 1,
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return parityLocals(map[string]string{"bd-10": "https://test.test/EXT-10"}),
					map[string]string{"bd-10": "https://test.test/EXT-OLD"},
					nil, []TrackerIssue{remote("EXT-10", -time.Minute)}, errors.New("tracker down")
			},
		},
		{
			name: "no linked locals make no history calls",
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				return nil, map[string]string{}, nil, nil, nil
			},
		},
		{
			name: "nil and blank refs are not candidates",
			build: func() ([]*types.Issue, map[string]string, []TrackerIssue, []TrackerIssue, error) {
				noRef := &types.Issue{ID: "bd-nil", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				return []*types.Issue{noRef, newBatchOverlapLocal("bd-blank", " ")},
					map[string]string{}, nil, nil, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			batchOut, _, batchStore := runPrelinkedPull(t, true, test.build)
			legacyOut, _, _ := runPrelinkedPull(t, false, test.build)

			if (batchOut.err == nil) != (legacyOut.err == nil) {
				t.Fatalf("error mismatch: batch = %v, per-issue = %v", batchOut.err, legacyOut.err)
			}
			if batchOut.err != nil {
				if batchOut.err.Error() != legacyOut.err.Error() {
					t.Fatalf("error text mismatch: batch = %q, per-issue = %q", batchOut.err, legacyOut.err)
				}
				return
			}
			if !slices.Equal(batchOut.hydratedIDs, legacyOut.hydratedIDs) {
				t.Fatalf("hydrated identifiers: batch = %v, per-issue = %v", batchOut.hydratedIDs, legacyOut.hydratedIDs)
			}
			if !reflect.DeepEqual(batchOut.localIDs, legacyOut.localIDs) {
				t.Fatalf("hydratedLocalIDs: batch = %v, per-issue = %v", batchOut.localIDs, legacyOut.localIDs)
			}
			if !slices.Equal(batchOut.fetchedIDs, legacyOut.fetchedIDs) {
				t.Fatalf("FetchIssue calls: batch = %v, per-issue = %v", batchOut.fetchedIDs, legacyOut.fetchedIDs)
			}
			// The parity above only says something if the batched run was
			// actually served by the batch path: exactly one batch call when
			// there are candidates, deciding every one of them without a
			// single per-issue read.
			if batchStore.batchCalls != test.wantBatch {
				t.Fatalf("PreviousExternalRefs calls = %d, want %d", batchStore.batchCalls, test.wantBatch)
			}
			if test.wantBatch == 1 && batchStore.singleCalls != 0 {
				t.Fatalf("PreviousExternalRef calls = %d on the batch path, want 0", batchStore.singleCalls)
			}
		})
	}

	t.Run("nil lastSync reads no history at all", func(t *testing.T) {
		locals := parityLocals(map[string]string{"bd-1": "https://test.test/EXT-1"})
		base := newOverlapStore(nil)
		base.issues = locals
		base.previousRefs = map[string]string{"bd-1": "https://test.test/EXT-OLD"}
		store := &batchOverlapStore{overlapStore: base}
		engine := NewEngine(newRecordingTracker(t, []TrackerIssue{remote("EXT-1", -time.Minute)}, nil), store, "sync")

		hydrated, localIDs, err := engine.fetchPrelinkedIssues(context.Background(), nil, locals, nil)
		if err != nil {
			t.Fatalf("fetchPrelinkedIssues(nil lastSync): %v", err)
		}
		if len(hydrated) != 0 || len(localIDs) != 0 || store.batchCalls != 0 || store.singleCalls != 0 {
			t.Fatalf("nil lastSync must read no history: hydrated=%d localIDs=%v batch=%d single=%d",
				len(hydrated), localIDs, store.batchCalls, store.singleCalls)
		}
	})
}

// failingBatchStore is batch-capable but can fail its batch read and, for the
// fallback-error case, its per-issue reads too.
type failingBatchStore struct {
	*overlapStore
	batchErr    error
	perIssueErr error
	batchCalls  int
	singleCalls int
}

func (s *failingBatchStore) PreviousExternalRef(ctx context.Context, id string, asOf time.Time) (string, bool, error) {
	s.singleCalls++
	if s.perIssueErr != nil {
		return "", false, s.perIssueErr
	}
	return s.overlapStore.PreviousExternalRef(ctx, id, asOf)
}

func (s *failingBatchStore) PreviousExternalRefs(_ context.Context, issueIDs []string, _ time.Time) (map[string]string, error) {
	s.batchCalls++
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	refs := make(map[string]string, len(issueIDs))
	for _, id := range issueIDs {
		if ref, ok := s.previousRefs[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

// TestFetchPrelinkedIssuesBatchErrorFallsBackPerIssue pins the batch path's
// failure contract on a live context (AC-3): a failed batch read costs a
// single warning and the pull continues on the per-issue path, producing the
// same result it always produced — and a per-issue error after the fallback
// still aborts the pull with the existing error.
func TestFetchPrelinkedIssuesBatchErrorFallsBackPerIssue(t *testing.T) {
	remote := func(identifier string) TrackerIssue {
		return TrackerIssue{ID: identifier, Identifier: identifier, Title: "remote " + identifier, UpdatedAt: overlapLastSync.Add(-time.Minute)}
	}
	newScenario := func() ([]*types.Issue, map[string]string, []TrackerIssue) {
		locals := parityLocals(map[string]string{
			"bd-0": "https://test.test/EXT-0",
			"bd-1": "https://test.test/EXT-1",
		})
		return locals, map[string]string{
			"bd-0": "https://test.test/EXT-0", // unchanged
			"bd-1": "https://test.test/EXT-OLD", // relinked: hydrated
		}, []TrackerIssue{remote("EXT-1")}
	}

	t.Run("warns once and returns the per-issue result", func(t *testing.T) {
		locals, previous, remotes := newScenario()
		base := newOverlapStore(nil)
		base.issues = locals
		base.previousRefs = previous
		store := &failingBatchStore{overlapStore: base, batchErr: errors.New("history read blew up")}
		engine := NewEngine(newRecordingTracker(t, remotes, nil), store, "sync")

		hydrated, localIDs, err := engine.fetchPrelinkedIssues(context.Background(), nil, locals, &overlapLastSync)
		if err != nil {
			t.Fatalf("fetchPrelinkedIssues after batch failure: %v", err)
		}
		if len(engine.warnings) != 1 {
			t.Fatalf("warnings = %d (%v), want exactly 1 for the failed batch read", len(engine.warnings), engine.warnings)
		}
		if store.batchCalls != 1 || store.singleCalls != 2 {
			t.Fatalf("batch calls = %d, per-issue calls = %d, want 1 batch attempt then the per-issue loop over both locals", store.batchCalls, store.singleCalls)
		}
		if len(hydrated) != 1 || hydrated[0].Identifier != "EXT-1" || !localIDs["bd-1"] || localIDs["bd-0"] {
			t.Fatalf("hydrated = %+v, localIDs = %v, want only the relinked bd-1 hydrated, as the per-issue path would", hydrated, localIDs)
		}
	})

	t.Run("per-issue error after the fallback still aborts the pull", func(t *testing.T) {
		locals, previous, remotes := newScenario()
		base := newOverlapStore(nil)
		base.issues = locals
		base.previousRefs = previous
		store := &failingBatchStore{
			overlapStore: base,
			batchErr:     errors.New("history read blew up"),
			perIssueErr:  errors.New("per-issue boom"),
		}
		engine := NewEngine(newRecordingTracker(t, remotes, nil), store, "sync")

		_, _, err := engine.fetchPrelinkedIssues(context.Background(), nil, locals, &overlapLastSync)
		if err == nil {
			t.Fatal("expected the per-issue error to abort the pull after the batch fallback")
		}
		if err.Error() != "checking pre-linked local issue bd-0: per-issue boom" {
			t.Fatalf("error = %q, want the existing per-issue error for bd-0", err)
		}
		if len(engine.warnings) != 1 {
			t.Fatalf("warnings = %d, want the single batch-failure warning before the abort", len(engine.warnings))
		}
	})
}

// TestFetchPrelinkedIssuesBatchCancelledContext pins the batch path's
// cancellation contract (AC-4): when the caller's context is done, a failed
// batch read aborts the pull with an error that wraps the caller's own
// context error — even when the store reported a different failure — and no
// per-issue fallback runs.
func TestFetchPrelinkedIssuesBatchCancelledContext(t *testing.T) {
	locals := parityLocals(map[string]string{"bd-1": "https://test.test/EXT-1"})
	storageErr := errors.New("i/o timeout")
	for _, test := range []struct {
		name    string
		ctxDone func(t *testing.T) context.Context
	}{
		{
			name: "cancelled context, store error differs",
			ctxDone: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		{
			name: "deadline exceeded, store error differs",
			ctxDone: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				return ctx
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := newOverlapStore(nil)
			base.issues = locals
			base.previousRefs = map[string]string{"bd-1": "https://test.test/EXT-OLD"}
			store := &failingBatchStore{overlapStore: base, batchErr: storageErr}
			engine := NewEngine(newRecordingTracker(t, nil, nil), store, "sync")
			ctx := test.ctxDone(t)

			_, _, err := engine.fetchPrelinkedIssues(ctx, nil, locals, &overlapLastSync)
			if err == nil {
				t.Fatal("expected the failed batch read on a done context to abort the pull")
			}
			if !strings.HasPrefix(err.Error(), "checking pre-linked local issues: ") {
				t.Fatalf("error = %q, want the checking pre-linked local issues prefix", err)
			}
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("error = %v, want errors.Is(err, ctx.Err()) with ctx.Err() = %v", err, ctx.Err())
			}
			if store.batchCalls != 1 || store.singleCalls != 0 {
				t.Fatalf("batch calls = %d, per-issue calls = %d, want 1 batch attempt and no per-issue fallback", store.batchCalls, store.singleCalls)
			}
		})
	}
}
