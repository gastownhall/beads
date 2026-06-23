package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
)

func (s *testSuite) TestLabelUseCase() {
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
