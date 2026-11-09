package db

import (
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// dbAgedUpdatedAt is decades before any clock these tests run under, so a
// stamp that did not move cannot pass for one that did.
var dbAgedUpdatedAt = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestLabelClock pins the proxied-server (uow) half of GH#5442: the repository
// write stack behind LabelUseCase Add/Remove/Set stamps the owning row's
// updated_at on a real label change and preserves a seeded past timestamp on
// a no-op, on both the durable and wisp planes.
func (s *testSuite) TestLabelClock() {
	s.Run("InsertAdvancesUpdatedAt", s.labelClockInsertAdvances)
	s.Run("DeleteAdvancesUpdatedAt", s.labelClockDeleteAdvances)
	s.Run("NoopInsertPreservesUpdatedAt", s.labelClockNoopInsert)
	s.Run("NoopDeletePreservesUpdatedAt", s.labelClockNoopDelete)
	s.Run("WispInsertAdvancesUpdatedAt", s.labelClockWispInsert)
	s.Run("SetLabelsDiffAdvancesUpdatedAt", s.labelClockSetLabelsDiff)
	s.Run("SetLabelsSameSetPreservesUpdatedAt", s.labelClockSetLabelsSame)
	s.Run("RenameAdvancesCarriersPreservesNonCarrier", s.labelClockRename)
}

func (s *testSuite) labelClockBackdate(table, id string) {
	//nolint:gosec // G201: table is one of two hardcoded constants
	_, err := s.Runner().ExecContext(s.Ctx(),
		"UPDATE "+table+" SET updated_at = ? WHERE id = ?", dbAgedUpdatedAt, id)
	s.Require().NoError(err)
}

func (s *testSuite) labelClockUpdatedAt(table, id string) time.Time {
	var got time.Time
	//nolint:gosec // G201: table is one of two hardcoded constants
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT updated_at FROM "+table+" WHERE id = ?", id).Scan(&got))
	return got.UTC()
}

func (s *testSuite) labelClockInsertAdvances() {
	s.seedIssueRow("bd-lclk-ins")
	s.labelClockBackdate("issues", "bd-lclk-ins")
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(), "bd-lclk-ins", "live", "tester", domain.LabelOpts{}))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-ins"); !got.After(dbAgedUpdatedAt) {
		s.T().Errorf("Insert (real add) left updated_at at %v, want a stamp after %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockDeleteAdvances() {
	s.seedIssueRow("bd-lclk-del")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lclk-del", "live", "tester", domain.LabelOpts{}))
	s.labelClockBackdate("issues", "bd-lclk-del")
	s.Require().NoError(r.Delete(s.Ctx(), "bd-lclk-del", "live", "tester", domain.LabelOpts{}))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-del"); !got.After(dbAgedUpdatedAt) {
		s.T().Errorf("Delete (real removal) left updated_at at %v, want a stamp after %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockNoopInsert() {
	s.seedIssueRow("bd-lclk-noop")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lclk-noop", "dup", "tester", domain.LabelOpts{}))
	s.labelClockBackdate("issues", "bd-lclk-noop")
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lclk-noop", "dup", "tester", domain.LabelOpts{}))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-noop"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("idempotent re-add moved updated_at to %v, want it preserved at %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockNoopDelete() {
	s.seedIssueRow("bd-lclk-absent")
	s.labelClockBackdate("issues", "bd-lclk-absent")
	s.Require().NoError(s.labelRepo().Delete(s.Ctx(), "bd-lclk-absent", "never-added", "tester", domain.LabelOpts{}))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-absent"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("absent-label removal moved updated_at to %v, want it preserved at %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockWispInsert() {
	s.seedWispRow("bd-lclk-wisp")
	s.labelClockBackdate("wisps", "bd-lclk-wisp")
	s.Require().NoError(s.labelRepo().Insert(s.Ctx(), "bd-lclk-wisp", "live", "tester",
		domain.LabelOpts{UseWispsTable: true}))
	if got := s.labelClockUpdatedAt("wisps", "bd-lclk-wisp"); !got.After(dbAgedUpdatedAt) {
		s.T().Errorf("wisp Insert (real add) left updated_at at %v, want a stamp after %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockSetLabelsDiff() {
	s.seedIssueRow("bd-lclk-set")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-lclk-set", []string{"keep", "drop"}, "tester"))
	s.labelClockBackdate("issues", "bd-lclk-set")
	// A diff that adds and removes: the uow labels-only-update shape.
	s.Require().NoError(uc.SetLabels(s.Ctx(), "bd-lclk-set", []string{"keep", "add"}, "tester"))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-set"); !got.After(dbAgedUpdatedAt) {
		s.T().Errorf("SetLabels (real diff) left updated_at at %v, want a stamp after %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockSetLabelsSame() {
	s.seedIssueRow("bd-lclk-same")
	uc := s.labelUseCase()
	s.Require().NoError(uc.AddLabels(s.Ctx(), "bd-lclk-same", []string{"x"}, "tester"))
	s.labelClockBackdate("issues", "bd-lclk-same")
	s.Require().NoError(uc.SetLabels(s.Ctx(), "bd-lclk-same", []string{"x"}, "tester"))
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-same"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("SetLabels (same set) moved updated_at to %v, want it preserved at %v", got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockRename() {
	s.seedIssueRow("bd-lclk-rn-a")
	s.seedWispRow("bd-lclk-rn-w")
	s.seedIssueRow("bd-lclk-rn-nc")
	r := s.labelRepo()
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lclk-rn-a", "old", "tester", domain.LabelOpts{}))
	s.Require().NoError(r.Insert(s.Ctx(), "bd-lclk-rn-w", "old", "tester", domain.LabelOpts{UseWispsTable: true}))
	s.labelClockBackdate("issues", "bd-lclk-rn-a")
	s.labelClockBackdate("wisps", "bd-lclk-rn-w")
	s.labelClockBackdate("issues", "bd-lclk-rn-nc")

	_, _, _, err := s.labelUseCase().RenameLabel(s.Ctx(), "old", "new", "tester")
	s.Require().NoError(err)

	for _, tc := range []struct{ table, id string }{
		{"issues", "bd-lclk-rn-a"}, {"wisps", "bd-lclk-rn-w"},
	} {
		if got := s.labelClockUpdatedAt(tc.table, tc.id); !got.After(dbAgedUpdatedAt) {
			s.T().Errorf("rename left %s/%s updated_at at %v, want a stamp after %v",
				tc.table, tc.id, got, dbAgedUpdatedAt)
		}
	}
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-rn-nc"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("rename moved non-carrier updated_at to %v, want it preserved at %v", got, dbAgedUpdatedAt)
	}
}

// TestLabelClockCreatePreservation pins the uow create half of GH#5442 AC-3:
// a create persisting supplied (own or inherited) labels is a create, not a
// live label edit — the caller's timestamp T survives it. The live-edit touch
// fires only on edits (AC-1); LabelOpts.PreserveUpdatedAt is the seam.
func (s *testSuite) TestLabelClockCreatePreservation() {
	s.Run("OwnLabelsPreserveSuppliedTimestamp", s.labelClockCreateOwn)
	s.Run("InheritedLabelsPreserveSuppliedTimestamp", s.labelClockCreateInherit)
	s.Run("WispCreateWithLabelsPreservesSuppliedTimestamp", s.labelClockCreateWisp)
}

func (s *testSuite) labelClockCreateOwn() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()
	_, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		ExplicitID: "bd-lclk-cr",
		Issue: &types.Issue{
			Title: "create clock", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, UpdatedAt: dbAgedUpdatedAt,
		},
		Labels: []string{"own"},
	}, "tester")
	s.Require().NoError(err)
	if got := s.labelClockUpdatedAt("issues", "bd-lclk-cr"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("create with own labels moved updated_at to %v, want the supplied %v preserved",
			got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockCreateInherit() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()
	_, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		ExplicitID: "bd-lclk-par",
		Issue: &types.Issue{
			Title: "parent", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		},
		Labels: []string{"shared"},
	}, "tester")
	s.Require().NoError(err)

	res, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		ParentID: "bd-lclk-par", InheritLabelsFromParent: true,
		Issue: &types.Issue{
			Title: "child", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, UpdatedAt: dbAgedUpdatedAt,
		},
	}, "tester")
	s.Require().NoError(err)
	s.Require().NotEmpty(res.InheritedLabels)
	if got := s.labelClockUpdatedAt("issues", res.Issue.ID); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("create with inherited labels moved updated_at to %v, want the supplied %v preserved",
			got, dbAgedUpdatedAt)
	}
}

func (s *testSuite) labelClockCreateWisp() {
	s.resetMintConfig("bd", "")
	uc := s.issueUseCase()
	_, err := uc.CreateWisp(s.Ctx(), domain.CreateIssueParams{
		ExplicitID: "bd-lclk-wcr",
		Issue: &types.Issue{
			Title: "wisp create clock", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, UpdatedAt: dbAgedUpdatedAt,
		},
		Labels: []string{"own"},
	}, "tester")
	s.Require().NoError(err)
	if got := s.labelClockUpdatedAt("wisps", "bd-lclk-wcr"); !got.Equal(dbAgedUpdatedAt) {
		s.T().Errorf("wisp create with own labels moved updated_at to %v, want the supplied %v preserved",
			got, dbAgedUpdatedAt)
	}
}
