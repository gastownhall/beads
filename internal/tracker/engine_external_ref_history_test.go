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
	"fmt"
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

// prelinkedHistoryStore answers the per-issue external_ref history read for
// fetchPrelinkedIssues tests from a fixed map: an id absent from previous has
// no history at the cutoff.
type prelinkedHistoryStore struct {
	storage.Storage

	previous    map[string]string
	singleErr   map[string]error
	singleCalls []string
}

func (s *prelinkedHistoryStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	s.singleCalls = append(s.singleCalls, id)
	if err := s.singleErr[id]; err != nil {
		return "", false, err
	}
	ref, ok := s.previous[id]
	return ref, ok, nil
}

// prelinkedBatchHistoryStore adds the batch read over the same map. The
// method is declared here without naming the engine's batch interface, so
// the fake describes a store's shape rather than the engine's expectations.
type prelinkedBatchHistoryStore struct {
	*prelinkedHistoryStore

	batchCalls   [][]string
	batchErr     error
	batchPartial map[string]string // returned alongside batchErr
	onBatch      func()
}

func (s *prelinkedBatchHistoryStore) PreviousExternalRefs(_ context.Context, ids []string, _ time.Time) (map[string]string, error) {
	s.batchCalls = append(s.batchCalls, append([]string(nil), ids...))
	if s.onBatch != nil {
		s.onBatch()
	}
	if s.batchErr != nil {
		return s.batchPartial, s.batchErr
	}
	refs := make(map[string]string)
	for _, id := range ids {
		if ref, ok := s.previous[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

// prelinkTracker records every FetchIssue identifier in call order.
type prelinkTracker struct {
	*mockExternalRefTracker
	fetched   []string
	failFetch map[string]error
}

func (p *prelinkTracker) FetchIssue(ctx context.Context, identifier string) (*TrackerIssue, error) {
	p.fetched = append(p.fetched, identifier)
	if err := p.failFetch[identifier]; err != nil {
		return nil, err
	}
	return p.mockExternalRefTracker.FetchIssue(ctx, identifier)
}

var prelinkLastSync = time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC)

const prelinkRefPrefix = "https://prelink.test/"

func prelinkRef(identifier string) string { return prelinkRefPrefix + identifier }

func newPrelinkTracker(remote ...string) *prelinkTracker {
	mock := newMockTracker("prelink")
	for _, identifier := range remote {
		mock.issues = append(mock.issues, TrackerIssue{ID: identifier, Identifier: identifier, Title: identifier})
	}
	return &prelinkTracker{mockExternalRefTracker: &mockExternalRefTracker{
		mockTracker: mock,
		isRef:       func(ref string) bool { return strings.HasPrefix(ref, prelinkRefPrefix) },
	}}
}

func prelinkLocal(id string, ref *string) *types.Issue {
	return &types.Issue{ID: id, ExternalRef: ref, CreatedAt: prelinkLastSync.Add(-time.Hour), UpdatedAt: prelinkLastSync.Add(-time.Hour)}
}

// prelinkScenario is one pull's pre-linked locals, the stored history at
// last_sync and the rows the bulk fetch returned, covering every way
// fetchPrelinkedIssues can decide a local.
type prelinkScenario struct {
	locals   []*types.Issue
	previous map[string]string
	fetched  []TrackerIssue
	remote   []string
	eligible []string // the locals whose history the pull must read, in order
}

func newPrelinkScenario() prelinkScenario {
	return prelinkScenario{
		locals: []*types.Issue{
			nil,
			prelinkLocal("bd-noref", nil),
			prelinkLocal("bd-emptyref", strPtr("")),
			prelinkLocal("bd-blank", strPtr("   ")),
			prelinkLocal("bd-foreign", strPtr("https://other.test/EXT-FOREIGN")),
			prelinkLocal("bd-same", strPtr(prelinkRef("EXT-SAME"))),
			prelinkLocal("bd-trim", strPtr(" "+prelinkRef("EXT-TRIM")+" ")),
			prelinkLocal("bd-changed", strPtr(prelinkRef("EXT-CHANGED"))),
			prelinkLocal("bd-missing", strPtr(prelinkRef("EXT-MISSING"))),
			prelinkLocal("bd-null", strPtr(prelinkRef("EXT-NULL"))),
			prelinkLocal("bd-seen", strPtr(prelinkRef("EXT-SEEN"))),
			prelinkLocal("bd-overlap", strPtr(prelinkRef("EXT-OVERLAP"))),
			prelinkLocal("bd-dup-a", strPtr(prelinkRef("EXT-DUP"))),
			prelinkLocal("bd-dup-b", strPtr(prelinkRef("ext-dup"))),
			prelinkLocal("bd-gone", strPtr(prelinkRef("EXT-GONE"))),
			prelinkLocal("bd-noident", strPtr(prelinkRefPrefix)),
		},
		previous: map[string]string{
			"bd-same":    prelinkRef("EXT-SAME"),
			"bd-trim":    prelinkRef("EXT-TRIM") + "  ",
			"bd-changed": prelinkRef("EXT-OLD"),
			"bd-null":    "",
		},
		fetched: []TrackerIssue{
			{ID: "EXT-SEEN", Identifier: "EXT-SEEN", UpdatedAt: prelinkLastSync.Add(time.Minute)},
			{ID: "EXT-OVERLAP", Identifier: "EXT-OVERLAP", UpdatedAt: prelinkLastSync.Add(-time.Minute)},
		},
		remote: []string{"EXT-SAME", "EXT-TRIM", "EXT-CHANGED", "EXT-MISSING", "EXT-NULL", "EXT-SEEN", "EXT-OVERLAP", "EXT-DUP"},
		eligible: []string{
			"bd-same", "bd-trim", "bd-changed", "bd-missing", "bd-null", "bd-seen",
			"bd-overlap", "bd-dup-a", "bd-dup-b", "bd-gone", "bd-noident",
		},
	}
}

// prelinkOutcome is everything fetchPrelinkedIssues decided for one pull.
type prelinkOutcome struct {
	hydrated []string
	localIDs []string
	fetched  []string
	err      error
	warnings []string
}

func runPrelinked(ctx context.Context, sc prelinkScenario, store Store, tracker *prelinkTracker, lastSync *time.Time) prelinkOutcome {
	e := &Engine{Tracker: tracker, Store: store}
	hydrated, localIDs, err := e.fetchPrelinkedIssues(ctx, sc.fetched, sc.locals, lastSync)
	out := prelinkOutcome{fetched: tracker.fetched, err: err, warnings: e.warnings}
	for _, issue := range hydrated {
		out.hydrated = append(out.hydrated, issue.Identifier)
	}
	for id := range localIDs {
		out.localIDs = append(out.localIDs, id)
	}
	sort.Strings(out.localIDs)
	return out
}

func (sc prelinkScenario) stores() (*prelinkedHistoryStore, *prelinkedBatchHistoryStore) {
	legacy := &prelinkedHistoryStore{previous: sc.previous}
	batch := &prelinkedBatchHistoryStore{prelinkedHistoryStore: &prelinkedHistoryStore{previous: sc.previous}}
	return legacy, batch
}

// TestFetchPrelinkedIssuesMakesOneBatchCall covers AC-1: a store that can
// read many issues' history at once gets one call per pull, naming every
// eligible local, and the per-issue read is never used.
func TestFetchPrelinkedIssuesMakesOneBatchCall(t *testing.T) {
	ctx := context.Background()
	lastSync := prelinkLastSync
	sc := prelinkScenario{
		locals: []*types.Issue{
			prelinkLocal("bd-1", strPtr(prelinkRef("EXT-1"))),
			prelinkLocal("bd-2", strPtr(prelinkRef("EXT-2"))),
			prelinkLocal("bd-3", strPtr(prelinkRef("EXT-3"))),
		},
		previous: map[string]string{"bd-1": prelinkRef("EXT-1")},
		remote:   []string{"EXT-2", "EXT-3"},
	}
	_, store := sc.stores()
	tracker := newPrelinkTracker(sc.remote...)

	got := runPrelinked(ctx, sc, store, tracker, &lastSync)
	if got.err != nil {
		t.Fatalf("fetchPrelinkedIssues: %v", got.err)
	}
	if len(store.batchCalls) != 1 || fmt.Sprint(store.batchCalls[0]) != fmt.Sprint([]string{"bd-1", "bd-2", "bd-3"}) {
		t.Fatalf("PreviousExternalRefs calls = %q, want one call with [bd-1 bd-2 bd-3]", store.batchCalls)
	}
	if len(store.singleCalls) != 0 {
		t.Fatalf("PreviousExternalRef calls = %q, want none once the batch answered", store.singleCalls)
	}
	if fmt.Sprint(got.hydrated) != "[EXT-2 EXT-3]" {
		t.Fatalf("hydrated = %v, want [EXT-2 EXT-3]", got.hydrated)
	}
}

// TestFetchPrelinkedIssuesSkipsBatchWithNothingToCheck: no cursor or no
// eligible local means no history read at all.
func TestFetchPrelinkedIssuesSkipsBatchWithNothingToCheck(t *testing.T) {
	ctx := context.Background()
	lastSync := prelinkLastSync
	for _, test := range []struct {
		name     string
		locals   []*types.Issue
		lastSync *time.Time
	}{
		{"nil last_sync", newPrelinkScenario().locals, nil},
		{"no eligible locals", []*types.Issue{nil, prelinkLocal("bd-noref", nil), prelinkLocal("bd-foreign", strPtr("https://other.test/X"))}, &lastSync},
	} {
		t.Run(test.name, func(t *testing.T) {
			sc := prelinkScenario{locals: test.locals}
			_, store := sc.stores()
			got := runPrelinked(ctx, sc, store, newPrelinkTracker(), test.lastSync)
			if got.err != nil || len(store.batchCalls) != 0 || len(store.singleCalls) != 0 {
				t.Fatalf("err = %v, batch calls = %q, single calls = %q; want no history read", got.err, store.batchCalls, store.singleCalls)
			}
		})
	}
}
