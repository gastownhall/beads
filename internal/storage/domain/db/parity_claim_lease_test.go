package db

// Claim + assignee override lease parity (bd-zprbhg, GH PR #6501): a claim
// whose patch also names an assignee runs the claim leg, which arms a lease
// for the claiming actor, and then the fields leg, which hands the claim to the
// override. Both stacks read the SAME database here: the classic stack through
// issueops.ExecuteUpdate (what DoltStore and the embedded store run) and the
// proxied/domain stack through IssueUseCase.ApplyUpdate. Each case runs on
// both and asserts the same lease row, so the two backends cannot drift.
//
// The leases table is dolt-ignored, so the suite's per-method DOLT_RESET does
// not clear it between subtests: every case uses its own issue ID.

import (
	"database/sql"
	"errors"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// claimLeaseUpdate is one update request, spelled once and translated into
// each stack's request shape.
type claimLeaseUpdate struct {
	claim    bool
	assignee *string
	status   *types.Status
}

// claimLeaseStack is one backend's way of applying a claimLeaseUpdate.
type claimLeaseStack struct {
	name  string
	apply func(s *testSuite, id, actor string, u claimLeaseUpdate) error
}

func claimLeaseStacks() []claimLeaseStack {
	return []claimLeaseStack{
		{name: "classic", apply: (*testSuite).classicClaimLeaseUpdate},
		{name: "domain", apply: (*testSuite).domainClaimLeaseUpdate},
	}
}

func (s *testSuite) classicClaimLeaseUpdate(id, actor string, u claimLeaseUpdate) error {
	request := publicops.UpdateRequest{Actor: actor, IssueID: id, Claim: u.claim}
	if u.assignee != nil {
		request.Patch.Assignee = publicops.Field[string]{Set: true, Value: *u.assignee}
	}
	if u.status != nil {
		request.Patch.Status = publicops.Field[publicops.Status]{Set: true, Value: *u.status}
	}
	tx := s.beginClassicTx()
	if _, _, err := issueops.ExecuteUpdate(s.Ctx(), tx, request); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *testSuite) domainClaimLeaseUpdate(id, actor string, u claimLeaseUpdate) error {
	spec := domain.UpdateSpec{Claim: u.claim, Fields: map[string]any{}}
	if u.assignee != nil {
		spec.Fields["assignee"] = *u.assignee
	}
	if u.status != nil {
		spec.Fields["status"] = string(*u.status)
	}
	_, err := s.issueUseCase().ApplyUpdate(s.Ctx(), id, spec, actor)
	return err
}

// claimLeaseRow is an issue's lease row; ok is false when it has none.
type claimLeaseRow struct {
	ok        bool
	holder    string
	grantedAt time.Time
	expiresAt time.Time
}

func (s *testSuite) readClaimLeaseRow(id string) claimLeaseRow {
	var row claimLeaseRow
	err := s.Runner().QueryRowContext(s.Ctx(),
		"SELECT holder, granted_at, lease_expires_at FROM leases WHERE issue_id = ?", id).
		Scan(&row.holder, &row.grantedAt, &row.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return row
	}
	s.Require().NoError(err)
	row.ok = true
	return row
}

func (s *testSuite) countClaimLeaseRows(id string) int {
	var n int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM leases WHERE issue_id = ?", id).Scan(&n))
	return n
}

func (s *testSuite) readClaimLeaseIssue(id string, wisp bool) *types.Issue {
	out, err := s.issueRepo().Get(s.Ctx(), id, domain.IssueTableOpts{UseWispsTable: wisp})
	s.Require().NoError(err)
	return out
}

func claimLeaseString(v string) *string { return &v }

func claimLeaseStatus(v types.Status) *types.Status { return &v }

func (s *testSuite) TestClaimAssigneeOverrideLease() {
	s.Run("ClaimOverrideArmsLeaseForFinalHolder", s.claimOverrideArmsLeaseForFinalHolder)
	s.Run("ClaimOverrideRedolesHeldLeaseFromUpdateTime", s.claimOverrideRedolesHeldLeaseFromUpdateTime)
	s.Run("ClaimOverrideToPoolAliasLeavesNoLease", s.claimOverrideToPoolAliasLeavesNoLease)
	s.Run("ClaimWhosePatchEndsTheClaimLeavesNoLease", s.claimWhosePatchEndsTheClaimLeavesNoLease)
	s.Run("ClaimOverrideOnWispLeavesNoLease", s.claimOverrideOnWispLeavesNoLease)
}

// claimOverrideArmsLeaseForFinalHolder is AC-1: alice claims an open,
// unassigned issue on bob's behalf. Both stacks leave it in_progress/bob with
// exactly one lease row, held by bob and expiring in the future. Before the fix
// the fields leg deleted the lease the claim leg had just armed.
func (s *testSuite) claimOverrideArmsLeaseForFinalHolder() {
	for _, stack := range claimLeaseStacks() {
		s.Run(stack.name, func() {
			id := "bd-cl-ovr-" + stack.name
			s.seedOpenIssue(id)
			s.Require().NoError(stack.apply(s, id, "alice", claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob")}))

			got := s.readClaimLeaseIssue(id, false)
			s.Equal(types.StatusInProgress, got.Status)
			s.Equal("bob", got.Assignee)
			s.Equal(1, s.countClaimLeaseRows(id), "claim with an assignee override must leave exactly one lease row")
			lease := s.readClaimLeaseRow(id)
			s.Require().True(lease.ok)
			s.Equal("bob", lease.holder, "the lease belongs to the final assignee, not the claiming actor")
			s.True(lease.expiresAt.After(time.Now().UTC()), "lease_expires_at = %v, want a future time", lease.expiresAt)
		})
	}
}

// claimOverrideRedolesHeldLeaseFromUpdateTime is AC-2: alice already holds the
// issue under a live lease and re-doles it to bob with --claim. The lease row
// is bob's, granted at the update's own time (the updated_at it wrote) and
// expiring LeaseTTL after it, not the remainder of alice's lease.
func (s *testSuite) claimOverrideRedolesHeldLeaseFromUpdateTime() {
	for _, stack := range claimLeaseStacks() {
		s.Run(stack.name, func() {
			id := "bd-cl-redole-" + stack.name
			s.seedOpenIssue(id)
			_, err := s.issueUseCase().ClaimIssue(s.Ctx(), id, "alice")
			s.Require().NoError(err)
			s.Require().Equal("alice", s.readClaimLeaseRow(id).holder, "seed claim must arm alice's lease")

			s.Require().NoError(stack.apply(s, id, "alice", claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob")}))

			var updatedAt time.Time
			s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
				"SELECT updated_at FROM issues WHERE id = ?", id).Scan(&updatedAt))
			s.Equal(1, s.countClaimLeaseRows(id))
			lease := s.readClaimLeaseRow(id)
			s.Require().True(lease.ok, "a re-dole with --claim must stay leased")
			s.Equal("bob", lease.holder)
			s.True(lease.grantedAt.Equal(updatedAt), "granted_at = %v, want the update time %v", lease.grantedAt, updatedAt)
			s.Equal(issueops.DefaultLeaseTTL, lease.expiresAt.Sub(lease.grantedAt), "lease_expires_at must be granted_at + LeaseTTL")
		})
	}
}

// claimOverrideToPoolAliasLeavesNoLease is AC-3: a claim.pools alias is a
// shared pseudo-assignee nobody heartbeats as, so a lease for it would only
// expire and let bd reclaim un-assign live pool work (GH#4716). Claiming on
// the pool's behalf leaves the issue in_progress/<pool> with no lease row.
func (s *testSuite) claimOverrideToPoolAliasLeavesNoLease() {
	cleanup := s.setClaimPools("fable-crew")
	defer cleanup()
	for _, stack := range claimLeaseStacks() {
		s.Run(stack.name, func() {
			id := "bd-cl-pool-" + stack.name
			s.seedOpenIssue(id)
			s.Require().NoError(stack.apply(s, id, "alice", claimLeaseUpdate{claim: true, assignee: claimLeaseString("fable-crew")}))

			got := s.readClaimLeaseIssue(id, false)
			s.Equal(types.StatusInProgress, got.Status)
			s.Equal("fable-crew", got.Assignee)
			s.Equal(0, s.countClaimLeaseRows(id), "a claim handed to a claim.pools alias must hold no lease")
		})
	}
}

// claimWhosePatchEndsTheClaimLeavesNoLease is AC-4: a --claim update whose
// patch ends the claim (a status other than in_progress, built-in or custom,
// or an empty assignee) commits with no lease row.
func (s *testSuite) claimWhosePatchEndsTheClaimLeavesNoLease() {
	s.Require().NoError(s.configRepo().SetConfig(s.Ctx(), "status.custom", "triaged:active"))
	defer func() { s.Require().NoError(s.configRepo().SetConfig(s.Ctx(), "status.custom", "")) }()
	cases := []struct {
		name   string
		update claimLeaseUpdate
	}{
		{name: "open", update: claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob"), status: claimLeaseStatus(types.StatusOpen)}},
		{name: "blocked", update: claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob"), status: claimLeaseStatus(types.StatusBlocked)}},
		{name: "custom", update: claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob"), status: claimLeaseStatus("triaged")}},
		{name: "unassigned", update: claimLeaseUpdate{claim: true, assignee: claimLeaseString("")}},
	}
	for _, stack := range claimLeaseStacks() {
		for _, tc := range cases {
			s.Run(stack.name+"/"+tc.name, func() {
				id := "bd-cl-end-" + stack.name + "-" + tc.name
				s.seedOpenIssue(id)
				s.Require().NoError(stack.apply(s, id, "alice", tc.update))

				got := s.readClaimLeaseIssue(id, false)
				if tc.update.status != nil {
					s.Equal(*tc.update.status, got.Status)
				}
				s.Equal(*tc.update.assignee, got.Assignee)
				s.Equal(0, s.countClaimLeaseRows(id), "a claim whose patch ends the claim must leave no lease")
			})
		}
	}
}

// claimOverrideOnWispLeavesNoLease is AC-5: wisps are never leased, so a
// --claim with an assignee override on one leaves no lease row.
func (s *testSuite) claimOverrideOnWispLeavesNoLease() {
	for _, stack := range claimLeaseStacks() {
		s.Run(stack.name, func() {
			id := "bd-cl-wisp-" + stack.name
			s.seedParityIssue(id, nil, true)
			s.Require().NoError(stack.apply(s, id, "alice", claimLeaseUpdate{claim: true, assignee: claimLeaseString("bob")}))

			got := s.readClaimLeaseIssue(id, true)
			s.Equal(types.StatusInProgress, got.Status)
			s.Equal("bob", got.Assignee)
			s.Equal(0, s.countClaimLeaseRows(id), "a wisp must never hold a lease")
		})
	}
}
