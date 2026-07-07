package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// bd-6dnrw.44 item 11: the descendants CTE walked only parent-child edges,
// so children that exist purely by dotted-ID convention (classic ParentID
// fallback, issueops/filters.go) were dropped from --tree --parent under the
// proxied stack.
func (s *testSuite) TestGetDescendantsDottedOrphans() {
	r := s.issueRepo()
	deps := s.depRepo()

	for _, id := range []string{
		"bd-tree-r",     // root
		"bd-tree-c",     // edge child of root
		"bd-tree-c.7",   // dotted orphan under the edge child (no dep rows)
		"bd-tree-r.1",   // dotted orphan under the root (no dep rows)
		"bd-tree-r.1.2", // nested dotted orphan (no dep rows)
		"bd-tree-m",     // edge child of the dotted orphan bd-tree-r.1
		"bd-tree-z",     // unrelated root
		"bd-tree-r.9",   // dotted ID but re-parented by edge to bd-tree-z
	} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "tree "+id), "tester", domain.InsertIssueOpts{}))
	}

	for _, e := range []struct{ child, parent string }{
		{"bd-tree-c", "bd-tree-r"},
		{"bd-tree-m", "bd-tree-r.1"},
		{"bd-tree-r.9", "bd-tree-z"},
	} {
		s.Require().NoError(deps.Insert(s.Ctx(),
			&types.Dependency{IssueID: e.child, DependsOnID: e.parent, Type: types.DepParentChild}, "tester", domain.DepInsertOpts{}))
	}

	// Wisps participate in the same walk: an edge wisp child plus a dotted
	// wisp orphan, with their edges in wisp_dependencies. A non-empty wisps
	// table also flips walkWisps on, exercising the wisp CTE branches.
	for _, id := range []string{"bd-tree-wc", "bd-tree-r.5"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "wisp "+id), "tester",
			domain.InsertIssueOpts{UseWispsTable: true}))
	}
	s.Require().NoError(deps.Insert(s.Ctx(),
		&types.Dependency{IssueID: "bd-tree-wc", DependsOnID: "bd-tree-r", Type: types.DepParentChild},
		"tester", domain.DepInsertOpts{UseWispsTable: true}))

	got, err := r.GetDescendants(s.Ctx(), "bd-tree-r", types.IssueFilter{})
	s.Require().NoError(err)

	ids := make([]string, len(got))
	for i, issue := range got {
		ids[i] = issue.ID
	}
	s.ElementsMatch([]string{
		"bd-tree-c",     // edge child
		"bd-tree-c.7",   // dotted orphan under edge child
		"bd-tree-r.1",   // dotted orphan under root
		"bd-tree-r.1.2", // nested dotted orphan
		"bd-tree-m",     // edge child hanging off a dotted orphan
		"bd-tree-wc",    // edge wisp child
		"bd-tree-r.5",   // dotted wisp orphan
	}, ids, "dotted-ID orphans must be walked like classic's ParentID fallback; "+
		"bd-tree-r.9 has a parent-child edge elsewhere and must stay out")

	skip := types.IssueFilter{SkipWisps: true}
	got, err = r.GetDescendants(s.Ctx(), "bd-tree-r", skip)
	s.Require().NoError(err)
	ids = ids[:0]
	for _, issue := range got {
		ids = append(ids, issue.ID)
	}
	s.ElementsMatch([]string{"bd-tree-c", "bd-tree-c.7", "bd-tree-r.1", "bd-tree-r.1.2", "bd-tree-m"},
		ids, "SkipWisps must drop the wisp rows but keep the dotted-ID issue walk")
}

// TestGetDescendantsFilteredByStatus guards the dolt 2.1.6 analyzer
// workaround (commit 341c7a5a4): when GetDescendants carries a level filter,
// each branch of the recursive descendants CTE references the same
// `id IN (SELECT id FROM <table> WHERE ...)` predicate. Inlining that
// subquery into 3+ branches trips the analyzer ("unable to find field with
// index N in row of M columns"); hoisting it into a named non-recursive CTE
// (issue_matches / wisp_matches) dodges it. The existing dotted-orphans test
// uses an empty filter and so never builds the predicate — this test does.
func (s *testSuite) TestGetDescendantsFilteredByStatus() {
	r := s.issueRepo()
	deps := s.depRepo()

	mk := func(id string, st types.Status) {
		iss := newTestIssue(id, "f "+id)
		iss.Status = st
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	mk("bd-f-r", types.StatusOpen)     // root
	mk("bd-f-a", types.StatusOpen)     // open edge child
	mk("bd-f-b", types.StatusClosed)   // closed edge child (must be filtered out)
	mk("bd-f-r.1", types.StatusOpen)   // open dotted orphan
	mk("bd-f-r.2", types.StatusClosed) // closed dotted orphan (must be filtered out)

	for _, e := range []struct{ child, parent string }{
		{"bd-f-a", "bd-f-r"},
		{"bd-f-b", "bd-f-r"},
	} {
		s.Require().NoError(deps.Insert(s.Ctx(),
			&types.Dependency{IssueID: e.child, DependsOnID: e.parent, Type: types.DepParentChild},
			"tester", domain.DepInsertOpts{}))
	}

	st := types.StatusOpen
	got, err := r.GetDescendants(s.Ctx(), "bd-f-r", types.IssueFilter{Status: &st})
	s.Require().NoError(err) // dolt analyzer bug surfaces here without the named-CTE hoist

	ids := make([]string, len(got))
	for i, g := range got {
		ids[i] = g.ID
	}
	s.ElementsMatch([]string{"bd-f-a", "bd-f-r.1"}, ids,
		"only open descendants must be returned; the per-level filter must apply across edge and dotted branches")
}
