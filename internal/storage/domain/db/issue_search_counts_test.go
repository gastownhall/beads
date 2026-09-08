package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueSearchAcrossIssuesAndWispsWithCounts() {
	s.Run("DependencyAndDependentCounts", s.searchCountsDepAndRDep)
	s.Run("CommentCount", s.searchCountsComment)
	s.Run("ParentPopulated", s.searchCountsParent)
	s.Run("MergesIssuesAndWisps", s.searchCountsMergesTables)
	s.Run("SkipWispsExcludesWisps", s.searchCountsSkipWisps)
	s.Run("EphemeralTrueOnlyWisps", s.searchCountsEphemeralOnly)
	s.Run("LabelHydration", s.searchCountsLabelHydration)
	s.Run("SkipLabelsLeavesEmpty", s.searchCountsSkipLabels)
	s.Run("SortByPriorityThenCreatedAt", s.searchCountsSortOrder)
	s.Run("LimitRespected", s.searchCountsLimit)
	s.Run("CollisionAcrossTablesIsError", s.searchCountsCollision)
}

func (s *testSuite) searchCountsDepAndRDep() {
	r := s.issueRepo()
	dep := s.depRepo()

	mid := newTestIssue("bd-srxc-dr-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-dr-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-dr-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))
	c := newTestIssue("bd-srxc-dr-c", "c")
	s.Require().NoError(r.Insert(s.Ctx(), c, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-mid", "bd-srxc-dr-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-mid", "bd-srxc-dr-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-dr-c", "bd-srxc-dr-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-dr-mid"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Equal(2, out.Items[0].DependencyCount, "outgoing blocks count")
	s.Equal(1, out.Items[0].DependentCount, "incoming blocks count")
}

func (s *testSuite) searchCountsComment() {
	r := s.issueRepo()
	issue := newTestIssue("bd-srxc-cmt-1", "with comments")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))

	for i := 0; i < 3; i++ {
		_, err := s.Runner().ExecContext(s.Ctx(),
			"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
			"bd-srxc-cmt-1", "tester", "comment")
		s.Require().NoError(err)
	}

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-cmt-1"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Equal(3, out.Items[0].CommentCount)
}

func (s *testSuite) searchCountsParent() {
	r := s.issueRepo()
	dep := s.depRepo()
	parent := newTestIssue("bd-srxc-par-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	child := newTestIssue("bd-srxc-par-child", "child")
	s.Require().NoError(r.Insert(s.Ctx(), child, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-srxc-par-child", "bd-srxc-par-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-par-child"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Require().NotNil(out.Items[0].Parent)
	s.Equal("bd-srxc-par-parent", *out.Items[0].Parent)
}

func (s *testSuite) searchCountsMergesTables() {
	r := s.issueRepo()
	perm := newTestIssue("bd-srxc-mrg-perm", "perm")
	s.Require().NoError(r.Insert(s.Ctx(), perm, "tester", domain.InsertIssueOpts{}))

	w := newTestIssue("bd-srxc-mrg-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-mrg-"})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-mrg-perm")
	s.Contains(got, "bd-srxc-mrg-wisp")
}

func (s *testSuite) searchCountsSkipWisps() {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-srxc-sk-perm", "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue("bd-srxc-sk-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-sk-", SkipWisps: true})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-sk-perm")
	s.NotContains(got, "bd-srxc-sk-wisp")
}

func (s *testSuite) searchCountsEphemeralOnly() {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-srxc-eo-perm", "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue("bd-srxc-eo-wisp", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	yes := true
	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-eo-", Ephemeral: &yes})
	s.Require().NoError(err)
	got := iwcIDs(out)
	s.Contains(got, "bd-srxc-eo-wisp")
	s.NotContains(got, "bd-srxc-eo-perm")
}

func (s *testSuite) searchCountsLabelHydration() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	issue := newTestIssue("bd-srxc-lbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-lbl-1", "alpha", "tester", domain.LabelOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-lbl-1", "beta", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-lbl-1"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.ElementsMatch([]string{"alpha", "beta"}, out.Items[0].Issue.Labels)
}

func (s *testSuite) searchCountsSkipLabels() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	issue := newTestIssue("bd-srxc-nolbl-1", "labeled")
	s.Require().NoError(r.Insert(s.Ctx(), issue, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-nolbl-1", "gamma", "tester", domain.LabelOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-nolbl-1"}, SkipWisps: true, SkipLabels: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1)
	s.Empty(out.Items[0].Issue.Labels)
}

func (s *testSuite) searchCountsSortOrder() {
	r := s.issueRepo()
	hi := newTestIssue("bd-srxc-srt-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-srt-mid", "mid")
	mid.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	lo := newTestIssue("bd-srxc-srt-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-srt-", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 3)
	s.Equal("bd-srxc-srt-hi", out.Items[0].Issue.ID)
	s.Equal("bd-srxc-srt-mid", out.Items[1].Issue.ID)
	s.Equal("bd-srxc-srt-lo", out.Items[2].Issue.ID)
}

func (s *testSuite) searchCountsLimit() {
	r := s.issueRepo()
	for i := 0; i < 5; i++ {
		s.Require().NoError(r.Insert(s.Ctx(),
			newTestIssue("bd-srxc-lim-"+string(rune('a'+i)), "x"),
			"tester", domain.InsertIssueOpts{}))
	}
	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-lim-", Limit: 3, SkipWisps: true})
	s.Require().NoError(err)
	s.Len(out.Items, 3)
}

func (s *testSuite) searchCountsCollision() {
	r := s.issueRepo()
	const id = "bd-srxc-coll-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue(id, "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	_, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-coll-"})
	s.Require().Error(err)
	s.Contains(err.Error(), "exists in both issues and wisps")
}

func iwcIDs(page domain.SearchCountsPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, iwc := range page.Items {
		if iwc == nil || iwc.Issue == nil {
			continue
		}
		out = append(out, iwc.Issue.ID)
	}
	return out
}
