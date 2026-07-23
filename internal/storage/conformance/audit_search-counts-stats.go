package conformance

import (
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Audit cases for the "search-counts-stats" slice. Each case encodes a strange
// but real behavior of the embedded-Dolt reference (the oracle) that the shared
// SQL surface — SearchIssuesWithCounts, CountIssuesByGroup, GetStatistics,
// GetStaleIssues, GetEpicsEligibleForClosure, GetIssuesByLabel, and the default
// SearchIssues ordering contract — is expected to reproduce on every backend.
//
// Grounded in: sqlbuild/counts.go (the counts mega-query), issueops/count.go
// (COALESCE(col,'') + P-prefix / (unassigned) / (no labels) normalization),
// issueops/statistics.go (blocked count + Ready clamp), issueops/stale.go
// (status override + ephemeral exclusion), issueops/epic_closure.go (zero-child
// skip), issueops/bulk_ops.go (wisp_labels union), and sqlbuild/sort.go
// (priority/created_at/id and NULLS-last-on-DESC ordering).

// RunAudit_search_counts_stats runs every store case in this slice.
func RunAudit_search_counts_stats(t *testing.T, f Factory) {
	t.Helper()
	t.Run("SearchIssuesWithCounts", func(t *testing.T) { testAuditSearchIssuesWithCounts(t, f) })
	t.Run("ReadyWorkDepCreatedAtParity", func(t *testing.T) { testAuditReadyWorkDepCreatedAtParity(t, f) })
	t.Run("CountByPriority", func(t *testing.T) { testAuditCountByPriority(t, f) })
	t.Run("CountByLabel", func(t *testing.T) { testAuditCountByLabel(t, f) })
	t.Run("CountByAssigneeAndType", func(t *testing.T) { testAuditCountByAssigneeAndType(t, f) })
	t.Run("Statistics", func(t *testing.T) { testAuditStatistics(t, f) })
	t.Run("StatisticsReadyClamp", func(t *testing.T) { testAuditStatisticsReadyClamp(t, f) })
	t.Run("SearchDefaultOrderTieBreak", func(t *testing.T) { testAuditSearchDefaultOrderTieBreak(t, f) })
	t.Run("SearchIdenticalTimestampIDOrder", func(t *testing.T) { testAuditSearchIdenticalTimestampIDOrder(t, f) })
	t.Run("SearchSortByClosedNullsLast", func(t *testing.T) { testAuditSearchSortByClosedNullsLast(t, f) })
	t.Run("SearchTextIDBranchExternalRef", func(t *testing.T) { testAuditSearchTextIDBranchExternalRef(t, f) })
	t.Run("SearchIDPrefixCaseSensitive", func(t *testing.T) { testAuditSearchIDPrefixCaseSensitive(t, f) })
	t.Run("SearchParentDescendantCaseSensitive", func(t *testing.T) { testAuditSearchParentDescendantCaseSensitive(t, f) })
	t.Run("StaleStatusOverride", func(t *testing.T) { testAuditStaleStatusOverride(t, f) })
	t.Run("EpicsEligiblePartial", func(t *testing.T) { testAuditEpicsEligiblePartial(t, f) })
	t.Run("GetIssuesByLabelWithWisp", func(t *testing.T) { testAuditGetIssuesByLabelWithWisp(t, f) })
	t.Run("WispMergeSearchCount", func(t *testing.T) { testAuditWispMergeSearchCount(t, f) })
}

// --- file-private helpers (audit-prefixed to avoid collisions) ---

func auditCountsByID(items []*types.IssueWithCounts) map[string]*types.IssueWithCounts {
	m := make(map[string]*types.IssueWithCounts, len(items))
	for _, it := range items {
		if it != nil && it.Issue != nil {
			m[it.Issue.ID] = it
		}
	}
	return m
}

func auditWholeSec(y int) time.Time {
	return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
}

// --- cases ---

// The counts mega-query (sqlbuild.SearchCountsSQL) computes DependencyCount from
// outgoing type='blocks' edges, DependentCount from reverse blockers, CommentCount,
// and Parent from the MIN parent-child target. Every construct is dialect text
// (JSON_ARRAYAGG / DATE_FORMAT / CAST / MIN(COALESCE)) translated verbatim.
func testAuditSearchIssuesWithCounts(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "swc-e", Title: "Epic", IssueType: types.TypeEpic}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "swc-c1", Title: "Child"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "swc-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "swc-b", Title: "B"}), "a"))
	// swc-a depends_on swc-b (blocks); swc-b depends_on swc-c1 (blocks); swc-c1 parent-child swc-e.
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "swc-a", DependsOnID: "swc-b", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "swc-b", DependsOnID: "swc-c1", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "swc-c1", DependsOnID: "swc-e", Type: types.DepParentChild}, "a"))
	_, err := s.AddIssueComment(c, "swc-a", "alice", "one")
	must(t, err)
	_, err = s.AddIssueComment(c, "swc-a", "bob", "two")
	must(t, err)
	must(t, s.AddLabel(c, "swc-a", "bug", "a"))

	items, err := s.SearchIssuesWithCounts(c, "", types.IssueFilter{})
	must(t, err)
	byID := auditCountsByID(items)

	a := byID["swc-a"]
	if a == nil {
		t.Fatal("swc-a missing from counts result")
	}
	if a.DependencyCount != 1 {
		t.Errorf("swc-a DependencyCount = %d, want 1 (outgoing blocks)", a.DependencyCount)
	}
	if a.DependentCount != 0 {
		t.Errorf("swc-a DependentCount = %d, want 0", a.DependentCount)
	}
	if a.CommentCount != 2 {
		t.Errorf("swc-a CommentCount = %d, want 2", a.CommentCount)
	}
	if a.Parent != nil {
		t.Errorf("swc-a Parent = %v, want nil", *a.Parent)
	}
	if !contains(a.Labels, "bug") {
		t.Errorf("swc-a Labels = %v, want to include bug", a.Labels)
	}

	b := byID["swc-b"]
	if b == nil {
		t.Fatal("swc-b missing from counts result")
	}
	if b.DependencyCount != 1 {
		t.Errorf("swc-b DependencyCount = %d, want 1 (blocked by swc-c1)", b.DependencyCount)
	}
	if b.DependentCount != 1 {
		t.Errorf("swc-b DependentCount = %d, want 1 (swc-a depends on it)", b.DependentCount)
	}

	c1 := byID["swc-c1"]
	if c1 == nil {
		t.Fatal("swc-c1 missing from counts result")
	}
	if c1.Parent == nil || *c1.Parent != "swc-e" {
		t.Errorf("swc-c1 Parent = %v, want swc-e", c1.Parent)
	}
	if c1.DependentCount != 1 {
		t.Errorf("swc-c1 DependentCount = %d, want 1 (swc-b depends on it)", c1.DependentCount)
	}
}

// The counts mega-query renders each dependency's created_at through
// DATE_FORMAT(created_at,'%Y-%m-%dT%H:%i:%sZ') into deps_json, and every backend must
// reproduce the stored timestamp — not the zero time. This is the assertion the suite
// was missing: DependencyCount only proves the edge exists, so a backend that rendered
// the edge's created_at as NULL/zero stayed green. SQLite does exactly that when a
// dependency created_at bound as a Go time.Time is stored in t.String() form and
// strftime cannot parse it — the reason the DSN must set _time_format=datetime. The
// batch/import path (CreateIssuesWithFullOptions, i.e. `bd import`) binds dep.CreatedAt
// verbatim, so it is the path that exposes the divergence. GetDependencies returns the
// target issues, not the edges, so it cannot witness the edge timestamp; assert the
// rendered edge created_at equals the value that was imported.
func testAuditReadyWorkDepCreatedAtParity(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	// A fixed non-zero past timestamp, whole-second to match DATE_FORMAT's granularity.
	depCreatedAt := time.Date(2023, 5, 15, 10, 20, 30, 0, time.UTC)

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "dca-t", Title: "target"}), "a"))
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{
		withDefaults(&types.Issue{
			ID:    "dca-s",
			Title: "source",
			Dependencies: []*types.Dependency{
				{IssueID: "dca-s", DependsOnID: "dca-t", Type: types.DepBlocks, CreatedAt: depCreatedAt},
			},
		}),
	}, "a", storage.BatchCreateOptions{OrphanHandling: storage.OrphanAllow, SkipPrefixValidation: true}))

	// Rendered path: SearchIssuesWithCounts (bd list --with-counts) and
	// GetReadyWorkWithCounts (bd ready) share ScanReadyWorkRowWithCounts, which parses
	// deps_json into issue.Dependencies. The rendered created_at must be the real
	// imported timestamp, not the zero time a NULL DATE_FORMAT would unmarshal to.
	items, err := s.SearchIssuesWithCounts(c, "", types.IssueFilter{})
	must(t, err)
	src := auditCountsByID(items)["dca-s"]
	if src == nil {
		t.Fatal("dca-s missing from SearchIssuesWithCounts result")
	}
	if len(src.Dependencies) != 1 {
		t.Fatalf("dca-s rendered deps = %d, want 1", len(src.Dependencies))
	}
	got := src.Dependencies[0].CreatedAt
	if got.IsZero() {
		t.Fatal("rendered dependency created_at is the zero time: deps_json lost the timestamp " +
			"(SQLite DATE_FORMAT/strftime parity break — DSN needs _time_format=datetime)")
	}
	if !got.Equal(depCreatedAt) {
		t.Fatalf("rendered dependency created_at = %v, want %v (imported edge timestamp)", got, depCreatedAt)
	}
}

// countByColumnInTx emits COALESCE(priority, ”) GROUP BY priority; priority is
// integer NOT NULL. Both maintained implementations return a string key, then
// countGroupForTablesInTx prepends 'P'. Reference keys: P0/P1/P2.
func testAuditCountByPriority(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.SetConfig(c, "issue_prefix", "test"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cp-1", Title: "a", Priority: 0}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cp-2", Title: "b", Priority: 1}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cp-3", Title: "c", Priority: 1}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cp-4", Title: "d", Priority: 2}), "a"))

	got, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "priority")
	must(t, err)
	want := map[string]int{"P0": 1, "P1": 2, "P2": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CountIssuesByGroup(priority) = %v, want %v", got, want)
	}
}

// countByLabelInTx does not join: it counts labels via an IN-subquery (dodging the
// Dolt joinIter panic), counts multi-label issues once per label (overlapping
// buckets), and appends a synthetic "(no labels)" bucket ONLY when >0.
func testAuditCountByLabel(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cl-1", Title: "a"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cl-2", Title: "b"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cl-3", Title: "c"}), "a"))
	must(t, s.AddLabel(c, "cl-1", "bug", "a"))
	must(t, s.AddLabel(c, "cl-1", "urgent", "a"))
	must(t, s.AddLabel(c, "cl-2", "bug", "a"))

	got, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "label")
	must(t, err)
	want := map[string]int{"bug": 2, "urgent": 1, "(no labels)": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CountIssuesByGroup(label) = %v, want %v", got, want)
	}

	// With every issue labeled, "(no labels)" is ABSENT (not present with value 0).
	must(t, s.AddLabel(c, "cl-3", "bug", "a"))
	got2, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "label")
	must(t, err)
	if _, present := got2["(no labels)"]; present {
		t.Errorf("(no labels) key present when all issues labeled: %v", got2)
	}
	if got2["bug"] != 3 {
		t.Errorf("bug = %d, want 3", got2["bug"])
	}
}

// assignee: COALESCE(assignee,”) collapses NULL/” into one key, then normalized
// to "(unassigned)". type maps to issue_type and returns raw strings unprefixed.
func testAuditCountByAssigneeAndType(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ca-1", Title: "a", Assignee: "alice", IssueType: "bug"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ca-2", Title: "b", Assignee: "alice", IssueType: "bug"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ca-3", Title: "c", IssueType: "task"}), "a"))

	gotA, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "assignee")
	must(t, err)
	wantA := map[string]int{"alice": 2, "(unassigned)": 1}
	if !reflect.DeepEqual(gotA, wantA) {
		t.Errorf("CountIssuesByGroup(assignee) = %v, want %v", gotA, wantA)
	}

	gotT, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "type")
	must(t, err)
	wantT := map[string]int{"bug": 2, "task": 1}
	if !reflect.DeepEqual(gotT, wantT) {
		t.Errorf("CountIssuesByGroup(type) = %v, want %v (raw, unprefixed)", gotT, wantT)
	}
}

// GetStatistics computes six status counts plus BlockedIssues (is_blocked=1 and
// status not closed/pinned) and PinnedIssues (the pinned=1 column flag, distinct
// from status='pinned'). ReadyIssues = OpenIssues - BlockedIssues clamped at 0.
func testAuditStatistics(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-o1", Title: "o1", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-o2", Title: "o2", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-p1", Title: "p1", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-c1", Title: "c1", Status: types.StatusClosed}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-d1", Title: "d1", Status: types.StatusDeferred}), "a"))
	// pinned=1 column flag, with a non-open/closed status so it isolates PinnedIssues.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "st-pin", Title: "pin", Status: types.StatusPinned, Pinned: true}), "a"))
	// st-p1 (in_progress) blocked by an open issue -> is_blocked=1, counted in BlockedIssues.
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "st-p1", DependsOnID: "st-o1", Type: types.DepBlocks}, "a"))

	stats, err := s.GetStatistics(c)
	must(t, err)
	if stats.TotalIssues != 6 {
		t.Errorf("TotalIssues = %d, want 6", stats.TotalIssues)
	}
	if stats.OpenIssues != 2 {
		t.Errorf("OpenIssues = %d, want 2", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("InProgressIssues = %d, want 1", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("ClosedIssues = %d, want 1", stats.ClosedIssues)
	}
	if stats.DeferredIssues != 1 {
		t.Errorf("DeferredIssues = %d, want 1", stats.DeferredIssues)
	}
	if stats.PinnedIssues != 1 {
		t.Errorf("PinnedIssues = %d, want 1", stats.PinnedIssues)
	}
	if stats.BlockedIssues != 1 {
		t.Errorf("BlockedIssues = %d, want 1", stats.BlockedIssues)
	}
	// Ready = Open(2) - Blocked(1) = 1 (the blocked issue is in_progress, not open).
	if stats.ReadyIssues != 1 {
		t.Errorf("ReadyIssues = %d, want 1", stats.ReadyIssues)
	}
}

// The Ready clamp is load-bearing: when BlockedIssues exceeds OpenIssues,
// OpenIssues - BlockedIssues goes negative and is clamped to 0.
func testAuditStatisticsReadyClamp(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	// A blocker plus two in_progress issues blocked by it: zero OPEN issues, two blocked.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-blk", Title: "blk", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-1", Title: "b1", Status: types.StatusInProgress}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-2", Title: "b2", Status: types.StatusInProgress}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "rc-1", DependsOnID: "rc-blk", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "rc-2", DependsOnID: "rc-blk", Type: types.DepBlocks}, "a"))

	stats, err := s.GetStatistics(c)
	must(t, err)
	if stats.OpenIssues != 0 {
		t.Errorf("OpenIssues = %d, want 0", stats.OpenIssues)
	}
	if stats.BlockedIssues != 2 {
		t.Errorf("BlockedIssues = %d, want 2", stats.BlockedIssues)
	}
	// 0 - 2 = -2, clamped to 0.
	if stats.ReadyIssues != 0 {
		t.Errorf("ReadyIssues = %d, want 0 (clamped)", stats.ReadyIssues)
	}
}

// The default/priority sort contract is ORDER BY priority ASC, created_at DESC,
// id ASC. With pinned distinct whole-second timestamps the ordering is fully
// deterministic and portable.
func testAuditSearchDefaultOrderTieBreak(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	t1, t2, t3 := auditWholeSec(2020), auditWholeSec(2021), auditWholeSec(2022)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "so-p0", Title: "p0", Priority: 0, CreatedAt: t1, UpdatedAt: t1}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "so-old", Title: "old", Priority: 1, CreatedAt: t1, UpdatedAt: t1}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "so-mid", Title: "mid", Priority: 1, CreatedAt: t2, UpdatedAt: t2}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "so-new", Title: "new", Priority: 1, CreatedAt: t3, UpdatedAt: t3}), "a"))

	results, err := s.SearchIssues(c, "", types.IssueFilter{})
	must(t, err)
	// priority ASC (p0 first), then created_at DESC among the prio-1 rows.
	want := []string{"so-p0", "so-new", "so-mid", "so-old"}
	if got := orderedIDs(results); !reflect.DeepEqual(got, want) {
		t.Errorf("default order = %v, want %v", got, want)
	}
}

// Same priority + identical whole-second created_at forces the final id-ASC leg.
func testAuditSearchIdenticalTimestampIDOrder(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	tie := auditWholeSec(2020)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "tie-b", Title: "b", Priority: 1, CreatedAt: tie, UpdatedAt: tie}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "tie-a", Title: "a", Priority: 1, CreatedAt: tie, UpdatedAt: tie}), "a"))

	results, err := s.SearchIssues(c, "", types.IssueFilter{})
	must(t, err)
	want := []string{"tie-a", "tie-b"}
	if got := orderedIDs(results); !reflect.DeepEqual(got, want) {
		t.Errorf("identical-timestamp order = %v, want %v (id ASC)", got, want)
	}
}

// SortBy="closed" emits ORDER BY closed_at DESC, id ASC. closed_at is nullable;
// the storage contract places NULLs last on DESC. The open (NULL-closed) rows follow the
// closed ones, ordered by id ASC.
func testAuditSearchSortByClosedNullsLast(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	tNew, tOld := auditWholeSec(2022), auditWholeSec(2021)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cs-open-a", Title: "oa", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cs-open-b", Title: "ob", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cs-closed-new", Title: "cn", Status: types.StatusClosed, ClosedAt: &tNew}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "cs-closed-old", Title: "co", Status: types.StatusClosed, ClosedAt: &tOld}), "a"))

	results, err := s.SearchIssues(c, "", types.IssueFilter{SortBy: "closed"})
	must(t, err)
	want := []string{"cs-closed-new", "cs-closed-old", "cs-open-a", "cs-open-b"}
	if got := orderedIDs(results); !reflect.DeepEqual(got, want) {
		t.Errorf("closed-sort order = %v, want %v (NULLs last on DESC)", got, want)
	}
}

// The text-query ID branch (LooksLikeIssueID) matches external_ref substrings via
// LOWER(external_ref) LIKE, and matches id exactly via id = lowerQuery.
func testAuditSearchTextIDBranchExternalRef(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	ref := "JIRA-1234"
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-9", Title: "Zulu", ExternalRef: &ref}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-8", Title: "Yankee"}), "a"))

	// ID-shaped token hits the external_ref branch (LOWER(external_ref) LIKE).
	got, err := s.SearchIssues(c, "jira-12", types.IssueFilter{})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-9"}) {
		t.Errorf("search 'jira-12' = %v, want [test-9]", ids)
	}

	// ID-shaped token that equals the (lowercase) id hits the id = ? branch.
	got, err = s.SearchIssues(c, "test-9", types.IssueFilter{})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-9"}) {
		t.Errorf("search 'test-9' = %v, want [test-9]", ids)
	}
}

// LIKE over raw-cased operands is case-sensitive on both implementations: Dolt uses
// a binary table collation, and SQLite set PRAGMA case_sensitive_like (formerly sqlite/dsn.go).
// SQLite's default LIKE is
// ASCII-case-insensitive and silently diverged (bd-oyvc2.10). IDPrefix filtering
// (sqlbuild/filter.go `id LIKE ?`) must therefore distinguish test-AB from test-ab.
func testAuditSearchIDPrefixCaseSensitive(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-AB1", Title: "Upper"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-ab2", Title: "Lower"}), "a"))

	got, err := s.SearchIssues(c, "", types.IssueFilter{IDPrefix: "test-AB"})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-AB1"}) {
		t.Errorf("IDPrefix test-AB = %v, want [test-AB1] (LIKE must be case-sensitive)", ids)
	}

	got, err = s.SearchIssues(c, "", types.IssueFilter{IDPrefix: "test-ab"})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-ab2"}) {
		t.Errorf("IDPrefix test-ab = %v, want [test-ab2] (LIKE must be case-sensitive)", ids)
	}
}

// The ParentID descendant branch (sqlbuild/filter.go `id LIKE CONCAT(?, '.%')`)
// is likewise case-sensitive: with two sibling parents differing only by case,
// each with a dotted child and no parent-child dep rows, listing one parent's
// descendants must not leak the other-cased parent's child (bd-oyvc2.10).
func testAuditSearchParentDescendantCaseSensitive(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-pc", Title: "lower parent"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-PC", Title: "upper parent"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-pc.1", Title: "lower child"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-PC.1", Title: "upper child"}), "a"))

	parent := "test-pc"
	got, err := s.SearchIssues(c, "", types.IssueFilter{ParentID: &parent})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-pc.1"}) {
		t.Errorf("ParentID test-pc = %v, want [test-pc.1] (different-cased sibling's child must be excluded)", ids)
	}

	upper := "test-PC"
	got, err = s.SearchIssues(c, "", types.IssueFilter{ParentID: &upper})
	must(t, err)
	if ids := issueIDs(got); !reflect.DeepEqual(ids, []string{"test-PC.1"}) {
		t.Errorf("ParentID test-PC = %v, want [test-PC.1] (different-cased sibling's child must be excluded)", ids)
	}
}

// A non-empty filter.Status REPLACES the default open+in_progress set entirely,
// so Status="closed" returns aged closed issues. Ephemeral rows are always
// excluded (the query hits only the issues table).
func testAuditStaleStatusOverride(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	y2020 := auditWholeSec(2020)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sa-open", Title: "aged open", Status: types.StatusOpen, CreatedAt: y2020, UpdatedAt: y2020}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sa-closed", Title: "aged closed", Status: types.StatusClosed, CreatedAt: y2020, UpdatedAt: y2020}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sa-eph", Title: "aged ephemeral", Status: types.StatusOpen, Ephemeral: true, CreatedAt: y2020, UpdatedAt: y2020}), "a"))

	// Status override: the normally-excluded closed issue is returned.
	closed, err := s.GetStaleIssues(c, types.StaleFilter{Days: 30, Status: "closed"})
	must(t, err)
	if got := orderedIDs(closed); !reflect.DeepEqual(got, []string{"sa-closed"}) {
		t.Errorf("stale(status=closed) = %v, want [sa-closed]", got)
	}

	// Default (open+in_progress): the aged open issue, never the ephemeral one.
	def, err := s.GetStaleIssues(c, types.StaleFilter{Days: 30})
	must(t, err)
	if got := orderedIDs(def); !reflect.DeepEqual(got, []string{"sa-open"}) {
		t.Errorf("stale(default) = %v, want [sa-open] (ephemeral excluded)", got)
	}
}

// Epics with ZERO children are silently skipped; epics WITH children are returned
// with Total/Closed counts and EligibleForClose = (Total>0 && Total==Closed).
func testAuditEpicsEligiblePartial(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-1", Title: "E1", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-1a", Title: "c", Status: types.StatusClosed}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-1b", Title: "o", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-2", Title: "E2", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-2a", Title: "c", Status: types.StatusClosed}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "ee-3", Title: "E3", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "ee-1a", DependsOnID: "ee-1", Type: types.DepParentChild}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "ee-1b", DependsOnID: "ee-1", Type: types.DepParentChild}, "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "ee-2a", DependsOnID: "ee-2", Type: types.DepParentChild}, "a"))

	epics, err := s.GetEpicsEligibleForClosure(c)
	must(t, err)
	byID := make(map[string]*types.EpicStatus, len(epics))
	for _, e := range epics {
		if e != nil && e.Epic != nil {
			byID[e.Epic.ID] = e
		}
	}

	e1 := byID["ee-1"]
	if e1 == nil {
		t.Fatal("ee-1 missing (has children, must be returned)")
	}
	if e1.TotalChildren != 2 || e1.ClosedChildren != 1 || e1.EligibleForClose {
		t.Errorf("ee-1 = {Total:%d Closed:%d Eligible:%v}, want {2 1 false}", e1.TotalChildren, e1.ClosedChildren, e1.EligibleForClose)
	}

	e2 := byID["ee-2"]
	if e2 == nil {
		t.Fatal("ee-2 missing (has children, must be returned)")
	}
	if e2.TotalChildren != 1 || e2.ClosedChildren != 1 || !e2.EligibleForClose {
		t.Errorf("ee-2 = {Total:%d Closed:%d Eligible:%v}, want {1 1 true}", e2.TotalChildren, e2.ClosedChildren, e2.EligibleForClose)
	}

	if _, present := byID["ee-3"]; present {
		t.Error("ee-3 (zero children) must be ABSENT (silent skip)")
	}
}

// GetIssuesByLabel unions wisp_labels: an ephemeral (wisp) issue sharing the label
// IS included. Exact label equality; order is engine-dependent (assert as a set).
func testAuditGetIssuesByLabelWithWisp(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "gil-d1", Title: "d1"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "gil-d2", Title: "d2"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "gil-w1", Title: "w1", Ephemeral: true}), "a"))
	must(t, s.AddLabel(c, "gil-d1", "shared", "a"))
	must(t, s.AddLabel(c, "gil-d2", "other", "a"))
	must(t, s.AddLabel(c, "gil-w1", "shared", "a"))

	issues, err := s.GetIssuesByLabel(c, "shared")
	must(t, err)
	if got := issueIDs(issues); !reflect.DeepEqual(got, []string{"gil-d1", "gil-w1"}) {
		t.Errorf("GetIssuesByLabel(shared) = %v, want {gil-d1 gil-w1} (wisp included, d2 excluded)", got)
	}
}

// The durable+wisp merge: default reads merge the wisps tier; SkipWisps counts the
// durable tier only; Ephemeral=&true routes to wisps; Ephemeral=&false returns
// durable non-ephemeral rows only.
func testAuditWispMergeSearchCount(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "wm-d1", Title: "D1", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "wm-w1", Title: "W1", Status: types.StatusOpen, Ephemeral: true}), "a"))

	total, err := s.CountIssues(c, "", types.IssueFilter{})
	must(t, err)
	if total != 2 {
		t.Errorf("CountIssues (merged) = %d, want 2", total)
	}

	durOnly, err := s.CountIssues(c, "", types.IssueFilter{SkipWisps: true})
	must(t, err)
	if durOnly != 1 {
		t.Errorf("CountIssues(SkipWisps) = %d, want 1", durOnly)
	}

	all, err := s.SearchIssues(c, "", types.IssueFilter{})
	must(t, err)
	if got := issueIDs(all); !reflect.DeepEqual(got, []string{"wm-d1", "wm-w1"}) {
		t.Errorf("SearchIssues (merged) = %v, want {wm-d1 wm-w1}", got)
	}

	yes := true
	onlyWisp, err := s.SearchIssues(c, "", types.IssueFilter{Ephemeral: &yes})
	must(t, err)
	if got := issueIDs(onlyWisp); !reflect.DeepEqual(got, []string{"wm-w1"}) {
		t.Errorf("SearchIssues(Ephemeral=true) = %v, want [wm-w1]", got)
	}

	no := false
	onlyDur, err := s.SearchIssues(c, "", types.IssueFilter{Ephemeral: &no})
	must(t, err)
	if got := issueIDs(onlyDur); !reflect.DeepEqual(got, []string{"wm-d1"}) {
		t.Errorf("SearchIssues(Ephemeral=false) = %v, want [wm-d1]", got)
	}

	byStatus, err := s.CountIssuesByGroup(c, types.IssueFilter{}, "status")
	must(t, err)
	if byStatus["open"] != 2 {
		t.Errorf("CountIssuesByGroup(status) open = %d, want 2 (merged)", byStatus["open"])
	}
}
