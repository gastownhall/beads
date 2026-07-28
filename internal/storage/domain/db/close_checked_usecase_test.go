package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_CloseIssueChecked() {
	s.Run("DirectBlockerRefusesWithSentinelAndNamesBlocker", s.uccCloseCheckedDirectBlockerRefuses)
	s.Run("TransitivelyBlockedChildClosesWithoutForce", s.uccCloseCheckedTransitiveCloses)
	s.Run("ForceClosesDespiteDirectBlocker", s.uccCloseCheckedForceCloses)
	s.Run("AlreadyClosedMatchesUncheckedSemantics", s.uccCloseCheckedAlreadyClosed)
	s.Run("AlreadyClosedWithStaleBlockerReClosesIdempotently", s.uccCloseCheckedAlreadyClosedStaleBlocker)
	s.Run("UnblockedClosesAndReturnsIssue", s.uccCloseCheckedUnblockedCloses)
	s.Run("WispDirectBlockerRefuses", s.uccCloseWispCheckedDirectBlockerRefuses)
	s.Run("WispForceClosesDespiteDirectBlocker", s.uccCloseWispCheckedForceCloses)
}

func (s *testSuite) uccCloseCheckedDirectBlockerRefuses() {
	s.seedIssueRow("bd-ucc-clc-src")
	s.seedIssueRow("bd-ucc-clc-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-clc-src", "bd-ucc-clc-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ucc-clc-src"))

	res, err := s.issueUseCase().CloseIssueChecked(s.Ctx(), "bd-ucc-clc-src",
		domain.CloseIssueParams{Reason: "done"}, "tester", false)
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrCloseBlocked), "refusal must carry the shared sentinel")
	s.ErrorContains(err, "is blocked by")
	s.ErrorContains(err, "bd-ucc-clc-tgt", "message must name the live blocker")
	s.False(res.Closed)

	// The refused issue stays open.
	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM issues WHERE id = ?", "bd-ucc-clc-src").Scan(&status))
	s.Equal(string(types.StatusOpen), status)
}

func (s *testSuite) uccCloseCheckedTransitiveCloses() {
	// parent has a live direct blocker; child inherits is_blocked=1 through the
	// parent-child edge but has no direct blocker of its own, so the historical
	// predicate (blocked && len(blockers) > 0) lets it close without force.
	s.seedIssueRow("bd-ucc-clc-tr-blocker")
	s.seedIssueRow("bd-ucc-clc-tr-parent")
	s.seedIssueRow("bd-ucc-clc-tr-child")
	dep := s.depRepo()
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-ucc-clc-tr-parent", "bd-ucc-clc-tr-blocker", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(dep.Insert(s.Ctx(),
		newDep("bd-ucc-clc-tr-child", "bd-ucc-clc-tr-parent", types.DepParentChild), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ucc-clc-tr-child"), "child must inherit is_blocked from blocked parent")

	// Predicate parity: is_blocked=1 but no live direct blocker.
	blocked, blockers, err := s.depUseCase().IsBlocked(s.Ctx(), "bd-ucc-clc-tr-child")
	s.Require().NoError(err)
	s.True(blocked)
	s.Empty(blockers, "transitively-blocked child has no direct blocker")

	res, err := s.issueUseCase().CloseIssueChecked(s.Ctx(), "bd-ucc-clc-tr-child",
		domain.CloseIssueParams{Reason: "done"}, "tester", false)
	s.Require().NoError(err, "transitively-blocked child must close without force")
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseCheckedForceCloses() {
	s.seedIssueRow("bd-ucc-clc-fsrc")
	s.seedIssueRow("bd-ucc-clc-ftgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-clc-fsrc", "bd-ucc-clc-ftgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().True(s.isBlocked("bd-ucc-clc-fsrc"))

	res, err := s.issueUseCase().CloseIssueChecked(s.Ctx(), "bd-ucc-clc-fsrc",
		domain.CloseIssueParams{Reason: "override"}, "tester", true)
	s.Require().NoError(err, "force must bypass the block guard")
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseCheckedAlreadyClosed() {
	s.seedIssueRow("bd-ucc-clc-idem")
	uc := s.issueUseCase()

	first, err := uc.CloseIssueChecked(s.Ctx(), "bd-ucc-clc-idem",
		domain.CloseIssueParams{Reason: "first"}, "tester", false)
	s.Require().NoError(err)
	s.True(first.Closed)

	// A second checked close reports the same already-closed semantics as the
	// unchecked verb: Closed == false, issue still closed.
	second, err := uc.CloseIssueChecked(s.Ctx(), "bd-ucc-clc-idem",
		domain.CloseIssueParams{Reason: "second"}, "tester", false)
	s.Require().NoError(err)
	s.False(second.Closed)
	s.Require().NotNil(second.Issue)
	s.Equal(types.StatusClosed, second.Issue.Status)
}

func (s *testSuite) uccCloseCheckedAlreadyClosedStaleBlocker() {
	// Regression for PR #4911 (proxied checked-close parity): an already-closed row that still carries
	// a live direct blocker with is_blocked=1 (e.g. force-closed while depending
	// on an open issue, or a stale is_blocked after a cross-clone Dolt merge) must
	// re-close idempotently, NOT refuse with ErrCloseBlocked. The proxied checked
	// close skips the block guard for already-closed rows, mirroring the embedded
	// issueops.CloseIssueCheckedInTx path. The prior AlreadyClosed test seeded an
	// unblocked row, so it never exercised the closed + live-blocker interaction.
	s.seedIssueRow("bd-ucc-clc-stale-src")
	s.seedIssueRow("bd-ucc-clc-stale-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-clc-stale-src", "bd-ucc-clc-stale-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	// The guard reads IsBlocked; a live direct blocker here proves that without the
	// already-closed short-circuit the checked close below would refuse.
	blocked, blockers, err := s.depUseCase().IsBlocked(s.Ctx(), "bd-ucc-clc-stale-src")
	s.Require().NoError(err)
	s.Require().True(blocked)
	s.Require().Contains(blockers, "bd-ucc-clc-stale-tgt")

	// Close the source directly, leaving the live blocker edge and is_blocked=1 in
	// place — the stale "closed but still blocked" state.
	_, err = s.Runner().ExecContext(s.Ctx(),
		"UPDATE issues SET status = ? WHERE id = ?", string(types.StatusClosed), "bd-ucc-clc-stale-src")
	s.Require().NoError(err)
	s.Require().True(s.isBlocked("bd-ucc-clc-stale-src"), "is_blocked stays 1 on the closed row")

	// A checked re-close without force must be idempotent, not ErrCloseBlocked.
	res, err := s.issueUseCase().CloseIssueChecked(s.Ctx(), "bd-ucc-clc-stale-src",
		domain.CloseIssueParams{Reason: "re-close"}, "tester", false)
	s.Require().NoError(err, "already-closed row must re-close idempotently despite a live blocker")
	s.False(res.Closed, "idempotent re-close reports Closed=false")
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseCheckedUnblockedCloses() {
	s.seedIssueRow("bd-ucc-clc-free")
	res, err := s.issueUseCase().CloseIssueChecked(s.Ctx(), "bd-ucc-clc-free",
		domain.CloseIssueParams{Reason: "done", Session: "sess-1"}, "tester", false)
	s.Require().NoError(err)
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal("bd-ucc-clc-free", res.Issue.ID)
	s.Equal(types.StatusClosed, res.Issue.Status)
}

func (s *testSuite) uccCloseWispCheckedDirectBlockerRefuses() {
	s.seedWispRow("bd-ucc-wclc-src")
	s.seedWispRow("bd-ucc-wclc-tgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-wclc-src", "bd-ucc-wclc-tgt", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	blocked, blockers, err := s.depUseCase().IsWispBlocked(s.Ctx(), "bd-ucc-wclc-src")
	s.Require().NoError(err)
	s.Require().True(blocked)
	s.Require().Contains(blockers, "bd-ucc-wclc-tgt")

	res, err := s.issueUseCase().CloseWispChecked(s.Ctx(), "bd-ucc-wclc-src",
		domain.CloseIssueParams{Reason: "done"}, "tester", false)
	s.Require().Error(err)
	s.True(errors.Is(err, storage.ErrCloseBlocked))
	s.ErrorContains(err, "bd-ucc-wclc-tgt")
	s.False(res.Closed)

	var status string
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT status FROM wisps WHERE id = ?", "bd-ucc-wclc-src").Scan(&status))
	s.Equal(string(types.StatusOpen), status)
}

func (s *testSuite) uccCloseWispCheckedForceCloses() {
	s.seedWispRow("bd-ucc-wclc-fsrc")
	s.seedWispRow("bd-ucc-wclc-ftgt")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-ucc-wclc-fsrc", "bd-ucc-wclc-ftgt", types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	res, err := s.issueUseCase().CloseWispChecked(s.Ctx(), "bd-ucc-wclc-fsrc",
		domain.CloseIssueParams{Reason: "override"}, "tester", true)
	s.Require().NoError(err, "force must bypass the wisp block guard")
	s.True(res.Closed)
	s.Require().NotNil(res.Issue)
	s.Equal(types.StatusClosed, res.Issue.Status)
}
