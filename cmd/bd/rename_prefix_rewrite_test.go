package main

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestRewriteIssueID_NoDoublePrefix(t *testing.T) {
	t.Parallel()
	// GH#4827: config still "global" but rows already "atlas-*"
	got := rewriteIssueID("global", "atlas", "atlas-1")
	if got != "atlas-1" {
		t.Fatalf("got %q, want atlas-1 (must not double)", got)
	}
	got = rewriteIssueID("global-", "atlas-", "atlas-99")
	if got != "atlas-99" {
		t.Fatalf("got %q, want atlas-99", got)
	}
}

func TestRewriteIssueID_NormalRename(t *testing.T) {
	t.Parallel()
	got := rewriteIssueID("global", "atlas", "global-1")
	if got != "atlas-1" {
		t.Fatalf("got %q, want atlas-1", got)
	}
	got = rewriteIssueID("old-", "new-", "old-abc")
	if got != "new-abc" {
		t.Fatalf("got %q, want new-abc", got)
	}
}

func TestRewriteIssueID_UnrelatedPrefixUnchanged(t *testing.T) {
	t.Parallel()
	got := rewriteIssueID("global", "atlas", "other-1")
	if got != "other-1" {
		t.Fatalf("got %q, want other-1", got)
	}
}

// TestRewriteIssueID_PrefixShortening pins PR #5135 review blocker B1
// (maphew, 2026-07-29): the "already on target prefix" guard was checked
// before the old-prefix match, so shortening a multi-part prefix to one of
// its own leading segments (e.g. "beads-vscode-" -> "beads-") silently
// skipped every ID, because "beads-vscode-1" also starts with "beads-".
func TestRewriteIssueID_PrefixShortening(t *testing.T) {
	t.Parallel()
	got := rewriteIssueID("beads-vscode", "beads", "beads-vscode-1")
	if got != "beads-1" {
		t.Fatalf("got %q, want beads-1 (shortening regression)", got)
	}
	got = rewriteIssueID("kw-team", "kw", "kw-team-42")
	if got != "kw-42" {
		t.Fatalf("got %q, want kw-42 (shortening regression)", got)
	}
}

// generatedRootPattern matches a fallback root the planner mints for a
// conflicting family: target prefix, 8 lowercase hex chars.
var generatedRootPattern = regexp.MustCompile(`^kb-[0-9a-f]{8}$`)

// planTestIssue builds a minimal row for planner tests. Fixed timestamps keep
// fallback hashing deterministic.
func planTestIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "issue " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Unix(1700000000, 0),
	}
}

// repairRenamesByID folds a plan's renames into a from->to map.
func repairRenamesByID(plan *prefixRepairPlan) map[string]string {
	m := make(map[string]string, len(plan.Renames))
	for _, r := range plan.Renames {
		m[r.From] = r.To
	}
	return m
}

// staticRetiredLookup answers a fixed retired set and records every probe.
func staticRetiredLookup(retired ...string) (repairRetiredSet, *[][]string) {
	var probes [][]string
	set := make(map[string]bool, len(retired))
	for _, id := range retired {
		set[id] = true
	}
	return func(ids []string) (map[string]bool, error) {
		probes = append(probes, ids)
		return set, nil
	}, &probes
}

func TestPlanPrefixRepair(t *testing.T) {
	t.Parallel()

	t.Run("free suffixes and retained target ids", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("kb-adr-0001"),
			planTestIssue("taxdb-5su1"),
			planTestIssue("cre-db-c2a"),
		}, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan.Unchanged != 1 {
			t.Errorf("Unchanged = %d, want 1 (kb-adr-0001 is a hand-named target id)", plan.Unchanged)
		}
		if len(plan.Collisions) != 0 {
			t.Errorf("Collisions = %+v, want none", plan.Collisions)
		}
		renames := repairRenamesByID(plan)
		if renames["taxdb-5su1"] != "kb-5su1" || renames["cre-db-c2a"] != "kb-c2a" {
			t.Errorf("renames = %v, want suffix-preserving kb-5su1 and kb-c2a", renames)
		}
		if plan.RootMap["taxdb-5su1"] != "kb-5su1" {
			t.Errorf("RootMap[taxdb-5su1] = %q, want kb-5su1", plan.RootMap["taxdb-5su1"])
		}
	})

	t.Run("live root collision re-roots the family", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("kb-5su1"),
			planTestIssue("taxdb-5su1"),
		}, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.OldRoot != "taxdb-5su1" || c.WantedRoot != "kb-5su1" || c.Reason != repairReasonExists || c.Conflicting != "kb-5su1" {
			t.Errorf("collision = %+v, want taxdb-5su1 -> kb-5su1 [exists] held by kb-5su1", c)
		}
		if !generatedRootPattern.MatchString(c.AssignedRoot) {
			t.Errorf("AssignedRoot = %q, want a generated kb-<8hex> root", c.AssignedRoot)
		}
		if renames := repairRenamesByID(plan); renames["taxdb-5su1"] != c.AssignedRoot {
			t.Errorf("rename = %v, want the family on %s", renames, c.AssignedRoot)
		}
	})

	t.Run("child-only namespace collision", func(t *testing.T) {
		t.Parallel()
		// No live kb-5su1 root row, but kb-5su1.13 occupies the namespace:
		// adopting the root would graft the taxdb family onto that child's
		// ancestry.
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("kb-5su1.13"),
			planTestIssue("taxdb-5su1"),
			planTestIssue("taxdb-5su1.13"),
		}, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.Reason != repairReasonExists || c.Conflicting != "kb-5su1.13" {
			t.Errorf("collision = %+v, want reason exists held by the live child kb-5su1.13", c)
		}
		renames := repairRenamesByID(plan)
		if renames["taxdb-5su1"] != c.AssignedRoot || renames["taxdb-5su1.13"] != c.AssignedRoot+".13" {
			t.Errorf("renames = %v, want the family nested under %s", renames, c.AssignedRoot)
		}
		if !generatedRootPattern.MatchString(c.AssignedRoot) {
			t.Errorf("AssignedRoot = %q, want a generated kb-<8hex> root", c.AssignedRoot)
		}
	})

	t.Run("earlier family's claim", func(t *testing.T) {
		t.Parallel()
		// Both families want kb-u6ch; family order (prefix, then root)
		// gives cre-db-u6ch the natural destination and taxdb-u6ch the
		// fallback.
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("taxdb-u6ch"),
			planTestIssue("cre-db-u6ch"),
		}, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		renames := repairRenamesByID(plan)
		if renames["cre-db-u6ch"] != "kb-u6ch" {
			t.Errorf("cre-db-u6ch -> %q, want the natural kb-u6ch", renames["cre-db-u6ch"])
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.OldRoot != "taxdb-u6ch" || c.WantedRoot != "kb-u6ch" || c.Reason != repairReasonClaimed || c.Conflicting != "kb-u6ch" {
			t.Errorf("collision = %+v, want taxdb-u6ch -> kb-u6ch [claimed]", c)
		}
		if renames["taxdb-u6ch"] != c.AssignedRoot || !generatedRootPattern.MatchString(c.AssignedRoot) {
			t.Errorf("taxdb-u6ch -> %q (assigned %q), want a generated root", renames["taxdb-u6ch"], c.AssignedRoot)
		}
	})

	t.Run("retired wanted root", func(t *testing.T) {
		t.Parallel()
		lookup, probes := staticRetiredLookup("kb-5su1")
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("taxdb-5su1"),
		}, lookup)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(*probes) == 0 {
			t.Fatal("the planner never probed the retirement seam")
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.Reason != repairReasonRetired || c.Conflicting != "kb-5su1" {
			t.Errorf("collision = %+v, want reason retired held by kb-5su1", c)
		}
		if !generatedRootPattern.MatchString(c.AssignedRoot) {
			t.Errorf("AssignedRoot = %q, want a generated kb-<8hex> root", c.AssignedRoot)
		}
	})

	t.Run("retired member id re-roots the family", func(t *testing.T) {
		t.Parallel()
		// The wanted root is free, but a member's mapped id is retired: the
		// family moves whole rather than splitting.
		lookup, _ := staticRetiredLookup("kb-5su1.13")
		plan, err := planPrefixRepair("kb", "actor", []*types.Issue{
			planTestIssue("taxdb-5su1"),
			planTestIssue("taxdb-5su1.13"),
		}, lookup)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.Reason != repairReasonRetired || c.Conflicting != "kb-5su1.13" {
			t.Errorf("collision = %+v, want reason retired held by kb-5su1.13", c)
		}
		renames := repairRenamesByID(plan)
		if renames["taxdb-5su1"] != c.AssignedRoot || renames["taxdb-5su1.13"] != c.AssignedRoot+".13" {
			t.Errorf("renames = %v, want the family nested under %s", renames, c.AssignedRoot)
		}
	})

	t.Run("retired candidate is skipped", func(t *testing.T) {
		t.Parallel()
		// Family aaa-q7 conflicts on a live row and mints fallback roots.
		// The fake seam retires the first candidate probed in the candidate
		// batch, so the family must land on the next one — and phase-one
		// reservation keeps the first candidate available to family b,
		// whose natural destination it is.
		const actor = "deterministic-actor"
		seed := planTestIssue("aaa-q7")
		seed.Title = "fallback seed"
		live := map[string]bool{"kb-q7": true, "aaa-q7": true}
		first, err := generateRepairHashID("kb", seed, actor, live)
		if err != nil {
			t.Fatalf("precompute first candidate: %v", err)
		}
		taken := map[string]bool{}
		for id := range live {
			taken[id] = true
		}
		taken[first] = true
		second, err := generateRepairHashID("kb", seed, actor, taken)
		if err != nil {
			t.Fatalf("precompute second candidate: %v", err)
		}
		taken[second] = true
		third, err := generateRepairHashID("kb", seed, actor, taken)
		if err != nil {
			t.Fatalf("precompute third candidate: %v", err)
		}
		if second == third || first == third {
			t.Fatal("precomputed candidates collided; the fixture is not exercising the walk")
		}

		bRoot := "b-" + strings.TrimPrefix(first, "kb-")
		calls := 0
		lookup := func(ids []string) (map[string]bool, error) {
			calls++
			if calls == 2 && len(ids) > 0 {
				// Retire the first probed candidate.
				return map[string]bool{ids[0]: true}, nil
			}
			return nil, nil
		}

		plan, err := planPrefixRepair("kb", actor, []*types.Issue{
			planTestIssue("kb-q7"),
			seed,
			// A family whose natural destination IS the first candidate the
			// blocked family would mint.
			planTestIssue(bRoot),
		}, lookup)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if calls != 2 {
			t.Fatalf("retirement probes = %d, want 2 (destinations, then candidates)", calls)
		}
		renames := repairRenamesByID(plan)
		// Family b reserved its natural root in phase one, so the blocked
		// family's candidates never include it.
		if renames[bRoot] != first {
			t.Errorf("family b -> %q, want its natural root %s (reserved before fallback allocation)", renames[bRoot], first)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("Collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.AssignedRoot != third {
			t.Errorf("AssignedRoot = %q, want %q (first candidate is family b's, second is retired)", c.AssignedRoot, third)
		}
		if renames["aaa-q7"] != third {
			t.Errorf("aaa-q7 -> %q, want the third candidate %q", renames["aaa-q7"], third)
		}
	})

	t.Run("lookup error fails planning", func(t *testing.T) {
		t.Parallel()
		lookup := func(ids []string) (map[string]bool, error) {
			return nil, errors.New("boom")
		}
		if _, err := planPrefixRepair("kb", "actor", []*types.Issue{planTestIssue("taxdb-5su1")}, lookup); err == nil || !strings.Contains(err.Error(), "read retired ids") {
			t.Fatalf("error = %v, want a read retired ids failure", err)
		}
	})

	t.Run("shuffled input yields an identical plan", func(t *testing.T) {
		t.Parallel()
		issues := []*types.Issue{
			planTestIssue("kb-hand"),
			planTestIssue("taxdb-5su1"),
			planTestIssue("taxdb-5su1.13"),
			planTestIssue("cre-db-c2a"),
			planTestIssue("cre-db-u6ch"),
			planTestIssue("taxdb-u6ch"),
		}
		forward, err := planPrefixRepair("kb", "actor", issues, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		reversed := make([]*types.Issue, len(issues))
		for i, issue := range issues {
			reversed[len(issues)-1-i] = issue
		}
		backward, err := planPrefixRepair("kb", "actor", reversed, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if !reflect.DeepEqual(forward.Renames, backward.Renames) || !reflect.DeepEqual(forward.Collisions, backward.Collisions) {
			t.Errorf("plan depends on row order:\nforward:  %+v %+v\nbackward: %+v %+v", forward.Renames, backward.Renames, backward.Collisions, forward.Collisions)
		}
		if !reflect.DeepEqual(forward.RootMap, backward.RootMap) || forward.Unchanged != backward.Unchanged {
			t.Errorf("root map or unchanged count depends on row order: %v vs %v (%d vs %d)", forward.RootMap, backward.RootMap, forward.Unchanged, backward.Unchanged)
		}
	})
}

// fakeRetiredSeam is a bare store carrying only the optional retirement
// seam, the way the direct stores do under their decorators.
type fakeRetiredSeam struct {
	storage.DoltStorage
	retired map[string]time.Time
	probes  [][]string
}

func (f *fakeRetiredSeam) RetiredIssueIDs(ctx context.Context, ids []string) (map[string]time.Time, error) {
	f.probes = append(f.probes, ids)
	return f.retired, nil
}

// fakeDecoratedStore is a decorator that carries no seams itself, only an
// Unwrap to the store underneath.
type fakeDecoratedStore struct {
	storage.DoltStorage
	inner storage.DoltStorage
}

func (f *fakeDecoratedStore) Unwrap() storage.DoltStorage { return f.inner }

func TestRepairRetiredLookupReachesTheSeam(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	direct := &fakeRetiredSeam{retired: map[string]time.Time{"kb-aa": {}}}
	lookup := repairRetiredLookup(ctx, direct)
	if lookup == nil {
		t.Fatal("direct store: lookup = nil, want the seam")
	}
	retired, err := lookup([]string{"kb-aa", "kb-bb"})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !retired["kb-aa"] || retired["kb-bb"] {
		t.Errorf("retired set = %v, want exactly kb-aa", retired)
	}

	// The CLI's store is decorated; the seam lives underneath.
	inner := &fakeRetiredSeam{}
	decorated := repairRetiredLookup(ctx, &fakeDecoratedStore{inner: inner})
	if decorated == nil {
		t.Fatal("decorated store: lookup = nil, want the seam after unwrapping")
	}
	if _, err := decorated([]string{"kb-cc"}); err != nil {
		t.Fatalf("decorated lookup: %v", err)
	}
	if len(inner.probes) != 1 || len(inner.probes[0]) != 1 || inner.probes[0][0] != "kb-cc" {
		t.Errorf("inner probes = %v, want one probe of [kb-cc]", inner.probes)
	}

	// A store without the seam (test fakes) plans without retirement checks.
	type seamlessStore struct{ storage.DoltStorage }
	if got := repairRetiredLookup(ctx, &seamlessStore{}); got != nil {
		t.Errorf("seamless store: lookup = %v, want nil", got)
	}
}
