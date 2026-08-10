package db

import (
	"errors"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUpdateRejectsInvalidCanonicalFields() {
	created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			ID:        "bd-domain-invalid-update",
			Title:     "valid incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)

	incumbent, err := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
	s.Require().NoError(err)

	var eventCount int
	s.Require().NoError(s.Runner().QueryRowContext(
		s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		created.Issue.ID,
	).Scan(&eventCount))

	checks := []struct {
		name    string
		updates map[string]any
	}{
		{"empty title", map[string]any{"title": ""}},
		{"overlong title", map[string]any{"title": strings.Repeat("x", 501)}},
		{"negative priority", map[string]any{"priority": -1}},
		{"priority above maximum", map[string]any{"priority": 5}},
		{"negative estimated minutes", map[string]any{"estimated_minutes": -1}},
	}
	for _, check := range checks {
		s.Run(check.name, func() {
			err := s.issueUseCase().UpdateIssue(s.Ctx(), created.Issue.ID, check.updates, "tester")
			s.ErrorIs(err, storage.ErrValidation)

			stored, getErr := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
			s.Require().NoError(getErr)
			s.Equal("valid incumbent", stored.Title)
			s.Equal(2, stored.Priority)
			s.Nil(stored.EstimatedMinutes)
			s.Equal(incumbent.RowVersion, stored.RowVersion)

			var afterEvents int
			s.Require().NoError(s.Runner().QueryRowContext(
				s.Ctx(),
				"SELECT COUNT(*) FROM events WHERE issue_id = ?",
				created.Issue.ID,
			).Scan(&afterEvents))
			s.True(errors.Is(err, storage.ErrValidation))
			s.Equal(eventCount, afterEvents)
		})
	}
}

func (s *testSuite) TestIssueUpdateRejectsUnsupportedCanonicalFieldTypes() {
	checks := []struct {
		name    string
		id      string
		updates map[string]any
	}{
		{"non-string title", "bd-domain-title-type", map[string]any{"title": 7}},
		{"int64 priority", "bd-domain-priority-type", map[string]any{"priority": int64(-1)}},
		{"int64 estimated minutes", "bd-domain-estimate-type", map[string]any{"estimated_minutes": int64(-1)}},
	}
	for _, check := range checks {
		s.Run(check.name, func() {
			created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
				Issue: &types.Issue{
					ID:        check.id,
					Title:     "valid incumbent",
					IssueType: types.TypeTask,
					Priority:  2,
				},
			}, "tester")
			s.Require().NoError(err)

			before, err := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
			s.Require().NoError(err)
			var beforeEvents int
			s.Require().NoError(s.Runner().QueryRowContext(
				s.Ctx(),
				"SELECT COUNT(*) FROM events WHERE issue_id = ?",
				created.Issue.ID,
			).Scan(&beforeEvents))

			err = s.issueUseCase().UpdateIssue(s.Ctx(), created.Issue.ID, check.updates, "tester")
			s.ErrorIs(err, storage.ErrValidation)

			after, getErr := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
			s.Require().NoError(getErr)
			s.Equal(before.Title, after.Title)
			s.Equal(before.Priority, after.Priority)
			s.Equal(before.EstimatedMinutes, after.EstimatedMinutes)
			s.Equal(before.Status, after.Status)
			s.Equal(before.Assignee, after.Assignee)
			s.Equal(before.RowVersion, after.RowVersion)
			var afterEvents int
			s.Require().NoError(s.Runner().QueryRowContext(
				s.Ctx(),
				"SELECT COUNT(*) FROM events WHERE issue_id = ?",
				created.Issue.ID,
			).Scan(&afterEvents))
			s.Equal(beforeEvents, afterEvents)
		})
	}
}

func (s *testSuite) TestApplyUpdateValidatesFieldsBeforeClaim() {
	created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			ID:        "bd-domain-validate-before-claim",
			Title:     "valid incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)

	before, err := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
	s.Require().NoError(err)
	var beforeEvents int
	s.Require().NoError(s.Runner().QueryRowContext(
		s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		created.Issue.ID,
	).Scan(&beforeEvents))

	_, err = s.issueUseCase().ApplyUpdate(s.Ctx(), created.Issue.ID, domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"title": ""},
	}, "claimant")
	s.ErrorIs(err, storage.ErrValidation)

	after, getErr := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
	s.Require().NoError(getErr)
	s.Equal(before.Title, after.Title)
	s.Equal(before.Priority, after.Priority)
	s.Equal(before.EstimatedMinutes, after.EstimatedMinutes)
	s.Equal(before.Status, after.Status)
	s.Equal(before.Assignee, after.Assignee)
	s.Equal(before.RowVersion, after.RowVersion)
	var afterEvents int
	s.Require().NoError(s.Runner().QueryRowContext(
		s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		created.Issue.ID,
	).Scan(&afterEvents))
	s.Equal(beforeEvents, afterEvents)
}

func (s *testSuite) TestApplyUpdateValidatesIssueTypeBeforeClaim() {
	created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			ID:        "bd-domain-validate-type-before-claim",
			Title:     "valid incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)

	before, err := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
	s.Require().NoError(err)
	var beforeEvents int
	s.Require().NoError(s.Runner().QueryRowContext(
		s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		created.Issue.ID,
	).Scan(&beforeEvents))

	_, err = s.issueUseCase().ApplyUpdate(s.Ctx(), created.Issue.ID, domain.UpdateSpec{
		Claim:  true,
		Fields: map[string]any{"issue_type": "bogus"},
	}, "claimant")
	s.ErrorIs(err, storage.ErrValidation)

	after, getErr := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
	s.Require().NoError(getErr)
	s.Equal(before.IssueType, after.IssueType)
	s.Equal(before.Status, after.Status)
	s.Equal(before.Assignee, after.Assignee)
	s.Equal(before.RowVersion, after.RowVersion)
	var afterEvents int
	s.Require().NoError(s.Runner().QueryRowContext(
		s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		created.Issue.ID,
	).Scan(&afterEvents))
	s.Equal(beforeEvents, afterEvents)
}

func (s *testSuite) TestIssueUpdateAcceptsLegacyIssueTypeRepresentations() {
	checks := []struct {
		name      string
		id        string
		issueType any
		want      types.IssueType
	}{
		{"string", "bd-domain-type-string", "bug", types.TypeBug},
		{"typed", "bd-domain-type-typed", types.TypeFeature, types.TypeFeature},
	}
	for _, check := range checks {
		s.Run(check.name, func() {
			created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
				Issue: &types.Issue{
					ID:        check.id,
					Title:     "legacy type representation",
					IssueType: types.TypeTask,
					Priority:  2,
				},
			}, "tester")
			s.Require().NoError(err)

			err = s.issueUseCase().UpdateIssue(s.Ctx(), created.Issue.ID, map[string]any{
				"issue_type": check.issueType,
			}, "tester")
			s.Require().NoError(err)

			updated, err := s.issueUseCase().GetIssue(s.Ctx(), created.Issue.ID)
			s.Require().NoError(err)
			s.Equal(check.want, updated.IssueType)
		})
	}
}
