// Regression tests for #4549: prelinkVerdicts' fast path must be gated on
// the storage.ExternalRefHistoryQuerier capability, not on whether the store
// happens to expose a raw *sql.DB. See engine.go's externalRefHistoryQuerier
// and prelinkVerdicts for the rationale.
//
// This file MUST NOT carry a `//go:build cgo` tag (see test_helpers_pure_test.go):
// the mocks below are pure Go and exercise the capability-detection logic
// directly, without a real Dolt store.

package tracker

import (
	"context"
	"errors"
	"slices"
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

func (h *historyQuerierStore) PreviousExternalRefs(_ context.Context, ids []string, _ time.Time) (map[string]string, error) {
	h.calls++
	return uniformHistory(ids, h.prevRef, h.prevFound, h.prevErr)
}

// uniformHistory answers a batch history request with the same (ref, found)
// for every id, or err.
func uniformHistory(ids []string, ref string, found bool, err error) (map[string]string, error) {
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	if found {
		for _, id := range ids {
			refs[id] = ref
		}
	}
	return refs, nil
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

func (d *doltLikeStore) PreviousExternalRefs(_ context.Context, ids []string, _ time.Time) (map[string]string, error) {
	d.calls++
	return uniformHistory(ids, d.prevRef, d.prevFound, nil)
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

// prelinkVerdictFor decides one local the way a pull whose only linked local
// is local would: through prelinkVerdicts, with a single candidate.
func (e *Engine) prelinkVerdictFor(ctx context.Context, local *types.Issue, currentRef string, lastSync time.Time) (changed, historyUnknown bool, err error) {
	verdicts, err := e.prelinkVerdicts(ctx, []prelinkCandidate{{local: local, ref: currentRef}}, lastSync)
	if err != nil {
		return false, false, err
	}
	return verdicts[0].changed, verdicts[0].historyUnknown, nil
}

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
	// does not directly satisfy ExternalRefHistoryQuerier (PreviousExternalRefs
	// is not part of storage.DoltStorage, so it isn't promoted).
	if _, ok := dec.(storage.ExternalRefHistoryQuerier); ok {
		t.Fatal("test setup invalid: decorator should not directly expose PreviousExternalRefs")
	}

	q, ok := externalRefHistoryQuerier(dec)
	if !ok {
		t.Fatal("expected decorator wrapping a history-capable store to be detected via UnwrapStore")
	}
	refs, err := q.PreviousExternalRefs(context.Background(), []string{"bd-1"}, time.Now())
	if ref, found := refs["bd-1"]; err != nil || !found || ref != "ref-1" {
		t.Fatalf("PreviousExternalRefs via unwrapped decorator = (%q, %v, %v), want (\"ref-1\", true, nil)", ref, found, err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected inner store to be called once, got %d", inner.calls)
	}
}

func TestEngineExternalRefChangedAfter_NilLocalIssue(t *testing.T) {
	e := &Engine{Store: newPureTestStore()}
	changed, _, err := e.prelinkVerdictFor(context.Background(), nil, "ref", time.Now())
	if err != nil || changed {
		t.Fatalf("externalRefChangedAfter(nil local) = (%v, %v), want (false, nil)", changed, err)
	}
}

func TestEngineExternalRefChangedAfter_FastPathSameRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, _, err := e.prelinkVerdictFor(context.Background(), local, "https://tracker.test/EXT-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false when previous ref matches current ref")
	}
	if querier.calls != 1 {
		t.Fatalf("expected fast path to call PreviousExternalRefs once, got %d calls", querier.calls)
	}
}

func TestEngineExternalRefChangedAfter_FastPathDifferentRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "https://tracker.test/EXT-OLD", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, _, err := e.prelinkVerdictFor(context.Background(), local, "https://tracker.test/EXT-NEW", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when previous ref differs from current ref")
	}
	if querier.calls != 1 {
		t.Fatalf("expected fast path to call PreviousExternalRefs once, got %d calls", querier.calls)
	}
}

// TestEngineExternalRefChangedAfter_FastPathNotFound pins the missing-row
// decision (#7294): no history entry at or before lastSync reports changed
// only for an issue created at or after lastSync (a new pre-linked stub to
// hydrate). created_at is stored rounded to a whole second and last_sync is
// one, so a stub created within half a second after the stamp reads as
// created exactly at lastSync. An older issue, or one with no creation
// time, reports historyUnknown instead, whatever its UpdatedAt says: the
// row may be missing because history was compacted or rewritten, and a
// local edit after lastSync is what the pull's guards must keep.
func TestEngineExternalRefChangedAfter_FastPathNotFound(t *testing.T) {
	lastSync := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	plus5 := time.FixedZone("UTC+5", 5*60*60)
	for _, test := range []struct {
		name               string
		created, updated   time.Time
		wantChanged        bool
		wantHistoryUnknown bool
	}{
		{name: "old unchanged", created: lastSync.Add(-24 * time.Hour), updated: lastSync.Add(-time.Hour), wantHistoryUnknown: true},
		{name: "old locally edited", created: lastSync.Add(-24 * time.Hour), updated: lastSync.Add(time.Hour), wantHistoryUnknown: true},
		{name: "created a second before lastSync", created: lastSync.Add(-time.Second), updated: lastSync.Add(time.Hour), wantHistoryUnknown: true},
		{name: "newly created", created: lastSync.Add(time.Hour), updated: lastSync.Add(time.Hour), wantChanged: true},
		{name: "created at lastSync (rounded to the stamp)", created: lastSync, updated: lastSync, wantChanged: true},
		{name: "zero creation time", updated: lastSync.Add(time.Hour), wantHistoryUnknown: true},
		{name: "created at lastSync in another zone", created: lastSync.In(plus5), updated: lastSync.Add(time.Hour).In(plus5), wantChanged: true},
		{name: "created after lastSync in another zone", created: lastSync.Add(time.Second).In(plus5), updated: lastSync.Add(time.Second).In(plus5), wantChanged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			querier := &historyQuerierStore{prevFound: false}
			e := &Engine{Store: querier}
			local := newTestIssue("bd-1", test.created, test.updated)

			changed, historyUnknown, err := e.prelinkVerdictFor(context.Background(), local, "https://tracker.test/EXT-1", lastSync)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if changed != test.wantChanged || historyUnknown != test.wantHistoryUnknown {
				t.Errorf("externalRefChangedAfter = (changed %v, historyUnknown %v), want (%v, %v)",
					changed, historyUnknown, test.wantChanged, test.wantHistoryUnknown)
			}
			if querier.calls != 1 {
				t.Errorf("PreviousExternalRefs calls = %d, want 1", querier.calls)
			}
		})
	}
}

// TestEngineExternalRefChangedAfter_KnownAnswersReportHistoryKnown pins that
// every case other than a missing history row keeps its prior answer and
// never reports historyUnknown: exact history (same, different and NULL
// refs), a propagated error, a decorated store and the no-capability
// heuristic.
func TestEngineExternalRefChangedAfter_KnownAnswersReportHistoryKnown(t *testing.T) {
	lastSync := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	old := newTestIssue("bd-1", lastSync.Add(-24*time.Hour), lastSync.Add(time.Hour))
	boom := errors.New("boom")
	inner := &doltLikeStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}
	decorated := &fakeStoreDecorator{DoltStorage: inner, inner: inner}
	for _, test := range []struct {
		name        string
		store       Store
		local       *types.Issue
		currentRef  string
		wantChanged bool
		wantErr     error
	}{
		{name: "nil local", store: &historyQuerierStore{}, currentRef: "ref"},
		{name: "same historical ref", store: &historyQuerierStore{prevRef: "https://tracker.test/EXT-1", prevFound: true}, local: old, currentRef: "https://tracker.test/EXT-1"},
		{name: "different historical ref", store: &historyQuerierStore{prevRef: "https://tracker.test/EXT-OLD", prevFound: true}, local: old, currentRef: "https://tracker.test/EXT-1", wantChanged: true},
		{name: "NULL historical ref", store: &historyQuerierStore{prevRef: "", prevFound: true}, local: old, currentRef: "https://tracker.test/EXT-1", wantChanged: true},
		{name: "history error", store: &historyQuerierStore{prevErr: boom}, local: old, currentRef: "ref", wantErr: boom},
		{name: "decorated store", store: decorated, local: old, currentRef: "https://tracker.test/EXT-1"},
		{name: "no capability, touched after lastSync", store: newPureTestStore(), local: old, currentRef: "ref", wantChanged: true},
		{name: "no capability, untouched", store: newPureTestStore(), local: newTestIssue("bd-2", lastSync.Add(-2*time.Hour), lastSync.Add(-time.Hour)), currentRef: "ref"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := &Engine{Store: test.store}
			changed, historyUnknown, err := e.prelinkVerdictFor(context.Background(), test.local, test.currentRef, lastSync)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if changed != test.wantChanged || historyUnknown {
				t.Errorf("externalRefChangedAfter = (changed %v, historyUnknown %v), want (%v, false)", changed, historyUnknown, test.wantChanged)
			}
		})
	}
}

// TestPrelinkDecision pins the pure pre-link decision that every path
// reading external_ref history must share (#7294): a history row decides by
// comparing refs, and a missing row hydrates only an issue created at or
// after lastSync. An older issue without a row reports historyUnknown,
// unless it is on the wisps plane, which never has history.
func TestPrelinkDecision(t *testing.T) {
	lastSync := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	old := newTestIssue("bd-old", lastSync.Add(-24*time.Hour), lastSync.Add(time.Hour))
	atStamp := newTestIssue("bd-stamp", lastSync, lastSync)
	ephemeral := func(issue *types.Issue) *types.Issue {
		wisp := *issue
		wisp.Ephemeral = true
		return &wisp
	}
	noHistory := func(issue *types.Issue) *types.Issue {
		bead := *issue
		bead.NoHistory = true
		return &bead
	}
	const ref = "https://tracker.test/EXT-1"
	for _, test := range []struct {
		name               string
		local              *types.Issue
		previousRef        string
		found              bool
		wantChanged        bool
		wantHistoryUnknown bool
	}{
		{name: "same ref at lastSync", local: old, previousRef: ref, found: true},
		{name: "same ref modulo spaces", local: old, previousRef: " " + ref + " ", found: true},
		{name: "other ref at lastSync", local: old, previousRef: "https://tracker.test/EXT-OLD", found: true, wantChanged: true},
		{name: "NULL ref at lastSync", local: old, found: true, wantChanged: true},
		{name: "no row, created at lastSync", local: atStamp, wantChanged: true},
		{name: "no row, created before lastSync", local: old, wantHistoryUnknown: true},
		{name: "no row, no creation time", local: newTestIssue("bd-zero", time.Time{}, lastSync.Add(time.Hour)), wantHistoryUnknown: true},
		{name: "no row, ephemeral, created before lastSync", local: ephemeral(old)},
		{name: "no row, no-history, created before lastSync", local: noHistory(old)},
		{name: "no row, ephemeral, created at lastSync", local: ephemeral(atStamp), wantChanged: true},
		{name: "no row, no-history, created at lastSync", local: noHistory(atStamp), wantChanged: true},
		// A bead moved to the wisps plane after lastSync keeps its history.
		{name: "no-history, other ref at lastSync", local: noHistory(old), previousRef: "https://tracker.test/EXT-OLD", found: true, wantChanged: true},
		{name: "nil local", previousRef: ref},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, historyUnknown := prelinkDecision(test.local, ref, test.previousRef, test.found, lastSync)
			if changed != test.wantChanged || historyUnknown != test.wantHistoryUnknown {
				t.Errorf("prelinkDecision = (changed %v, historyUnknown %v), want (%v, %v)",
					changed, historyUnknown, test.wantChanged, test.wantHistoryUnknown)
			}
		})
	}
}

// Locks the NULL-historical contract: a NULL external_ref column surfaces
// from PreviousExternalRefsInTx as a present "" (see
// issueops.PreviousExternalRefsInTx, internal/storage/issueops),
// not as "not found". The fast path must therefore compare it against
// currentRef like any other value, rather than treating it as an
// unconditional "changed" the way the old inline !previousRef.Valid check did.
func TestEngineExternalRefChangedAfter_FastPathNullHistoricalRef(t *testing.T) {
	querier := &historyQuerierStore{prevRef: "", prevFound: true}
	e := &Engine{Store: querier}
	local := newTestIssue("bd-1", time.Now(), time.Now())

	changed, _, err := e.prelinkVerdictFor(context.Background(), local, "", time.Now())
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

	_, _, err := e.prelinkVerdictFor(context.Background(), local, "ref", time.Now())
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

	changed, _, err := e.prelinkVerdictFor(context.Background(), local, "https://tracker.test/EXT-1", lastSync)
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
		changed, _, err := e.prelinkVerdictFor(context.Background(), local, "ref", lastSync)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true when UpdatedAt is after lastSync (heuristic fallback)")
		}
	})

	t.Run("untouched since lastSync reports unchanged", func(t *testing.T) {
		local := newTestIssue("bd-2", lastSync.Add(-2*time.Hour), lastSync.Add(-time.Hour))
		changed, _, err := e.prelinkVerdictFor(context.Background(), local, "ref", lastSync)
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

	changed, _, err := e.prelinkVerdictFor(context.Background(), local, "https://tracker.test/EXT-1", time.Now())
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

// countingHistoryStore answers external_ref history from previousRefs through
// both the batch method and a single-id PreviousExternalRef (the per-issue
// read GH#6605 replaced), and records every call, so a test can tell which
// one a pull used.
type countingHistoryStore struct {
	*missingHistoryStore
	singleCalls int
	batchCalls  [][]string
	batchAsOf   []time.Time
	// failID, when set, makes any history read that covers it fail with
	// failErr.
	failID  string
	failErr error
}

var _ ExternalRefHistoryStore = (*countingHistoryStore)(nil)

func (s *countingHistoryStore) PreviousExternalRef(_ context.Context, id string, _ time.Time) (string, bool, error) {
	s.singleCalls++
	if s.failID != "" && id == s.failID {
		return "", false, s.failErr
	}
	ref, ok := s.previousRefs[id]
	return ref, ok, nil
}

func (s *countingHistoryStore) PreviousExternalRefs(_ context.Context, ids []string, asOf time.Time) (map[string]string, error) {
	s.batchCalls = append(s.batchCalls, append([]string(nil), ids...))
	s.batchAsOf = append(s.batchAsOf, asOf)
	if s.failID != "" && slices.Contains(ids, s.failID) {
		return nil, s.failErr
	}
	refs := make(map[string]string)
	for _, id := range ids {
		if ref, ok := s.previousRefs[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

// TestEnginePullAsksExternalRefHistoryOncePerPull is the GH#6605 regression:
// an incremental pull asks a history-capable store about all its linked
// locals in one batch at lastSync, never once per linked issue, and asks
// nothing when no local is linked or there is no lastSync.
func TestEnginePullAsksExternalRefHistoryOncePerPull(t *testing.T) {
	old := overlapLastSync.Add(-24 * time.Hour)
	before := overlapLastSync.Add(-time.Hour)
	unlinked := func(id string) *types.Issue {
		issue := linkedIssue(id, "", "unlinked", old, before)
		issue.ExternalRef = nil
		return issue
	}
	blank := func(id string) *types.Issue {
		issue := linkedIssue(id, "", "blank ref", old, before)
		ref := "   "
		issue.ExternalRef = &ref
		return issue
	}

	for _, test := range []struct {
		name        string
		locals      []*types.Issue
		noLastSync  bool
		wantBatchID []string
	}{
		{
			name: "linked and unrelated locals",
			locals: []*types.Issue{
				linkedIssue("bd-a", "EXT-1", "a", old, before),
				unlinked("bd-u"),
				linkedIssue("bd-b", "EXT-2", "b", old, before),
				blank("bd-blank"),
				linkedIssue("bd-c", "EXT-3", "c", old, before),
			},
			wantBatchID: []string{"bd-a", "bd-b", "bd-c"},
		},
		{
			name:   "no linked locals",
			locals: []*types.Issue{unlinked("bd-u"), blank("bd-blank")},
		},
		{
			name:       "no lastSync",
			locals:     []*types.Issue{linkedIssue("bd-a", "EXT-1", "a", old, before)},
			noLastSync: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &countingHistoryStore{missingHistoryStore: newMissingHistoryStore(test.locals...)}
			if test.noLastSync {
				delete(store.localMetadata, "test.last_sync")
			}
			for _, local := range test.locals {
				if local.ExternalRef != nil {
					store.previousRefs[local.ID] = *local.ExternalRef
				}
			}
			mock, _ := newOverlapTracker()

			if _, err := NewEngine(mock, store, "sync").Sync(context.Background(), SyncOptions{Pull: true, DryRun: true}); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if store.singleCalls != 0 {
				t.Errorf("single-id history calls = %d, want 0", store.singleCalls)
			}
			if test.wantBatchID == nil {
				if len(store.batchCalls) != 0 {
					t.Errorf("batch history calls = %v, want none", store.batchCalls)
				}
				return
			}
			if len(store.batchCalls) != 1 {
				t.Fatalf("batch history calls = %d (%v), want exactly 1", len(store.batchCalls), store.batchCalls)
			}
			got := append([]string(nil), store.batchCalls[0]...)
			slices.Sort(got)
			if !slices.Equal(got, test.wantBatchID) {
				t.Errorf("batch ids = %v, want %v", got, test.wantBatchID)
			}
			if !store.batchAsOf[0].Equal(overlapLastSync) {
				t.Errorf("batch asOf = %v, want lastSync %v", store.batchAsOf[0], overlapLastSync)
			}
			if mock.fetchCalls != 0 {
				t.Errorf("FetchIssue calls = %d, want 0: every link was unchanged", mock.fetchCalls)
			}
		})
	}
}

// TestEnginePullPrelinkHydrationKeepsDecisionsAndAbortsOnHistoryError pins
// what the batch read must keep from the per-issue loop: duplicate local rows
// cost one id and one hydration, a store without the history capability
// still decides by timestamps, and a history error fails the pull before it
// fetches any remote, never falling back to the heuristic for some links.
func TestEnginePullPrelinkHydrationKeepsDecisionsAndAbortsOnHistoryError(t *testing.T) {
	old := overlapLastSync.Add(-24 * time.Hour)
	before := overlapLastSync.Add(-time.Hour)
	after := overlapLastSync.Add(time.Hour)
	ctx := context.Background()
	// remote is a row the bulk fetch from lastSync misses, so only a
	// FetchIssue can hydrate a link to it.
	remote := TrackerIssue{ID: "EXT-1", Identifier: "EXT-1", Title: "remote", UpdatedAt: old}

	t.Run("history error aborts before any FetchIssue", func(t *testing.T) {
		// bd-a was relinked after lastSync, so it would be hydrated; bd-b's
		// history read fails. Asking per issue fetched bd-a's remote first.
		relinked := linkedIssue("bd-a", "EXT-1", "a", old, before)
		failing := linkedIssue("bd-b", "EXT-2", "b", old, before)
		store := &countingHistoryStore{missingHistoryStore: newMissingHistoryStore(relinked, failing)}
		store.previousRefs["bd-a"] = "https://test.test/EXT-OLD"
		store.previousRefs["bd-b"] = *failing.ExternalRef
		cause := errors.New("history unavailable")
		store.failID, store.failErr = "bd-b", cause
		mock, _ := newOverlapTracker(remote)

		_, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: true})
		if !errors.Is(err, cause) {
			t.Fatalf("Sync error = %v, want it to wrap %v", err, cause)
		}
		if mock.fetchCalls != 0 {
			t.Errorf("FetchIssue calls = %d, want 0: a history error must stop hydration before it starts", mock.fetchCalls)
		}
	})

	t.Run("duplicate local rows ask once and hydrate once", func(t *testing.T) {
		relinked := linkedIssue("bd-a", "EXT-1", "a", old, before)
		store := &countingHistoryStore{missingHistoryStore: newMissingHistoryStore(relinked, relinked)}
		store.previousRefs["bd-a"] = "https://test.test/EXT-OLD"
		mock, _ := newOverlapTracker(remote)

		if _, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: true}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if len(store.batchCalls) != 1 || !slices.Equal(store.batchCalls[0], []string{"bd-a"}) {
			t.Errorf("batch history calls = %v, want one call for [bd-a]", store.batchCalls)
		}
		if mock.fetchCalls != 1 {
			t.Errorf("FetchIssue calls = %d, want 1: both rows name the same relinked remote", mock.fetchCalls)
		}
	})

	t.Run("no history capability decides by timestamps", func(t *testing.T) {
		edited := linkedIssue("bd-a", "EXT-1", "a", old, after)
		quiet := linkedIssue("bd-b", "EXT-2", "b", old, before)
		// Only the Store methods are promoted, which hides the history
		// capability the inner store has.
		store := struct{ Store }{newMissingHistoryStore(edited, quiet)}
		if _, ok := externalRefHistoryQuerier(store); ok {
			t.Fatal("store unexpectedly has the history capability")
		}
		mock, _ := newOverlapTracker(remote)

		if _, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true, DryRun: true}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if mock.fetchCalls != 1 {
			t.Errorf("FetchIssue calls = %d, want 1: only the local updated after lastSync", mock.fetchCalls)
		}
	})
}
