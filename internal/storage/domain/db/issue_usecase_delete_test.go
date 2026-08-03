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
		s.Run("MixedIssueAndWispMutatesCorrectTables", s.iucDeleteIssuesMixedIssueAndWispMutatesCorrectTables)
		s.Run("UpdateTextReferencesFalseLeavesRefs", s.iucDeleteSkipsRefsWhenFlagOff)
	})
	s.Run("DeleteWisp", func() {
		s.Run("DispatchesToWispsTable", s.iucDeleteWispDispatches)
		s.Run("CleansAuxiliaryTablesAndCascadesAcrossDependencyTypes", s.iucDeleteWispCleansAuxiliaryTablesAndCascadesAcrossDependencyTypes)
		s.Run("RewritesTextReferencesInWisps", s.iucDeleteWispRewritesTextReferencesInWisps)
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
	s.seedOpenWisp("bd-iuc-dry-c")
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-dry-b", "bd-iuc-dry-a", types.DepBlocks), "tester", domain.DepInsertOpts{}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep("bd-iuc-dry-c", "bd-iuc-dry-b", types.DepParentChild), "tester",
		domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(),
		"bd-iuc-dry-a", "dry-run-label", "tester", domain.LabelOpts{}))
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(),
		"bd-iuc-dry-c", "dry-run-wisp-label", "tester", domain.LabelOpts{UseWispsTable: true}))

	res, err := s.issueUseCase().DeleteIssues(s.Ctx(), domain.DeleteIssuesParams{
		IDs:     []string{"bd-iuc-dry-a"},
		Cascade: true,
		DryRun:  true,
	}, "tester")
	s.Require().NoError(err)
	s.Equal(3, res.DeletedCount, "DryRun must report the cascade candidate count")
	s.Equal(2, res.DependenciesCount)
	s.Equal(2, res.LabelsCount)
	s.Equal(5, res.EventsCount)

	for _, table := range []struct {
		name  string
		where string
		args  []any
		want  int
	}{
		{name: "issues", where: "id IN (?, ?)", args: []any{"bd-iuc-dry-a", "bd-iuc-dry-b"}, want: 2},
		{name: "wisps", where: "id = ?", args: []any{"bd-iuc-dry-c"}, want: 1},
		{
			name:  "dependencies",
			where: "issue_id = ? AND depends_on_issue_id = ?",
			args:  []any{"bd-iuc-dry-b", "bd-iuc-dry-a"},
			want:  1,
		},
		{
			name:  "wisp_dependencies",
			where: "issue_id = ? AND depends_on_issue_id = ?",
			args:  []any{"bd-iuc-dry-c", "bd-iuc-dry-b"},
			want:  1,
		},
		{name: "labels", where: "issue_id = ?", args: []any{"bd-iuc-dry-a"}, want: 1},
		{name: "wisp_labels", where: "issue_id = ?", args: []any{"bd-iuc-dry-c"}, want: 1},
		{name: "events", where: "issue_id IN (?, ?)", args: []any{"bd-iuc-dry-a", "bd-iuc-dry-b"}, want: 3},
		{name: "wisp_events", where: "issue_id = ?", args: []any{"bd-iuc-dry-c"}, want: 2},
	} {
		var rows int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM "+table.name+" WHERE "+table.where, table.args...).Scan(&rows))
		s.Equal(table.want, rows, "%s rows must remain after DryRun", table.name)
	}
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

func (s *testSuite) iucDeleteIssuesMixedIssueAndWispMutatesCorrectTables() {
	s.seedOpenIssue("bd-iuc-mixed-issue")
	s.seedOpenWisp("bd-iuc-mixed-wisp")
	s.seedOpenIssue("bd-iuc-mixed-wisp")

	res, err := s.issueUseCase().DeleteIssues(s.Ctx(), domain.DeleteIssuesParams{
		IDs:                  []string{"bd-iuc-mixed-issue", "bd-iuc-mixed-wisp"},
		Cascade:              true,
		UpdateTextReferences: true,
	}, "tester")
	s.Require().NoError(err)
	s.Equal(2, res.DeletedCount)

	var issueRows, wispRows, shadowIssueRows int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-iuc-mixed-issue").Scan(&issueRows))
	s.Equal(0, issueRows)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM wisps WHERE id = ?", "bd-iuc-mixed-wisp").Scan(&wispRows))
	s.Equal(0, wispRows)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", "bd-iuc-mixed-wisp").Scan(&shadowIssueRows))
	s.Equal(1, shadowIssueRows, "durable row shadowing the wisp ID must remain")
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

func (s *testSuite) iucDeleteWispCleansAuxiliaryTablesAndCascadesAcrossDependencyTypes() {
	root := "bd-iuc-wisp-cascade-root"
	mid := "bd-iuc-wisp-cascade-mid"
	leaf := "bd-iuc-wisp-cascade-leaf"
	for _, id := range []string{root, mid, leaf} {
		s.seedOpenWisp(id)
	}
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep(mid, root, types.DepBlocks), "tester", domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep(leaf, mid, types.DepParentChild), "tester", domain.DepInsertOpts{UseWispsTable: true}))
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(),
		root, "delete-me", "tester", domain.LabelOpts{UseWispsTable: true}))
	s.Require().NoError(s.eventsRepo().Record(s.Ctx(),
		domain.Event{IssueID: root, Type: types.EventCreated, Actor: "tester"},
		domain.RecordEventOpts{UseWispsTable: true}))

	res, err := s.issueUseCase().DeleteWisp(s.Ctx(), root, "tester")
	s.Require().NoError(err)
	s.Equal(3, res.DeletedCount, "root plus both transitive dependents")

	for _, id := range []string{root, mid, leaf} {
		var wispRows, labelRows, eventRows, dependencyRows int
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&wispRows))
		s.Equal(0, wispRows, "%s should be deleted", id)
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", id).Scan(&labelRows))
		s.Equal(0, labelRows, "%s labels should be deleted", id)
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?", id).Scan(&eventRows))
		s.Equal(0, eventRows, "%s events should be deleted", id)
		s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
			"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? OR depends_on_issue_id = ? OR depends_on_wisp_id = ?",
			id, id, id).Scan(&dependencyRows))
		s.Equal(0, dependencyRows, "%s dependencies should be deleted", id)
	}
}

func (s *testSuite) iucDeleteWispRewritesTextReferencesInWisps() {
	target := "bd-iuc-wisp-ref-target"
	neighbor := "bd-iuc-wisp-ref-neighbor"
	s.seedOpenWisp(target)
	s.seedOpenWisp(neighbor)
	s.Require().NoError(s.issueRepo().Update(s.Ctx(), neighbor,
		map[string]any{"description": "see " + target + " for context"},
		"seeder", domain.IssueTableOpts{UseWispsTable: true}))
	s.Require().NoError(s.depRepo().Insert(s.Ctx(),
		newDep(target, neighbor, types.DepRelated), "tester", domain.DepInsertOpts{UseWispsTable: true}))

	res, err := s.issueUseCase().DeleteWisp(s.Ctx(), target, "tester")
	s.Require().NoError(err)
	s.Equal(1, res.DeletedCount)
	s.GreaterOrEqual(res.ReferencesUpdated, 1)

	updated, err := s.issueRepo().Get(s.Ctx(), neighbor, domain.IssueTableOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Contains(updated.Description, "[deleted:"+target+"]")
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
