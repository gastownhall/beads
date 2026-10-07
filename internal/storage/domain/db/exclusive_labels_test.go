package db

import (
	"github.com/steveyegge/beads/internal/labelns"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestExclusiveLabelNamespaces pins the proxied-server (uow) half of the
// labels.exclusive-prefixes contract (bd-7u5ki): every label write refuses a
// second label in an exclusive namespace, exactly as the direct route does in
// issueops.addLabelInTx. Subtests share one database, so each uses its own ids
// and labels.
func (s *testSuite) TestExclusiveLabelNamespaces() {
	s.Require().NoError(NewConfigSQLRepository(s.Runner()).SetConfig(s.Ctx(), labelns.ConfigKey, "tier:"))
	s.Run("InsertRejectsSecondLabel", s.exclInsertRejectsSecondLabel)
	s.Run("InsertReAddOnViolatingIssuePasses", s.exclInsertReAddOnViolatingIssue)
	s.Run("UpdateSwapsInOneSpec", s.exclUpdateSwapsInOneSpec)
	s.Run("UpdateRemoveWinsOverAdd", s.exclUpdateRemoveWinsOverAdd)
	s.Run("CreateRejectsTwoExplicitLabels", s.exclCreateRejectsTwoExplicit)
	s.Run("CreateExplicitBeatsInherited", s.exclCreateExplicitBeatsInherited)
	s.Run("CreateInheritedConflictNamesParent", s.exclCreateInheritedConflict)
	s.Run("RenameRefusesNewViolation", s.exclRenameRefusesNewViolation)
	s.Run("RenameMergeAndSameNamespacePass", s.exclRenameMergeAndSameNamespace)
}

// seedRawLabels writes label rows directly, bypassing the guard, to stage a
// violation the way an import or a Dolt merge can leave one behind.
func (s *testSuite) seedRawLabels(issueID string, labels ...string) {
	for _, label := range labels {
		_, err := s.Runner().ExecContext(s.Ctx(), "INSERT INTO labels (issue_id, label) VALUES (?, ?)", issueID, label)
		s.Require().NoError(err)
	}
}

func (s *testSuite) requireLabels(issueID string, want ...string) {
	got, err := s.labelRepo().List(s.Ctx(), issueID, domain.LabelOpts{})
	s.Require().NoError(err)
	s.Equal(want, got)
}

func (s *testSuite) exclInsertRejectsSecondLabel() {
	s.seedIssueRow("bd-excl-ins")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-ins", "tier:fable", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-ins", "area:x", "tester", domain.LabelOpts{}))

	err := r.Insert(s.Ctx(), "bd-excl-ins", "tier:opus", "tester", domain.LabelOpts{})
	s.Require().Error(err)
	s.Contains(err.Error(), `namespace "tier:" is exclusive`)
	s.Contains(err.Error(), `already has "tier:fable"`)
	s.requireLabels("bd-excl-ins", "area:x", "tier:fable")
}

func (s *testSuite) exclInsertReAddOnViolatingIssue() {
	s.seedIssueRow("bd-excl-readd")
	s.seedRawLabels("bd-excl-readd", "tier:fable", "tier:opus")
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(), "bd-excl-readd", "tier:fable", "tester", domain.LabelOpts{}),
		"re-adding a label the issue already carries writes nothing and must pass")
}

func (s *testSuite) exclUpdateSwapsInOneSpec() {
	s.seedOpenIssue("bd-excl-swap")
	uc := s.issueUseCase()
	_, err := uc.ApplyUpdate(s.Ctx(), "bd-excl-swap", domain.UpdateSpec{AddLabels: []string{"tier:fable"}}, "tester")
	s.Require().NoError(err)

	_, err = uc.ApplyUpdate(s.Ctx(), "bd-excl-swap", domain.UpdateSpec{
		AddLabels:    []string{"tier:opus"},
		RemoveLabels: []string{"tier:fable"},
	}, "tester")
	s.Require().NoError(err, "removes must run before adds so a swap passes the guard")
	s.requireLabels("bd-excl-swap", "tier:opus")
}

func (s *testSuite) exclUpdateRemoveWinsOverAdd() {
	s.seedOpenIssue("bd-excl-rmwins")
	uc := s.issueUseCase()
	_, err := uc.ApplyUpdate(s.Ctx(), "bd-excl-rmwins", domain.UpdateSpec{AddLabels: []string{"keep"}}, "tester")
	s.Require().NoError(err)

	_, err = uc.ApplyUpdate(s.Ctx(), "bd-excl-rmwins", domain.UpdateSpec{
		AddLabels:    []string{"tier:fable"},
		RemoveLabels: []string{"tier:fable"},
	}, "tester")
	s.Require().NoError(err)
	s.requireLabels("bd-excl-rmwins", "keep")
}

func (s *testSuite) exclCreateRejectsTwoExplicit() {
	s.resetMintConfig("exa", "")
	_, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:  &types.Issue{Title: "two tiers", IssueType: types.TypeTask, Priority: 2},
		Labels: []string{"tier:fable", "tier:opus"},
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), `namespace "tier:" is exclusive`)
}

func (s *testSuite) exclCreateExplicitBeatsInherited() {
	s.resetMintConfig("exb", "")
	uc := s.issueUseCase()
	parent, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:  &types.Issue{Title: "parent", IssueType: types.TypeEpic, Priority: 2},
		Labels: []string{"area:x", "tier:fable"},
	}, "tester")
	s.Require().NoError(err)

	child, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:                   &types.Issue{Title: "child", IssueType: types.TypeTask, Priority: 2},
		ParentID:                parent.Issue.ID,
		InheritLabelsFromParent: true,
		Labels:                  []string{"tier:opus"},
	}, "tester")
	s.Require().NoError(err)
	s.Equal([]string{"area:x"}, child.InheritedLabels, "the parent's tier:fable yields to the explicit tier:opus")
	s.requireLabels(child.Issue.ID, "area:x", "tier:opus")
}

func (s *testSuite) exclCreateInheritedConflict() {
	s.resetMintConfig("exc", "")
	uc := s.issueUseCase()
	parent, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{Title: "violating parent", IssueType: types.TypeEpic, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	s.seedRawLabels(parent.Issue.ID, "tier:fable", "tier:opus")

	_, err = uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:                   &types.Issue{Title: "child", IssueType: types.TypeTask, Priority: 2},
		ParentID:                parent.Issue.ID,
		InheritLabelsFromParent: true,
	}, "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), "parent "+parent.Issue.ID+" carries tier:fable, tier:opus")
	s.Contains(err.Error(), "--no-inherit-labels")

	child, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:                   &types.Issue{Title: "child", IssueType: types.TypeTask, Priority: 2},
		ParentID:                parent.Issue.ID,
		InheritLabelsFromParent: true,
		Labels:                  []string{"tier:opus"},
	}, "tester")
	s.Require().NoError(err, "an explicit label settles the violating parent's namespace")
	s.requireLabels(child.Issue.ID, "tier:opus")
}

func (s *testSuite) exclRenameRefusesNewViolation() {
	s.seedIssueRow("bd-excl-rn-a")
	s.seedIssueRow("bd-excl-rn-b")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-rn-a", "legacy-rn", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-rn-b", "legacy-rn", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-rn-b", "tier:fable", "tester", domain.LabelOpts{}))

	_, _, _, err := r.RenameLabel(s.Ctx(), "legacy-rn", "tier:opus", "tester")
	s.Require().Error(err)
	s.Contains(err.Error(), `cannot rename label "legacy-rn" to "tier:opus"`)
	s.Contains(err.Error(), `bd-excl-rn-b already has "tier:fable"`)
	s.requireLabels("bd-excl-rn-a", "legacy-rn")
	s.requireLabels("bd-excl-rn-b", "legacy-rn", "tier:fable")
}

func (s *testSuite) exclRenameMergeAndSameNamespace() {
	s.seedIssueRow("bd-excl-rm")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-rm", "legacy-rm", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-excl-rm", "tier:rm-a", "tester", domain.LabelOpts{}))

	renamed, merged, _, err := r.RenameLabel(s.Ctx(), "legacy-rm", "tier:rm-a", "tester")
	s.Require().NoError(err, "a carrier already holding the new label merges into it")
	s.Equal(1, renamed)
	s.Equal(1, merged)
	s.requireLabels("bd-excl-rm", "tier:rm-a")

	_, _, _, err = r.RenameLabel(s.Ctx(), "tier:rm-a", "tier:rm-b", "tester")
	s.Require().NoError(err, "a rename within one namespace swaps a label and never adds one")
	s.requireLabels("bd-excl-rm", "tier:rm-b")
}
