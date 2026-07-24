package conformance

import (
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// testReclaimScoped: bd reclaim honors the scope filters (--label / --label-any /
// --exclude-label / --assignee / --id), reverting ONLY the stale leases the
// filter selects and leaving every other stale lease in_progress. The filters
// never widen the set — an unmatched scope reclaims nothing even though a global
// reclaim would grab everything — which is the property a federated deployment
// partitions reclaim on. Runs identically on Dolt and SQLite because both route
// through issueops.ReclaimExpiredLeasesInTx + sqlbuild.ReclaimScopeSQL.
//
// The whole test runs one reaper cutoff (-time.Hour), so every fresh lease is
// already "expired": scoping, not staleness, decides what each call reclaims.
func testReclaimScoped(t *testing.T, f Factory) {
	s := f(t)

	// Four claimed, all-stale issues across two lanes and three workers.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rs-a1", Title: "lane-a"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rs-a2", Title: "lane-a opus"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rs-b1", Title: "lane-b"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rs-c1", Title: "no lane"}), "a"))
	must(t, s.AddLabel(ctx(), "rs-a1", "lane-a", "a"))
	must(t, s.AddLabel(ctx(), "rs-a2", "lane-a", "a"))
	must(t, s.AddLabel(ctx(), "rs-a2", "tier:opus", "a"))
	must(t, s.AddLabel(ctx(), "rs-b1", "lane-b", "a"))
	must(t, s.ClaimIssue(ctx(), "rs-a1", "wa"))
	must(t, s.ClaimIssue(ctx(), "rs-a2", "wa"))
	must(t, s.ClaimIssue(ctx(), "rs-b1", "wb"))
	must(t, s.ClaimIssue(ctx(), "rs-c1", "wc"))

	// reclaim runs a scoped reaper and returns the sorted set of reclaimed IDs.
	reclaim := func(filter types.ReclaimFilter) []string {
		t.Helper()
		got, err := s.ReclaimExpiredLeases(ctx(), -time.Hour, filter, "reaper")
		must(t, err)
		ids := make([]string, 0, len(got))
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		sort.Strings(ids)
		return ids
	}
	inProgress := func(id string) bool {
		t.Helper()
		got, err := s.GetIssue(ctx(), id)
		must(t, err)
		return got.Status == types.StatusInProgress
	}
	eq := slices.Equal[[]string]

	// --label (AND-set): only lane-b's stale lease is reverted; the lane-a and
	// unlabeled claims are untouched even though a global reaper would take them.
	if got := reclaim(types.ReclaimFilter{Labels: []string{"lane-b"}}); !eq(got, []string{"rs-b1"}) {
		t.Fatalf("reclaim(--label lane-b) = %v, want [rs-b1]", got)
	}
	if !inProgress("rs-a1") || !inProgress("rs-a2") || !inProgress("rs-c1") {
		t.Fatalf("label-scoped reclaim disturbed out-of-scope claims")
	}

	// --assignee: only worker wc's stale lease.
	if got := reclaim(types.ReclaimFilter{Assignees: []string{"wc"}}); !eq(got, []string{"rs-c1"}) {
		t.Fatalf("reclaim(--assignee wc) = %v, want [rs-c1]", got)
	}
	if !inProgress("rs-a1") || !inProgress("rs-a2") {
		t.Fatalf("assignee-scoped reclaim disturbed out-of-scope claims")
	}

	// --id: exactly the named issue, not its lane-mate rs-a2.
	if got := reclaim(types.ReclaimFilter{IDs: []string{"rs-a1"}}); !eq(got, []string{"rs-a1"}) {
		t.Fatalf("reclaim(--id rs-a1) = %v, want [rs-a1]", got)
	}
	if !inProgress("rs-a2") {
		t.Fatalf("id-scoped reclaim disturbed rs-a2")
	}

	// --exclude-label: rs-a2 is the only stale lease left, and it carries the
	// excluded label, so the reaper reverts nothing (fail-closed on no match).
	if got := reclaim(types.ReclaimFilter{ExcludeLabels: []string{"tier:opus"}}); len(got) != 0 {
		t.Fatalf("reclaim(--exclude-label tier:opus) = %v, want []", got)
	}
	if !inProgress("rs-a2") {
		t.Fatalf("exclude-label reclaim wrongly reverted the excluded rs-a2")
	}

	// --label-any: OR-set matches rs-a2 via lane-a; the previous owner is reported.
	got, err := s.ReclaimExpiredLeases(ctx(), -time.Hour, types.ReclaimFilter{LabelsAny: []string{"lane-a", "lane-z"}}, "reaper")
	must(t, err)
	if len(got) != 1 || got[0].ID != "rs-a2" || got[0].PreviousOwner != "wa" {
		t.Fatalf("reclaim(--label-any lane-a,lane-z) = %+v, want [rs-a2 held by wa]", got)
	}
	if inProgress("rs-a2") {
		t.Fatalf("rs-a2 should be reverted to open after its lane was reclaimed")
	}
}
