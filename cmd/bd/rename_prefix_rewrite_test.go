package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
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

// repairHashRootPattern matches a fallback root the planner mints under kb.
var repairHashRootPattern = regexp.MustCompile(`^kb-[0-9a-f]{8}$`)

func planIssues(ids ...string) []*types.Issue {
	issues := make([]*types.Issue, 0, len(ids))
	for i, id := range ids {
		issues = append(issues, &types.Issue{ID: id, Title: "issue " + id, CreatedAt: time.Unix(int64(i), 0)})
	}
	return issues
}

func planMapping(plan *prefixRepairPlan) map[string]string {
	got := make(map[string]string, len(plan.Renames))
	for _, r := range plan.Renames {
		got[r.OldID] = r.NewID
	}
	return got
}

func TestPlanPrefixRepair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("same plan for any input order", func(t *testing.T) {
		t.Parallel()
		ids := []string{"kb-c2a", "taxdb-c2a", "cre-db-c2a", "taxdb-5su1.13", "taxdb-5su1", "cre-db-nwb.3", "kb-x.1"}
		first, err := planPrefixRepair(ctx, "kb", planIssues(ids...), nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		reversed := slices.Clone(ids)
		slices.Reverse(reversed)
		shuffled := planIssues(reversed...)
		// The same rows, so the same hash seeds, in another order.
		for _, issue := range shuffled {
			for i, id := range ids {
				if issue.ID == id {
					issue.CreatedAt = time.Unix(int64(i), 0)
				}
			}
		}
		second, err := planPrefixRepair(ctx, "kb", shuffled, nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first.Renames, second.Renames) || !reflect.DeepEqual(first.Collisions, second.Collisions) {
			t.Fatalf("plans differ by input order:\n%+v\n%+v", first, second)
		}
		if first.Unchanged != 2 || len(first.Collisions) != 2 {
			t.Fatalf("plan = %+v, want 2 unchanged and 2 collisions (kb-c2a exists for both c2a families)", first)
		}
		if got := planMapping(first); got["taxdb-5su1.13"] != "kb-5su1.13" || got["cre-db-nwb.3"] != "kb-nwb.3" {
			t.Fatalf("mapping = %v", got)
		}
	})

	t.Run("lookup error fails the plan", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("lookup down")
		_, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-u6ch"), nil, "actor",
			func(context.Context, []string) (map[string]time.Time, error) { return nil, boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the lookup error", err)
		}
	})

	t.Run("allocation is bounded", func(t *testing.T) {
		t.Parallel()
		probes := 0
		_, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-u6ch"), nil, "actor",
			func(_ context.Context, ids []string) (map[string]time.Time, error) {
				probes++
				all := make(map[string]time.Time, len(ids))
				for _, id := range ids {
					all[id] = time.Unix(1, 0)
				}
				return all, nil
			})
		if err == nil || !strings.Contains(err.Error(), "after 100 attempts") {
			t.Fatalf("err = %v, want exhaustion after 100 attempts", err)
		}
		if probes != 1+repairHashAttempts {
			t.Fatalf("probes = %d, want one batch plus %d candidates", probes, repairHashAttempts)
		}
	})

	t.Run("free suffixes and retained target ids", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("kb-adr-0001", "taxdb-5su1", "cre-db-c2a"), nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Unchanged != 1 || len(plan.Collisions) != 0 {
			t.Fatalf("plan = %+v, want kb-adr-0001 unchanged and no collisions", plan)
		}
		if got := planMapping(plan); got["taxdb-5su1"] != "kb-5su1" || got["cre-db-c2a"] != "kb-c2a" {
			t.Fatalf("mapping = %v, want suffix-preserving kb-5su1 and kb-c2a", got)
		}
		if plan.rootMap["taxdb-5su1"] != "kb-5su1" {
			t.Fatalf("rootMap[taxdb-5su1] = %q, want kb-5su1", plan.rootMap["taxdb-5su1"])
		}
	})

	t.Run("live root collision re-roots the family", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("kb-5su1", "taxdb-5su1"), nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.OldRoot != "taxdb-5su1" || c.WantedRoot != "kb-5su1" || c.Reason != repairCollisionExists || c.ConflictID != "kb-5su1" {
			t.Fatalf("collision = %+v, want taxdb-5su1 -> kb-5su1 [exists] held by kb-5su1", c)
		}
		if !repairHashRootPattern.MatchString(c.AssignedRoot) {
			t.Fatalf("assigned root = %q, want kb-<8 hex>", c.AssignedRoot)
		}
		if got := planMapping(plan); got["taxdb-5su1"] != c.AssignedRoot {
			t.Fatalf("mapping = %v, want the family on %s", got, c.AssignedRoot)
		}
	})

	t.Run("child-only namespace collision", func(t *testing.T) {
		t.Parallel()
		// No live kb-5su1 row, but kb-5su1.13 holds the namespace: taking the
		// root would graft the taxdb family onto that child's ancestry.
		plan, err := planPrefixRepair(ctx, "kb", planIssues("kb-5su1.13", "taxdb-5su1", "taxdb-5su1.13"), nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.Reason != repairCollisionExists || c.ConflictID != "kb-5su1.13" || !repairHashRootPattern.MatchString(c.AssignedRoot) {
			t.Fatalf("collision = %+v, want exists held by the live child kb-5su1.13, on a hash root", c)
		}
		if got := planMapping(plan); got["taxdb-5su1"] != c.AssignedRoot || got["taxdb-5su1.13"] != c.AssignedRoot+".13" {
			t.Fatalf("mapping = %v, want the family nested under %s", got, c.AssignedRoot)
		}
	})

	t.Run("earlier family's claim", func(t *testing.T) {
		t.Parallel()
		// Both families want kb-u6ch; prefix order gives cre-db the natural
		// destination and taxdb the fallback.
		plan, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-u6ch", "cre-db-u6ch"), []string{"cre-db", "taxdb"}, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := planMapping(plan)
		if got["cre-db-u6ch"] != "kb-u6ch" {
			t.Fatalf("cre-db-u6ch -> %q, want the natural kb-u6ch", got["cre-db-u6ch"])
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.OldRoot != "taxdb-u6ch" || c.WantedRoot != "kb-u6ch" || c.Reason != repairCollisionClaimed || c.ConflictID != "cre-db-u6ch" {
			t.Fatalf("collision = %+v, want taxdb-u6ch -> kb-u6ch [claimed] by cre-db-u6ch", c)
		}
		if got["taxdb-u6ch"] != c.AssignedRoot || !repairHashRootPattern.MatchString(c.AssignedRoot) {
			t.Fatalf("taxdb-u6ch -> %q (assigned %q), want a hash root", got["taxdb-u6ch"], c.AssignedRoot)
		}
	})

	t.Run("orphan family's root claim makes a later root family fall back", func(t *testing.T) {
		t.Parallel()
		// cre-db-x.3 has no cre-db-x row, but its virtual root still claims
		// the kb-x namespace; taxdb-x must not become kb-x, a false parent of
		// kb-x.3.
		plan, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-x", "cre-db-x.3"), []string{"cre-db", "taxdb"}, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := planMapping(plan)
		if got["cre-db-x.3"] != "kb-x.3" {
			t.Fatalf("cre-db-x.3 -> %q, want kb-x.3", got["cre-db-x.3"])
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.OldRoot != "taxdb-x" || c.WantedRoot != "kb-x" || c.Reason != repairCollisionClaimed || c.ConflictID != "cre-db-x" {
			t.Fatalf("collision = %+v, want taxdb-x -> kb-x [claimed] by the orphan root cre-db-x", c)
		}
		if got["taxdb-x"] != c.AssignedRoot || got["taxdb-x"] == "kb-x" || !repairHashRootPattern.MatchString(c.AssignedRoot) {
			t.Fatalf("taxdb-x -> %q (assigned %q), want a hash root, not kb-x", got["taxdb-x"], c.AssignedRoot)
		}
	})

	t.Run("retired member id re-roots the family", func(t *testing.T) {
		t.Parallel()
		// The wanted root is free but a member's destination is retired: the
		// family moves whole rather than splitting.
		plan, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-5su1", "taxdb-5su1.13"), nil, "actor",
			func(_ context.Context, ids []string) (map[string]time.Time, error) {
				if slices.Contains(ids, "kb-5su1.13") {
					return map[string]time.Time{"kb-5su1.13": time.Unix(1, 0)}, nil
				}
				return nil, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Collisions) != 1 {
			t.Fatalf("collisions = %+v, want one", plan.Collisions)
		}
		c := plan.Collisions[0]
		if c.Reason != repairCollisionRetired || c.ConflictID != "kb-5su1.13" {
			t.Fatalf("collision = %+v, want retired held by kb-5su1.13", c)
		}
		if got := planMapping(plan); got["taxdb-5su1"] != c.AssignedRoot || got["taxdb-5su1.13"] != c.AssignedRoot+".13" {
			t.Fatalf("mapping = %v, want the family nested under %s", got, c.AssignedRoot)
		}
	})

	t.Run("retired candidate is skipped", func(t *testing.T) {
		t.Parallel()
		// aaa-q7 collides with live kb-q7 and walks hash candidates. Family b
		// naturally wants the first candidate and reserves it before any
		// fallback is allocated; the probe retires the second, so aaa-q7
		// lands on the third.
		const actor = "deterministic-actor"
		issues := planIssues("kb-q7", "aaa-q7")
		seed := issues[1]
		first := repairHashRoot("kb", seed, actor, 0)
		second := repairHashRoot("kb", seed, actor, 1)
		third := repairHashRoot("kb", seed, actor, 2)
		if first == second || second == third || first == third {
			t.Fatal("precomputed candidates collided; the fixture does not exercise the walk")
		}
		bRoot := "b-" + strings.TrimPrefix(first, "kb-")
		issues = append(issues, planIssues(bRoot)...)
		plan, err := planPrefixRepair(ctx, "kb", issues, []string{"aaa", "b"}, actor,
			func(_ context.Context, ids []string) (map[string]time.Time, error) {
				if slices.Contains(ids, second) {
					return map[string]time.Time{second: time.Unix(1, 0)}, nil
				}
				return nil, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		got := planMapping(plan)
		if got[bRoot] != first {
			t.Fatalf("family b -> %q, want its natural root %s", got[bRoot], first)
		}
		if len(plan.Collisions) != 1 || plan.Collisions[0].AssignedRoot != third || got["aaa-q7"] != third {
			t.Fatalf("collisions = %+v, mapping = %v; want aaa-q7 on the third candidate %s", plan.Collisions, got, third)
		}
	})

	t.Run("known prefixes keep hyphenated suffixes whole", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("cre-db-a1b", "cre-db-wxyz"), []string{"cre-db"}, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := planMapping(plan); got["cre-db-a1b"] != "kb-a1b" || got["cre-db-wxyz"] != "kb-wxyz" {
			t.Fatalf("mapping = %v, want kb-a1b and kb-wxyz", got)
		}
	})

	t.Run("heuristic without known prefixes", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("cre-db-wxyz"), nil, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := planMapping(plan); got["cre-db-wxyz"] != "kb-db-wxyz" {
			t.Fatalf("mapping = %v, want the heuristic kb-db-wxyz", got)
		}
	})

	t.Run("all-letter suffix under a known prefix", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("me-py-toolkit-abcd"), []string{"me", "me-py-toolkit"}, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := planMapping(plan); got["me-py-toolkit-abcd"] != "kb-abcd" {
			t.Fatalf("mapping = %v, want kb-abcd (longest known prefix wins)", got)
		}
	})

	t.Run("retired wanted root moves the family", func(t *testing.T) {
		t.Parallel()
		plan, err := planPrefixRepair(ctx, "kb", planIssues("taxdb-u6ch", "taxdb-u6ch.1"), nil, "actor",
			func(_ context.Context, ids []string) (map[string]time.Time, error) {
				if slices.Contains(ids, "kb-u6ch") {
					return map[string]time.Time{"kb-u6ch": time.Unix(1, 0)}, nil
				}
				return nil, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Collisions) != 1 || plan.Collisions[0].Reason != "retired" || plan.Collisions[0].ConflictID != "kb-u6ch" {
			t.Fatalf("collisions = %+v, want one retired kb-u6ch", plan.Collisions)
		}
		root := plan.Collisions[0].AssignedRoot
		if got := planMapping(plan); got["taxdb-u6ch"] != root || got["taxdb-u6ch.1"] != root+".1" {
			t.Fatalf("mapping = %v, want the family under %s", got, root)
		}
	})
}

// retiredLookupOnly is a backend that answers only the retired-ID probe.
type retiredLookupOnly struct {
	storage.DoltStorage
}

func (retiredLookupOnly) RetiredIssueIDs(_ context.Context, ids []string) (map[string]time.Time, error) {
	return map[string]time.Time{ids[0]: time.Unix(1, 0)}, nil
}

// repairDecoratorStore is a decorator over a backend, like the CLI's hook store.
type repairDecoratorStore struct {
	storage.DoltStorage
	inner storage.DoltStorage
}

func (s repairDecoratorStore) Unwrap() storage.DoltStorage { return s.inner }

func TestRepairRetiredLookupFindsTheDecoratedBackend(t *testing.T) {
	t.Parallel()
	lookup := repairRetiredLookup(repairDecoratorStore{inner: retiredLookupOnly{}})
	if lookup == nil {
		t.Fatal("no retired lookup found under the decorator")
	}
	got, err := lookup(context.Background(), []string{"kb-1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("lookup = %v, %v; want the backend's answer", got, err)
	}
	if repairRetiredLookup(repairDecoratorStore{inner: repairDecoratorStore{}}) != nil {
		t.Fatal("a store without the probe should skip it")
	}
}

func TestPlanPrefixRepairKnownPrefixes(t *testing.T) {
	t.Parallel()
	// Known prefixes split longest-first; without one, the heuristic reads an
	// all-letter suffix as part of the prefix. Target IDs, custom or not,
	// stay, even when the target is a leading segment of a source prefix.
	for _, tt := range []struct {
		target string
		known  []string
		id     string
		want   string
	}{
		{target: "kb", known: []string{"cre", "cre-db"}, id: "cre-db-wxyz", want: "kb-wxyz"},
		{target: "kb", known: nil, id: "cre-db-wxyz", want: "kb-db-wxyz"},
		{target: "kb", known: []string{"investment", "investment-advisor"}, id: "investment-advisor-q1x.4", want: "kb-q1x.4"},
		{target: "kb", known: nil, id: "kb-adr-0001", want: ""},
		{target: "beads", known: []string{"beads-vscode"}, id: "beads-vscode-1", want: ""},
		{target: "kb", known: nil, id: "nohyphen", want: "kb-nohyphen"},
	} {
		plan, err := planPrefixRepair(context.Background(), tt.target, planIssues(tt.id), tt.known, "actor", nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := planMapping(plan)[tt.id]; got != tt.want {
			t.Errorf("%s with known %v -> %q, want %q", tt.id, tt.known, got, tt.want)
		}
	}
}

func TestIsDependencyRefusal(t *testing.T) {
	t.Parallel()
	// A repair steps over an edge storage refuses, as it refuses a new one,
	// and stops on any other failure after its renames landed. Each refusal
	// must match bare, wrapped and joined with a rollback error, the forms
	// the two backends return it in.
	refusals := []error{
		publicops.ErrDependencyCycle,
		fmt.Errorf("%w: kb-a cannot depend on itself", publicops.ErrSelfDependency),
		&publicops.DependencyHierarchyConflictError{IssueID: "kb-a", BlockerID: "kb-p", BlockerIsAncestor: true},
		&publicops.DependencyTypeConflictError{IssueID: "kb-a", DependsOnID: "kb-b", ExistingType: "blocks", RequestedType: "parent-child"},
		&publicops.DependencyEndpointNotFoundError{IssueID: "kb-a", DependsOnID: "kb-b", MissingID: "kb-b", Err: publicops.ErrDependencyTargetNotFound},
	}
	for _, refusal := range refusals {
		for _, err := range []error{refusal, fmt.Errorf("add dependency: %w", refusal), errors.Join(refusal, errors.New("rollback failed"))} {
			if !isDependencyRefusal(err) {
				t.Errorf("isDependencyRefusal(%v) = false, want true", err)
			}
		}
	}
	for _, err := range []error{
		errors.New(publicops.ErrDependencyCycle.Error()),
		fmt.Errorf("failed to check blocker ancestry: %w", errors.New("connection reset")),
		fmt.Errorf("stage and commit after regular SQL commit: %w", storage.ErrCommitIndeterminate),
		context.Canceled,
	} {
		if isDependencyRefusal(err) {
			t.Errorf("isDependencyRefusal(%v) = true, want false", err)
		}
	}
}
