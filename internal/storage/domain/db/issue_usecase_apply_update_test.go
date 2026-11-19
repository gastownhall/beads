package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
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
	s.Run("ClaimAssigneeOverrideArmsLease", s.iucApplyUpdateClaimAssigneeOverrideArmsLease)
	s.Run("ClaimAssigneeOverrideOnLiveClaimReArmsForNewHolder", s.iucApplyUpdateClaimOverrideOnLiveClaimReArms)
	s.Run("ClaimAssigneeOverridePoolAliasStaysLeaseless", s.iucApplyUpdateClaimOverridePoolAliasStaysLeaseless)
	s.Run("ClaimOverrideEndingClaimStillDeletesLease", s.iucApplyUpdateClaimOverrideEndingClaimStillDeletesLease)
	s.Run("ClaimAssigneeOverrideOnWispNeverLeases", s.iucApplyUpdateClaimOverrideOnWispNeverLeases)
	s.Run("PlainUpdatesDeleteLeaseAndArmNone", s.iucApplyUpdatePlainUpdatesDeleteLeaseAndArmNone)
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
		map[string]any{"assignee": "alice"}, "seeder", domain.IssueTableOpts{}, false))

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

// iucApplyUpdateClaimAssigneeOverrideArmsLease pins bd-zprbhg: a claim that
// also overrides the assignee leaves a live in_progress claim, so it must
// keep a lease held by the final assignee instead of deleting the lease the
// claim leg just armed.
func (s *testSuite) iucApplyUpdateClaimAssigneeOverrideArmsLease() {
	s.seedOpenIssue("bd-iuc-au-cao")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-cao", domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"assignee": "bob"},
	}, "alice")
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, updated.Status)
	s.Equal("bob", updated.Assignee)

	var holder string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT holder FROM leases WHERE issue_id = ?", "bd-iuc-au-cao").Scan(&holder),
		"claim with an assignee override must leave a lease row")
	s.Equal("bob", holder)
}

// iucApplyUpdateClaimOverrideOnLiveClaimReArms pins AC-2 of bd-zprbhg on the
// proxied/domain stack: the current holder re-doling their own claim with an
// assignee override (the claim leg's idempotent AlreadyClaimed path) re-arms
// the lease for the final assignee with a fresh grant at the update time.
func (s *testSuite) iucApplyUpdateClaimOverrideOnLiveClaimReArms() {
	s.seedOpenIssue("bd-iuc-au-cao2")
	uc := s.issueUseCase()
	_, err := uc.ClaimIssue(s.Ctx(), "bd-iuc-au-cao2", "alice")
	s.Require().NoError(err)

	updateStart := time.Now().UTC()
	updated, err := uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-cao2", domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"assignee": "bob"},
	}, "alice")
	s.Require().NoError(err)
	updateEnd := time.Now().UTC()
	s.Equal(types.StatusInProgress, updated.Status)
	s.Equal("bob", updated.Assignee)

	var holder string
	var grantedAt, expiresAt time.Time
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT holder, granted_at, lease_expires_at FROM leases WHERE issue_id = ?",
		"bd-iuc-au-cao2").Scan(&holder, &grantedAt, &expiresAt),
		"claim+override on a live claim must re-arm the lease")
	s.Equal("bob", holder)
	// DATETIME is second-granular; allow one second of rounding either way.
	s.False(grantedAt.Before(updateStart.Add(-time.Second)), "granted_at %v before update %v", grantedAt, updateStart)
	s.False(grantedAt.After(updateEnd.Add(time.Second)), "granted_at %v after update %v", grantedAt, updateEnd)
	wantExpiry := grantedAt.Add(issueops.DefaultLeaseTTL)
	s.LessOrEqual(expiresAt.Sub(wantExpiry).Abs(), time.Second,
		"lease_expires_at %v, want granted_at + DefaultLeaseTTL %v", expiresAt, wantExpiry)
}

// iucApplyUpdateClaimOverridePoolAliasStaysLeaseless pins AC-3 of bd-zprbhg
// on the proxied/domain stack: a claim whose assignee override names a
// claim.pools alias leaves the issue in_progress under the alias with NO
// lease row. Nothing heartbeats as an alias, so an armed lease would be
// certain reclaim-bait (GH#4716); pool members reach the issue through the
// claim CAS.
func (s *testSuite) iucApplyUpdateClaimOverridePoolAliasStaysLeaseless() {
	cleanup := s.setClaimPools("fable-crew")
	defer cleanup()

	s.seedOpenIssue("bd-iuc-au-cao3")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-cao3", domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"assignee": "fable-crew"},
	}, "alice")
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, updated.Status)
	s.Equal("fable-crew", updated.Assignee)

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM leases WHERE issue_id = ?", "bd-iuc-au-cao3").Scan(&count))
	s.Equal(0, count, "a claim.pools alias must not hold a lease row")
}

// iucApplyUpdateClaimOverrideEndingClaimStillDeletesLease pins AC-4 of
// bd-zprbhg on the proxied/domain stack: a claim update whose patch ends the
// claim (status reverted to open, a custom active status, or a cleared
// assignee) commits with no lease row. The re-arm covers a live claim only.
func (s *testSuite) iucApplyUpdateClaimOverrideEndingClaimStillDeletesLease() {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{name: "StatusOpen", fields: map[string]any{"assignee": "bob", "status": types.StatusOpen}},
		{name: "ClearedAssignee", fields: map[string]any{"assignee": ""}},
	}
	for i, tc := range cases {
		s.Run(tc.name, func() {
			id := fmt.Sprintf("bd-iuc-au-cao4-%d", i)
			s.seedOpenIssue(id)
			_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), id, domain.UpdateSpec{
				Claim:  true,
				Fields: tc.fields,
			}, "alice")
			s.Require().NoError(err)

			var count int
			s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
				"SELECT COUNT(*) FROM leases WHERE issue_id = ?", id).Scan(&count))
			s.Equal(0, count, "patch ending the claim must leave no lease row")
		})
	}
}

// iucApplyUpdateClaimOverrideOnWispNeverLeases pins AC-5 of bd-zprbhg on the
// proxied/domain stack: a wisp claimed with an assignee override lands
// in_progress under the override target but never gets a lease row — wisps
// are not reclaimable work and are never leased.
func (s *testSuite) iucApplyUpdateClaimOverrideOnWispNeverLeases() {
	s.seedOpenWisp("bd-iuc-au-cao5")
	updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-cao5", domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"assignee": "bob"},
	}, "alice")
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, updated.Status)
	s.Equal("bob", updated.Assignee)

	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM leases WHERE issue_id = ?", "bd-iuc-au-cao5").Scan(&count))
	s.Equal(0, count, "wisps must never get a lease row, even via claim+override")
}

// iucApplyUpdatePlainUpdatesDeleteLeaseAndArmNone pins AC-6 of bd-zprbhg on
// the proxied/domain stack: updates without Claim stay clear-only. A bare
// status+assignee hand-dole arms nothing (GH#4716: nobody heartbeats an
// interactive claim, so a lease would be reclaim-bait), and a plain assignee
// transfer deletes the previous holder's lease without arming one for the
// new holder.
func (s *testSuite) iucApplyUpdatePlainUpdatesDeleteLeaseAndArmNone() {
	s.Run("HandDoleArmsNothing", func() {
		s.seedOpenIssue("bd-iuc-au-cao6a")
		updated, err := s.issueUseCase().ApplyUpdate(s.Ctx(), "bd-iuc-au-cao6a", domain.UpdateSpec{
			Fields: map[string]any{"assignee": "crow", "status": types.StatusInProgress},
		}, "crow")
		s.Require().NoError(err)
		s.Equal(types.StatusInProgress, updated.Status)
		s.Equal("crow", updated.Assignee)

		var count int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM leases WHERE issue_id = ?", "bd-iuc-au-cao6a").Scan(&count))
		s.Equal(0, count, "a plain update must not arm a lease")
	})

	s.Run("TransferDeletesHoldersLease", func() {
		s.seedOpenIssue("bd-iuc-au-cao6b")
		uc := s.issueUseCase()
		_, err := uc.ClaimIssue(s.Ctx(), "bd-iuc-au-cao6b", "alice")
		s.Require().NoError(err)

		updated, err := uc.ApplyUpdate(s.Ctx(), "bd-iuc-au-cao6b", domain.UpdateSpec{
			Fields: map[string]any{"assignee": "bob"},
		}, "alice")
		s.Require().NoError(err)
		s.Equal("bob", updated.Assignee)

		var count int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM leases WHERE issue_id = ?", "bd-iuc-au-cao6b").Scan(&count))
		s.Equal(0, count, "a plain transfer must delete the holder's lease and arm none")
	})
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
