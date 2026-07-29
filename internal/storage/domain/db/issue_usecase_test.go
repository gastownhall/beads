package db

import (
	"database/sql"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_MintTopLevelID() {
	s.Run("HashModeMintsBdPrefixedID", s.useCaseMintHashMode)
	s.Run("CounterModeMintsSequentialID", s.useCaseMintCounterMode)
	s.Run("CounterModeUnavailableForWisps", s.useCaseMintWispIgnoresCounterMode)
	s.Run("IssuePrefixOverrideHonored", s.useCaseMintRespectsPrefixOverride)
	s.Run("IDPrefixSubprefixHonored", s.useCaseMintRespectsIDPrefix)
	s.Run("WispUsesWispPrefix", s.useCaseMintWispPrefix)
	s.Run("MissingConfigPrefixErrors", s.useCaseMintMissingPrefix)
}

func (s *testSuite) issueUseCase() domain.IssueUseCase {
	runner := s.Runner()
	labelUC := domain.NewLabelUseCase(NewLabelSQLRepository(runner))
	depUC := domain.NewDependencyUseCase(NewDependencySQLRepository(runner))
	return domain.NewIssueUseCase(
		NewIssueSQLRepository(runner),
		NewDependencySQLRepository(runner),
		NewLabelSQLRepository(runner),
		NewChildCounterSQLRepository(runner),
		NewCommentSQLRepository(runner),
		NewConfigSQLRepository(runner),
		NewEventsSQLRepository(runner),
		labelUC,
		depUC,
	)
}

func (s *testSuite) resetMintConfig(prefix, idMode string) {
	r := NewConfigSQLRepository(s.Runner())
	s.Require().NoError(r.SetConfig(s.Ctx(), "issue_prefix", prefix))
	s.Require().NoError(r.SetConfig(s.Ctx(), "issue_id_mode", idMode))
}

func (s *testSuite) useCaseMintHashMode() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()

	res, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			Title:     "fresh top-level",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)

	s.Require().NotEmpty(res.Issue.ID)
	s.True(strings.HasPrefix(res.Issue.ID, "bd-"), "expected bd- prefix, got %q", res.Issue.ID)
	suffix := strings.TrimPrefix(res.Issue.ID, "bd-")
	s.NotContains(suffix, ".", "hash IDs must not contain '.'")
	s.True(len(suffix) >= 3 && len(suffix) <= 8, "expected base36 hash 3..8 chars, got %q", suffix)
}

func (s *testSuite) useCaseMintCounterMode() {
	s.resetMintConfig("ucCnt", "counter")
	uc := s.issueUseCase()

	first, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "one", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	s.Equal("ucCnt-1", first.Issue.ID)

	second, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "two", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	s.Equal("ucCnt-2", second.Issue.ID)
}

func (s *testSuite) useCaseMintWispIgnoresCounterMode() {
	s.resetMintConfig("ucWcnt", "counter")
	uc := s.issueUseCase()

	res, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			Title:     "wisp",
			IssueType: types.TypeTask,
			Priority:  2,
			Ephemeral: true,
		},
	}, "tester")
	s.Require().NoError(err)
	s.True(strings.HasPrefix(res.Issue.ID, "ucWcnt-wisp-"), "expected ucWcnt-wisp- prefix, got %q", res.Issue.ID)
}

func (s *testSuite) useCaseMintRespectsPrefixOverride() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()

	res, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			Title:          "overridden",
			IssueType:      types.TypeTask,
			Priority:       2,
			PrefixOverride: "spec",
		},
	}, "tester")
	s.Require().NoError(err)
	s.True(strings.HasPrefix(res.Issue.ID, "spec-"), "expected override prefix, got %q", res.Issue.ID)
}

func (s *testSuite) useCaseMintRespectsIDPrefix() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()

	res, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			Title:     "subprefixed",
			IssueType: types.TypeTask,
			Priority:  2,
			IDPrefix:  "exp",
		},
	}, "tester")
	s.Require().NoError(err)
	s.True(strings.HasPrefix(res.Issue.ID, "bd-exp-"), "expected bd-exp- subprefix, got %q", res.Issue.ID)
}

func (s *testSuite) useCaseMintWispPrefix() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()

	res, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			Title:     "wispy",
			IssueType: types.TypeTask,
			Priority:  2,
			Ephemeral: true,
		},
	}, "tester")
	s.Require().NoError(err)
	s.True(strings.HasPrefix(res.Issue.ID, "bd-wisp-"), "expected bd-wisp- prefix, got %q", res.Issue.ID)
}

func (s *testSuite) useCaseMintMissingPrefix() {
	s.resetMintConfig("", "")
	uc := s.issueUseCase()
	_, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "no prefix", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "issue_prefix")
}

func (s *testSuite) TestIssueUseCase_ApplyGraph() {
	s.Run("ChildrenBeforeParentsSucceed", s.applyGraphChildrenBeforeParents)
	s.Run("ExplicitParentChildEdgeIsDeduped", s.applyGraphParentChildEdgeDedup)
	s.Run("DifferentTypeOverParentChildPairErrors", s.applyGraphDifferentTypeOverPair)
	s.Run("ReverseBlockingOverParentChildPairErrors", s.applyGraphReverseBlocking)
	s.Run("LiveCycleThroughExistingDepsErrors", s.applyGraphLiveCycle)
	s.Run("ExternalIDIntraBatchBlockingCycleErrors", s.applyGraphExternalIDBlockingCycle)
	s.Run("CombinedSchedulingCycleErrors", s.applyGraphCombinedSchedulingCycle)
	s.Run("RegularGraphCycleThroughExistingWispDepErrors", s.applyGraphRegularCycleThroughWispDep)
	s.Run("WispGraphCycleThroughExistingRegularDepErrors", s.applyGraphWispCycleThroughRegularDep)
	s.Run("RejectsBlockingThroughExistingParentChild", s.applyGraphRejectsBlockingThroughParentChild)
	s.Run("RejectsBlockingThroughPlannedHierarchy", s.applyGraphRejectsBlockingThroughPlannedHierarchy)
	s.Run("HealthyPlanRoundTrips", s.applyGraphHealthy)
	s.Run("WispGraphRoutesToWispTables", s.applyGraphWispRouting)
}

func (s *testSuite) TestIssueUseCase_MixedParentChildRouting() {
	s.Run("WispChildOfRegularParent", s.mixedWispChildOfRegularParent)
	s.Run("DepTargetClassification", s.mixedDepTargetClassification)
}

func (s *testSuite) mixedWispChildOfRegularParent() {
	s.resetMintConfig("mw", "")
	uc := s.issueUseCase()

	pRes, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "regular parent", IssueType: types.TypeEpic, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	parentID := pRes.Issue.ID

	cRes, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue:    &types.Issue{Title: "wisp child", IssueType: types.TypeTask, Priority: 2, Ephemeral: true},
		ParentID: parentID,
	}, "tester")
	s.Require().NoError(err)
	childID := cRes.Issue.ID
	s.True(strings.HasPrefix(childID, parentID+"."), "child ID %q should start with %q.", childID, parentID)

	var regularCounter, wispCounter int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", parentID).Scan(&regularCounter))
	s.Equal(1, regularCounter, "regular parent's counter must land in child_counters")
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?", parentID).Scan(&wispCounter))
	s.Equal(0, wispCounter, "regular parent's counter must NOT land in wisp_child_counters")

	var wispChildExists, regularParentExists int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisps WHERE id = ?", childID).Scan(&wispChildExists))
	s.Equal(1, wispChildExists, "wisp child must live in wisps table")
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", parentID).Scan(&regularParentExists))
	s.Equal(1, regularParentExists, "regular parent must live in issues table")

	deps := s.loadDepRows("wisp_dependencies", "mw-%")
	s.Require().Len(deps, 1)
	s.Equal(childID, deps[0].issueID)
	s.Equal(parentID, deps[0].dependsOnID)
	s.Equal(string(types.DepParentChild), deps[0].depType)
	s.Equal("depends_on_issue_id", deps[0].targetColumn(),
		"regular-issue target must use depends_on_issue_id, got %+v", deps[0])
}

func (s *testSuite) mixedDepTargetClassification() {
	s.resetMintConfig("dx", "")
	uc := s.issueUseCase()
	depRepo := NewDependencySQLRepository(s.Runner())

	src, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "source", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	regular, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "regular target", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	wisp, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "wisp target", IssueType: types.TypeTask, Priority: 2, Ephemeral: true},
	}, "tester")
	s.Require().NoError(err)

	s.Require().NoError(depRepo.Insert(s.Ctx(), &types.Dependency{
		IssueID: src.Issue.ID, DependsOnID: regular.Issue.ID, Type: types.DepRelated,
	}, "tester", domain.DepInsertOpts{}))
	s.Require().NoError(depRepo.Insert(s.Ctx(), &types.Dependency{
		IssueID: src.Issue.ID, DependsOnID: wisp.Issue.ID, Type: types.DepRelated,
	}, "tester", domain.DepInsertOpts{}))
	s.Require().NoError(depRepo.Insert(s.Ctx(), &types.Dependency{
		IssueID: src.Issue.ID, DependsOnID: "external:GH-42", Type: types.DepRelated,
	}, "tester", domain.DepInsertOpts{}))

	deps := s.loadDepRows("dependencies", "dx-%")
	s.Require().Len(deps, 3)

	byTarget := make(map[string]depRow, len(deps))
	for _, d := range deps {
		byTarget[d.dependsOnID] = d
	}
	s.Equal("depends_on_issue_id", byTarget[regular.Issue.ID].targetColumn(),
		"regular target must use depends_on_issue_id, got %+v", byTarget[regular.Issue.ID])
	s.Equal("depends_on_wisp_id", byTarget[wisp.Issue.ID].targetColumn(),
		"wisp target must use depends_on_wisp_id, got %+v", byTarget[wisp.Issue.ID])
	s.Equal("depends_on_external", byTarget["external:GH-42"].targetColumn(),
		"external: target must use depends_on_external, got %+v", byTarget["external:GH-42"])
}

type depRow struct {
	issueID      string
	dependsOnID  string
	depType      string
	depsOnIssue  sql.NullString
	depsOnWisp   sql.NullString
	depsOnExtern sql.NullString
}

func (r depRow) targetColumn() string {
	switch {
	case r.depsOnIssue.Valid:
		return "depends_on_issue_id"
	case r.depsOnWisp.Valid:
		return "depends_on_wisp_id"
	case r.depsOnExtern.Valid:
		return "depends_on_external"
	default:
		return ""
	}
}

func (s *testSuite) loadDepRows(table, prefixLike string) []depRow {
	rows, err := s.Runner().QueryContext(s.Ctx(),
		//nolint:gosec // G201: table is one of two hardcoded constants for tests
		"SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id, type, depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+" WHERE issue_id LIKE ? OR depends_on_issue_id LIKE ? OR depends_on_wisp_id LIKE ? OR depends_on_external LIKE ? ORDER BY issue_id, depends_on_id, type",
		prefixLike, prefixLike, prefixLike, prefixLike,
	)
	s.Require().NoError(err)
	defer rows.Close()
	var out []depRow
	for rows.Next() {
		var r depRow
		s.Require().NoError(rows.Scan(&r.issueID, &r.dependsOnID, &r.depType, &r.depsOnIssue, &r.depsOnWisp, &r.depsOnExtern))
		out = append(out, r)
	}
	s.Require().NoError(rows.Err())
	return out
}

func newGraphNode(key, title string) domain.GraphNode {
	return domain.GraphNode{
		Key: key,
		Issue: &types.Issue{
			Title:     title,
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}
}

func (s *testSuite) applyGraphChildrenBeforeParents() {
	s.resetMintConfig("gA", "")
	uc := s.issueUseCase()

	child := newGraphNode("child", "child node")
	child.ParentKey = "parent"
	parent := newGraphNode("parent", "parent node")

	res, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{child, parent},
	}, "tester")
	s.Require().NoError(err)
	s.Require().Len(res.IDs, 2)

	childID := res.IDs["child"]
	parentID := res.IDs["parent"]
	s.True(strings.HasPrefix(childID, "gA-"), "child should mint a top-level gA- ID, got %q", childID)
	s.True(strings.HasPrefix(parentID, "gA-"), "parent should mint a top-level gA- ID, got %q", parentID)
	s.NotContains(childID, ".", "graph child should not get a counter-style ID")

	deps := s.loadDepRows("dependencies", "gA-%")
	s.Require().Len(deps, 1, "expected one parent-child dep, got %d: %+v", len(deps), deps)
	s.Equal(childID, deps[0].issueID)
	s.Equal(parentID, deps[0].dependsOnID)
	s.Equal(string(types.DepParentChild), deps[0].depType)
}

func (s *testSuite) applyGraphParentChildEdgeDedup() {
	s.resetMintConfig("gB", "")
	uc := s.issueUseCase()

	child := newGraphNode("child", "child")
	child.ParentKey = "parent"
	parent := newGraphNode("parent", "parent")

	res, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{parent, child},
		Edges: []domain.GraphEdge{{
			FromKey: "child",
			ToKey:   "parent",
			Type:    types.DepParentChild,
		}},
	}, "tester")
	s.Require().NoError(err)

	deps := s.loadDepRows("dependencies", "gB-%")
	s.Require().Len(deps, 1, "explicit parent-child edge duplicating an implicit pair must collapse to one row")
	s.Equal(string(types.DepParentChild), deps[0].depType)
	s.Equal(res.IDs["child"], deps[0].issueID)
	s.Equal(res.IDs["parent"], deps[0].dependsOnID)
}

func (s *testSuite) applyGraphDifferentTypeOverPair() {
	s.resetMintConfig("gC", "")
	uc := s.issueUseCase()

	child := newGraphNode("child", "child")
	child.ParentKey = "parent"
	parent := newGraphNode("parent", "parent")

	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{parent, child},
		Edges: []domain.GraphEdge{{
			FromKey: "child",
			ToKey:   "parent",
			Type:    types.DepBlocks,
		}},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "duplicates a parent-child relationship")

	deps := s.loadDepRows("dependencies", "gC-%")
	s.Empty(deps, "no deps should be written when the plan is rejected before pass 2")
}

func (s *testSuite) applyGraphReverseBlocking() {
	s.resetMintConfig("gD", "")
	uc := s.issueUseCase()

	child := newGraphNode("child", "child")
	child.ParentKey = "parent"
	parent := newGraphNode("parent", "parent")

	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{parent, child},
		Edges: []domain.GraphEdge{{
			FromKey: "parent",
			ToKey:   "child",
			Type:    types.DepBlocks,
		}},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "creates a blocking reverse")

	deps := s.loadDepRows("dependencies", "gD-%")
	s.Empty(deps)
}

func (s *testSuite) applyGraphLiveCycle() {
	s.resetMintConfig("gE", "")
	uc := s.issueUseCase()

	issueRepo := NewIssueSQLRepository(s.Runner())
	x := newTestIssue("gE-existing-x", "existing X")
	y := newTestIssue("gE-existing-y", "existing Y")
	s.Require().NoError(issueRepo.Insert(s.Ctx(), x, "seeder", domain.InsertIssueOpts{}))
	s.Require().NoError(issueRepo.Insert(s.Ctx(), y, "seeder", domain.InsertIssueOpts{}))

	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(), &types.Dependency{
		IssueID:     "gE-existing-x",
		DependsOnID: "gE-existing-y",
		Type:        types.DepBlocks,
	}, "seeder", domain.DepInsertOpts{}))

	parent := newGraphNode("parent", "new parent")
	child := newGraphNode("child", "new child")
	child.ParentKey = "parent"

	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{parent, child},
		Edges: []domain.GraphEdge{
			{FromKey: "parent", ToID: "gE-existing-x", Type: types.DepBlocks},
			{FromID: "gE-existing-y", ToKey: "child", Type: types.DepBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "planned blocking dependencies create a path from parent")

	deps := s.loadDepRows("dependencies", "gE-%")
	for _, d := range deps {
		s.NotEqual(string(types.DepParentChild), d.depType, "parent-child dep must not be written when live cycle detected: %+v", d)
	}
}

// applyGraphExternalIDBlockingCycle proves the ported whole-graph preflight
// (validatePlannedBlockingCycles) rejects an intra-batch blocking cycle formed
// entirely by external-ID edges, before any edge is inserted.
func (s *testSuite) applyGraphExternalIDBlockingCycle() {
	s.resetMintConfig("gH", "")
	uc := s.issueUseCase()

	issueRepo := NewIssueSQLRepository(s.Runner())
	p := newTestIssue("gH-existing-p", "existing P")
	q := newTestIssue("gH-existing-q", "existing Q")
	s.Require().NoError(issueRepo.Insert(s.Ctx(), p, "seeder", domain.InsertIssueOpts{}))
	s.Require().NoError(issueRepo.Insert(s.Ctx(), q, "seeder", domain.InsertIssueOpts{}))

	// Two planned blocking edges between existing issues, referenced by ID,
	// close a 2-cycle within a single graph-apply batch.
	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Edges: []domain.GraphEdge{
			{FromID: "gH-existing-p", ToID: "gH-existing-q", Type: types.DepBlocks},
			{FromID: "gH-existing-q", ToID: "gH-existing-p", Type: types.DepBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "creates a blocking dependency cycle")

	deps := s.loadDepRows("dependencies", "gH-%")
	s.Empty(deps, "no blocking edge may be written when an intra-batch external-ID cycle is detected")
}

func (s *testSuite) applyGraphCombinedSchedulingCycle() {
	s.resetMintConfig("gM", "")
	uc := s.issueUseCase()

	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{
			newGraphNode("a", "A"),
			newGraphNode("b", "B"),
			newGraphNode("c", "C"),
		},
		Edges: []domain.GraphEdge{
			{FromKey: "a", ToKey: "b", Type: types.DepBlocks},
			{FromKey: "b", ToKey: "c", Type: types.DepParentChild},
			{FromKey: "c", ToKey: "a", Type: types.DepConditionalBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "cycle")
	// This low-level suite calls the use case without its normal outer UOW, so
	// earlier acyclic writes may remain. The edge that closes the cycle must not.
	for _, d := range s.loadDepRows("dependencies", "gM-%") {
		s.NotEqual(string(types.DepConditionalBlocks), d.depType, "cycle-closing edge must not be written: %+v", d)
	}
}

// applyGraphRegularCycleThroughWispDep proves a regular graph-apply rejects a
// planned blocking edge that closes a cycle through an existing blocking edge
// living in wisp_dependencies. The per-edge depRepo.HasCycle probe this
// preflight replaced walked both dependency tables, so the whole-graph walk
// must too — otherwise a mixed regular/wisp blocking cycle commits undetected.
func (s *testSuite) applyGraphRegularCycleThroughWispDep() {
	s.resetMintConfig("gI", "")
	uc := s.issueUseCase()

	s.seedWispRow("gI-w")
	s.seedIssueRow("gI-r")

	// Existing blocking edge gI-w -> gI-r lives in wisp_dependencies.
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("gI-w", "gI-r", types.DepBlocks), "seeder",
		domain.DepInsertOpts{UseWispsTable: true}))

	// Regular graph-apply adds gI-r -> gI-w (blocks) into dependencies, closing
	// a blocking cycle that crosses the regular and wisp dependency tables.
	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Edges: []domain.GraphEdge{
			{FromID: "gI-r", ToID: "gI-w", Type: types.DepBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "creates a blocking dependency cycle")

	s.Empty(s.loadDepRows("dependencies", "gI-%"),
		"no regular blocking edge may be written when the cycle closes through an existing wisp dep")
}

// applyGraphWispCycleThroughRegularDep is the mirror of the case above: a wisp
// graph-apply must reject a planned blocking edge that closes a cycle through
// an existing blocking edge living in the regular dependencies table.
func (s *testSuite) applyGraphWispCycleThroughRegularDep() {
	s.resetMintConfig("gJ", "")
	uc := s.issueUseCase()

	s.seedIssueRow("gJ-r")
	s.seedWispRow("gJ-w")

	// Existing blocking edge gJ-r -> gJ-w lives in the regular dependencies
	// table (with a wisp target column).
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("gJ-r", "gJ-w", types.DepBlocks), "seeder",
		domain.DepInsertOpts{}))

	// Wisp graph-apply adds gJ-w -> gJ-r (blocks) into wisp_dependencies,
	// closing a cross-table blocking cycle.
	_, err := uc.ApplyWispGraph(s.Ctx(), domain.GraphPlan{
		Edges: []domain.GraphEdge{
			{FromID: "gJ-w", ToID: "gJ-r", Type: types.DepBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "creates a blocking dependency cycle")

	s.Empty(s.loadDepRows("wisp_dependencies", "gJ-%"),
		"no wisp blocking edge may be written when the cycle closes through an existing regular dep")
}

// applyGraphRejectsBlockingThroughParentChild verifies that graph apply uses
// the same hierarchy-deadlock guard as a single dependency add. This is not a
// mixed-edge cycle check: the parent-child edge only establishes that the
// proposed blocker is the source's own descendant.
func (s *testSuite) applyGraphRejectsBlockingThroughParentChild() {
	s.resetMintConfig("gK", "")
	uc := s.issueUseCase()

	s.seedIssueRow("gK-parent")
	s.seedIssueRow("gK-child")

	// Existing parent-child dep gK-child -> gK-parent: the only return path.
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("gK-child", "gK-parent", types.DepParentChild), "seeder",
		domain.DepInsertOpts{}))

	// A planned blocking edge gK-parent -> gK-child would gate the parent on its
	// own descendant and can never clear under blocked-state cascading.
	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Edges: []domain.GraphEdge{
			{FromID: "gK-parent", ToID: "gK-child", Type: types.DepBlocks},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot be blocked by its descendant")

	for _, d := range s.loadDepRows("dependencies", "gK-%") {
		if d.depType == string(types.DepBlocks) && d.issueID == "gK-parent" && d.dependsOnID == "gK-child" {
			s.Fail("hierarchy-deadlocking blocking edge must not be written")
		}
	}
}

func (s *testSuite) applyGraphRejectsBlockingThroughPlannedHierarchy() {
	s.resetMintConfig("gL", "")
	uc := s.issueUseCase()

	grand := newGraphNode("grand", "grand")
	parent := newGraphNode("parent", "parent")
	child := newGraphNode("child", "child")
	_, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{grand, parent, child},
		Edges: []domain.GraphEdge{
			{FromKey: "child", ToKey: "grand", Type: types.DepConditionalBlocks}, // Deliberately first.
			{FromKey: "child", ToKey: "parent", Type: types.DepParentChild},
			{FromKey: "parent", ToKey: "grand", Type: types.DepParentChild},
		},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "cannot be blocked by its ancestor")

	for _, d := range s.loadDepRows("dependencies", "gL-%") {
		if d.depType == string(types.DepConditionalBlocks) {
			s.Fail("block-first graph edge must not escape planned hierarchy validation")
		}
	}
}

func (s *testSuite) applyGraphHealthy() {
	s.resetMintConfig("gF", "")
	uc := s.issueUseCase()

	parent := newGraphNode("p", "parent")
	child := newGraphNode("c", "child")
	child.ParentKey = "p"
	sibling := newGraphNode("s", "sibling")

	res, err := uc.ApplyIssueGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{parent, child, sibling},
		Edges: []domain.GraphEdge{{
			FromKey: "c",
			ToKey:   "s",
			Type:    types.DepRelated,
		}},
	}, "tester")
	s.Require().NoError(err)
	s.Len(res.IDs, 3)

	deps := s.loadDepRows("dependencies", "gF-%")
	s.Require().Len(deps, 2)
	var pcSeen, relSeen bool
	for _, d := range deps {
		switch d.depType {
		case string(types.DepParentChild):
			pcSeen = true
			s.Equal(res.IDs["c"], d.issueID)
			s.Equal(res.IDs["p"], d.dependsOnID)
		case string(types.DepRelated):
			relSeen = true
			s.Equal(res.IDs["c"], d.issueID)
			s.Equal(res.IDs["s"], d.dependsOnID)
		}
	}
	s.True(pcSeen, "expected parent-child dep")
	s.True(relSeen, "expected related dep")
}

func (s *testSuite) applyGraphWispRouting() {
	s.resetMintConfig("gG", "")
	uc := s.issueUseCase()

	parent := newGraphNode("p", "parent wisp")
	parent.Issue.Ephemeral = true
	child := newGraphNode("c", "child wisp")
	child.Issue.Ephemeral = true
	child.ParentKey = "p"

	res, err := uc.ApplyWispGraph(s.Ctx(), domain.GraphPlan{
		Nodes: []domain.GraphNode{child, parent},
	}, "tester")
	s.Require().NoError(err)
	s.Require().Len(res.IDs, 2)
	s.True(strings.HasPrefix(res.IDs["p"], "gG-wisp-"), "wisp parent should carry the -wisp suffix, got %q", res.IDs["p"])
	s.True(strings.HasPrefix(res.IDs["c"], "gG-wisp-"), "wisp child should carry the -wisp suffix, got %q", res.IDs["c"])

	regular := s.loadDepRows("dependencies", "gG-%")
	s.Empty(regular, "no deps should appear in dependencies table for a wisp graph")
	regularIssues := 0
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT COUNT(*) FROM issues WHERE id LIKE ?", "gG-%").Scan(&regularIssues))
	s.Equal(0, regularIssues, "wisp graph issues must not land in `issues`")

	wispDeps := s.loadDepRows("wisp_dependencies", "gG-%")
	s.Require().Len(wispDeps, 1)
	s.Equal(string(types.DepParentChild), wispDeps[0].depType)
	s.Equal(res.IDs["c"], wispDeps[0].issueID)
	s.Equal(res.IDs["p"], wispDeps[0].dependsOnID)
	s.Equal("depends_on_wisp_id", wispDeps[0].targetColumn(),
		"wisp-target dep must use depends_on_wisp_id, got %+v", wispDeps[0])
}
