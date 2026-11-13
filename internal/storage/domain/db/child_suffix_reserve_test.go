package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
)

// TestChildSuffixReservationOnRemoval pins GH#4750 on the domain repository's
// removal entries, the ones the unit-of-work delete runs (Delete, and
// DeleteByIDs per plane): removing P.3, the highest of P.1-P.3, from a parent
// whose counter is missing keeps suffix 3 used in the parent's counter, so the
// next generated children are P.4 and P.5. Seeded rows write no counter, which
// is the legacy shape.
func (s *testSuite) TestChildSuffixReservationOnRemoval() {
	cases := []struct {
		name                  string
		parentWisp, childWisp bool
		bulk                  bool
	}{
		{"Delete", false, false, false},
		{"DeleteOfAWispChildOfADurableParent", false, true, false},
		{"DeleteUnderAWispParent", true, true, false},
		{"DeleteByIDs", false, false, true},
		{"DeleteByIDsOfAWispChildOfADurableParent", false, true, true},
		{"DeleteByIDsUnderAWispParent", true, true, true},
	}
	for i, tc := range cases {
		s.Run(tc.name, func() {
			parent := "bd-csr-" + string(rune('a'+i))
			seed := func(id string, wisp bool) {
				if wisp {
					s.seedWispRow(id)
				} else {
					s.seedIssueRow(id)
				}
			}
			seed(parent, tc.parentWisp)
			for _, n := range []string{".1", ".2", ".3"} {
				seed(parent+n, tc.childWisp)
			}

			removed := parent + ".3"
			opts := domain.IssueTableOpts{UseWispsTable: tc.childWisp}
			if tc.bulk {
				n, err := s.issueRepo().DeleteByIDs(s.Ctx(), []string{removed}, opts, "tester")
				s.Require().NoError(err)
				s.Require().Equal(1, n)
			} else {
				s.Require().NoError(s.issueRepo().Delete(s.Ctx(), removed, opts, "tester"))
			}

			counters := s.childCounterRepo()
			for _, want := range []string{parent + ".4", parent + ".5"} {
				got, err := counters.NextChildID(s.Ctx(), parent, domain.ChildCounterOpts{UseWispsTable: tc.parentWisp})
				s.Require().NoError(err)
				s.Require().Equal(want, got, "removing %s must keep its suffix reserved under %s", removed, parent)
			}
		})
	}
}
