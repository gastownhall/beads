package db

import (
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueGetReadyWork() {
	s.Run("ReturnsOpenAndInProgress", s.readyOpenAndInProgress)
	s.Run("ExcludesClosed", s.readyExcludesClosed)
	s.Run("ExcludesPinned", s.readyExcludesPinned)
	s.Run("ExcludesBlocked", s.readyExcludesBlocked)
	s.Run("ExcludesEphemeralByDefault", s.readyExcludesEphemeral)
	s.Run("ExcludesDefaultTypes", s.readyExcludesDefaultTypes)
	s.Run("FilterByPriority", s.readyFilterByPriority)
	s.Run("FilterByAssignee", s.readyFilterByAssignee)
	s.Run("Unassigned", s.readyUnassigned)
	s.Run("ExcludesDeferred", s.readyExcludesDeferred)
	s.Run("IncludeDeferred", s.readyIncludeDeferred)
	s.Run("LabelFilter", s.readyLabelFilter)
	s.Run("LimitRespected", s.readyLimitRespected)
	s.Run("SortByPriority", s.readySortByPriority)
	s.Run("CrossTableCollisionError", s.readyCollisionError)
}

func (s *testSuite) readyOpenAndInProgress() {
	r := s.issueRepo()

	openIss := newTestIssue("bd-rdy-oa-open", "open")
	s.Require().NoError(r.Insert(s.Ctx(), openIss, "tester", domain.InsertIssueOpts{}))

	ip := newTestIssue("bd-rdy-oa-ip", "in progress")
	ip.Status = types.StatusInProgress
	s.Require().NoError(r.Insert(s.Ctx(), ip, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-oa-open")
	s.Contains(got, "bd-rdy-oa-ip")
}

func (s *testSuite) readyExcludesClosed() {
	r := s.issueRepo()

	closed := newTestIssue("bd-rdy-cls-1", "closed")
	closed.Status = types.StatusClosed
	s.Require().NoError(r.Insert(s.Ctx(), closed, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(issueIDsFrom(out), "bd-rdy-cls-1")
}

func (s *testSuite) readyExcludesPinned() {
	r := s.issueRepo()
	pinned := newTestIssue("bd-rdy-pin-1", "pinned")
	s.Require().NoError(r.Insert(s.Ctx(), pinned, "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET pinned = 1 WHERE id = ?", "bd-rdy-pin-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(issueIDsFrom(out), "bd-rdy-pin-1")
}

func (s *testSuite) readyExcludesBlocked() {
	r := s.issueRepo()
	blocked := newTestIssue("bd-rdy-blk-1", "blocked")
	s.Require().NoError(r.Insert(s.Ctx(), blocked, "tester", domain.InsertIssueOpts{}))
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET is_blocked = 1 WHERE id = ?", "bd-rdy-blk-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(issueIDsFrom(out), "bd-rdy-blk-1")
}

func (s *testSuite) readyExcludesEphemeral() {
	r := s.issueRepo()
	ephemeral := newTestIssue("bd-rdy-eph-1", "ephemeral")
	ephemeral.Ephemeral = true
	s.Require().NoError(r.Insert(s.Ctx(), ephemeral, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(issueIDsFrom(out), "bd-rdy-eph-1", "ephemeral=1 must be excluded by default")
}

func (s *testSuite) readyExcludesDefaultTypes() {
	r := s.issueRepo()
	mol := newTestIssue("bd-rdy-dt-mol", "molecule")
	mol.IssueType = types.TypeMolecule
	s.Require().NoError(r.Insert(s.Ctx(), mol, "tester", domain.InsertIssueOpts{}))

	gate := newTestIssue("bd-rdy-dt-gate", "gate")
	gate.IssueType = types.TypeGate
	s.Require().NoError(r.Insert(s.Ctx(), gate, "tester", domain.InsertIssueOpts{}))

	task := newTestIssue("bd-rdy-dt-task", "task")
	s.Require().NoError(r.Insert(s.Ctx(), task, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-dt-task")
	s.NotContains(got, "bd-rdy-dt-mol")
	s.NotContains(got, "bd-rdy-dt-gate")
}

func (s *testSuite) readyFilterByPriority() {
	r := s.issueRepo()
	hi := newTestIssue("bd-rdy-pr-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	lo := newTestIssue("bd-rdy-pr-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))

	pri := 1
	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{Priority: &pri})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-pr-hi")
	s.NotContains(got, "bd-rdy-pr-lo")
}

func (s *testSuite) readyFilterByAssignee() {
	r := s.issueRepo()
	mine := newTestIssue("bd-rdy-as-mine", "mine")
	mine.Assignee = "alice"
	s.Require().NoError(r.Insert(s.Ctx(), mine, "tester", domain.InsertIssueOpts{}))
	theirs := newTestIssue("bd-rdy-as-theirs", "theirs")
	theirs.Assignee = "bob"
	s.Require().NoError(r.Insert(s.Ctx(), theirs, "tester", domain.InsertIssueOpts{}))

	alice := "alice"
	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{Assignee: &alice})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-as-mine")
	s.NotContains(got, "bd-rdy-as-theirs")
}

func (s *testSuite) readyUnassigned() {
	r := s.issueRepo()
	unassigned := newTestIssue("bd-rdy-un-yes", "unassigned")
	s.Require().NoError(r.Insert(s.Ctx(), unassigned, "tester", domain.InsertIssueOpts{}))
	assigned := newTestIssue("bd-rdy-un-no", "assigned")
	assigned.Assignee = "alice"
	s.Require().NoError(r.Insert(s.Ctx(), assigned, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{Unassigned: true})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-un-yes")
	s.NotContains(got, "bd-rdy-un-no")
}

func (s *testSuite) readyExcludesDeferred() {
	r := s.issueRepo()
	deferred := newTestIssue("bd-rdy-df-1", "deferred")
	s.Require().NoError(r.Insert(s.Ctx(), deferred, "tester", domain.InsertIssueOpts{}))
	future := time.Now().UTC().Add(24 * time.Hour)
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET defer_until = ? WHERE id = ?", future, "bd-rdy-df-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	s.NotContains(issueIDsFrom(out), "bd-rdy-df-1")
}

func (s *testSuite) readyIncludeDeferred() {
	r := s.issueRepo()
	deferred := newTestIssue("bd-rdy-idf-1", "deferred")
	s.Require().NoError(r.Insert(s.Ctx(), deferred, "tester", domain.InsertIssueOpts{}))
	future := time.Now().UTC().Add(24 * time.Hour)
	_, err := s.Runner().ExecContext(s.Ctx(), "UPDATE issues SET defer_until = ? WHERE id = ?", future, "bd-rdy-idf-1")
	s.Require().NoError(err)

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{IncludeDeferred: true})
	s.Require().NoError(err)
	s.Contains(issueIDsFrom(out), "bd-rdy-idf-1")
}

func (s *testSuite) readyLabelFilter() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())

	hot := newTestIssue("bd-rdy-lbl-hot", "hot")
	s.Require().NoError(r.Insert(s.Ctx(), hot, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-rdy-lbl-hot", "hot", "tester", domain.LabelOpts{}))

	cold := newTestIssue("bd-rdy-lbl-cold", "cold")
	s.Require().NoError(r.Insert(s.Ctx(), cold, "tester", domain.InsertIssueOpts{}))
	s.Require().NoError(labelRepo.Insert(s.Ctx(), "bd-rdy-lbl-cold", "cold", "tester", domain.LabelOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{Labels: []string{"hot"}})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-lbl-hot")
	s.NotContains(got, "bd-rdy-lbl-cold")
}

func (s *testSuite) readyLimitRespected() {
	r := s.issueRepo()
	for i := 0; i < 5; i++ {
		iss := newTestIssue("bd-rdy-lim-"+string(rune('a'+i)), "x")
		iss.Priority = 1
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
	}
	pri := 1
	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{Priority: &pri, Limit: 3, SortPolicy: types.SortPolicyPriority})
	s.Require().NoError(err)
	s.Len(out.Items, 3)
}

func (s *testSuite) readySortByPriority() {
	r := s.issueRepo()
	lo := newTestIssue("bd-rdy-srt-lo", "lo")
	lo.Priority = 3
	s.Require().NoError(r.Insert(s.Ctx(), lo, "tester", domain.InsertIssueOpts{}))
	hi := newTestIssue("bd-rdy-srt-hi", "hi")
	hi.Priority = 1
	s.Require().NoError(r.Insert(s.Ctx(), hi, "tester", domain.InsertIssueOpts{}))
	mid := newTestIssue("bd-rdy-srt-mid", "mid")
	mid.Priority = 2
	s.Require().NoError(r.Insert(s.Ctx(), mid, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{SortPolicy: types.SortPolicyPriority})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	hiIdx, midIdx, loIdx := indexOf(got, "bd-rdy-srt-hi"), indexOf(got, "bd-rdy-srt-mid"), indexOf(got, "bd-rdy-srt-lo")
	s.Require().GreaterOrEqual(hiIdx, 0)
	s.Require().GreaterOrEqual(midIdx, 0)
	s.Require().GreaterOrEqual(loIdx, 0)
	s.Less(hiIdx, midIdx, "priority=1 should sort before priority=2")
	s.Less(midIdx, loIdx, "priority=2 should sort before priority=3")
}

func (s *testSuite) readyCollisionError() {
	r := s.issueRepo()
	const id = "bd-rdy-coll-1"
	s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "perm"), "tester", domain.InsertIssueOpts{}))
	w := newTestIssue(id, "wisp")
	w.Ephemeral = false
	s.Require().NoError(r.Insert(s.Ctx(), w, "tester", domain.InsertIssueOpts{UseWispsTable: true}))

	_, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().Error(err)
	s.Contains(err.Error(), "exists in both issues and wisps")
}

func issueIDsFrom(page domain.SearchPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, iss := range page.Items {
		out = append(out, iss.ID)
	}
	return out
}

func issueIDsFromCounts(page domain.SearchCountsPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, iss := range page.Items {
		out = append(out, iss.ID)
	}
	return out
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}
