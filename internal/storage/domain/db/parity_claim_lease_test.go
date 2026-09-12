package db

// Seam B cross-mode parity tests (be-plv): compares the direct/embedded
// stack's issueops.ExecuteUpdate (what DoltStore's issueops.Lifecycle.Update
// runs inside its own transaction, internal/storage/dolt/issue_operations.go)
// against the domain use case's IssueUseCase.ApplyUpdate (what the
// proxied-server issueops.Lifecycle.Update calls via updateSpec,
// internal/storage/uow/issue_operations.go). Both are the composition layer
// where a same-request Claim and field Patch combine, and that is exactly
// where the lease behavior below lives: neither backend's own Lifecycle
// wrapper (dolt's issueOperations.Update, uow's issueOperations.Update) adds
// or removes any lease-relevant logic above this layer, so comparing here is
// equivalent to comparing at the CLI's `bd update --claim` entry point
// without paying for the full transaction-retry/history/provider machinery
// each wrapper also carries.
//
// Two of the three cases are genuine agreement, proven by actually running
// both backends against the same table shapes and comparing. The third,
// claim + assignee override, is a characterization test: it pins a KNOWN,
// CURRENT divergence rather than asserting equality. Classic drops the lease
// on that case (a pre-existing bug, tracked by unmerged PR #5349, out of
// scope for be-plv to fix or for this bead to reopen); domain re-arms it
// (the be-plv fix, internal/storage/domain/issue.go's update()). Once #5349
// merges and classic re-arms too, that third test should be rewritten into a
// genuine equality assertion like its two siblings.
//
// leaseRow queries the leases table by issue_id and does not fail on a
// missing row: DELETE removes the row entirely (see ManageLeaseOnUpdate),
// so "no row" is the normal shape for "not currently leased", not an error.

import (
	"database/sql"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func (s *testSuite) leaseRow(id string) (holder string, hasLease bool) {
	var h sql.NullString
	err := s.Runner().QueryRowContext(s.Ctx(), "SELECT holder FROM leases WHERE issue_id = ?", id).Scan(&h)
	if err == sql.ErrNoRows {
		return "", false
	}
	s.Require().NoError(err)
	return h.String, true
}

// TestParityPlainTransferDeletesLeaseOnBothBackends: a non-claim assignee
// transfer issued by the current holder (no fence applies: Claim is false
// and the actor already holds the row) deletes the lease identically on
// both backends today, independent of be-plv and PR #5349.
func (s *testSuite) TestParityPlainTransferDeletesLeaseOnBothBackends() {
	ctx := s.Ctx()
	r := s.issueRepo()

	classicID := "bd-parity-transfer-classic"
	s.Require().NoError(r.Insert(ctx, newTestIssue(classicID, "x"), "tester", domain.InsertIssueOpts{}))
	tx := s.beginClassicTx()
	_, _, err := issueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{Actor: "alice", IssueID: classicID, Claim: true})
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())

	tx = s.beginClassicTx()
	_, _, err = issueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: classicID,
		Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "bob"}},
	})
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())

	_, classicHasLease := s.leaseRow(classicID)
	s.False(classicHasLease, "classic: a plain (non-claim) transfer must delete the lease")

	domainID := "bd-parity-transfer-domain"
	s.Require().NoError(r.Insert(ctx, newTestIssue(domainID, "x"), "tester", domain.InsertIssueOpts{}))
	uc := s.issueUseCase()
	_, err = uc.ApplyUpdate(ctx, domainID, domain.UpdateSpec{Claim: true}, "alice")
	s.Require().NoError(err)
	_, err = uc.ApplyUpdate(ctx, domainID, domain.UpdateSpec{Fields: map[string]any{"assignee": "bob"}}, "alice")
	s.Require().NoError(err)

	_, domainHasLease := s.leaseRow(domainID)
	s.False(domainHasLease, "domain: a plain (non-claim) transfer must delete the lease")
	s.Equal(classicHasLease, domainHasLease, "backends must agree on the plain-transfer case")
}

// TestParityClaimNoOverridePreservesLeaseOnBothBackends: a bare claim with no
// field-level override grants a lease identically on both backends, since
// neither ExecuteUpdate's `len(updates) > 0` guard nor ApplyUpdate's
// `len(spec.Fields) > 0` guard is entered — the lease the claim itself just
// granted is never revisited by either backend's update-half.
func (s *testSuite) TestParityClaimNoOverridePreservesLeaseOnBothBackends() {
	ctx := s.Ctx()
	r := s.issueRepo()

	classicID := "bd-parity-nooverride-classic"
	s.Require().NoError(r.Insert(ctx, newTestIssue(classicID, "x"), "tester", domain.InsertIssueOpts{}))
	tx := s.beginClassicTx()
	_, _, err := issueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{Actor: "alice", IssueID: classicID, Claim: true})
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())

	classicHolder, classicHasLease := s.leaseRow(classicID)
	s.True(classicHasLease, "classic: a bare claim must grant a lease")
	s.Equal("alice", classicHolder)

	domainID := "bd-parity-nooverride-domain"
	s.Require().NoError(r.Insert(ctx, newTestIssue(domainID, "x"), "tester", domain.InsertIssueOpts{}))
	uc := s.issueUseCase()
	_, err = uc.ApplyUpdate(ctx, domainID, domain.UpdateSpec{Claim: true}, "alice")
	s.Require().NoError(err)

	domainHolder, domainHasLease := s.leaseRow(domainID)
	s.True(domainHasLease, "domain: a bare claim must grant a lease")
	s.Equal("alice", domainHolder)
	s.Equal(classicHasLease, domainHasLease, "backends must agree on the claim-with-no-override case")
	s.Equal(classicHolder, domainHolder)
}

// TestParityClaimOverrideLeaseDivergesPendingPR5349 is a characterization
// test, not a parity assertion: it pins the CURRENT, confirmed-divergent
// behavior of `bd update <id> --claim --assignee=<other>` across backends.
//
// Classic's issueops.ExecuteUpdate composes ClaimIssueInTx (arms alice's
// lease) then the raw-map UpdateIssueInTx, whose issueops.ManageLeaseOnUpdate
// only ever CLEARS lease columns by design (see its doc comment: leases are
// armed only by the lease-aware verbs, never by a generic update) — so the
// same-request override deletes unconditionally. Net effect: the row lands
// exactly where the claim+override asked (status=in_progress,
// assignee=bob), but with no leases row at all — a live claim with no
// lease. That is a real, currently-shipping bug, tracked by unmerged PR
// #5349, deliberately left alone here: ManageLeaseOnUpdate is a pinned
// contract for be-plv and #5349 is not to be reopened or modified from this
// bead.
//
// Domain's IssueUseCase.ApplyUpdate, after the be-plv fix, re-arms the lease
// for the override target instead: internal/storage/domain/issue.go's
// update() reads the row back post-write and calls issueops.UpsertLeaseInTx
// when it is still in_progress with a live assignee.
func (s *testSuite) TestParityClaimOverrideLeaseDivergesPendingPR5349() {
	ctx := s.Ctx()
	r := s.issueRepo()

	classicID := "bd-parity-override-classic"
	s.Require().NoError(r.Insert(ctx, newTestIssue(classicID, "x"), "tester", domain.InsertIssueOpts{}))
	tx := s.beginClassicTx()
	_, _, err := issueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: classicID,
		Claim:   true,
		Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "bob"}},
	})
	s.Require().NoError(err)
	s.Require().NoError(tx.Commit())

	classicOut, err := r.Get(ctx, classicID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, classicOut.Status)
	s.Equal("bob", classicOut.Assignee)
	_, classicHasLease := s.leaseRow(classicID)
	s.False(classicHasLease, "KNOWN BUG pending PR #5349: classic drops the lease on claim+override, leaving a live claim with no lease row")

	domainID := "bd-parity-override-domain"
	s.Require().NoError(r.Insert(ctx, newTestIssue(domainID, "x"), "tester", domain.InsertIssueOpts{}))
	uc := s.issueUseCase()
	_, err = uc.ApplyUpdate(ctx, domainID, domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"assignee": "bob"},
	}, "alice")
	s.Require().NoError(err)

	domainOut, err := r.Get(ctx, domainID, domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(types.StatusInProgress, domainOut.Status)
	s.Equal("bob", domainOut.Assignee)
	domainHolder, domainHasLease := s.leaseRow(domainID)
	s.True(domainHasLease, "be-plv fix: domain must re-arm the lease for the override target")
	s.Equal("bob", domainHolder)

	// Spelled out so this breaks LOUDLY, not silently, the day #5349 lands:
	// if classic starts re-arming too, this equality flips and this whole
	// test should be rewritten into a genuine parity assertion like its two
	// siblings above instead of continuing to assert a now-stale divergence.
	s.NotEqual(classicHasLease, domainHasLease, "if this now fails, PR #5349 likely merged and closed the classic-side gap — rewrite this test into a parity assertion")
}
