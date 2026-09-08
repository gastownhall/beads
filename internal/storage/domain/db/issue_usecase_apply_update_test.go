package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_Claim() {
	s.Run("SuccessReturnsEmptyResult", s.iucClaimSuccess)
	s.Run("IdempotentReclaimMarksAlreadyClaimed", s.iucClaimIdempotent)
	s.Run("ConflictWrapsErrAlreadyClaimed", s.iucClaimConflict)
	s.Run("ClosedWrapsErrNotClaimable", s.iucClaimClosed)
	s.Run("EmptyIDReturnsError", s.iucClaimEmptyID)
	s.Run("EmptyActorReturnsError", s.iucClaimEmptyActor)
	s.Run("ClaimWispWritesToWispsTable", s.iucClaimWispWritesToWispsTable)
}

func (s *testSuite) TestIssueUseCase_ApplyUpdate() {
	s.Run("EmptyIDReturnsError", s.iucApplyUpdateEmptyID)
	s.Run("FieldsOnlyAppliesAndReFetches", s.iucApplyUpdateFieldsOnly)
	s.Run("ClaimAndFieldsRunTogether", s.iucApplyUpdateClaimPlusFields)
	s.Run("AddRemoveLabelPaths", s.iucApplyUpdateAddRemoveLabels)
	s.Run("SetLabelsDiffsAgainstCurrent", s.iucApplyUpdateSetLabels)
	s.Run("SetLabelsTakesPrecedenceOverAddRemove", s.iucApplyUpdateSetLabelsBeatsAddRemove)
	s.Run("ReparentReplacesParent", s.iucApplyUpdateReparent)
	s.Run("ReparentEmptyUnparents", s.iucApplyUpdateUnparent)
	s.Run("NoSpecBitsIsHarmless", s.iucApplyUpdateEmptySpec)
	s.Run("WispIDDispatchesToWispTables", s.iucApplyUpdateDispatchesToWisp)
	s.Run("ClaimAgainstWispDispatches", s.iucClaimDispatchesToWisp)
}

func (s *testSuite) seedOpenIssue(id string) {
	r := s.issueRepo()
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "seed"), "seeder", domain.InsertIssueOpts{}))
}

func (s *testSuite) iucClaimSuccess() {
	s.seedOpenIssue("bd-iuc-cl-ok")
	res, err := s.issueUseCase().ClaimIssue(s.Ctx(), "bd-iuc-cl-ok", "alice")
	s.Require().NoError(err)
	s.False(res.AlreadyClaimed)
	s.Equal("", res.PriorAssignee)
}

func (s *testSuite) iucClaimIdempotent() {
	s.seedOpenIssue("bd-iuc-cl-idem")
	uc := s.issueUseCase()
	_, err := uc.ClaimIssue(s.Ctx(), "bd-iuc-cl-idem", "alice")
	s.Require().NoError(err)

	res, err := uc.ClaimIssue(s.Ctx(), "bd-iuc-cl-idem", "alice")
	s.Require().NoError(err)
	s.True(res.AlreadyClaimed)
	s.Equal("alice", res.PriorAssignee)
}

func (s *testSuite) iucClaimConflict() {
	s.seedOpenIssue("bd-iuc-cl-conf")
	uc := s.issueUseCase()
	_, err := uc.ClaimIssue(s.Ctx(), "bd-iuc-cl-conf", "alice")
	s.Require().NoError(err)

	_, err = uc.ClaimIssue(s.Ctx(), "bd-iuc-cl-conf", "bob")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrAlreadyClaimed), "expected ErrAlreadyClaimed, got %v", err)
	s.Contains(err.Error(), "alice")
}

func (s *testSuite) iucClaimClosed() {
	s.seedOpenIssue("bd-iuc-cl-closed")
	r := s.issueRepo()
	s.Require().NoError(r.Update(s.Ctx(), "bd-iuc-cl-closed",
		map[string]any{"status": string(types.StatusClosed)}, "seeder", domain.IssueTableOpts{}))

	_, err := s.issueUseCase().ClaimIssue(s.Ctx(), "bd-iuc-cl-closed", "alice")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrNotClaimable), "expected ErrNotClaimable, got %v", err)
}

func (s *testSuite) iucClaimEmptyID() {
	_, err := s.issueUseCase().ClaimIssue(s.Ctx(), "", "alice")
	s.Require().Error(err)
}

func (s *testSuite) iucClaimEmptyActor() {
	_, err := s.issueUseCase().ClaimIssue(s.Ctx(), "bd-x", "")
	s.Require().Error(err)
}

func (s *testSuite) iucApplyUpdateEmptyID() {
	_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "", domain.UpdateSpec{}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) iucApplyUpdateFieldsOnly() {
	s.seedOpenIssue("bd-iuc-au-f")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-f", domain.UpdateSpec{
		Fields: map[string]any{"title": "renamed", "priority": 0},
	}, "tester")
	s.Require().NoError(err)
	s.Equal("renamed", updated.Title)
	s.Equal(0, updated.Priority)
}

func (s *testSuite) iucApplyUpdateClaimPlusFields() {
	s.seedOpenIssue("bd-iuc-au-cf")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-cf", domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"priority": 1},
	}, "alice")
	s.Require().NoError(err)
	s.Equal("alice", updated.Assignee)
	s.Equal(types.StatusInProgress, updated.Status)
	s.Equal(1, updated.Priority)
}

func (s *testSuite) iucApplyUpdateAddRemoveLabels() {
	s.seedOpenIssue("bd-iuc-au-arl")
	uc := s.issueUseCase()
	_, err := uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-arl", domain.UpdateSpec{
		AddLabels: []string{"keep", "drop"},
	}, "tester")
	s.Require().NoError(err)

	_, err = uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-arl", domain.UpdateSpec{
		AddLabels:    []string{"new"},
		RemoveLabels: []string{"drop"},
	}, "tester")
	s.Require().NoError(err)

	labels, err := s.labelUseCase().GetLabels(s.Ctx(), "bd-iuc-au-arl")
	s.Require().NoError(err)
	s.Equal([]string{"keep", "new"}, labels)
}

func (s *testSuite) iucApplyUpdateSetLabels() {
	s.seedOpenIssue("bd-iuc-au-sl")
	uc := s.issueUseCase()
	_, err := uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-sl", domain.UpdateSpec{
		AddLabels: []string{"x", "y"},
	}, "tester")
	s.Require().NoError(err)

	desired := []string{"y", "z"}
	_, err = uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-sl", domain.UpdateSpec{
		SetLabels: &desired,
	}, "tester")
	s.Require().NoError(err)

	labels, err := s.labelUseCase().GetLabels(s.Ctx(), "bd-iuc-au-sl")
	s.Require().NoError(err)
	s.Equal([]string{"y", "z"}, labels)
}

func (s *testSuite) iucApplyUpdateSetLabelsBeatsAddRemove() {
	s.seedOpenIssue("bd-iuc-au-sl-prec")
	uc := s.issueUseCase()

	desired := []string{"only-this"}
	_, err := uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-sl-prec", domain.UpdateSpec{
		AddLabels:    []string{"add-me"},
		RemoveLabels: []string{"remove-me"},
		SetLabels:    &desired,
	}, "tester")
	s.Require().NoError(err)

	labels, err := s.labelUseCase().GetLabels(s.Ctx(), "bd-iuc-au-sl-prec")
	s.Require().NoError(err)
	s.Equal([]string{"only-this"}, labels)
}

func (s *testSuite) iucApplyUpdateReparent() {
	s.seedOpenIssue("bd-iuc-au-rp-c")
	s.seedOpenIssue("bd-iuc-au-rp-old")
	s.seedOpenIssue("bd-iuc-au-rp-new")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-iuc-au-rp-c", "bd-iuc-au-rp-old", types.DepParentChild), "seeder", domain.DepInsertOpts{}))

	newParent := "bd-iuc-au-rp-new"
	_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-rp-c", domain.UpdateSpec{
		Reparent: &newParent,
	}, "tester")
	s.Require().NoError(err)

	s.Equal("bd-iuc-au-rp-new", s.currentParent("bd-iuc-au-rp-c"))
}

func (s *testSuite) iucApplyUpdateUnparent() {
	s.seedOpenIssue("bd-iuc-au-up-c")
	s.seedOpenIssue("bd-iuc-au-up-p")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		newDep("bd-iuc-au-up-c", "bd-iuc-au-up-p", types.DepParentChild), "seeder", domain.DepInsertOpts{}))

	empty := ""
	_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-up-c", domain.UpdateSpec{
		Reparent: &empty,
	}, "tester")
	s.Require().NoError(err)

	s.Equal("", s.currentParent("bd-iuc-au-up-c"))
}

func (s *testSuite) iucApplyUpdateEmptySpec() {
	s.seedOpenIssue("bd-iuc-au-empty")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-empty", domain.UpdateSpec{}, "tester")
	s.Require().NoError(err)
	s.Equal("bd-iuc-au-empty", updated.ID)
}

func (s *testSuite) seedOpenWisp(id string) {
	r := s.issueRepo()
	w := newTestIssue(id, "seed wisp")
	w.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), w, "seeder", domain.InsertIssueOpts{UseWispsTable: true}))
}

func (s *testSuite) iucApplyUpdateDispatchesToWisp() {
	s.seedOpenWisp("bd-iuc-au-wisp-c")
	s.seedOpenWisp("bd-iuc-au-wisp-newp")
	depRepo := NewDependencySQLRepository(s.Runner())
	s.Require().NoError(depRepo.Insert(s.Ctx(),
		&types.Dependency{IssueID: "bd-iuc-au-wisp-c", DependsOnID: "bd-iuc-au-wisp-newp", Type: types.DepParentChild},
		"seeder", domain.DepInsertOpts{UseWispsTable: true}))

	setLabels := []string{"alpha", "beta"}
	reparent := "bd-iuc-au-wisp-newp"
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-wisp-c", domain.UpdateSpec{
		Fields:    map[string]any{"title": "wisp renamed"},
		SetLabels: &setLabels,
		Reparent:  &reparent,
	}, "tester")
	s.Require().NoError(err)
	s.Equal("wisp renamed", updated.Title)

	wispRow, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-au-wisp-c", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("wisp renamed", wispRow.Title, "update must land in wisps table")

	wispLabels, err := s.labelUseCase().GetWispLabels(s.Ctx(), "bd-iuc-au-wisp-c")
	s.Require().NoError(err)
	s.Equal([]string{"alpha", "beta"}, wispLabels)

	var issueLabelCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM labels WHERE issue_id = ?", "bd-iuc-au-wisp-c").Scan(&issueLabelCount))
	s.Equal(0, issueLabelCount, "wisp-dispatched ApplyUpdate must not write to issues label table")
}

func (s *testSuite) iucClaimWispWritesToWispsTable() {
	s.seedOpenWisp("bd-iuc-clw-wisp")

	res, err := s.issueUseCase().ClaimWisp(s.Ctx(), "bd-iuc-clw-wisp", "alice")
	s.Require().NoError(err)
	s.False(res.AlreadyClaimed)

	wispRow, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-clw-wisp", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("alice", wispRow.Assignee)
	s.Equal(types.StatusInProgress, wispRow.Status)
}

func (s *testSuite) iucClaimDispatchesToWisp() {
	s.seedOpenWisp("bd-iuc-au-clw-wisp")

	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-clw-wisp", domain.UpdateSpec{
		Claim: true,
	}, "alice")
	s.Require().NoError(err)
	s.Equal("alice", updated.Assignee)
	s.Equal(types.StatusInProgress, updated.Status)

	wispRow, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-au-clw-wisp", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("alice", wispRow.Assignee, "ApplyUpdate's Claim branch must route to ClaimWisp for a wisp id")
}
