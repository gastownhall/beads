package db

// Seam A cross-mode parity tests (bd-6dnrw.45): the direct/embedded stack
// (internal/storage/issueops, what DoltStore executes) and the domain/db
// repository (what proxied-server mode executes) read the SAME database here,
// so list/ready semantics are compared row-for-row instead of relying on
// mirrored fixtures in two suites that never diff outputs.
//
// Ordering caveat: issueops.SearchIssuesInTx appends wisps after issues
// without re-sorting (the cmd layer re-sorts client-side), while the domain
// union ORDER BYs across both tables, so mixed-table list cases compare
// membership; everything else compares exact sequences.

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) parityBase() time.Time {
	// Wisps live in the dolt-ignored (untracked) schema, so the suite's
	// DOLT_RESET baseline restore does not clear them between tests. Parity
	// compares against an exact corpus, so start from empty wisp tables.
	// Deleting wisps cascades the aux tables; wisp_dependencies has no FK.
	for _, stmt := range []string{"DELETE FROM wisps", "DELETE FROM wisp_dependencies"} {
		if _, err := s.db.ExecContext(s.Ctx(), stmt); err != nil && !dberrors.IsTableNotExist(err) {
			s.Require().NoError(err)
		}
	}
	return time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
}

func (s *testSuite) seedParityIssue(id string, mutate func(*types.Issue), wisp bool) {
	iss := newTestIssue(id, "parity "+id)
	if mutate != nil {
		mutate(iss)
	}
	s.Require().NoError(s.issueRepo().Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{UseWispsTable: wisp}))
}

func (s *testSuite) beginClassicTx() *sql.Tx {
	tx, err := s.db.BeginTx(s.Ctx(), nil)
	s.Require().NoError(err)
	return tx
}

func (s *testSuite) classicList(filter types.IssueFilter) []*types.Issue {
	tx := s.beginClassicTx()
	defer func() { _ = tx.Rollback() }()
	out, err := issueops.SearchIssuesInTx(s.Ctx(), tx, "", filter)
	s.Require().NoError(err)
	return out
}

func (s *testSuite) classicListWithCounts(filter types.IssueFilter) []*types.IssueWithCounts {
	tx := s.beginClassicTx()
	defer func() { _ = tx.Rollback() }()
	out, err := issueops.SearchIssuesWithCountsInTx(s.Ctx(), tx, "", filter)
	s.Require().NoError(err)
	return out
}

func (s *testSuite) classicReady(filter types.WorkFilter) []*types.Issue {
	tx := s.beginClassicTx()
	defer func() { _ = tx.Rollback() }()
	out, err := issueops.GetReadyWorkInTx(s.Ctx(), tx, filter)
	s.Require().NoError(err)
	return out
}

// classicAddDep writes a dependency through the direct stack (committed), the
// same path DoltStore.AddDependency runs, including is_blocked maintenance.
func (s *testSuite) classicAddDep(issueID, dependsOn string, depType types.DependencyType) {
	tx := s.beginClassicTx()
	dep := &types.Dependency{IssueID: issueID, DependsOnID: dependsOn, Type: depType}
	s.Require().NoError(issueops.AddDependencyInTx(s.Ctx(), tx, dep, "tester", issueops.AddDependencyOpts{}))
	s.Require().NoError(tx.Commit())
}

func (s *testSuite) domainList(filter types.IssueFilter) []*types.Issue {
	page, err := s.issueRepo().SearchAcrossIssuesAndWisps(s.Ctx(), "", filter)
	s.Require().NoError(err)
	return page.Items
}

func (s *testSuite) domainListWithCounts(filter types.IssueFilter) []*types.IssueWithCounts {
	page, err := s.issueRepo().SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "", filter)
	s.Require().NoError(err)
	return page.Items
}

func (s *testSuite) domainReady(filter types.WorkFilter) domain.SearchPage {
	page, err := s.issueRepo().GetReadyWork(s.Ctx(), filter)
	s.Require().NoError(err)
	return page
}

func idsOf(items []*types.Issue) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func idsOfCounts(items []*types.IssueWithCounts) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Issue.ID)
	}
	return out
}

// Case 1: status filter over mixed statuses — identical ID order.
func (s *testSuite) TestParityListStatusFilter() {
	base := s.parityBase()
	mk := func(id string, status types.Status, prio, minute int) {
		s.seedParityIssue(id, func(i *types.Issue) {
			i.Status = status
			i.Priority = prio
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
			if status == types.StatusClosed {
				closedAt := i.CreatedAt.Add(time.Minute)
				i.ClosedAt = &closedAt
			}
		}, false)
	}
	mk("bd-par-st-a", types.StatusOpen, 1, 0)
	mk("bd-par-st-b", types.StatusInProgress, 2, 1)
	mk("bd-par-st-c", types.StatusClosed, 0, 2)
	mk("bd-par-st-d", types.StatusOpen, 2, 3)

	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusClosed} {
		st := status
		filter := types.IssueFilter{Status: &st}
		classic := idsOf(s.classicList(filter))
		dom := idsOf(s.domainList(filter))
		s.Equal(classic, dom, "status=%s: direct and domain stacks must return the same ID sequence", status)
	}
}

// Case 2: wisp inclusion — 5 permanent + 3 wisps (2 ephemeral, 1 NoHistory).
// Pins the ready_work_union forced-IncludeEphemeral fix (bd-6dnrw.44 item 2).
func (s *testSuite) TestParityWispInclusion() {
	base := s.parityBase()
	for n := 1; n <= 5; n++ {
		minute := n
		s.seedParityIssue(fmt.Sprintf("bd-par-w-p%d", n), func(i *types.Issue) {
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}
	s.seedParityIssue("bd-par-w-eph1", func(i *types.Issue) {
		i.Ephemeral = true
		i.CreatedAt = base.Add(10 * time.Minute)
	}, true)
	s.seedParityIssue("bd-par-w-eph2", func(i *types.Issue) {
		i.Ephemeral = true
		i.CreatedAt = base.Add(11 * time.Minute)
	}, true)
	s.seedParityIssue("bd-par-w-nohist", func(i *types.Issue) {
		i.Ephemeral = false
		i.CreatedAt = base.Add(12 * time.Minute)
	}, true)

	permanents := []string{"bd-par-w-p1", "bd-par-w-p2", "bd-par-w-p3", "bd-par-w-p4", "bd-par-w-p5"}
	all := append(append([]string{}, permanents...), "bd-par-w-eph1", "bd-par-w-eph2", "bd-par-w-nohist")
	nonEphemeral := append(append([]string{}, permanents...), "bd-par-w-nohist")

	// List, Ephemeral=nil: everything from both tables (membership; see header).
	classic := idsOf(s.classicList(types.IssueFilter{}))
	dom := idsOf(s.domainList(types.IssueFilter{}))
	s.ElementsMatch(classic, dom, "list all: same membership")
	s.ElementsMatch(all, dom, "list all: domain membership")

	// List, Ephemeral=false: NoHistory wisp survives, true ephemerals do not.
	ephFalse := false
	classic = idsOf(s.classicList(types.IssueFilter{Ephemeral: &ephFalse}))
	dom = idsOf(s.domainList(types.IssueFilter{Ephemeral: &ephFalse}))
	s.ElementsMatch(classic, dom, "list non-ephemeral: same membership")
	s.ElementsMatch(nonEphemeral, dom, "list non-ephemeral: domain membership")

	// Ready, default: ephemeral wisps excluded, NoHistory wisp included.
	classic = idsOf(s.classicReady(types.WorkFilter{}))
	dom = idsOf(s.domainReady(types.WorkFilter{}).Items)
	s.ElementsMatch(classic, dom, "ready default: same membership")
	s.ElementsMatch(nonEphemeral, dom, "ready default: ephemeral wisps must not leak in")

	// Ready, IncludeEphemeral: both stacks include the ephemerals.
	classic = idsOf(s.classicReady(types.WorkFilter{IncludeEphemeral: true}))
	dom = idsOf(s.domainReady(types.WorkFilter{IncludeEphemeral: true}).Items)
	s.ElementsMatch(classic, dom, "ready include-ephemeral: same membership")
	s.ElementsMatch(all, dom, "ready include-ephemeral: domain membership")
}

// Case 3: Limit N and Limit 0 return the same sequences (issues only).
func (s *testSuite) TestParityListLimit() {
	base := s.parityBase()
	for n := 1; n <= 6; n++ {
		minute, prio := n, n%3
		s.seedParityIssue(fmt.Sprintf("bd-par-lim-%d", n), func(i *types.Issue) {
			i.Priority = prio
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}

	full := idsOf(s.classicList(types.IssueFilter{}))
	s.Equal(full, idsOf(s.domainList(types.IssueFilter{})), "limit 0: same sequence")
	s.Len(full, 6)

	classic := idsOf(s.classicList(types.IssueFilter{Limit: 4}))
	dom := idsOf(s.domainList(types.IssueFilter{Limit: 4}))
	s.Equal(classic, dom, "limit 4: same sequence")
	s.Equal(full[:4], dom, "limit 4 must be a prefix of the unlimited sequence")
}

// Case 4: ready with a blocked dependency graph — the case the proxied CLI
// cannot express end-to-end (bd dep add has no proxied dispatch). The b<-a
// edge is written through the DOMAIN dependency use case, pinning is_blocked
// maintenance in DependencySQLRepository.Insert (bd-6dnrw.44 item 3); the
// f<-g edge goes through the direct stack as the ground truth control.
func (s *testSuite) TestParityReadyBlockedGraph() {
	base := s.parityBase()
	for n, id := range []string{"bd-par-rdy-a", "bd-par-rdy-b", "bd-par-rdy-c", "bd-par-rdy-d", "bd-par-rdy-e", "bd-par-rdy-f", "bd-par-rdy-g"} {
		minute := n
		s.seedParityIssue(id, func(i *types.Issue) {
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}

	depUC := domain.NewDependencyUseCase(NewDependencySQLRepository(s.Runner()))
	s.Require().NoError(depUC.AddDependency(s.Ctx(),
		&types.Dependency{IssueID: "bd-par-rdy-b", DependsOnID: "bd-par-rdy-a", Type: types.DepBlocks}, "tester"))

	s.classicAddDep("bd-par-rdy-f", "bd-par-rdy-g", types.DepBlocks)

	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET pinned = 1 WHERE id = ?", "bd-par-rdy-d")
	s.Require().NoError(err)
	_, err = s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET defer_until = DATE_ADD(UTC_TIMESTAMP(), INTERVAL 1 DAY) WHERE id = ?", "bd-par-rdy-e")
	s.Require().NoError(err)

	want := []string{"bd-par-rdy-a", "bd-par-rdy-c", "bd-par-rdy-g"}
	classic := idsOf(s.classicReady(types.WorkFilter{}))
	dom := idsOf(s.domainReady(types.WorkFilter{}).Items)
	s.Equal(classic, dom, "ready blocked graph: same sequence")
	s.ElementsMatch(want, dom, "ready blocked graph: blocked/pinned/deferred excluded regardless of which stack wrote the edge")
}

// Case 4b: descendant expansion on blocking-edge insert (bd-6dnrw.44 item 3
// residual): a blocks edge added to a PARENT must also drop its parent-child
// descendants out of ready, whichever stack writes the edge. Case 4 is flat,
// so it cannot see this; the empirical probe on 27bbecbd1 showed the domain
// path leaving the child is_blocked=0.
func (s *testSuite) TestParityReadyBlockedDescendants() {
	base := s.parityBase()
	for n, id := range []string{"bd-par-desc-x", "bd-par-desc-p", "bd-par-desc-c", "bd-par-desc-g", "bd-par-desc-n"} {
		minute := n
		s.seedParityIssue(id, func(i *types.Issue) {
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}

	// Tree structure via the direct stack: c is a child of p, g a child of c.
	s.classicAddDep("bd-par-desc-c", "bd-par-desc-p", types.DepParentChild)
	s.classicAddDep("bd-par-desc-g", "bd-par-desc-c", types.DepParentChild)

	// The blocking edge on the PARENT goes through the domain use case — the
	// write path that used to mark only the source row.
	depUC := domain.NewDependencyUseCase(NewDependencySQLRepository(s.Runner()))
	s.Require().NoError(depUC.AddDependency(s.Ctx(),
		&types.Dependency{IssueID: "bd-par-desc-p", DependsOnID: "bd-par-desc-x", Type: types.DepBlocks}, "tester"))

	classic := idsOf(s.classicReady(types.WorkFilter{}))
	dom := idsOf(s.domainReady(types.WorkFilter{}).Items)
	s.Equal(classic, dom, "ready after domain blocks-on-parent: same sequence")
	s.ElementsMatch([]string{"bd-par-desc-x", "bd-par-desc-n"}, dom,
		"descendants of a blocked parent must drop out of ready")

	// A new child attached UNDER the blocked parent through the domain path
	// must inherit blocked state (parent-child insert runs the recompute).
	s.Require().NoError(depUC.AddDependency(s.Ctx(),
		&types.Dependency{IssueID: "bd-par-desc-n", DependsOnID: "bd-par-desc-p", Type: types.DepParentChild}, "tester"))

	classic = idsOf(s.classicReady(types.WorkFilter{}))
	dom = idsOf(s.domainReady(types.WorkFilter{}).Items)
	s.Equal(classic, dom, "ready after domain parent-child under blocked parent: same sequence")
	s.ElementsMatch([]string{"bd-par-desc-x"}, dom, "new child of a blocked parent must not be ready")
}

// Case 5: ready limit boundary — membership parity at the cut, and the domain
// HasMore flag agrees with what the direct stack's unlimited read implies.
func (s *testSuite) TestParityReadyLimitBoundary() {
	base := s.parityBase()
	for n := 1; n <= 5; n++ {
		minute, prio := n, n%2
		s.seedParityIssue(fmt.Sprintf("bd-par-rlb-%d", n), func(i *types.Issue) {
			i.Priority = prio
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}

	unlimited := idsOf(s.classicReady(types.WorkFilter{}))
	s.Len(unlimited, 5)

	for _, limit := range []int{3, 5} {
		classic := idsOf(s.classicReady(types.WorkFilter{Limit: limit}))
		page := s.domainReady(types.WorkFilter{Limit: limit})
		s.Equal(classic, idsOf(page.Items), "ready limit %d: same sequence", limit)
		s.Equal(len(unlimited) > limit, page.HasMore, "ready limit %d: HasMore must match the unlimited row count", limit)
	}
}

// Case 6: dep/rdep/comment counts and parent projection agree per ID.
func (s *testSuite) TestParityCountsProjection() {
	base := s.parityBase()
	for n, id := range []string{"bd-par-cnt-x", "bd-par-cnt-y", "bd-par-cnt-z", "bd-par-cnt-p"} {
		minute := n
		s.seedParityIssue(id, func(i *types.Issue) {
			i.CreatedAt = base.Add(time.Duration(minute) * time.Minute)
		}, false)
	}

	s.classicAddDep("bd-par-cnt-y", "bd-par-cnt-x", types.DepBlocks)
	s.classicAddDep("bd-par-cnt-z", "bd-par-cnt-p", types.DepParentChild)

	for n, issueID := range []string{"bd-par-cnt-x", "bd-par-cnt-x", "bd-par-cnt-z"} {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO comments (id, issue_id, author, text) VALUES (?, ?, 'tester', 'parity comment')",
			fmt.Sprintf("par-cnt-comment-%d", n), issueID)
		s.Require().NoError(err)
	}

	type counts struct {
		deps, rdeps, comments int
		parent                string
	}
	collect := func(items []*types.IssueWithCounts) map[string]counts {
		out := make(map[string]counts, len(items))
		for _, it := range items {
			c := counts{deps: it.DependencyCount, rdeps: it.DependentCount, comments: it.CommentCount}
			if it.Parent != nil {
				c.parent = *it.Parent
			}
			out[it.Issue.ID] = c
		}
		return out
	}

	classicItems := s.classicListWithCounts(types.IssueFilter{})
	domItems := s.domainListWithCounts(types.IssueFilter{})
	s.Equal(idsOfCounts(classicItems), idsOfCounts(domItems), "counts list: same sequence")

	classic, dom := collect(classicItems), collect(domItems)
	s.Equal(classic, dom, "counts list: per-ID projections must match")
	s.Equal(counts{deps: 0, rdeps: 1, comments: 2}, dom["bd-par-cnt-x"])
	s.Equal(counts{deps: 1, rdeps: 0, comments: 0}, dom["bd-par-cnt-y"])
	s.Equal(counts{deps: 0, rdeps: 0, comments: 1, parent: "bd-par-cnt-p"}, dom["bd-par-cnt-z"])
}

// Case 7: label AND / ANY / exclude sets.
func (s *testSuite) TestParityLabelFilters() {
	base := s.parityBase()
	labels := map[string][]string{
		"bd-par-lbl-1": {"red", "blue"},
		"bd-par-lbl-2": {"red"},
		"bd-par-lbl-3": {"blue"},
		"bd-par-lbl-4": nil,
	}
	labelRepo := NewLabelSQLRepository(s.Runner())
	minute := 0
	for _, id := range []string{"bd-par-lbl-1", "bd-par-lbl-2", "bd-par-lbl-3", "bd-par-lbl-4"} {
		m := minute
		s.seedParityIssue(id, func(i *types.Issue) {
			i.CreatedAt = base.Add(time.Duration(m) * time.Minute)
		}, false)
		minute++
		for _, label := range labels[id] {
			s.Require().NoError(labelRepo.Insert(s.Ctx(), id, label, "tester", domain.LabelOpts{}))
		}
	}

	cases := []struct {
		name   string
		filter types.IssueFilter
		want   []string
	}{
		{"and", types.IssueFilter{Labels: []string{"red", "blue"}}, []string{"bd-par-lbl-1"}},
		{"any", types.IssueFilter{LabelsAny: []string{"red", "blue"}}, []string{"bd-par-lbl-1", "bd-par-lbl-2", "bd-par-lbl-3"}},
		{"exclude", types.IssueFilter{ExcludeLabels: []string{"red"}}, []string{"bd-par-lbl-3", "bd-par-lbl-4"}},
	}
	for _, tc := range cases {
		classic := idsOf(s.classicList(tc.filter))
		dom := idsOf(s.domainList(tc.filter))
		s.ElementsMatch(classic, dom, "labels %s: same membership", tc.name)
		s.ElementsMatch(tc.want, dom, "labels %s: domain membership", tc.name)
	}
}

// Case 8: sort tie-breaking with equal priority and created_at — the likeliest
// real divergence (SQL ORDER BY vs Go-side sorts). Insert out of lexical order
// so an id ASC tiebreak is observable, and run the counts path too, which had
// its own ordering bugs (bd-6dnrw.43).
func (s *testSuite) TestParitySortTieBreak() {
	s.parityBase()
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"bd-par-tie-c", "bd-par-tie-a", "bd-par-tie-d", "bd-par-tie-b"} {
		s.seedParityIssue(id, func(i *types.Issue) {
			i.CreatedAt = created
		}, false)
	}
	wantLexical := []string{"bd-par-tie-a", "bd-par-tie-b", "bd-par-tie-c", "bd-par-tie-d"}

	for _, sortBy := range []string{"", "priority", "created"} {
		filter := types.IssueFilter{SortBy: sortBy}
		classic := idsOf(s.classicList(filter))
		dom := idsOf(s.domainList(filter))
		s.Equal(classic, dom, "sort=%q: same sequence", sortBy)
		s.Equal(wantLexical, dom, "sort=%q: ties must break on id ASC", sortBy)

		classicCounts := idsOfCounts(s.classicListWithCounts(filter))
		domCounts := idsOfCounts(s.domainListWithCounts(filter))
		s.Equal(classicCounts, domCounts, "sort=%q (counts): same sequence", sortBy)
		s.Equal(wantLexical, domCounts, "sort=%q (counts): ties must break on id ASC", sortBy)
	}
}
