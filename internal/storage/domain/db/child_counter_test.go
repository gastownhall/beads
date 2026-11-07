package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func (s *testSuite) TestChildCounterSQLRepository() {
	s.Run("AdvanceForChild", func() {
		s.Run("RaisesCounterToTheChildSuffix", s.childCounterAdvanceRaises)
		s.Run("NeverLowersTheCounter", s.childCounterAdvanceNeverLowers)
		s.Run("SkipsTopLevelIDAndMissingParent", s.childCounterAdvanceSkips)
		s.Run("RoutesAWispParentToTheWispCounter", s.childCounterAdvanceWispParent)
		s.Run("LeavesCounterTablesByteIdenticalWhenNothingToRaise", s.childCounterAdvanceNoChange)
	})
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
		s.Run("FollowsACallerNamedChildAfterItsRowIsGone", s.childCounterFollowsAdvancedChild)
		s.Run("Wisp", func() {
			s.Run("RoutesToWispCounterAndWispsTable", s.childCounterWispRouting)
			s.Run("WispCounterIsolatedFromPermanent", s.childCounterWispIsolated)
			s.Run("WispRecoversFromStaleCounter", s.childCounterWispStaleRecovery)
			s.Run("WispSkipsARetiredChild", s.childCounterWispSkipsRetiredChild)
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

func (s *testSuite) counterValue(table, parentID string) (int, bool) {
	var lastChild int
	//nolint:gosec // G201: table is a test constant
	err := s.Runner().QueryRowContext(s.Ctx(), "SELECT last_child FROM "+table+" WHERE parent_id = ?", parentID).Scan(&lastChild)
	if err == sql.ErrNoRows {
		return 0, false
	}
	s.Require().NoError(err)
	return lastChild, true
}

// counterTablesDump renders every row of both counter tables in a fixed order,
// one "table parent last_child" line each, and returns it with its sha256.
func (s *testSuite) counterTablesDump() (string, string) {
	var b strings.Builder
	for _, table := range []string{"child_counters", "wisp_child_counters"} {
		//nolint:gosec // G202: table is a test constant
		rows, err := s.Runner().QueryContext(s.Ctx(), "SELECT parent_id, last_child FROM "+table+" ORDER BY parent_id")
		s.Require().NoError(err)
		for rows.Next() {
			var parent string
			var last int
			s.Require().NoError(rows.Scan(&parent, &last))
			fmt.Fprintf(&b, "%s %s %d\n", table, parent, last)
		}
		s.Require().NoError(rows.Err())
		s.Require().NoError(rows.Close())
	}
	sum := sha256.Sum256([]byte(b.String()))
	return b.String(), hex.EncodeToString(sum[:])
}

func (s *testSuite) childCounterAdvanceRaises() {
	s.seedIssueRow("bd-cc-adv")
	s.seedIssueRow("bd-cc-adv.8")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-adv.8"}))
	got, ok := s.counterValue("child_counters", "bd-cc-adv")
	s.Require().True(ok, "a caller-named child must create the parent's counter row")
	s.Equal(8, got)
}

func (s *testSuite) childCounterAdvanceNeverLowers() {
	s.seedIssueRow("bd-cc-low")
	r := s.childCounterRepo()
	s.seedIssueRow("bd-cc-low.5")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-low.5"}))
	s.seedIssueRow("bd-cc-low.2")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-low.2"}))
	got, _ := s.counterValue("child_counters", "bd-cc-low")
	s.Equal(5, got, "a lower caller-named child must not lower the counter")
}

func (s *testSuite) childCounterAdvanceSkips() {
	r := s.childCounterRepo()
	s.seedIssueRow("bd-cc-top")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-top"}))
	_, ok := s.counterValue("child_counters", "bd-cc-top")
	s.False(ok, "a top-level ID names no parent, so no counter row")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), nil))
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-orphan.3"}))
	_, ok = s.counterValue("child_counters", "bd-cc-orphan")
	s.False(ok, "an absent parent gets no counter row")
}

func (s *testSuite) childCounterAdvanceWispParent() {
	s.seedWispRow("bd-cc-wadv")
	s.seedWispRow("bd-cc-wadv.4")
	s.Require().NoError(s.childCounterRepo().AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-wadv.4", Ephemeral: true}))
	got, ok := s.counterValue("wisp_child_counters", "bd-cc-wadv")
	s.Require().True(ok)
	s.Equal(4, got)
	_, ok = s.counterValue("child_counters", "bd-cc-wadv")
	s.False(ok, "a wisp parent's counter lives in wisp_child_counters only")
}

// Where AdvanceForChild has nothing to raise (an equal or lower suffix, a
// lower wisp suffix, a top-level ID, an absent parent, nil), both counter
// tables must come out byte for byte as they went in, digest included.
func (s *testSuite) childCounterAdvanceNoChange() {
	r := s.childCounterRepo()
	s.seedIssueRow("bd-cc-same")
	s.seedIssueRow("bd-cc-same.6")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-same.6"}))
	s.seedWispRow("bd-cc-wsame")
	s.seedWispRow("bd-cc-wsame.3")
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-wsame.3", Ephemeral: true}))
	s.seedIssueRow("bd-cc-same.2")
	s.seedWispRow("bd-cc-wsame.1")
	s.seedIssueRow("bd-cc-flat")

	before, beforeSum := s.counterTablesDump()
	for _, issue := range []*types.Issue{
		nil,
		{ID: "bd-cc-same.6"},
		{ID: "bd-cc-same.2"},
		{ID: "bd-cc-wsame.1", Ephemeral: true},
		{ID: "bd-cc-flat"},
		{ID: "bd-cc-none.9"},
	} {
		s.Require().NoError(r.AdvanceForChild(s.Ctx(), issue))
	}
	after, afterSum := s.counterTablesDump()
	s.Equal(before, after, "nothing to raise must leave both counter tables byte-identical")
	s.Equal(beforeSum, afterSum, "nothing to raise must leave the counter tables' digest unchanged")
}

// The GH#4750 sequence at the repository seam: a caller-named child advanced
// the counter, then its row was deleted (a plain delete keeps no tombstone),
// so the counter is the only record of the suffix.
func (s *testSuite) childCounterFollowsAdvancedChild() {
	s.seedIssueRow("bd-cc-gone")
	s.seedIssueRow("bd-cc-gone.8")
	r := s.childCounterRepo()
	s.Require().NoError(r.AdvanceForChild(s.Ctx(), &types.Issue{ID: "bd-cc-gone.8"}))
	_, err := s.Runner().ExecContext(s.Ctx(), "DELETE FROM issues WHERE id = ?", "bd-cc-gone.8")
	s.Require().NoError(err)
	id, err := r.NextChildID(s.Ctx(), "bd-cc-gone", domain.ChildCounterOpts{})
	s.Require().NoError(err)
	s.Equal("bd-cc-gone.9", id, "a deleted caller-named child's suffix must not be minted again")
}

// The create door (domain.IssueUseCase.CreateIssue, the proxied-server path)
// must advance the counter for a child ID the caller names (GH#4750).
func (s *testSuite) TestIssueUseCaseChildCounter() {
	s.Run("ExplicitChildIDAdvancesParentCounter", s.useCaseExplicitChildAdvancesCounter)
	s.Run("CallerSetChildIDAdvancesParentCounter", s.useCaseCallerSetChildAdvancesCounter)
	s.Run("MintedChildLeavesOtherCounterRowsByteIdentical", s.useCaseMintedChildLeavesOtherRows)
}

// GH#4750 through the create door: bd create --id P.8 must raise P's counter,
// so after P.8 is deleted the parent's next minted child is P.9, not P.8 again.
func (s *testSuite) useCaseExplicitChildAdvancesCounter() {
	uc := s.issueUseCase()
	s.seedIssueRow("bd-ucx")
	first, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:    &types.Issue{Title: "minted child", IssueType: types.TypeTask, Priority: 2},
		ParentID: "bd-ucx",
	}, "tester")
	s.Require().NoError(err)
	s.Equal("bd-ucx.1", first.Issue.ID)

	_, err = uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:      &types.Issue{Title: "explicit child", IssueType: types.TypeTask, Priority: 2},
		ExplicitID: "bd-ucx.8",
	}, "tester")
	s.Require().NoError(err)
	got, _ := s.counterValue("child_counters", "bd-ucx")
	s.Equal(8, got, "an explicit child ID must advance the parent's counter")

	_, err = s.Runner().ExecContext(s.Ctx(), "DELETE FROM issues WHERE id = ?", "bd-ucx.8")
	s.Require().NoError(err)
	next, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:    &types.Issue{Title: "later child", IssueType: types.TypeTask, Priority: 2},
		ParentID: "bd-ucx",
	}, "tester")
	s.Require().NoError(err)
	s.Equal("bd-ucx.9", next.Issue.ID, "the deleted explicit child's suffix must not be reissued")
}

// A caller can name the ID on the issue itself (Issue.ID set, no ExplicitID,
// no ParentID); createIDNamedByCaller counts that as caller-named too.
func (s *testSuite) useCaseCallerSetChildAdvancesCounter() {
	uc := s.issueUseCase()
	s.seedIssueRow("bd-ucy")
	_, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue: &types.Issue{ID: "bd-ucy.5", Title: "caller-set child", IssueType: types.TypeTask, Priority: 2},
	}, "tester")
	s.Require().NoError(err)
	got, _ := s.counterValue("child_counters", "bd-ucy")
	s.Equal(5, got, "a caller-set child ID must advance the parent's counter")
}

// The minted path is the one this change leaves alone: a child minted under
// its parent writes that parent's counter row and nothing else, so every
// other counter row stays byte-identical.
func (s *testSuite) useCaseMintedChildLeavesOtherRows() {
	uc := s.issueUseCase()
	s.seedIssueRow("bd-ucm")
	before, _ := s.counterTablesDump()
	res, err := uc.CreateIssue(s.Ctx(), domain.CreateIssueParams{
		Issue:    &types.Issue{Title: "minted child", IssueType: types.TypeTask, Priority: 2},
		ParentID: "bd-ucm",
	}, "tester")
	s.Require().NoError(err)
	s.Equal("bd-ucm.1", res.Issue.ID)

	after, _ := s.counterTablesDump()
	const own = "child_counters bd-ucm 1\n"
	s.Contains(after, own, "the minted child must write its parent's counter row")
	s.Equal(before, strings.Replace(after, own, "", 1), "a minted child must leave every other counter row byte-identical")
}
