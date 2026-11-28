//go:build cgo

package httpclient

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/issueops"
)

// TestServedBlockedListerContract runs the BlockedLister contract against a
// real bd serve through this client's own BlockedLister accessor: the role
// dialed over listBlockedIssues must answer what the store legs answer.
func TestServedBlockedListerContract(t *testing.T) {
	c := composition(t)
	f := c.fixture(t)
	lister := bindRole(t, f, func(s *Store) (issueops.BlockedLister, error) { return s.BlockedLister() })
	fixture := conformance.BlockedListerFixture{
		IssuePrefix:   servedIssuePrefix + "-blk",
		BlockedLister: lister,
		CreateIssue:   c.seedIssue,
		CreateWisp:    c.seedIssue,
		AddDependency: c.seedDependency,
	}
	conformance.RunBlockedLister(t, t.Context(), fixture)
	// The Ctx case parks on its ledger row: listBlockedIssues publishes no ctx
	// parameter and the client refuses a declared context, even an empty one,
	// before the dial. TestBlockedWithADeclaredContextRefusesBeforeTheDial
	// asserts the refusal.
	t.Run("CtxHidesTheRowsTheDeclaredContextDoesNotSatisfy", func(t *testing.T) {
		skipKnownDivergence(t, "E-BlockedRequest.Ctx", readParkBead,
			"the case's subject is BlockedRequest.Ctx, and listBlockedIssues publishes no ctx parameter; the "+
				"client refuses a declared context rather than list as blocked the rows the caller said it cannot run")
		conformance.RunBlockedListerCtxHidesTheRowsTheDeclaredContextDoesNotSatisfy(t, t.Context(), fixture)
	})
}
