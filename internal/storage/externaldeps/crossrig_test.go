package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// GH#5784: a bare cross-prefix blocking target (a source in one rig naming an
// issue ID another rig owns, stored in depends_on_external) blocks readiness
// exactly like an explicit external ref — unless a settled row is known for
// it. The tests in this file pin that policy on every composition that owns
// it: the direct store decorator, the unit-of-work wrapper, and the remote
// role composition.

// xrigTarget is a bare ID whose prefix belongs to another rig.
const xrigTarget = "other-200"

// fakeResolver is a ForeignIssueResolver harness: it records every batch it
// is asked about and answers from a fixed table; an id with no entry models a
// target the resolver could not answer.
type fakeResolver struct {
	results map[string]*ForeignIssueResult
	calls   [][]string
	err     error
}

func (f *fakeResolver) ResolveForeignIssues(_ context.Context, ids []string) (map[string]*ForeignIssueResult, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]*ForeignIssueResult, len(ids))
	for _, id := range ids {
		if result, ok := f.results[id]; ok {
			out[id] = result
		}
	}
	return out, nil
}

// crossRigStore wires a bare-target resolver into the standard test store.
// The foreign-project locator stays unconfigured: bare targets never go
// through it, only explicit external: refs do.
func crossRigStore(raw storage.DoltStorage, resolver ForeignIssueResolver) *Store {
	store := testStore(raw, &fakeStore{}, false)
	store.resolveForeign = resolver
	return store
}

// captureCrossRigWarnings swaps in a recording warning hook and returns it.
func captureCrossRigWarnings(store *Store) func() []string {
	var warned []string
	store.warnCrossRig = func(target, reason string) {
		warned = append(warned, target+" ("+reason+")")
	}
	return func() []string { return warned }
}

// TestGetReadyWorkGatesBareCrossRigEdgesByType pins which dependency types a
// bare cross-prefix edge gates readiness with: blocks and conditional-blocks
// block (they are the ready-affecting pair); every other type — waits-for
// above all — leaves the source offered, exactly as a local edge of that type
// does. A bare target in this store's own prefix is the raw store's own
// blocking machinery, never this policy's: no foreign read, no exclusion.
func TestGetReadyWorkGatesBareCrossRigEdgesByType(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		depType  types.DependencyType
		wantRead []string
		wantAsk  bool
	}{
		{name: "blocks", target: xrigTarget, depType: types.DepBlocks, wantRead: []string{"be-b"}, wantAsk: true},
		{name: "conditional-blocks", target: "other-201", depType: types.DepConditionalBlocks, wantRead: []string{"be-b"}, wantAsk: true},
		{name: "waits-for", target: "other-202", depType: types.DepWaitsFor, wantRead: []string{"be-a", "be-b"}},
		{name: "tracks", target: "other-203", depType: types.DepTracks, wantRead: []string{"be-a", "be-b"}},
		{name: "parent-child", target: "other-204", depType: types.DepParentChild, wantRead: []string{"be-a", "be-b"}},
		{name: "same-prefix bare target", target: "be-z", depType: types.DepBlocks, wantRead: []string{"be-a", "be-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := issue("be-a"), issue("be-b")
			raw := &fakeStore{
				ready: []*types.Issue{a, b},
				deps: map[string][]*types.Dependency{
					a.ID: {externalDep(a.ID, tc.target, tc.depType)},
				},
			}
			resolver := &fakeResolver{}
			store := crossRigStore(raw, resolver)

			got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(got); !slices.Equal(ids, tc.wantRead) {
				t.Fatalf("ready IDs = %v, want %v", ids, tc.wantRead)
			}
			asked := len(resolver.calls) > 0
			if asked != tc.wantAsk {
				t.Fatalf("resolver asked about %v; want asked = %v (only bare cross-prefix blocks/conditional-blocks edges may be)", resolver.calls, tc.wantAsk)
			}
		})
	}
}

// TestGetReadyWorkFailsClosedForUnresolvedBareCrossRigTarget: with no
// resolver wired (the plain New constructor, an older embedding), an
// unresolved bare target stays blocking and is warned about — the same
// fail-closed contract an unconfigured external project has.
func TestGetReadyWorkFailsClosedForUnresolvedBareCrossRigTarget(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, xrigTarget, types.DepBlocks)},
			b.ID: {externalDep(b.ID, xrigTarget, types.DepBlocks)},
		},
	}
	store := crossRigStore(raw, nil)
	warned := captureCrossRigWarnings(store)

	got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ready = %v, want no issues: %s blocks both", issueIDs(got), xrigTarget)
	}
	// One warning per target per evaluation, however many sources name it.
	if len(warned()) != 1 || warned()[0] != xrigTarget+" (no cross-rig resolver is configured)" {
		t.Fatalf("warnings = %v, want one for %s", warned(), xrigTarget)
	}
}

// TestGetReadyWorkAnswersBareCrossRigTargetFromForeignState: the resolver's
// answer for the target decides. A closed (or pinned) foreign row releases
// the source; an open one withholds it; a target the resolver cannot answer
// — missing row, no route — fails closed with the reason warned; a resolver
// that fails outright is the foreign read failing, warned and closed like a
// project that cannot be opened, not an error that takes the ready read down.
func TestGetReadyWorkAnswersBareCrossRigTargetFromForeignState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    *ForeignIssueResult
		resErr    error
		wantReady bool
		wantWarn  string
	}{
		{name: "foreign close releases", result: &ForeignIssueResult{Issue: &types.Issue{ID: xrigTarget, Status: types.StatusClosed}}, wantReady: true},
		{name: "foreign pinned releases", result: &ForeignIssueResult{Issue: &types.Issue{ID: xrigTarget, Status: types.StatusPinned}}, wantReady: true},
		{name: "foreign reopen withholds", result: &ForeignIssueResult{Issue: &types.Issue{ID: xrigTarget, Status: types.StatusOpen}}},
		{name: "unresolved fails closed", result: &ForeignIssueResult{Reason: "no route for prefix \"other-\""}, wantWarn: "no route for prefix \"other-\""},
		{name: "missing answer fails closed", wantWarn: "the owning rig did not answer for it"},
		{name: "resolver failure fails closed", resErr: errors.New("foreign database unavailable"), wantWarn: "resolver failed: foreign database unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := issue("be-a")
			raw := &fakeStore{
				ready: []*types.Issue{a},
				deps: map[string][]*types.Dependency{
					a.ID: {externalDep(a.ID, xrigTarget, types.DepBlocks)},
				},
			}
			resolver := &fakeResolver{results: map[string]*ForeignIssueResult{xrigTarget: tc.result}, err: tc.resErr}
			store := crossRigStore(raw, resolver)
			warned := captureCrossRigWarnings(store)

			got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if tc.wantReady {
				if ids := issueIDs(got); !slices.Equal(ids, []string{a.ID}) {
					t.Fatalf("ready IDs = %v, want [%s] released by a settled foreign row", ids, a.ID)
				}
			} else if len(got) != 0 {
				t.Fatalf("ready = %v, want %s withheld", issueIDs(got), a.ID)
			}
			if len(resolver.calls) != 1 || !slices.Equal(resolver.calls[0], []string{xrigTarget}) {
				t.Fatalf("resolver calls = %v, want one asking about %s", resolver.calls, xrigTarget)
			}
			if tc.wantWarn != "" {
				if len(warned()) != 1 || warned()[0] != xrigTarget+" ("+tc.wantWarn+")" {
					t.Fatalf("warnings = %v, want one for %s (%s)", warned(), xrigTarget, tc.wantWarn)
				}
			} else if len(warned()) != 0 {
				t.Fatalf("warnings = %v, want none", warned())
			}
		})
	}
}

// TestBareCrossRigLocalSettledRowNeedsNoForeignRead: a bare target that owns
// a settled row in this store is satisfied by that row alone, mirroring the
// raw ready predicate's satisfaction probe, and the resolver is never asked.
func TestBareCrossRigLocalSettledRowNeedsNoForeignRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status types.Status
	}{{"closed", types.StatusClosed}, {"pinned", types.StatusPinned}} {
		t.Run(tc.name, func(t *testing.T) {
			a := issue("be-a")
			settled := issue(xrigTarget)
			settled.Status = tc.status
			raw := &fakeStore{
				ready: []*types.Issue{a},
				local: []*types.Issue{settled},
				deps: map[string][]*types.Dependency{
					a.ID: {externalDep(a.ID, xrigTarget, types.DepBlocks)},
				},
			}
			resolver := &fakeResolver{}
			store := crossRigStore(raw, resolver)

			got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(got); !slices.Equal(ids, []string{a.ID}) {
				t.Fatalf("ready IDs = %v, want [%s] released by the local %s row", ids, a.ID, tc.status)
			}
			if len(resolver.calls) != 0 {
				t.Fatalf("resolver asked about %v; a settled local row must spare the foreign read", resolver.calls)
			}
		})
	}
}

// TestGetBlockedIssuesListsBareCrossRigBlocker: the blocked listing names the
// bare target as the blocker of a source the stored is_blocked flag never
// marked, on the same decoration that names explicit external refs.
func TestGetBlockedIssuesListsBareCrossRigBlocker(t *testing.T) {
	a := issue("be-a")
	raw := &fakeStore{
		ready: []*types.Issue{a},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, xrigTarget, types.DepBlocks)},
		},
	}
	store := crossRigStore(raw, &fakeResolver{
		results: map[string]*ForeignIssueResult{xrigTarget: {Reason: "no route for prefix \"other-\""}},
	})

	blocked, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if ids := blockedIssueIDs(blocked); !slices.Equal(ids, []string{a.ID}) {
		t.Fatalf("blocked IDs = %v, want [%s] listed with its bare foreign blocker", ids, a.ID)
	}
	if by := blocked[0].BlockedBy; !slices.Equal(by, []string{xrigTarget}) {
		t.Fatalf("%s blocked by %v, want [%s]", a.ID, by, xrigTarget)
	}
}

// TestReadyWorkDelegationMarksCrossRigPolicyDelegated: every ready filter
// this decorator hands its inner store must set
// types.WorkFilter.CrossRigBlockerPolicyDelegated — bare cross-prefix blockers
// are this layer's verdict, and the raw predicate's own exclusion would make
// a satisfied one permanent. The flag is set even with nothing to exclude:
// the delegation is about who owns the policy, not what this pass found.
func TestReadyWorkDelegationMarksCrossRigPolicyDelegated(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps map[string][]*types.Dependency
	}{
		{name: "nothing to exclude"},
		{
			name: "a bare cross-prefix blocker",
			deps: map[string][]*types.Dependency{
				"be-a": {externalDep("be-a", xrigTarget, types.DepBlocks)},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := issue("be-a"), issue("be-b")
			raw := &fakeStore{ready: []*types.Issue{a, b}, deps: tc.deps}
			store := crossRigStore(raw, nil)

			if _, _, err := store.GetReadyWorkWithCountsAndTotal(t.Context(), types.WorkFilter{Limit: 1}); err != nil {
				t.Fatalf("GetReadyWorkWithCountsAndTotal: %v", err)
			}
			if len(raw.readyFilters) != 1 {
				t.Fatalf("inner ready queries = %d, want 1", len(raw.readyFilters))
			}
			if !raw.readyFilters[0].CrossRigBlockerPolicyDelegated {
				t.Fatal("inner ready filter does not mark the cross-rig policy delegated")
			}
		})
	}
}

// TestClaimReadyIssueSkipsUnresolvedBareCrossRigBlocker: the ready claim
// excludes a source whose bare foreign blocker is unresolved, so a worker is
// never handed work its own rig believes is blocked.
func TestClaimReadyIssueSkipsUnresolvedBareCrossRigBlocker(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, xrigTarget, types.DepBlocks)},
		},
	}
	store := crossRigStore(raw, nil)

	got, err := store.ClaimReadyIssue(t.Context(), types.WorkFilter{}, "worker")
	if err != nil {
		t.Fatalf("ClaimReadyIssue: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Fatalf("claimed = %+v, want %s", got, b.ID)
	}
}

// TestRemoteReaderExcludesUnresolvedBareCrossRigBlocker: the remote
// composition's client-side ready exclusion carries bare cross-prefix
// blockers too. The local satisfaction read goes through the served
// BatchGetter (a remote store refuses the legacy GetIssuesByIDs), and with
// no resolver wired the target fails closed.
func TestRemoteReaderExcludesUnresolvedBareCrossRigBlocker(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := newRemoteStore(a, b)
	raw.deps[a.ID] = []*types.Dependency{externalDep(a.ID, xrigTarget, types.DepBlocks)}
	reader, err := testStore(raw, &fakeStore{}, true).IssueReader()
	if err != nil {
		t.Fatalf("IssueReader: %v", err)
	}

	ready, err := reader.Ready(t.Context(), publicops.ReadyRequest{})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(ready.Items) != 1 || ready.Items[0].ID != b.ID {
		t.Fatalf("Ready = %d items, want %s alone (%s is blocked by the bare %s)", len(ready.Items), b.ID, a.ID, xrigTarget)
	}
}

// TestWrapUOWProviderAnswersBareCrossRigBlockers: the unit-of-work wrapper —
// the proxied and served routes — settles bare cross-prefix blockers by the
// same verdict the store decorator does: unresolved withholds, a foreign
// close releases, and the local read that precedes the resolver goes through
// the wrapper's own issue use case.
func TestWrapUOWProviderAnswersBareCrossRigBlockers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver ForeignIssueResolver
		want     []string
	}{
		{name: "unresolved target withholds", resolver: &fakeResolver{
			results: map[string]*ForeignIssueResult{xrigTarget: {Reason: "no route for prefix \"other-\""}},
		}, want: []string{"be-ready"}},
		{name: "no resolver fails closed", resolver: nil, want: []string{"be-ready"}},
		{name: "foreign close releases", resolver: &fakeResolver{
			results: map[string]*ForeignIssueResult{xrigTarget: {Issue: &types.Issue{ID: xrigTarget, Status: types.StatusClosed}}},
		}, want: []string{"be-blocked", "be-ready"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocked, ready := issue("be-blocked"), issue("be-ready")
			inner := &fakeUOW{
				issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}},
				deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
					blocked.ID: {externalDep(blocked.ID, xrigTarget, types.DepBlocks)},
				}},
			}
			provider := WrapUOWProviderWithForeignResolver(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
				return "", false
			}, nil, tc.resolver)
			uow, err := provider.NewUOW(t.Context())
			if err != nil {
				t.Fatalf("NewUOW: %v", err)
			}

			got, err := uow.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(got.Items); !slices.Equal(ids, tc.want) {
				t.Fatalf("proxied ready IDs = %v, want %v", ids, tc.want)
			}
		})
	}
}

// TestUOWBlockedFlagAgreesWithBareCrossRigVerdict: the served is_blocked
// answer on the unit-of-work arm must call a source blocked by an unresolved
// bare foreign target blocked, as its own ready path does.
func TestUOWBlockedFlagAgreesWithBareCrossRigVerdict(t *testing.T) {
	blocked := issue("be-blocked")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{blocked}},
		deps: &fakeDependencyUseCase{
			external: map[string][]*types.Dependency{
				blocked.ID: {externalDep(blocked.ID, xrigTarget, types.DepBlocks)},
			},
			isBlocked: map[string]bool{blocked.ID: false},
		},
	}
	provider := WrapUOWProviderWithForeignResolver(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil, &fakeResolver{
		results: map[string]*ForeignIssueResult{xrigTarget: {Reason: "no route for prefix \"other-\""}},
	})
	uow, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}

	got, err := uow.DependencyUseCase().IsBlockedBatch(t.Context(), []string{blocked.ID})
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	if !got[blocked.ID] {
		t.Fatalf("%s is_blocked = false, want the bare foreign blocker counted", blocked.ID)
	}
}

// TestRemoteReaderLeavesNonBlockingBareCrossRigEdgesReady (GH#5784 AC-9): a
// source whose only bare cross-prefix edges are non-blocking types stays
// ready on the served composition, and readiness never asks the resolver
// about those targets: no foreign store is opened for an edge that cannot
// gate, so a rig that is unreachable at read time costs nothing and warns
// nothing. waits-for is the sharpest case — the records query returns it as
// a candidate superset (GetExternalBlockingDependencyRecordsInTx), so this
// pins that the policy layer, not the query, owns the exact blocking-type
// predicate.
func TestRemoteReaderLeavesNonBlockingBareCrossRigEdgesReady(t *testing.T) {
	for _, depType := range []types.DependencyType{types.DepWaitsFor, types.DepTracks, types.DepRelated, types.DepParentChild} {
		t.Run(string(depType), func(t *testing.T) {
			a, b := issue("be-a"), issue("be-b")
			raw := newRemoteStore(a, b)
			raw.deps[a.ID] = []*types.Dependency{externalDep(a.ID, xrigTarget, depType)}
			resolver := &fakeResolver{}
			store := testStore(raw, &fakeStore{}, true)
			store.resolveForeign = resolver
			warned := captureCrossRigWarnings(store)
			reader, err := store.IssueReader()
			if err != nil {
				t.Fatalf("IssueReader: %v", err)
			}

			ready, err := reader.Ready(t.Context(), publicops.ReadyRequest{})
			if err != nil {
				t.Fatalf("Ready: %v", err)
			}
			if len(ready.Items) != 2 || ready.Items[0].ID != a.ID || ready.Items[1].ID != b.ID {
				t.Fatalf("Ready = %d items, want both %s and %s offered: a %s edge cannot gate readiness", len(ready.Items), a.ID, b.ID, depType)
			}
			if len(resolver.calls) != 0 {
				t.Fatalf("resolver asked about %v; readiness must open no foreign store for %s edges", resolver.calls, depType)
			}
			if len(warned()) != 0 {
				t.Fatalf("warnings = %v, want none for non-blocking bare cross-rig edges", warned())
			}
		})
	}
}

// TestUOWNonBlockingBareCrossRigEdgesStayReady: the proxied unit-of-work arm
// of AC-9 — the same verdict as the served reader, through the wrapper the
// proxied routes use.
func TestUOWNonBlockingBareCrossRigEdgesStayReady(t *testing.T) {
	for _, depType := range []types.DependencyType{types.DepWaitsFor, types.DepTracks, types.DepRelated, types.DepParentChild} {
		t.Run(string(depType), func(t *testing.T) {
			source, other := issue("be-source"), issue("be-other")
			inner := &fakeUOW{
				issues: &fakeIssueUseCase{ready: []*types.Issue{source, other}},
				deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
					source.ID: {externalDep(source.ID, xrigTarget, depType)},
				}},
			}
			resolver := &fakeResolver{}
			provider := WrapUOWProviderWithForeignResolver(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
				return "", false
			}, nil, resolver)
			uow, err := provider.NewUOW(t.Context())
			if err != nil {
				t.Fatalf("NewUOW: %v", err)
			}

			got, err := uow.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
			if err != nil {
				t.Fatalf("GetReadyWork: %v", err)
			}
			if ids := issueIDs(got.Items); !slices.Equal(ids, []string{source.ID, other.ID}) {
				t.Fatalf("proxied ready IDs = %v, want both offered: a %s edge cannot gate readiness", ids, depType)
			}
			if len(resolver.calls) != 0 {
				t.Fatalf("resolver asked about %v; readiness must open no foreign store for %s edges", resolver.calls, depType)
			}
		})
	}
}
