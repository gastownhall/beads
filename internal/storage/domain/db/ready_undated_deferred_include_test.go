package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestReadyWorkUndatedDeferredIncludeDeferred pins the IncludeDeferred bypass
// on this stack for the undated icebox (GH#7300, AC-4): buildReadyWorkPredicates
// skips the deferred-child computation when the filter asks for deferred rows,
// so the child of an undated deferred parent is withheld by the default read
// and listed anyway with IncludeDeferred — the same contract the classic
// stack serves. The withheld half fails on an unfixed gate.
func (s *testSuite) TestReadyWorkUndatedDeferredIncludeDeferred() {
	r := s.issueRepo()
	parent := newTestIssue("bd-rdy-uip-parent", "undated deferred parent")
	parent.IssueType = types.TypeEpic
	parent.Status = types.StatusDeferred
	s.Require().NoError(r.Insert(s.Ctx(), parent, "tester", domain.InsertIssueOpts{}))
	child := newTestIssue("bd-rdy-uip-child", "child of undated deferred parent")
	s.Require().NoError(r.Insert(s.Ctx(), child, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep(child.ID, parent.ID, types.DepParentChild), "tester", domain.DepInsertOpts{}))
	free := newTestIssue("bd-rdy-uip-free", "unrelated free issue")
	s.Require().NoError(r.Insert(s.Ctx(), free, "tester", domain.InsertIssueOpts{}))

	// Contrast: the default read withholds the child. The suite resets once
	// per method, so sibling methods' rows can share this read: the pin is
	// membership, not the whole set.
	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.Contains(issueIDsFrom(out), free.ID, "the unrelated issue stays ready")
	s.NotContains(issueIDsFrom(out), child.ID, "child of an undated deferred parent must be withheld")
	s.NotContains(issueIDsFrom(out), parent.ID)

	// The bypass: the child is listed anyway.
	bypassed, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{IncludeDeferred: true})
	s.Require().NoError(err)
	got := issueIDsFrom(bypassed)
	s.Contains(got, child.ID, "IncludeDeferred must still list the held-back child")
	s.Contains(got, free.ID)
	s.NotContains(got, parent.ID, "IncludeDeferred lists held-back children, not the deferred parent itself")
}
