package db

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// TestBuildDescendantsCTEResolvesEveryDepTargetColumn binds the walk's edge
// members to sqlbuild.DepTargetExpr, the single source of truth for how a
// dependency row's parent is resolved across the three typed target columns.
//
// The two expressions drifted before be-qfm: the edge members named only
// depends_on_issue_id and depends_on_wisp_id, so a parent recorded in
// depends_on_external (a cross-prefix or foreign-repo parent) coalesced to NULL
// and GetDescendants could never match the edge — while the classic ParentID
// filter this walk mirrors (sqlbuild/filter.go:176, built on DepTargetExpr) did
// match it. Deriving the expected column set from DepTargetExpr rather than
// restating it is what makes this test fail if a fourth target column is ever
// added there without being threaded through here. No database required: the
// builder is a pure function of its arguments.
func TestBuildDescendantsCTEResolvesEveryDepTargetColumn(t *testing.T) {
	t.Parallel()

	cols := depTargetColumns(t)
	// Both dependency-table aliases the walk joins its edge members through.
	// Each contributes one anchor member and one recursive member.
	const membersPerAlias = 2

	for _, walkWisps := range []bool{false, true} {
		aliases := []string{"d"}
		if walkWisps {
			aliases = append(aliases, "wd")
		}

		cte, _ := buildDescendantsCTE("root-1", walkWisps, predBundle{}, predBundle{})

		wantTotal := 0
		for _, alias := range aliases {
			qualified := make([]string, 0, len(cols))
			for _, col := range cols {
				qualified = append(qualified, alias+"."+col)
			}
			want := "COALESCE(" + strings.Join(qualified, ", ") + ")"
			if got := strings.Count(cte, want); got != membersPerAlias {
				t.Errorf("walkWisps=%v: %q on %d edge members, want %d — the %s members do not resolve the parent the way sqlbuild.DepTargetExpr does:\n%s",
					walkWisps, want, got, membersPerAlias, alias, cte)
			}
			wantTotal += membersPerAlias
		}

		// Every COALESCE in the CTE must be one of the fully-qualified forms
		// asserted above; a surviving 2-of-3 form would otherwise pass the
		// counts by sitting on a member the loop never names.
		if got := strings.Count(cte, "COALESCE("); got != wantTotal {
			t.Errorf("walkWisps=%v: %d COALESCE expressions, want %d — one resolves a different column set:\n%s",
				walkWisps, got, wantTotal, cte)
		}
	}
}

// depTargetColumns is the column list inside sqlbuild.DepTargetExpr, in its
// precedence order. Read from the exported origin rather than this package's
// depTargetExpr alias so the assertion names the source of truth it binds to.
func depTargetColumns(t *testing.T) []string {
	t.Helper()

	expr := sqlbuild.DepTargetExpr
	inner, ok := strings.CutPrefix(expr, "COALESCE(")
	if !ok {
		t.Fatalf("DepTargetExpr is no longer a COALESCE expression: %q", expr)
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok {
		t.Fatalf("DepTargetExpr is not parenthesised: %q", expr)
	}
	cols := strings.Split(inner, ", ")
	if len(cols) < 2 {
		t.Fatalf("DepTargetExpr resolves %d column(s), expected the typed target set: %q", len(cols), expr)
	}
	return cols
}

// TestGetDescendantsCrossPrefixParent is the behavioural half of
// TestBuildDescendantsCTEResolvesEveryDepTargetColumn: that test pins the
// walk's query text, this one pins that the widened COALESCE matches real rows.
// The write path classifies a target by prefix alone (issueops.IsExternalDepTarget),
// so a parent-child edge between two prefixes lands in depends_on_external even
// when both ends are local. Before be-qfm the walk coalesced only the issue and
// wisp columns, so each child below — and its subtree — was missing from the
// proxied `bd list --parent` tree. Each one is reached through a different edge
// member (issues and wisps, anchor and recursive), so dropping the column from
// any one member loses a different child.
func (s *testSuite) TestGetDescendantsCrossPrefixParent() {
	r := s.issueRepo()
	deps := s.depRepo()

	const (
		root   = "zz-xp-root"
		child  = "bd-xp-a" // issue under root: issues anchor member
		wisp   = "bd-xp-w" // wisp under root: wisps anchor member
		grand  = "zz-xp-b" // issue under child: issues recursive member
		grandW = "zz-xp-v" // wisp under child: wisps recursive member
	)
	for _, id := range []string{root, child, grand} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "xp "+id), "tester", domain.InsertIssueOpts{}))
	}
	for _, id := range []string{wisp, grandW} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "xp "+id), "tester",
			domain.InsertIssueOpts{UseWispsTable: true}))
	}
	for _, e := range []struct {
		child, parent string
		wisps         bool
	}{
		{child, root, false},
		{wisp, root, true},
		{grand, child, false},
		{grandW, child, true},
	} {
		s.Require().NoError(deps.Insert(s.Ctx(),
			&types.Dependency{IssueID: e.child, DependsOnID: e.parent, Type: types.DepParentChild},
			"tester", domain.DepInsertOpts{UseWispsTable: e.wisps}))
	}

	// Were the classifier ever to route one of these edges to another column,
	// the two-column walk would find that child too and the case would pin nothing.
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		rows := s.loadDepRows(table, "%-xp-%")
		s.Require().Len(rows, 2, table)
		for _, d := range rows {
			s.Equal("depends_on_external", d.targetColumn(), "%s: %s -> %s", table, d.issueID, d.dependsOnID)
		}
	}

	got, err := r.GetDescendants(s.Ctx(), root, types.IssueFilter{})
	s.Require().NoError(err)

	ids := make([]string, len(got))
	for i, issue := range got {
		ids[i] = issue.ID
	}
	s.ElementsMatch([]string{child, wisp, grand, grandW}, ids,
		"every edge member must resolve a parent recorded in depends_on_external")
}

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
