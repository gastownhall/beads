package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestRelationsAnswersInThePinnedOrder(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relord")
	defer cleanup()
	conformance.RunRelationsAnswersInThePinnedOrder(t, ctx, fixture)
}

func TestRelationsOrdersNeighborsFromBothPlanesTogether(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relplane")
	defer cleanup()
	conformance.RunRelationsOrdersNeighborsFromBothPlanesTogether(t, ctx, fixture)
}

func TestRelationsAnswersAWispTargetInTheOutDirection(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relwout")
	defer cleanup()
	conformance.RunRelationsAnswersAWispTargetInTheOutDirection(t, ctx, fixture)
}

func TestRelationsRefusesTheZeroDirection(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "reldirnone")
	defer cleanup()
	conformance.RunRelationsRefusesTheZeroDirection(t, ctx, fixture)
}

func TestRelationsSeparatesNoNeighborsFromNoSuchIssue(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relmiss")
	defer cleanup()
	conformance.RunRelationsSeparatesNoNeighborsFromNoSuchIssue(t, ctx, fixture)
}

func TestRelationsResolvesAWispAnchor(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relwisp")
	defer cleanup()
	conformance.RunRelationsResolvesAWispAnchor(t, ctx, fixture)
}

func TestRelationsFiltersByAnOpenTypeVocabulary(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relfilter")
	defer cleanup()
	conformance.RunRelationsFiltersByAnOpenTypeVocabulary(t, ctx, fixture)
}

func TestRelationsRefusesAnUnusableTypeFilter(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relbadfilter")
	defer cleanup()
	conformance.RunRelationsRefusesAnUnusableTypeFilter(t, ctx, fixture)
}

func TestRelationsRefusesATypeFilterOverTheColumnLength(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "rellongfilter")
	defer cleanup()
	conformance.RunRelationsRefusesATypeFilterOverTheColumnLength(t, ctx, fixture)
}

func TestRelationsDirectionSelectsTheInverseGraph(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "reldir")
	defer cleanup()
	conformance.RunRelationsDirectionSelectsTheInverseGraph(t, ctx, fixture)
}

func TestRelationsLeavesTheCallersRequestAlone(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relsnap")
	defer cleanup()
	conformance.RunRelationsLeavesTheCallersRequestAlone(t, ctx, fixture)
}

func TestRelationsLeavesAnExternalTargetOutOfTheAnswer(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relext")
	defer cleanup()
	conformance.RunRelationsLeavesAnExternalTargetOutOfTheAnswer(t, ctx, fixture)
}

func TestRelationsResolvesTheAnchorIDExactly(t *testing.T) {
	fixture, ctx, cleanup := newDoltRelationsFixture(t, "relexact")
	defer cleanup()
	conformance.RunRelationsResolvesTheAnchorIDExactly(t, ctx, fixture)
}

func newDoltRelationsFixture(t *testing.T, prefix string) (conformance.RelationsFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	relations, err := store.IssueRelations()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueRelations(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.RelationsFixture{
		IssuePrefix:   kit.IssuePrefix,
		Relations:     relations,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
