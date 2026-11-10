package db

import (
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateRefusesUnpoppedClosePolicyOverride is the proxied funnel's half of
// the reserved-key transport pin. This repository's field allowlist is a
// separate map from the embedded funnel's, so it gets its own proof that the
// override is not a column here either: a malformed one survives the pop and is
// refused by name, a well-formed one is popped and leaves no trace.
func (s *testSuite) TestUpdateRefusesUnpoppedClosePolicyOverride() {
	created, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{
			ID:        "bd-domain-unpopped-override",
			Title:     "override transport",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	}, "tester")
	s.Require().NoError(err)
	id := created.Issue.ID

	err = NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), id, map[string]any{
		"priority":                  1,
		issueops.OpForceClosePolicy: "yes",
	}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err, "Update accepted a malformed close-policy override")
	s.Contains(err.Error(), "is not allowed")
	s.Contains(err.Error(), issueops.OpForceClosePolicy)

	unchanged, err := s.issueUseCase().GetIssue(s.Ctx(), id)
	s.Require().NoError(err)
	s.Equal(2, unchanged.Priority, "a refused update must write nothing")

	s.Require().NoError(NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), id, map[string]any{
		"priority":                  1,
		issueops.OpForceClosePolicy: true,
	}, "tester", domain.IssueTableOpts{}))
	applied, err := s.issueUseCase().GetIssue(s.Ctx(), id)
	s.Require().NoError(err)
	s.Equal(1, applied.Priority)
	s.Equal(types.StatusOpen, applied.Status)
}

// TestUpdateRefusesUnreadableStatusInsteadOfSkippingClosePolicy is the proxied
// funnel's half of the same pin. This repository asks the shared crossing check
// the same question the embedded funnel does, so a status value the check
// cannot read must refuse here too: a false would send a close straight past
// the gate and into SQL, where a []byte lands as 'closed' like any string.
func (s *testSuite) TestUpdateRefusesUnreadableStatusInsteadOfSkippingClosePolicy() {
	const parent, child = "bd-domain-unreadable-parent", "bd-domain-unreadable-child"
	for _, id := range []string{parent, child} {
		_, err := s.issueUseCase().CreateIssue(s.Ctx(), domain.CreateIssueParams{
			Issue: &types.Issue{ID: id, Title: "unreadable status", IssueType: types.TypeTask, Priority: 2},
		}, "tester")
		s.Require().NoError(err)
	}
	s.Require().NoError(domain.NewDependencyUseCase(NewDependencySQLRepository(s.Runner())).AddDependency(s.Ctx(),
		&types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}, "tester"))

	err := NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), parent,
		map[string]any{"status": []byte("closed")}, "tester", domain.IssueTableOpts{})
	s.Require().ErrorIs(err, storage.ErrValidation, "an unreadable status must refuse, not skip the gate")

	untouched, err := s.issueUseCase().GetIssue(s.Ctx(), parent)
	s.Require().NoError(err)
	s.Equal(types.StatusOpen, untouched.Status, "a refused update must write nothing")

	// The refusal is about the transport: spelled as a string, the same close
	// reaches the gate and is refused on the open child instead.
	err = NewIssueSQLRepository(s.Runner()).Update(s.Ctx(), parent,
		map[string]any{"status": string(types.StatusClosed)}, "tester", domain.IssueTableOpts{})
	s.Require().Error(err, "Update closed a parent with an open child")
	s.Require().NotErrorIs(err, storage.ErrValidation, "want a close-policy refusal, not a validation error")
}
