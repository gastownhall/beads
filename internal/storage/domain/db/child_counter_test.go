package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
)

func (s *testSuite) TestChildCounterSQLRepository() {
	s.Run("NextChildID", func() {
		s.Run("FirstChildReturnsDotOne", s.childCounterFirstChild)
		s.Run("MonotonicallyIncrements", s.childCounterIncrements)
		s.Run("RecoversFromStaleCounter", s.childCounterStaleCounterRecovery)
		s.Run("GrandchildrenDoNotInfluenceCounter", s.childCounterIgnoresGrandchildren)
		s.Run("EmptyParentIDIsRejected", s.childCounterEmptyParent)
		s.Run("MissingParentFailsFK", s.childCounterFKViolation)
		s.Run("CounterRowPersisted", s.childCounterRowPersisted)
		s.Run("Wisp", func() {
			s.Run("RoutesToWispCounterAndWispsTable", s.childCounterWispRouting)
			s.Run("WispCounterIsolatedFromPermanent", s.childCounterWispIsolated)
			s.Run("WispRecoversFromStaleCounter", s.childCounterWispStaleRecovery)
		})
	})
}

func (s *testSuite) childCounterRepo() domain.ChildCounterSQLRepository {
	return NewChildCounterSQLRepository(s.Runner())
}

func (s *testSuite) childCounterFirstChild() {
	s.seedIssueRow("bd-cc-first")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-first", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-first.1", id)
}

func (s *testSuite) childCounterIncrements() {
	s.seedIssueRow("bd-cc-inc")
	r := s.childCounterRepo()
	a, err := r.NextChildID(s.Ctx(), "bd-cc-inc", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-inc.1", a)
	b, err := r.NextChildID(s.Ctx(), "bd-cc-inc", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-inc.2", b)
	c, err := r.NextChildID(s.Ctx(), "bd-cc-inc", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-inc.3", c)
}

func (s *testSuite) childCounterStaleCounterRecovery() {
	s.seedIssueRow("bd-cc-stale")
	s.seedIssueRow("bd-cc-stale.7")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-stale", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-stale.8", id, "next ID should follow the highest extant child suffix, not last_child=0")
}

func (s *testSuite) childCounterIgnoresGrandchildren() {
	s.seedIssueRow("bd-cc-gc")
	s.seedIssueRow("bd-cc-gc.1")
	s.seedIssueRow("bd-cc-gc.1.7")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-gc", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-gc.2", id)
}

func (s *testSuite) childCounterEmptyParent() {
	_, err := s.childCounterRepo().NextChildID(s.Ctx(), "", domain.ChildCounterOpts{})
	s.Require().Error(err)
}

func (s *testSuite) childCounterFKViolation() {
	_, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-no-such-parent", domain.ChildCounterOpts{})
	s.Require().Error(err, "missing parent should fail fk_counter_parent")
}

func (s *testSuite) childCounterRowPersisted() {
	s.seedIssueRow("bd-cc-row")
	_, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-row", domain.ChildCounterOpts{})
	s.Require().NoError(err)

	var lastChild int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT last_child FROM child_counters WHERE parent_id = ?",
		"bd-cc-row",
	).Scan(&lastChild))
	s.Equal(1, lastChild)

	_, err = s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-row", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT last_child FROM child_counters WHERE parent_id = ?",
		"bd-cc-row",
	).Scan(&lastChild))
	s.Equal(2, lastChild, "subsequent calls should UPDATE the existing row")
}

func (s *testSuite) childCounterWispRouting() {
	s.seedWispRow("bd-cc-wisp")
	r := s.childCounterRepo()
	a, err := r.NextChildID(s.Ctx(), "bd-cc-wisp", domain.ChildCounterOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("bd-cc-wisp.1", a)
	b, err := r.NextChildID(s.Ctx(), "bd-cc-wisp", domain.ChildCounterOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("bd-cc-wisp.2", b)

	// Counter row landed in wisp_child_counters, not child_counters.
	var lastChild int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT last_child FROM wisp_child_counters WHERE parent_id = ?",
		"bd-cc-wisp",
	).Scan(&lastChild))
	s.Equal(2, lastChild)

	var permCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?",
		"bd-cc-wisp",
	).Scan(&permCount))
	s.Equal(0, permCount, "wisp-routed counter must not write to child_counters")
}

func (s *testSuite) childCounterWispIsolated() {
	// Same parent ID in both tables would never happen in practice (the parent
	// is either a wisp or an issue, not both), but the routing must be strict:
	// permanent and wisp counters are independent rows in independent tables.
	s.seedIssueRow("bd-cc-iso-perm")
	s.seedWispRow("bd-cc-iso-wisp")
	r := s.childCounterRepo()

	_, err := r.NextChildID(s.Ctx(), "bd-cc-iso-perm", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	_, err = r.NextChildID(s.Ctx(), "bd-cc-iso-wisp", domain.ChildCounterOpts{UseWispsTable: true})
	s.Require().NoError(err)

	var permCount, wispCount int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT COUNT(*) FROM child_counters").Scan(&permCount))
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(), "SELECT COUNT(*) FROM wisp_child_counters").Scan(&wispCount))
	s.GreaterOrEqual(permCount, 1)
	s.GreaterOrEqual(wispCount, 1)
}

func (s *testSuite) childCounterWispStaleRecovery() {
	// Pre-existing wisp child without a counter row; NextChildID must skip past it.
	s.seedWispRow("bd-cc-ws-stale")
	s.seedWispRow("bd-cc-ws-stale.4")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-ws-stale", domain.ChildCounterOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("bd-cc-ws-stale.5", id)
}
