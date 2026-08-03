package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_Claim() {
	s.Run("SuccessReturnsEmptyResult", s.iucClaimSuccess)
	s.Run("IdempotentReclaimMarksAlreadyClaimed", s.iucClaimIdempotent)
	s.Run("ConflictWrapsErrAlreadyClaimed", s.iucClaimConflict)
	s.Run("OpenAssignedRefusalSteersToHolder", s.iucClaimOpenAssignedCopy)
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
	s.Run("SetLabelsThenAddsThenRemoves", s.iucApplyUpdateSetLabelsThenAddRemove)
	s.Run("SameScalarAndMetadataAreNoops", s.iucApplyUpdateSameScalarAndMetadataAreNoops)
	s.Run("ReparentReplacesParent", s.iucApplyUpdateReparent)
	s.Run("ReparentEmptyUnparents", s.iucApplyUpdateUnparent)
	s.Run("NoSpecBitsIsHarmless", s.iucApplyUpdateEmptySpec)
	s.Run("WispIDDispatchesToWispTables", s.iucApplyUpdateDispatchesToWisp)
	s.Run("ClaimAgainstWispDispatches", s.iucClaimDispatchesToWisp)
	s.Run("ExpectedVersionRefusesStaleWrite", s.iucApplyUpdateExpectedVersion)
	s.Run("PersistenceMovesNoOpsAndRollsBack", s.iucApplyUpdatePersistence)
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

// iucClaimOpenAssignedCopy pins the proxied-path refusal copy for an OPEN
// issue assigned to another real actor (bd-at6rc parity, found by review):
// it must steer toward the holder like issueops.ClaimIssueInTx, keep the
// ErrAlreadyClaimed wrap (the proxied batch exit code keys on errors.Is),
// and never name an eviction command.
func (s *testSuite) iucClaimOpenAssignedCopy() {
	s.seedOpenIssue("bd-iuc-cl-openassigned")
	r := s.issueRepo()
	s.Require().NoError(r.Update(s.Ctx(), "bd-iuc-cl-openassigned",
		map[string]any{"assignee": "alice"}, "seeder", domain.IssueTableOpts{}))

	_, err := s.issueUseCase().ClaimIssue(s.Ctx(), "bd-iuc-cl-openassigned", "bob")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrAlreadyClaimed), "expected ErrAlreadyClaimed wrap, got %v", err)
	s.Contains(err.Error(), "coordinate with the holder")
	s.NotContains(err.Error(), "unclaim")
	s.NotContains(err.Error(), "--force")
}

func (s *testSuite) iucClaimClosed() {
	s.seedOpenIssue("bd-iuc-cl-closed")
	_, err := s.issueUseCase().CloseIssue(s.Ctx(), "bd-iuc-cl-closed", domain.CloseIssueParams{}, "seeder")
	s.Require().NoError(err)

	_, err = s.issueUseCase().ClaimIssue(s.Ctx(), "bd-iuc-cl-closed", "alice")
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

func (s *testSuite) iucApplyUpdateSetLabelsThenAddRemove() {
	s.seedOpenIssue("bd-iuc-au-sl-order")
	desired := []string{"replace", "remove"}
	_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-sl-order", domain.UpdateSpec{
		SetLabels:    &desired,
		AddLabels:    []string{"add", "remove"},
		RemoveLabels: []string{"remove"},
	}, "tester")
	s.Require().NoError(err)
	labels, err := s.labelUseCase().GetLabels(s.Ctx(), "bd-iuc-au-sl-order")
	s.Require().NoError(err)
	s.Equal([]string{"add", "replace"}, labels)
}

func (s *testSuite) iucApplyUpdateSameScalarAndMetadataAreNoops() {
	s.seedOpenIssue("bd-iuc-au-noop")
	uc := s.issueUseCase()
	before, err := uc.GetIssue(s.Ctx(), "bd-iuc-au-noop")
	s.Require().NoError(err)
	var beforeEvents int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT COUNT(*) FROM events WHERE issue_id = ?", before.ID).Scan(&beforeEvents))

	updated, err := uc.ApplyUpdate(s.Ctx(), before.ID, domain.UpdateSpec{Fields: map[string]any{
		"priority": before.Priority,
		"metadata": json.RawMessage(`{}`),
	}}, "tester")
	s.Require().NoError(err)
	s.Equal(before.RowVersion, updated.RowVersion)
	var afterEvents int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT COUNT(*) FROM events WHERE issue_id = ?", before.ID).Scan(&afterEvents))
	s.Equal(beforeEvents, afterEvents)
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

func (s *testSuite) iucApplyUpdateExpectedVersion() {
	s.seedOpenIssue("bd-iuc-au-version")
	current, err := s.issueUseCase().GetIssue(s.Ctx(), "bd-iuc-au-version")
	s.Require().NoError(err)
	staleVersion := current.RowVersion + 1

	_, err = s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-version", domain.UpdateSpec{
		ExpectedVersion: &staleVersion,
		Fields:          map[string]any{"title": "must not persist"},
	}, "tester")
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrVersionMismatch), "want ErrVersionMismatch, got %v", err)

	stored, err := s.issueUseCase().GetIssue(s.Ctx(), "bd-iuc-au-version")
	s.Require().NoError(err)
	s.Equal("seed", stored.Title)
}

func (s *testSuite) iucApplyUpdatePersistence() {
	s.seedOpenIssue("bd-iuc-au-persist")
	mode := types.PersistenceModeEphemeral
	moved, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-persist", domain.UpdateSpec{Persistence: &mode}, "tester")
	s.Require().NoError(err)
	s.True(moved.Ephemeral)

	_, err = s.issueRepo().Get(s.Ctx(), "bd-iuc-au-persist", domain.IssueTableOpts{})
	s.True(errors.Is(err, sql.ErrNoRows), "durable source must be removed, got %v", err)
	stored, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-au-persist", domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	version := stored.RowVersion

	unchanged, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-persist", domain.UpdateSpec{Persistence: &mode}, "tester")
	s.Require().NoError(err)
	s.Equal(version, unchanged.RowVersion, "same persistence mode must not rewrite the row")

	s.seedOpenIssue("bd-iuc-au-persist-rollback")
	_, err = s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-persist-rollback", domain.UpdateSpec{
		Fields:      map[string]any{"not_a_column": "bad"},
		Persistence: &mode,
	}, "tester")
	s.Require().Error(err)
	_, err = s.issueRepo().Get(s.Ctx(), "bd-iuc-au-persist-rollback", domain.IssueTableOpts{})
	s.Require().NoError(err)
	_, err = s.issueRepo().Get(s.Ctx(), "bd-iuc-au-persist-rollback", domain.IssueTableOpts{UseWispsTable: true})
	s.True(errors.Is(err, sql.ErrNoRows), "failed update must not move persistence plane, got %v", err)
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
