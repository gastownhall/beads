package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestBlockedListerContract runs the BlockedLister contract against the
// unit-of-work provider, through its own capability accessor.
func TestBlockedListerContract(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "blk")
	source, ok := provider.(BlockedListerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BlockedLister accessor", provider)
	}
	lister, err := source.BlockedLister()
	if err != nil {
		t.Fatalf("BlockedLister(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, "blk")
	conformance.RunBlockedLister(t, ctx, conformance.BlockedListerFixture{
		IssuePrefix:   kit.IssuePrefix,
		BlockedLister: lister,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
	})
}
