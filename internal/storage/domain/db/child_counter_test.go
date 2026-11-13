package db

import (
	"strconv"
	"strings"

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
		s.Run("SkipsARetiredChild", s.childCounterSkipsRetiredChild)
		s.Run("SkipsAChildRetiredFromTheWispsPlane", s.childCounterSkipsWispRetiredChild)
		s.Run("Wisp", func() {
			s.Run("RoutesToWispCounterAndWispsTable", s.childCounterWispRouting)
			s.Run("WispCounterIsolatedFromPermanent", s.childCounterWispIsolated)
			s.Run("WispRecoversFromStaleCounter", s.childCounterWispStaleRecovery)
			s.Run("WispSkipsARetiredChild", s.childCounterWispSkipsRetiredChild)
		})
	})
	// AdvanceForChild is the caller-named half of the counter contract
	// (bd-qp2au6, #4750): what `bd create --id P.8` owes the parent's counter
	// once the row is in. The minted half above already advances through
	// NextChildID, so an extra advance on a minted suffix must change nothing.
	s.Run("AdvanceForChild", func() {
		s.Run("RaisesCounterToTheChildSuffix", s.advanceForChildRaises)
		s.Run("NeverLowersAHigherCounter", s.advanceForChildNeverLowers)
		s.Run("EqualSuffixLeavesTheTablesByteIdentical", s.advanceForChildEqualNoOp)
		s.Run("NothingToRaiseLeavesTheTablesByteIdentical", s.advanceForChildNoOpTables)
		s.Run("AMintedChildAdvancesOnceOnly", s.advanceForChildAfterMint)
		s.Run("GrandchildRaisesItsImmediateParentOnly", s.advanceForChildGrandchild)
		s.Run("LeadingZeroSuffixParses", s.advanceForChildLeadingZero)
		s.Run("WispParentRoutesToWispCounters", s.advanceForChildWispParent)
		s.Run("WispChildUnderADurableParentUsesChildCounters", s.advanceForChildWispChild)
		s.Run("EmptyChildIDIsRejected", s.advanceForChildEmptyID)
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

// seedTombstone retires id into table (id_tombstones or wisp_id_tombstones)
// the way a retaining sweep does, with no row and no counter left behind.
func (s *testSuite) seedTombstone(table, id string) {
	_, err := s.Runner().ExecContext(s.Ctx(),
		"INSERT INTO "+table+" (id, closed_at) VALUES (?, ?)",
		id, "2026-03-01 12:00:00")
	s.Require().NoError(err)
}

// A retaining sweep deleted the highest child and kept only its tombstone, and
// the parent has no counter row (a database written before GH#4750, or an
// explicit child created through a door that does not advance it): the
// tombstone is the only record that the suffix was used, so it is taken.
func (s *testSuite) childCounterSkipsRetiredChild() {
	s.seedIssueRow("bd-cc-retired")
	s.seedIssueRow("bd-cc-retired.1")
	s.seedTombstone("id_tombstones", "bd-cc-retired.3")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-retired", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-retired.4", id, "a retired child suffix must count as taken, not be minted again")
}

// A child can be swept from the other plane than its parent's (a durable
// parent's wisp child), so both tombstone tables count.
func (s *testSuite) childCounterSkipsWispRetiredChild() {
	s.seedIssueRow("bd-cc-wretired")
	s.seedTombstone("wisp_id_tombstones", "bd-cc-wretired.2")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-wretired", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-wretired.3", id, "a child retired from the wisps plane must count as taken too")
}

func (s *testSuite) childCounterWispSkipsRetiredChild() {
	s.seedWispRow("bd-cc-ws-retired")
	s.seedTombstone("wisp_id_tombstones", "bd-cc-ws-retired.5")
	id, err := s.childCounterRepo().NextChildID(s.Ctx(), "bd-cc-ws-retired", domain.ChildCounterOpts{UseWispsTable: true})
	s.Require().NoError(err)
	s.Equal("bd-cc-ws-retired.6", id)
}

// counterTablesFingerprint renders both counter tables, sorted, as one
// comparable string: a case that must change nothing compares this before and
// after, so an implementation that mints extra rows or nudges an unrelated
// parent cannot pass as a no-op.
func (s *testSuite) counterTablesFingerprint() string {
	var b strings.Builder
	for _, table := range []string{"child_counters", "wisp_child_counters"} {
		rows, err := s.Runner().QueryContext(s.Ctx(),
			"SELECT parent_id, last_child FROM "+table+" ORDER BY parent_id")
		s.Require().NoError(err)
		for rows.Next() {
			var parentID string
			var lastChild int
			s.Require().NoError(rows.Scan(&parentID, &lastChild))
			b.WriteString(table + ":" + parentID + "=" + strconv.Itoa(lastChild) + "\n")
		}
		s.Require().NoError(rows.Err())
		s.Require().NoError(rows.Close())
	}
	return b.String()
}

func (s *testSuite) requireCounterTable(table, parentID string, want int) {
	var lastChild int
	err := s.Runner().QueryRowContext(s.Ctx(),
		"SELECT last_child FROM "+table+" WHERE parent_id = ?", parentID).Scan(&lastChild)
	s.Require().NoError(err, "%s row for %s", table, parentID)
	s.Equal(want, lastChild, "%s row for %s", table, parentID)
}

func (s *testSuite) requireNoCounterRow(table, parentID string) {
	var count int
	s.Require().NoError(s.Runner().QueryRowContext(s.Ctx(),
		"SELECT COUNT(*) FROM "+table+" WHERE parent_id = ?", parentID).Scan(&count))
	s.Equal(0, count, "%s must hold no row for %s", table, parentID)
}

func (s *testSuite) advanceForChildRaises() {
	s.seedIssueRow("bd-cc-adv")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), "bd-cc-adv.5"))
	s.requireCounterTable("child_counters", "bd-cc-adv", 5)
}

// AC-3: a counter already past the child's suffix keeps its higher value.
func (s *testSuite) advanceForChildNeverLowers() {
	s.seedIssueRow("bd-cc-hold")
	r := s.childCounterRepo()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-hold.9"))
	s.requireCounterTable("child_counters", "bd-cc-hold", 9)

	before := s.counterTablesFingerprint()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-hold.3"))
	s.Equal(before, s.counterTablesFingerprint(), "an advance below the counter must write nothing")
	s.requireCounterTable("child_counters", "bd-cc-hold", 9)
}

func (s *testSuite) advanceForChildEqualNoOp() {
	s.seedIssueRow("bd-cc-eq")
	r := s.childCounterRepo()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-eq.2"))

	before := s.counterTablesFingerprint()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-eq.2"))
	s.Equal(before, s.counterTablesFingerprint(), "an advance to the counter's own value must write nothing")
	s.requireCounterTable("child_counters", "bd-cc-eq", 2)
}

// The ids that must cost nothing: a flat id (no parent), a .0 suffix (zero is
// no advance), a nonnumeric suffix, a parent that does not exist (the counter
// row would violate its foreign key), and a suffix below the counter. Every
// one of them must leave both tables byte-identical.
func (s *testSuite) advanceForChildNoOpTables() {
	s.seedIssueRow("bd-cc-flat")
	s.seedIssueRow("bd-cc-zero")
	s.seedIssueRow("bd-cc-nonnum")
	s.seedIssueRow("bd-cc-below")
	r := s.childCounterRepo()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-below.4"))

	before := s.counterTablesFingerprint()
	for _, id := range []string{
		"bd-cc-flat",
		"bd-cc-zero.0",
		"bd-cc-nonnum.abc",
		"bd-cc-nosuchparent.7",
		"bd-cc-below.2",
	} {
		s.Require().NoError(r.AdvanceForChild(s.Ctx(), id), "AdvanceForChild(%q)", id)
		s.Equal(before, s.counterTablesFingerprint(), "AdvanceForChild(%q) must leave both counter tables untouched", id)
	}
}

// A minted child's suffix already reached the counter through NextChildID; a
// second advance of the same id must be a no-op, not one more.
func (s *testSuite) advanceForChildAfterMint() {
	s.seedIssueRow("bd-cc-mint")
	r := s.childCounterRepo()
	id, err := r.NextChildID(s.Ctx(), "bd-cc-mint", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-mint.1", id)

	before := s.counterTablesFingerprint()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), "bd-cc-mint.1"))
	s.Equal(before, s.counterTablesFingerprint(), "advancing a minted suffix again must change nothing")
	s.requireCounterTable("child_counters", "bd-cc-mint", 1)
}

func (s *testSuite) advanceForChildGrandchild() {
	s.seedIssueRow("bd-cc-g2")
	s.seedIssueRow("bd-cc-g2.1")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), "bd-cc-g2.1.7"))
	s.requireCounterTable("child_counters", "bd-cc-g2.1", 7)
	s.requireNoCounterRow("child_counters", "bd-cc-g2")
}

// Leading zeros parse with the shared parser's semantics: ".08" is suffix 8.
func (s *testSuite) advanceForChildLeadingZero() {
	s.seedIssueRow("bd-cc-lz")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), "bd-cc-lz.08"))
	s.requireCounterTable("child_counters", "bd-cc-lz", 8)
}

// AC-4: the counter follows the PARENT's plane, so a caller-named child of a
// wisp parent advances wisp_child_counters and leaves child_counters alone.
func (s *testSuite) advanceForChildWispParent() {
	s.seedWispRow("bd-cc-wadv")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), "bd-cc-wadv.3"))
	s.requireCounterTable("wisp_child_counters", "bd-cc-wadv", 3)
	s.requireNoCounterRow("child_counters", "bd-cc-wadv")
}

// Routing reads the parent, not the child: a wisp child under a durable
// parent is a durable parent's counter row.
func (s *testSuite) advanceForChildWispChild() {
	s.seedIssueRow("bd-cc-pw")
	s.seedWispRow("bd-cc-pw.2")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), "bd-cc-pw.2"))
	s.requireCounterTable("child_counters", "bd-cc-pw", 2)
	s.requireNoCounterRow("wisp_child_counters", "bd-cc-pw")
}

func (s *testSuite) advanceForChildEmptyID() {
	s.Require().Error(s.childCounterRepo().AdvanceForChild(s.Ctx(), ""))
}
