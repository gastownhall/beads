package db

import (
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestFieldLengthGuards proves the proxied-server (uow) write stack enforces the
// assignee/owner/label VARCHAR(255) bounds — the gap the S3 embedded-path fix
// alone left open. Over-length values are rejected with a typed
// types.ErrFieldTooLong (not a raw backend "data too long" error, and not a
// silent label truncation), nothing is persisted, and values at the 255-char
// limit still round-trip.
func (s *testSuite) TestFieldLengthGuards() {
	over := strings.Repeat("x", 300)                  // 300 runes > MaxFieldLen (255)
	atLimit := strings.Repeat("y", types.MaxFieldLen) // exactly 255 runes

	s.Run("LabelInsertOverLengthRejectedNoRow", func() {
		s.seedIssueRow("bd-fl-label")
		r := s.labelRepo()
		err := r.Insert(s.Ctx(), "bd-fl-label", over, "tester", domain.LabelOpts{})
		s.Require().ErrorIs(err, types.ErrFieldTooLong)

		out, err := r.List(s.Ctx(), "bd-fl-label", domain.LabelOpts{})
		s.Require().NoError(err)
		s.Empty(out, "over-length label must not be stored (not even truncated)")
	})

	s.Run("CreateOverLengthAssigneeRejectedNotPersisted", func() {
		r := s.issueRepo()
		in := newTestIssue("bd-fl-assignee", "over-length assignee")
		in.Assignee = over
		err := r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{})
		s.Require().ErrorIs(err, types.ErrFieldTooLong)

		exists, err := r.Exists(s.Ctx(), "bd-fl-assignee", domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.False(exists, "issue must not be persisted when assignee is rejected")
	})

	s.Run("CreateOverLengthOwnerRejected", func() {
		r := s.issueRepo()
		in := newTestIssue("bd-fl-owner", "over-length owner")
		in.Owner = over
		err := r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{})
		s.Require().ErrorIs(err, types.ErrFieldTooLong)
	})

	// bd-wnp679 AC-4: the create-only branch (caller-selected, and forced for
	// every minted id) is bounded too, before the guard writes the id's
	// coordination cell or the strict writer reaches the backend.
	for _, field := range []string{"assignee", "owner"} {
		s.Run("CreateOnlyOverLength_"+field+"_RejectedBeforeAnyWrite", func() {
			r := s.issueRepo()
			id := "bd-fl-co-" + field
			in := newTestIssue(id, "over-length "+field+" create-only")
			if field == "assignee" {
				in.Assignee = over
			} else {
				in.Owner = over
			}
			cellBefore := s.coordinationCell(id)
			err := r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{CreateOnly: true})
			s.Require().ErrorIs(err, types.ErrFieldTooLong)

			exists, err := r.Exists(s.Ctx(), id, domain.IssueTableOpts{})
			s.Require().NoError(err)
			s.False(exists, "issue must not be persisted when %s is rejected", field)
			s.Equal(cellBefore, s.coordinationCell(id), "rejected create must not write its coordination cell")
		})
	}

	s.Run("CreateOnlyAtLimitAssigneeAndOwnerRoundTrip", func() {
		r := s.issueRepo()
		in := newTestIssue("bd-fl-co-ok", "at limit create-only")
		in.Assignee = atLimit
		in.Owner = atLimit
		s.Require().NoError(r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{CreateOnly: true}))
		out, err := r.Get(s.Ctx(), "bd-fl-co-ok", domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.Equal(atLimit, out.Assignee)
		s.Equal(atLimit, out.Owner)
	})

	s.Run("MintedCreateOverLengthAssigneeRejected", func() {
		s.resetMintConfig("flMint", "counter")
		_, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
			Issue: &types.Issue{Title: "minted over-length", IssueType: types.TypeTask, Priority: 2, Assignee: over},
		}, "tester")
		s.Require().ErrorIs(err, types.ErrFieldTooLong)
		exists, err := s.issueRepo().Exists(s.Ctx(), "flMint-1", domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.False(exists)
	})

	s.Run("UpdateOverLengthAssigneeRejected", func() {
		r := s.issueRepo()
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-fl-upd", "x"), "tester", domain.InsertIssueOpts{}))
		err := r.Update(s.Ctx(), "bd-fl-upd", map[string]any{"assignee": over}, "tester", domain.IssueTableOpts{})
		s.Require().ErrorIs(err, types.ErrFieldTooLong)
	})

	s.Run("ClaimOverLengthActorRejected", func() {
		r := s.issueRepo()
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue("bd-fl-claim", "x"), "tester", domain.InsertIssueOpts{}))
		_, err := r.Claim(s.Ctx(), "bd-fl-claim", over, domain.IssueTableOpts{})
		s.Require().ErrorIs(err, types.ErrFieldTooLong)
	})

	s.Run("AtLimitAssigneeAndLabelRoundTrip", func() {
		r := s.issueRepo()
		in := newTestIssue("bd-fl-ok", "at limit")
		in.Assignee = atLimit
		s.Require().NoError(r.Insert(s.Ctx(), in, "tester", domain.InsertIssueOpts{}))

		out, err := r.Get(s.Ctx(), "bd-fl-ok", domain.IssueTableOpts{})
		s.Require().NoError(err)
		s.Equal(atLimit, out.Assignee, "255-char assignee must round-trip unchanged")

		lr := s.labelRepo()
		s.Require().NoError(lr.Insert(s.Ctx(), "bd-fl-ok", atLimit, "tester", domain.LabelOpts{}))
		labels, err := lr.List(s.Ctx(), "bd-fl-ok", domain.LabelOpts{})
		s.Require().NoError(err)
		s.Equal([]string{atLimit}, labels, "255-char label must be stored intact")
	})
}
