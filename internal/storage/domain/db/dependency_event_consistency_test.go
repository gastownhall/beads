package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestDependencyEventConsistency locks in the explicit-verb boundary: the
// proxied plumbing records a dependency_added / dependency_removed event ONLY on
// the explicit dep verb (through the DependencyUseCase), never on create-with-deps
// (which calls depRepo.Insert directly). So `bd create --parent` / `--deps`
// produces the same empty dep history on the proxied backend as on the embedded
// backend, where create routes through issueops PersistDependencies (no emit).
func (s *testSuite) TestDependencyEventConsistency() {
	s.Run("ExplicitAddDependenciesEmits", s.consistencyExplicitAddEmits)
	s.Run("ExplicitRemoveDependencyEmits", s.consistencyExplicitRemoveEmits)
	s.Run("CreateWithParentDoesNotEmit", s.consistencyCreateParentNoEmit)
	s.Run("CreateWithDepsDoesNotEmit", s.consistencyCreateDepsNoEmit)
}

// consistencyExplicitAddEmits proves the real proxied `bd dep add` / `bd link`
// verb — which routes through DependencyUseCase.AddDependencies (bulk) — emits.
func (s *testSuite) consistencyExplicitAddEmits() {
	s.seedIssueRow("bd-cons-add-a")
	s.seedIssueRow("bd-cons-add-b")
	_, err := s.depUseCase().AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-cons-add-a", "bd-cons-add-b", types.DepBlocks)},
		"tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-cons-add-a", string(types.EventDependencyAdded)).Scan(&count))
	s.Equal(1, count, "explicit AddDependencies (bd dep add / bd link) must emit one dependency_added")

	var newValue string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT new_value FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-cons-add-a", string(types.EventDependencyAdded)).Scan(&newValue))
	s.Equal("Added dependency: bd-cons-add-a blocks bd-cons-add-b", newValue)
}

// consistencyExplicitRemoveEmits proves the explicit `bd dep remove` verb —
// DependencyUseCase.RemoveDependency — emits a dependency_removed event.
func (s *testSuite) consistencyExplicitRemoveEmits() {
	s.seedIssueRow("bd-cons-rm-a")
	s.seedIssueRow("bd-cons-rm-b")
	_, err := s.depUseCase().AddDependencies(s.Ctx(),
		[]*types.Dependency{newDep("bd-cons-rm-a", "bd-cons-rm-b", types.DepBlocks)},
		"tester", domain.BulkAddDepsOpts{})
	s.Require().NoError(err)
	s.Require().NoError(s.depUseCase().RemoveDependency(s.Ctx(), "bd-cons-rm-a", "bd-cons-rm-b", "remover"))

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-cons-rm-a", string(types.EventDependencyRemoved)).Scan(&count))
	s.Equal(1, count, "explicit RemoveDependency (bd dep remove) must emit one dependency_removed")
}

// consistencyCreateParentNoEmit proves `bd create --parent` on the proxied path
// creates the implicit parent-child edge but records NO dependency_added event,
// matching the embedded backend.
func (s *testSuite) consistencyCreateParentNoEmit() {
	s.resetMintConfig("cparent", "")
	uc := s.issueUseCase()
	parent, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "parent", IssueType: types.TypeEpic, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	child, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:    &types.Issue{Title: "child", IssueType: types.TypeTask, Priority: 2},
		ParentID: parent.Issue.ID,
	}, "tester")
	s.Require().NoError(err)

	// The implicit parent-child edge must exist ...
	var edgeCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND type = 'parent-child'",
		child.Issue.ID, parent.Issue.ID).Scan(&edgeCount))
	s.Equal(1, edgeCount, "create --parent must create the parent-child edge")

	// ... but must NOT record a dependency_added event (parity with embedded).
	var eventCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		child.Issue.ID, string(types.EventDependencyAdded)).Scan(&eventCount))
	s.Equal(0, eventCount, "create --parent must NOT emit dependency_added (matches embedded PersistDependencies)")
}

// consistencyCreateDepsNoEmit proves `bd create --deps` on the proxied path
// creates the edge but records NO dependency_added event, matching the embedded
// backend.
func (s *testSuite) consistencyCreateDepsNoEmit() {
	s.resetMintConfig("cdeps", "")
	uc := s.issueUseCase()
	target, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "target", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	src, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "source", IssueType: types.TypeTask, Priority: 2},
		Dependencies: []domain.DependencySpec{
			{Type: types.DepBlocks, TargetID: target.Issue.ID},
		},
	}, "tester")
	s.Require().NoError(err)

	var edgeCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		src.Issue.ID, target.Issue.ID).Scan(&edgeCount))
	s.Equal(1, edgeCount, "create --deps must create the dependency edge")

	var eventCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		src.Issue.ID, string(types.EventDependencyAdded)).Scan(&eventCount))
	s.Equal(0, eventCount, "create --deps must NOT emit dependency_added (matches embedded)")
}
