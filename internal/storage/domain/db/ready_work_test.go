package db

import (
	"fmt"
	"strings"
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
	s.Run("OffsetSkipsLeadingRows", s.readyOffsetSkipsLeadingRows)
	s.Run("OffsetWithLooseLimitReturnsRemainder", s.readyOffsetWithoutLimit)
	s.Run("OffsetHasMoreSignaling", s.readyOffsetHasMoreSignaling)
	s.Run("OffsetWalksAllPages", s.readyOffsetWalksAllPages)
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

	rig := newTestIssue("bd-rdy-dt-rig", "rig")
	rig.IssueType = types.IssueType("rig")
	s.Require().NoError(r.Insert(s.Ctx(), rig, "tester", domain.InsertIssueOpts{}))

	message := newTestIssue("bd-rdy-dt-message", "message")
	message.IssueType = types.TypeMessage
	s.Require().NoError(r.Insert(s.Ctx(), message, "tester", domain.InsertIssueOpts{}))

	task := newTestIssue("bd-rdy-dt-task", "task")
	s.Require().NoError(r.Insert(s.Ctx(), task, "tester", domain.InsertIssueOpts{}))

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{})
	s.Require().NoError(err)
	got := issueIDsFrom(out)
	s.Contains(got, "bd-rdy-dt-task")
	s.NotContains(got, "bd-rdy-dt-mol")
	s.NotContains(got, "bd-rdy-dt-gate")
	s.NotContains(got, "bd-rdy-dt-rig")
	s.NotContains(got, "bd-rdy-dt-message")
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

func (s *testSuite) readyOffsetSkipsLeadingRows() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	const isoLabel = "rdy-off-isolate"
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("bd-rdy-off-p%d", i)
		iss := newTestIssue(id, fmt.Sprintf("p%d", i))
		iss.Priority = i
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isoLabel, "tester", domain.LabelOpts{}))
	}

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
		Offset:     2,
		Limit:      2,
		SortPolicy: types.SortPolicyPriority,
		Labels:     []string{isoLabel},
	})
	s.Require().NoError(err)
	got := issueIDsFrom(out)

	s.Require().Len(got, 2, "expected exactly 2 paginated results, got %v", got)
	s.Equal("bd-rdy-off-p3", got[0], "Offset=2 must skip p1+p2")
	s.Equal("bd-rdy-off-p4", got[1], "Limit=2 must stop after p4")
}

func (s *testSuite) readyOffsetWithoutLimit() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	const n = 5
	const isoLabel = "rdy-owl-isolate"
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("bd-rdy-owl-p%d", i)
		iss := newTestIssue(id, fmt.Sprintf("p%d", i))
		iss.Priority = i
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isoLabel, "tester", domain.LabelOpts{}))
	}

	out, err := r.GetReadyWork(s.Ctx(), types.WorkFilter{
		Offset:     3,
		Limit:      100,
		SortPolicy: types.SortPolicyPriority,
		Labels:     []string{isoLabel},
	})
	s.Require().NoError(err)

	s.Require().Len(out.Items, n-3, "Offset=3 must skip 3 of 5, got %v", issueIDsFrom(out))
	s.Equal("bd-rdy-owl-p4", out.Items[0].ID, "first remaining row must be p4")
	s.Equal("bd-rdy-owl-p5", out.Items[1].ID, "second remaining row must be p5")
}

func (s *testSuite) readyOffsetHasMoreSignaling() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	const n = 6
	const isoLabel = "rdy-hms-isolate"
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("bd-rdy-hms-p%d", i)
		iss := newTestIssue(id, fmt.Sprintf("p%d", i))
		iss.Priority = 1
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isoLabel, "tester", domain.LabelOpts{}))
	}
	filter := types.WorkFilter{
		Labels:     []string{isoLabel},
		SortPolicy: types.SortPolicyPriority,
	}

	first, err := r.GetReadyWork(s.Ctx(), withOffsetLimit(filter, 0, 3))
	s.Require().NoError(err)
	s.Require().Len(first.Items, 3, "page 1 should have exactly 3 items")
	s.True(first.HasMore, "HasMore must be true when more rows exist beyond Limit")

	boundary, err := r.GetReadyWork(s.Ctx(), withOffsetLimit(filter, 3, 3))
	s.Require().NoError(err)
	s.Require().Len(boundary.Items, 3, "boundary page should fill exactly")
	s.False(boundary.HasMore, "HasMore must be false at exact boundary (N+1 overfetch returns no extra)")

	partial, err := r.GetReadyWork(s.Ctx(), withOffsetLimit(filter, 4, 3))
	s.Require().NoError(err)
	s.Require().Len(partial.Items, 2, "tail page should return remaining 2 items")
	s.False(partial.HasMore, "HasMore must be false on the partial tail page")
}

func withOffsetLimit(base types.WorkFilter, offset, limit int) types.WorkFilter {
	base.Offset = offset
	base.Limit = limit
	return base
}

func (s *testSuite) readyOffsetWalksAllPages() {
	r := s.issueRepo()
	labelRepo := NewLabelSQLRepository(s.Runner())
	const n = 6
	const pageSize = 2
	const isoLabel = "rdy-walk-isolate"
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("bd-rdy-walk-p%d", i)
		iss := newTestIssue(id, fmt.Sprintf("p%d", i))
		iss.Priority = 1
		s.Require().NoError(r.Insert(s.Ctx(), iss, "tester", domain.InsertIssueOpts{}))
		s.Require().NoError(labelRepo.Insert(s.Ctx(), id, isoLabel, "tester", domain.LabelOpts{}))
	}
	base := types.WorkFilter{Labels: []string{isoLabel}, SortPolicy: types.SortPolicyPriority}

	unpaginated, err := r.GetReadyWork(s.Ctx(), withOffsetLimit(base, 0, 0))
	s.Require().NoError(err)
	s.Require().Len(unpaginated.Items, n, "baseline must return all %d items", n)
	wantIDs := issueIDsFrom(unpaginated)

	var walked []string
	for page := 0; ; page++ {
		offset := page * pageSize
		got, err := r.GetReadyWork(s.Ctx(), withOffsetLimit(base, offset, pageSize))
		s.Require().NoError(err)
		for _, iss := range got.Items {
			walked = append(walked, iss.ID)
		}
		if !got.HasMore {
			if page == 0 || page == 1 {
				s.Failf("HasMore false too early", "page %d HasMore=false (only walked %d/%d)", page, len(walked), n)
			}
			break
		}
		if page > n {
			s.Failf("walk did not terminate", "more than %d pages", n)
			break
		}
	}

	s.Equal(wantIDs, walked, "concatenated pages must equal the full unpaginated result with no gaps or duplicates")
}

func filterPrefix(ids []string, prefix string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return out
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
