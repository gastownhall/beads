package httpclient

import (
	"context"
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
	client := bindRole(t, f, func(s *Store) (*Store, error) { return s, nil })
	conformance.RunBlockedLister(t, t.Context(), conformance.BlockedListerFixture{
		IssuePrefix:   servedIssuePrefix + "-blk",
		BlockedLister: lister,
		CreateIssue:   c.seedIssue,
		CreateWisp:    c.seedIssue,
		AddDependency: c.seedDependency,
		CloseIssue: func(ctx context.Context, id, reason, actor string) error {
			return client.CloseIssue(ctx, id, reason, actor, "")
		},
	})
}
