package db

import (
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestIssueUseCase_Delete() {
	s.Run("DeleteIssue", func() {
		s.Run("EmptyIDReturnsError", s.iucDeleteEmptyID)
		s.Run("RemovesRowAndDeps", s.iucDeleteRemovesRowAndDeps)
		s.Run("CascadesAcrossDepTypes", s.iucDeleteCascades)
		s.Run("RewritesTextReferencesInNeighbors", s.iucDeleteRewritesRefs)
		s.Run("RecomputesIsBlockedOnAffected", s.iucDeleteRecomputesBlocked)
	})
	s.Run("DeleteIssues", func() {
		s.Run("EmptyIDsIsNoop", s.iucDeleteIssuesEmpty)
		s.Run("DryRunCountsButDoesNotDelete", s.iucDeleteIssuesDryRun)
		s.Run("CleansLabelsAndEvents", s.iucDeleteCleansAuxiliaryTables)
		s.Run("UpdateTextReferencesFalseLeavesRefs", s.iucDeleteSkipsRefsWhenFlagOff)
	})
	s.Run("DeleteWisp", func() {
		s.Run("DispatchesToWispsTable", s.iucDeleteWispDispatches)
	})
	s.Run("PreviewDelete", func() {
		s.Run("EmptyInputReturnsEmpty", s.iucPreviewEmpty)
		s.Run("PopulatesIssuesNotFoundAndConnected", s.iucPreviewPopulates)
		s.Run("DoesNotMutate", s.iucPreviewIsReadOnly)
	})
	s.Run("PreviewDeleteWisp", func() {
		s.Run("PopulatesFromWispsTable", s.iucPreviewWisp)
	})
}

func (s *testSuite) iucDeleteEmptyID() {
	_, err := s.issueUseCase().DeleteIssue(s.Ctx(), "", "tester")
	s.Require().Error(err)
}

func (s *testSuite) iucDeleteRemovesRowAndDeps() {
	s.seedOpenIssue("bd-iuc-del-a")
	s.seedOpenIssue("bd-iuc-del-b")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-del-a", "bd-iuc-del-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssue(s.Ctx(), "bd-iuc-del-a", "tester")
	s.Require().NoError(err)
	s.Equal(1, res.DeletedCount)
	s.Equal(1, res.DependenciesCount, "the A->B edge must be counted")

	var rows int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-iuc-del-a").Scan(&rows))
	s.Equal(0, rows)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? OR depends_on_issue_id = ?",
		"bd-iuc-del-a", "bd-iuc-del-a").Scan(&rows))
	s.Equal(0, rows)
}

func (s *testSuite) iucDeleteCascades() {
	s.seedOpenIssue("bd-iuc-cas-root")
	s.seedOpenIssue("bd-iuc-cas-mid")
	s.seedOpenIssue("bd-iuc-cas-leaf")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-cas-mid", "bd-iuc-cas-root", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-cas-leaf", "bd-iuc-cas-mid", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssue(s.Ctx(), "bd-iuc-cas-root", "tester")
	s.Require().NoError(err)
	s.Equal(3, res.DeletedCount, "root + mid + leaf")

	for _, id := range []string{"bd-iuc-cas-root", "bd-iuc-cas-mid", "bd-iuc-cas-leaf"} {
		var rows int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&rows))
		s.Equal(0, rows, "%s should be deleted", id)
	}
}

func (s *testSuite) iucDeleteRewritesRefs() {
	s.seedOpenIssue("bd-iuc-ref-target")
	s.seedOpenIssue("bd-iuc-ref-neighbor")
	s.Require().NoError(s.issueRepo().Update(s.Ctx(), "bd-iuc-ref-neighbor",
		map[string]any{"description": "see bd-iuc-ref-target for context"},
		"seeder", domain.IssueTableOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-ref-target", "bd-iuc-ref-neighbor", types.DepRelated), "tester", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssue(s.Ctx(), "bd-iuc-ref-target", "tester")
	s.Require().NoError(err)
	s.GreaterOrEqual(res.ReferencesUpdated, 1)

	updated, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-ref-neighbor", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.True(strings.Contains(updated.Description, "[deleted:bd-iuc-ref-target]"),
		"neighbor description should be rewritten; got %q", updated.Description)
}

func (s *testSuite) iucDeleteRecomputesBlocked() {
	s.seedOpenIssue("bd-iuc-rib-blocker")
	s.seedOpenIssue("bd-iuc-rib-depender")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-rib-depender", "bd-iuc-rib-blocker", types.DepBlocks),
		"seeder", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssue(s.Ctx(), "bd-iuc-rib-blocker", "tester")
	s.Require().NoError(err)
	s.Equal(2, res.DeletedCount, "blocker + depender (cascade)")
}

func (s *testSuite) iucDeleteIssuesEmpty() {
	res, err := s.issueUseCase().DeleteIssues(s.Ctx(),
		domain.DeleteIssuesParams{}, "tester")
	s.Require().NoError(err)
	s.Equal(0, res.DeletedCount)
}

func (s *testSuite) iucDeleteIssuesDryRun() {
	s.seedOpenIssue("bd-iuc-dry-a")
	s.seedOpenIssue("bd-iuc-dry-b")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-dry-a", "bd-iuc-dry-b", types.DepBlocks), "tester", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssues(s.Ctx(), domain.DeleteIssuesParams{
		IDs:    []string{"bd-iuc-dry-a"},
		DryRun: true,
	}, "tester")
	s.Require().NoError(err)
	s.Equal(0, res.DeletedCount, "DryRun must not actually delete")
	s.Equal(1, res.DependenciesCount)

	var rows int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-iuc-dry-a").Scan(&rows))
	s.Equal(1, rows, "row must still exist after DryRun")
}

func (s *testSuite) iucDeleteCleansAuxiliaryTables() {
	s.seedOpenIssue("bd-iuc-aux-a")
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(),
		"bd-iuc-aux-a", "tag1", "tester", domain.LabelOpts{}))
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(),
		"bd-iuc-aux-a", "tag2", "tester", domain.LabelOpts{}))
	s.Require().NoError(s.eventsRepo().Record(s.Ctx(),
		domain.Event{IssueID: "bd-iuc-aux-a", Type: types.EventCreated, Actor: "tester"},
		domain.RecordEventOpts{}))

	res, err := s.issueUseCase().DeleteIssue(s.Ctx(), "bd-iuc-aux-a", "tester")
	s.Require().NoError(err)
	s.Equal(2, res.LabelsCount)
	s.GreaterOrEqual(res.EventsCount, 1)

	var rows int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM labels WHERE issue_id = ?", "bd-iuc-aux-a").Scan(&rows))
	s.Equal(0, rows)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ?", "bd-iuc-aux-a").Scan(&rows))
	s.Equal(0, rows)
}

func (s *testSuite) iucDeleteSkipsRefsWhenFlagOff() {
	s.seedOpenIssue("bd-iuc-noref-target")
	s.seedOpenIssue("bd-iuc-noref-neighbor")
	original := "links bd-iuc-noref-target here"
	s.Require().NoError(s.issueRepo().Update(s.Ctx(), "bd-iuc-noref-neighbor",
		map[string]any{"description": original},
		"seeder", domain.IssueTableOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-noref-target", "bd-iuc-noref-neighbor", types.DepRelated),
		"tester", domain.DepInsertOpts{}))

	res, err := s.issueUseCase().DeleteIssues(s.Ctx(), domain.DeleteIssuesParams{
		IDs:                  []string{"bd-iuc-noref-target"},
		UpdateTextReferences: false,
	}, "tester")
	s.Require().NoError(err)
	s.Equal(0, res.ReferencesUpdated)

	survived, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-noref-neighbor", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal(original, survived.Description, "description must be untouched when flag is off")
}

func (s *testSuite) iucDeleteWispDispatches() {
	s.seedOpenWisp("bd-iuc-delw-1")
	s.seedOpenIssue("bd-iuc-delw-1")

	res, err := s.issueUseCase().DeleteWisp(s.Ctx(), "bd-iuc-delw-1", "tester")
	s.Require().NoError(err)
	s.Equal(1, res.DeletedCount)

	var wispRows, issueRows int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisps WHERE id = ?", "bd-iuc-delw-1").Scan(&wispRows))
	s.Equal(0, wispRows)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-iuc-delw-1").Scan(&issueRows))
	s.Equal(1, issueRows, "issues row with shadowed ID must remain")
}

func (s *testSuite) iucPreviewEmpty() {
	out, err := s.issueUseCase().PreviewDelete(s.Ctx(), nil)
	s.Require().NoError(err)
	s.Empty(out.Issues)
	s.Empty(out.ConnectedIssues)
	s.Empty(out.NotFound)
}

func (s *testSuite) iucPreviewPopulates() {
	s.seedOpenIssue("bd-iuc-pv-target")
	s.seedOpenIssue("bd-iuc-pv-neighbor")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-pv-target", "bd-iuc-pv-neighbor", types.DepBlocks),
		"seeder", domain.DepInsertOpts{}))

	out, err := s.issueUseCase().PreviewDelete(s.Ctx(),
		[]string{"bd-iuc-pv-target", "bd-iuc-pv-missing"})
	s.Require().NoError(err)
	s.Contains(out.Issues, "bd-iuc-pv-target")
	s.Equal([]string{"bd-iuc-pv-missing"}, out.NotFound)
	s.Contains(out.ConnectedIssues, "bd-iuc-pv-neighbor")
	s.Require().Len(out.DepRecords["bd-iuc-pv-target"], 1)
	s.Equal("bd-iuc-pv-neighbor", out.DepRecords["bd-iuc-pv-target"][0].DependsOnID)
}

func (s *testSuite) iucPreviewIsReadOnly() {
	s.seedOpenIssue("bd-iuc-pvro")
	_, err := s.issueUseCase().PreviewDelete(s.Ctx(), []string{"bd-iuc-pvro"})
	s.Require().NoError(err)

	got, err := s.issueRepo().Get(s.Ctx(), "bd-iuc-pvro", domain.IssueTableOpts{})
	s.Require().NoError(err)
	s.Equal("bd-iuc-pvro", got.ID, "preview must not mutate")
}

func (s *testSuite) iucPreviewWisp() {
	s.seedOpenWisp("bd-iuc-pvw")
	out, err := s.issueUseCase().PreviewDeleteWisp(s.Ctx(), []string{"bd-iuc-pvw"})
	s.Require().NoError(err)
	s.Contains(out.Issues, "bd-iuc-pvw", "wisp target should be hydrated from wisps table")
	s.Empty(out.NotFound)
}
