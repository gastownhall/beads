package db

import (
	"errors"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestLabelUseCase() {
	s.Run("RenameLabel", func() {
		s.Run("EmptyOldLabelReturnsError", s.lucRenameLabelEmptyOld)
		s.Run("EmptyNewLabelReturnsError", s.lucRenameLabelEmptyNew)
		s.Run("SameNameRefused", s.lucRenameLabelSameName)
		s.Run("EmitsOneRenamedEventPerIssue", s.lucRenameLabelEmitsRenamedEvent)
		s.Run("MergesOnCollisionAcrossBothPlanes", s.lucRenameLabelMergesAcrossPlanes)
	})
	s.Run("RemoveLabel", func() {
		s.Run("EmptyIDReturnsError", s.lucRemoveLabelEmptyID)
		s.Run("EmptyLabelReturnsError", s.lucRemoveLabelEmptyLabel)
		s.Run("DelegatesToRepoDelete", s.lucRemoveLabelDelegates)
	})
	s.Run("AddLabels", func() {
		s.Run("EmptyIDReturnsError", s.lucAddLabelsEmptyID)
		s.Run("SkipsEmptyEntries", s.lucAddLabelsSkipsEmpty)
		s.Run("AddsAllProvided", s.lucAddLabelsAll)
	})
	s.Run("RemoveLabels", func() {
		s.Run("EmptyIDReturnsError", s.lucRemoveLabelsEmptyID)
		s.Run("SkipsEmptyEntries", s.lucRemoveLabelsSkipsEmpty)
		s.Run("RemovesAllProvided", s.lucRemoveLabelsAll)
	})
	s.Run("SetLabels", func() {
		s.Run("EmptyIDReturnsError", s.lucSetLabelsEmptyID)
		s.Run("DiffAddsAndRemoves", s.lucSetLabelsDiffs)
		s.Run("SameSetIsNoop", s.lucSetLabelsSameSet)
		s.Run("EmptyDesiredRemovesAll", s.lucSetLabelsEmptyClears)
	})
	s.Run("Wisp", func() {
		s.Run("RemoveWispLabelRoutesToWispLabels", s.lucRemoveWispLabelRoutes)
		s.Run("AddWispLabelsRoutesToWispLabels", s.lucAddWispLabelsRoutes)
		s.Run("RemoveWispLabelsRoutesToWispLabels", s.lucRemoveWispLabelsRoutes)
		s.Run("SetWispLabelsDiffsWispsTable", s.lucSetWispLabelsDiffs)
	})
}

func (s *testSuite) labelUseCase() domain.LabelUseCase {
	return domain.NewLabelUseCase(NewLabelSQLRepository(s.Runner()))
}

func (s *testSuite) lucRemoveLabelEmptyID() {
	err := s.labelUseCase().RemoveLabel(s.Ctx(), "", "x", "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucRemoveLabelEmptyLabel() {
	err := s.labelUseCase().RemoveLabel(s.Ctx(), "bd-luc-rl", "", "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucRemoveLabelDelegates() {
	s.seedIssueRow("bd-luc-rl-1")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rl-1", "drop-me", "tester"))

	s.Require().NoError(uc.RemoveLabel(s.Ctx(), "bd-luc-rl-1", "drop-me", "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-rl-1")
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) lucAddLabelsEmptyID() {
	err := s.labelUseCase().AddLabels(s.Ctx(), "", []string{"x"}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucAddLabelsSkipsEmpty() {
	s.seedIssueRow("bd-luc-al-skip")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-al-skip", []string{"a", "", "b", ""}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-al-skip")
	s.Require().NoError(err)
	s.Equal([]string{"a", "b"}, out)
}

func (s *testSuite) lucAddLabelsAll() {
	s.seedIssueRow("bd-luc-al-1")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-al-1", []string{"one", "two", "three"}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-al-1")
	s.Require().NoError(err)
	s.Equal([]string{"one", "three", "two"}, out)
}

func (s *testSuite) lucRemoveLabelsEmptyID() {
	err := s.labelUseCase().RemoveLabels(s.Ctx(), "", []string{"x"}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucRemoveLabelsSkipsEmpty() {
	s.seedIssueRow("bd-luc-rml-skip")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-rml-skip", []string{"a", "b", "c"}, "tester"))

	s.Require().NoError(uc.RemoveLabels(s.Ctx(), "bd-luc-rml-skip", []string{"a", "", "c"}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-rml-skip")
	s.Require().NoError(err)
	s.Equal([]string{"b"}, out)
}

func (s *testSuite) lucRemoveLabelsAll() {
	s.seedIssueRow("bd-luc-rml-1")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-rml-1", []string{"a", "b"}, "tester"))

	s.Require().NoError(uc.RemoveLabels(s.Ctx(), "bd-luc-rml-1", []string{"a", "b"}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-rml-1")
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) lucSetLabelsEmptyID() {
	err := s.labelUseCase().SetLabels(s.Ctx(), "", []string{"x"}, "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucSetLabelsDiffs() {
	s.seedIssueRow("bd-luc-sl-diff")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-sl-diff", []string{"keep", "drop"}, "tester"))

	s.Require().NoError(uc.SetLabels(s.Ctx(), "bd-luc-sl-diff", []string{"keep", "add"}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-sl-diff")
	s.Require().NoError(err)
	s.Equal([]string{"add", "keep"}, out)
}

func (s *testSuite) lucSetLabelsSameSet() {
	s.seedIssueRow("bd-luc-sl-same")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-sl-same", []string{"x", "y"}, "tester"))

	s.Require().NoError(uc.SetLabels(s.Ctx(), "bd-luc-sl-same", []string{"x", "y"}, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-sl-same")
	s.Require().NoError(err)
	s.Equal([]string{"x", "y"}, out)

	var removedEvents int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = 'label_removed'",
		"bd-luc-sl-same").Scan(&removedEvents))
	s.Equal(0, removedEvents)
}

func (s *testSuite) lucSetLabelsEmptyClears() {
	s.seedIssueRow("bd-luc-sl-clear")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-luc-sl-clear", []string{"a", "b"}, "tester"))

	s.Require().NoError(uc.SetLabels(s.Ctx(), "bd-luc-sl-clear", nil, "tester"))

	out, err := uc.GetLabels(s.Ctx(), "bd-luc-sl-clear")
	s.Require().NoError(err)
	s.Empty(out)
}

func (s *testSuite) lucRemoveWispLabelRoutes() {
	s.seedWispRow("bd-lwc-rwl")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-lwc-rwl", "drop", "tester"))

	s.Require().NoError(uc.RemoveWispLabel(s.Ctx(), "bd-lwc-rwl", "drop", "tester"))

	wispLabels, err := uc.GetWispLabels(s.Ctx(), "bd-lwc-rwl")
	s.Require().NoError(err)
	s.Empty(wispLabels)
}

func (s *testSuite) lucAddWispLabelsRoutes() {
	s.seedWispRow("bd-lwc-awl")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddWispLabels(s.Ctx(), "bd-lwc-awl", []string{"a", "", "b"}, "tester"))

	wispLabels, err := uc.GetWispLabels(s.Ctx(), "bd-lwc-awl")
	s.Require().NoError(err)
	s.Equal([]string{"a", "b"}, wispLabels)

	issueLabels, err := uc.GetLabels(s.Ctx(), "bd-lwc-awl")
	s.Require().NoError(err)
	s.Empty(issueLabels, "wisp-routed Add must not touch the issues label table")
}

func (s *testSuite) lucRemoveWispLabelsRoutes() {
	s.seedWispRow("bd-lwc-rwls")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddWispLabels(s.Ctx(), "bd-lwc-rwls", []string{"keep", "drop1", "drop2"}, "tester"))

	s.Require().NoError(uc.RemoveWispLabels(s.Ctx(), "bd-lwc-rwls", []string{"drop1", "drop2"}, "tester"))

	wispLabels, err := uc.GetWispLabels(s.Ctx(), "bd-lwc-rwls")
	s.Require().NoError(err)
	s.Equal([]string{"keep"}, wispLabels)
}

func (s *testSuite) lucSetWispLabelsDiffs() {
	s.seedWispRow("bd-lwc-swl")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddWispLabels(s.Ctx(), "bd-lwc-swl", []string{"keep", "drop"}, "tester"))

	s.Require().NoError(uc.SetWispLabels(s.Ctx(), "bd-lwc-swl", []string{"keep", "add"}, "tester"))

	wispLabels, err := uc.GetWispLabels(s.Ctx(), "bd-lwc-swl")
	s.Require().NoError(err)
	s.Equal([]string{"add", "keep"}, wispLabels)
}

func (s *testSuite) lucRenameLabelEmptyOld() {
	_, _, _, err := s.labelUseCase().RenameLabel(s.Ctx(), "", "new", "tester")
	s.Require().Error(err)
}

func (s *testSuite) lucRenameLabelEmptyNew() {
	_, _, _, err := s.labelUseCase().RenameLabel(s.Ctx(), "old", "", "tester")
	s.Require().Error(err)
}

// lucRenameLabelSameName confirms the proxied-server route inherits the
// storage layer's ErrRenameLabelSameName refusal (both routes reach
// issueops.RenameLabelInTx) rather than wiping the label the way the merge
// branch would if a self-rename fell through unrefused.
func (s *testSuite) lucRenameLabelSameName() {
	s.seedIssueRow("bd-luc-rn-same")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rn-same", "dup", "tester"))

	_, _, _, err := uc.RenameLabel(s.Ctx(), "dup", "dup", "tester")
	s.Require().Error(err)
	s.Require().True(errors.Is(err, issueops.ErrRenameLabelSameName), "want ErrRenameLabelSameName, got %v", err)

	labels, err := uc.GetLabels(s.Ctx(), "bd-luc-rn-same")
	s.Require().NoError(err)
	s.Equal([]string{"dup"}, labels, "self-rename must write nothing")
}

// lucRenameLabelEmitsRenamedEvent is the regression test for the divergence
// finding: runLabelRenameProxiedServer used to fan a rename out into a
// per-issue AddLabel + RemoveLabel pair, journaling label_added/label_removed
// instead of one label_renamed. Assert both halves of the fix: the renamed
// event exists with the right old/new values, and the two-event shape's rows
// do not appear beyond the one label_added the initial seed itself produced.
func (s *testSuite) lucRenameLabelEmitsRenamedEvent() {
	s.seedIssueRow("bd-luc-rn-evt")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rn-evt", "old-name", "tester"))

	renamed, merged, ids, err := uc.RenameLabel(s.Ctx(), "old-name", "new-name", "tester")
	s.Require().NoError(err)
	s.Equal(1, renamed)
	s.Equal(0, merged)
	s.Equal([]string{"bd-luc-rn-evt"}, ids)

	labels, err := uc.GetLabels(s.Ctx(), "bd-luc-rn-evt")
	s.Require().NoError(err)
	s.Equal([]string{"new-name"}, labels)

	var renamedCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ? AND old_value = ? AND new_value = ?",
		"bd-luc-rn-evt", string(types.EventLabelRenamed), "old-name", "new-name",
	).Scan(&renamedCount))
	s.Equal(1, renamedCount, "expected exactly one label_renamed event")

	var addedCount, removedCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-luc-rn-evt", string(types.EventLabelAdded),
	).Scan(&addedCount))
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
		"bd-luc-rn-evt", string(types.EventLabelRemoved),
	).Scan(&removedCount))
	s.Equal(1, addedCount, "the only label_added row must be the initial seed's, not a second one from a rename-as-add")
	s.Equal(0, removedCount, "rename must not journal label_removed (that was the pre-fix proxied shape)")
}

// lucRenameLabelMergesAcrossPlanes exercises the merge-on-collision path
// across BOTH label planes in one rename, matching the direct route's
// TestRenameLabel_Merge/TestRenameLabel_WispSwept coverage in
// internal/storage/dolt/label_rename_test.go.
func (s *testSuite) lucRenameLabelMergesAcrossPlanes() {
	uc := s.labelUseCase()

	s.seedIssueRow("bd-luc-rn-only-old")
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rn-only-old", "wip", "tester"))

	s.seedIssueRow("bd-luc-rn-both")
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rn-both", "wip", "tester"))
	s.Require().NoError(uc.AddLabel(s.Ctx(), "bd-luc-rn-both", "in-progress", "tester"))

	s.seedWispRow("bd-luc-rn-wisp")
	s.Require().NoError(uc.AddWispLabel(s.Ctx(), "bd-luc-rn-wisp", "wip", "tester"))

	renamed, merged, ids, err := uc.RenameLabel(s.Ctx(), "wip", "in-progress", "tester")
	s.Require().NoError(err)
	s.Equal(3, renamed)
	s.Equal(1, merged)
	s.ElementsMatch([]string{"bd-luc-rn-only-old", "bd-luc-rn-both", "bd-luc-rn-wisp"}, ids)

	labels, err := uc.GetLabels(s.Ctx(), "bd-luc-rn-only-old")
	s.Require().NoError(err)
	s.Equal([]string{"in-progress"}, labels)

	labels, err = uc.GetLabels(s.Ctx(), "bd-luc-rn-both")
	s.Require().NoError(err)
	s.Equal([]string{"in-progress"}, labels, "no duplicate, no leftover wip")

	wispLabels, err := uc.GetWispLabels(s.Ctx(), "bd-luc-rn-wisp")
	s.Require().NoError(err)
	s.Equal([]string{"in-progress"}, wispLabels)
}
