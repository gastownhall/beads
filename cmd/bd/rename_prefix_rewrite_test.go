package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
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
