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
	s.Run("CollisionAcrossTablesKeepsTheWispCopy", s.searchCountsCollision)
	s.Run("PredicateFormIDPrefixMatchesIDList", s.searchCountsPredicateIDFilterParity)
	s.Run("PredicateFormIDPrefixMatchesIDListOnWispPlane", s.searchCountsWispPredicateIDFilterParity)
	s.Run("ByIDsFormMatchesPredicateForm", s.searchCountsByIDsFormMatchesPredicateForm)
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

// searchCountsCollision pins what a CROSS-PLANE DUPLICATE answers with. One id resident in both
// tables is corruption — no local write path can produce it, only replication —
// and this read used to fail the whole query over it, which left a store with
// one bad id unable to answer any question about the others.
//
// The canonical copy is the WISPS one, and the read answers with it. That is
// the verdict the per-table seam has always reached (issueops, be-iabdi) and
// the one `bd doctor --check=validate --fix` acts on: it deletes the stale
// ISSUES copy, the same row this drops. scanIDSrcPage carries the full
// argument.
func (s *testSuite) searchCountsCollision() {
	r := s.issueRepo()
	const id = "bd-srxc-coll-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue(id, "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	out, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-coll-"})
	s.Require().NoError(err)
	s.Require().Len(out.Items, 1, "the id must come back once, not once per plane")
	s.Require().NotNil(out.Items[0].Issue)
	s.Equal("wisp", out.Items[0].Issue.Title, "the wisps copy is canonical; the issues copy is the stale one")
}

// searchCountsPredicateIDFilterParity pins that the predicate form projects the
// same counts and JSON whether the driver's row set is bounded by an id prefix
// or by an explicit id list.
//
// Both reads below take the SAME query form, and that is worth stating because
// the names in this area invite the opposite reading. SkipWisps routes past the
// union in searchAcrossIssuesAndWispsWithCounts, so each read lands on the
// single-plane predicate form, and types.IssueFilter.IDs renders as one more
// clause inside that form's whereSQL rather than selecting the by-IDs form —
// which is reached only through the union's fetchCountsByIDs. So this is
// coverage of the predicate form under two different bindings, NOT a comparison
// of the two forms; searchCountsByIDsFormMatchesPredicateForm is that one.
//
// Every count/JSON field the mega-query projects is exercised (dep/rdep/comment
// counts, parent, labels, deps_json), so a change that dropped rows from one
// subquery's bound surfaces here rather than passing as a coincidental 0 == 0.
func (s *testSuite) searchCountsPredicateIDFilterParity() {
	r := s.issueRepo()
	dep := s.depRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	parent := newTestIssue("bd-srxc-par2-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-par2-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-par2-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-par2-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))

	// mid depends on a (outgoing/DependencyCount); b depends on mid (incoming/
	// DependentCount); mid is a child of parent (Parent); mid has a label and
	// a comment. Every projected count/JSON field is nonzero so a subquery
	// that silently drops mid's rows would show up as a mismatch, not a
	// coincidental 0 == 0.
	//
	// The incoming edge comes from a distinct issue b, not from a: mid -> a
	// plus a -> mid is a 2-cycle, and Insert rejects scheduling deps that
	// close one (domain.ErrDependencyCycle). parent cannot play that role
	// either — a blocker that is the issue's ancestor trips
	// ValidateBlockingHierarchy — so the incoming blocker needs its own issue.
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-mid", "bd-srxc-par2-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-b", "bd-srxc-par2-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-par2-mid", "bd-srxc-par2-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-par2-mid", "alpha", "tester", domain.LabelOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
		"bd-srxc-par2-mid", "tester", "comment")
	s.Require().NoError(err)

	// IDPrefix bounds the driver with a LIKE; IDs bounds it with an IN. Same
	// issue, same filter intent, same query form.
	byPrefix, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-par2-mid", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byPrefix.Items, 1)

	byIDList, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-par2-mid"}, SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byIDList.Items, 1)

	p, i := byPrefix.Items[0], byIDList.Items[0]
	// Guard the premise before comparing: every parity assertion below is
	// vacuous if the fixture left the reference side at zero, so pin the
	// reference side's counts as nonzero first. Without this a fixture that
	// stopped producing edges would keep passing as 0 == 0.
	s.Require().NotZero(i.DependencyCount, "fixture must give the reference side an outgoing dep")
	s.Require().NotZero(i.DependentCount, "fixture must give the reference side an incoming dep")
	s.Require().NotZero(i.CommentCount, "fixture must give the reference side a comment")
	s.Require().NotEmpty(i.Issue.Labels, "fixture must give the reference side a label")
	s.Require().NotEmpty(i.Issue.Dependencies, "fixture must give the reference side deps_json rows")
	s.Equal(i.DependencyCount, p.DependencyCount, "dependency_count parity")
	s.Equal(i.DependentCount, p.DependentCount, "dependent_count parity")
	s.Equal(i.CommentCount, p.CommentCount, "comment_count parity")
	s.Require().NotNil(i.Parent)
	s.Require().NotNil(p.Parent)
	s.Equal(*i.Parent, *p.Parent, "parent parity")
	s.ElementsMatch(i.Issue.Labels, p.Issue.Labels, "labels parity")
	s.Require().Len(p.Issue.Dependencies, len(i.Issue.Dependencies), "deps_json length parity")
}

// searchCountsWispPredicateIDFilterParity is the wisp-plane twin of
// searchCountsPredicateIDFilterParity, and carries the same caveat: Ephemeral
// pins both reads to the single wisp plane, so both take the predicate form.
//
// It earns its place anyway, because the two planes resolve labels through
// different tables — the wisp plane reads wisp_labels — and that join had no
// coverage at this seam at all. Note which assertion carries the weight: a
// label bound that dropped the wisp would empty BOTH sides, so the ElementsMatch
// below would still pass. The NotEmpty premise guard is what fails.
func (s *testSuite) searchCountsWispPredicateIDFilterParity() {
	r := s.issueRepo()
	uc := s.labelUseCase()

	w := newTestIssue("bd-srxc-wpar-1", "wisp parity")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-srxc-wpar-1", "alpha", "tester"))
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-srxc-wpar-1", "beta", "tester"))

	// Ephemeral pins both reads to the wisp plane; IDPrefix bounds the driver
	// with a LIKE and IDs with an IN. Same wisp, same filter intent.
	yes := true
	byPrefix, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-wpar-1", Ephemeral: &yes})
	s.Require().NoError(err)
	s.Require().Len(byPrefix.Items, 1)

	byIDList, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDs: []string{"bd-srxc-wpar-1"}, Ephemeral: &yes})
	s.Require().NoError(err)
	s.Require().Len(byIDList.Items, 1)

	p, i := byPrefix.Items[0], byIDList.Items[0]
	// Guard the premise first: labels parity is vacuous if the reference side
	// came back empty, which is exactly what a wisp_labels bound that dropped
	// the row would look like.
	s.Require().NotEmpty(i.Issue.Labels, "fixture must give the reference side wisp labels")
	s.ElementsMatch(i.Issue.Labels, p.Issue.Labels, "wisp labels parity")
	s.Equal(i.DependencyCount, p.DependencyCount, "dependency_count parity")
	s.Equal(i.DependentCount, p.DependentCount, "dependent_count parity")
	s.Equal(i.CommentCount, p.CommentCount, "comment_count parity")
}

// searchCountsByIDsFormMatchesPredicateForm is the cross-form parity test the
// two subtests above are not. SearchCountsSQL renders two shapes, and which one
// a read gets is decided by the dispatch in
// searchAcrossIssuesAndWispsWithCounts — never by the filter's id fields:
//
//   - SkipWisps, Ephemeral-only, or an empty/missing wisps plane each take one
//     plane through runFilterSearchQuery, which renders the PREDICATE form
//     (ids nil, whereSQL bounding a derived driver subquery).
//   - anything else unions the two planes and hydrates the resulting page
//     through fetchCountsByIDs, which renders the BY-IDS form (whereSQL empty,
//     an id predicate at the outer level).
//
// So reaching the by-IDs form takes a search that spans both planes against a
// populated wisps plane — no id filter selects it. This drives one issue both
// ways and asserts the two shapes project identical counts and JSON, which is
// the equivalence a change to either shape has to preserve.
func (s *testSuite) searchCountsByIDsFormMatchesPredicateForm() {
	r := s.issueRepo()
	dep := s.depRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	parent := newTestIssue("bd-srxc-xform-parent", "parent")
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-srxc-xform-mid", "mid")
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))
	a := newTestIssue("bd-srxc-xform-a", "a")
	s.Require().NoError(r.Insert(s.Ctx(), a, "tester", domain.InsertIssueOpts{}))
	b := newTestIssue("bd-srxc-xform-b", "b")
	s.Require().NoError(r.Insert(s.Ctx(), b, "tester", domain.InsertIssueOpts{}))

	// wispsTableEmptyOrMissing short-circuits an empty wisp plane back to the
	// single-plane predicate form, which would quietly turn this into a third
	// predicate-vs-predicate test. Seed a wisp so the union leg is really
	// taken. It must NOT match the search prefix: the point is to compare one
	// row across two forms, not to merge two rows.
	w := newTestIssue("bd-srxc-xformwisp-1", "wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	// Same edge shape, and same reason, as searchCountsPredicateIDFilterParity:
	// mid -> a plus a -> mid is a 2-cycle Insert rejects, and a blocker that is
	// the issue's ancestor trips ValidateBlockingHierarchy, so the incoming
	// edge needs its own issue b.
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-mid", "bd-srxc-xform-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-b", "bd-srxc-xform-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(), newDep("bd-srxc-xform-mid", "bd-srxc-xform-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-srxc-xform-mid", "alpha", "tester", domain.LabelOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO comments (id, issue_id, author, text) VALUES (UUID(), ?, ?, ?)",
		"bd-srxc-xform-mid", "tester", "comment")
	s.Require().NoError(err)

	// Neither SkipWisps nor Ephemeral: the read spans both planes, so the page
	// is hydrated through fetchCountsByIDs — the by-IDs form.
	byIDsForm, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-xform-mid"})
	s.Require().NoError(err)
	s.Require().Len(byIDsForm.Items, 1)

	// SkipWisps on the otherwise-identical filter drops to the predicate form.
	byPredicateForm, err := r.SearchAcrossIssuesAndWispsWithCounts(s.Ctx(), "",
		types.IssueFilter{IDPrefix: "bd-srxc-xform-mid", SkipWisps: true})
	s.Require().NoError(err)
	s.Require().Len(byPredicateForm.Items, 1)

	i, p := byIDsForm.Items[0], byPredicateForm.Items[0]
	// The predicate form is the reference side, so guard ITS premise: two forms
	// that both return nothing agree perfectly. Guarding the reference and then
	// comparing the by-IDs form against it is what makes a bound that is wrong
	// in only one form fail on the parity line rather than on a guard.
	s.Require().NotZero(p.DependencyCount, "fixture must give the reference side an outgoing dep")
	s.Require().NotZero(p.DependentCount, "fixture must give the reference side an incoming dep")
	s.Require().NotZero(p.CommentCount, "fixture must give the reference side a comment")
	s.Require().NotEmpty(p.Issue.Labels, "fixture must give the reference side a label")
	s.Require().NotEmpty(p.Issue.Dependencies, "fixture must give the reference side deps_json rows")
	s.Equal(p.DependencyCount, i.DependencyCount, "dependency_count parity across forms")
	s.Equal(p.DependentCount, i.DependentCount, "dependent_count parity across forms")
	s.Equal(p.CommentCount, i.CommentCount, "comment_count parity across forms")
	s.Require().NotNil(p.Parent)
	s.Require().NotNil(i.Parent)
	s.Equal(*p.Parent, *i.Parent, "parent parity across forms")
	s.ElementsMatch(p.Issue.Labels, i.Issue.Labels, "labels parity across forms")
	s.Require().Len(i.Issue.Dependencies, len(p.Issue.Dependencies), "deps_json length parity across forms")
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
