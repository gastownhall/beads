//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

func TestEmbeddedRelationsAnswersInThePinnedOrder(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsAnswersInThePinnedOrder(t, ctx, newEmbeddedRelationsFixture(t, "relord"))
}

func TestEmbeddedRelationsOrdersNeighborsFromBothPlanesTogether(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsOrdersNeighborsFromBothPlanesTogether(t, ctx, newEmbeddedRelationsFixture(t, "relplane"))
}

func TestEmbeddedRelationsAnswersAWispTargetInTheOutDirection(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsAnswersAWispTargetInTheOutDirection(t, ctx, newEmbeddedRelationsFixture(t, "relwout"))
}

func TestEmbeddedRelationsRefusesTheZeroDirection(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsRefusesTheZeroDirection(t, ctx, newEmbeddedRelationsFixture(t, "reldirnone"))
}

func TestEmbeddedRelationsSeparatesNoNeighborsFromNoSuchIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsSeparatesNoNeighborsFromNoSuchIssue(t, ctx, newEmbeddedRelationsFixture(t, "relmiss"))
}

func TestEmbeddedRelationsResolvesAWispAnchor(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsResolvesAWispAnchor(t, ctx, newEmbeddedRelationsFixture(t, "relwisp"))
}

func TestEmbeddedRelationsFiltersByAnOpenTypeVocabulary(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsFiltersByAnOpenTypeVocabulary(t, ctx, newEmbeddedRelationsFixture(t, "relfilter"))
}

func TestEmbeddedRelationsRefusesAnUnusableTypeFilter(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsRefusesAnUnusableTypeFilter(t, ctx, newEmbeddedRelationsFixture(t, "relbadfilter"))
}

func TestEmbeddedRelationsRefusesATypeFilterOverTheColumnLength(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsRefusesATypeFilterOverTheColumnLength(t, ctx, newEmbeddedRelationsFixture(t, "rellongfilter"))
}

func TestEmbeddedRelationsDirectionSelectsTheInverseGraph(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsDirectionSelectsTheInverseGraph(t, ctx, newEmbeddedRelationsFixture(t, "reldir"))
}

func TestEmbeddedRelationsLeavesTheCallersRequestAlone(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsLeavesTheCallersRequestAlone(t, ctx, newEmbeddedRelationsFixture(t, "relsnap"))
}

func TestEmbeddedRelationsLeavesAnExternalTargetOutOfTheAnswer(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsLeavesAnExternalTargetOutOfTheAnswer(t, ctx, newEmbeddedRelationsFixture(t, "relext"))
}

func TestEmbeddedRelationsResolvesTheAnchorIDExactly(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	conformance.RunRelationsResolvesTheAnchorIDExactly(t, ctx, newEmbeddedRelationsFixture(t, "relexact"))
}

func newEmbeddedRelationsFixture(t *testing.T, prefix string) conformance.RelationsFixture {
	t.Helper()
	te := newTestEnv(t, prefix)
	relations, err := te.store.IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.RelationsFixture{
		IssuePrefix:   kit.IssuePrefix,
		Relations:     relations,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}
}
