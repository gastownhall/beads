package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestRelationsContract runs the whole Relations contract against ONE
// unit-of-work provider.
//
// One provider, subtests, and NO t.Parallel. Every provider boots a real Dolt
// sql-server, so a case-per-provider suite pays that boot nine times; the
// contract fixtures were designed for sharing, and IssuePrefix namespaces what
// each case seeds. The no-parallel half matters because this backend has no
// per-test copy-on-write branch — the tables are database-global here.
func TestRelationsContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWRelationsFixture(t, ctx, "rel")

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.RelationsFixture)
	}{
		{"AnswersInThePinnedOrder", conformance.RunRelationsAnswersInThePinnedOrder},
		{"OrdersNeighborsFromBothPlanesTogether", conformance.RunRelationsOrdersNeighborsFromBothPlanesTogether},
		{"AnswersAWispTargetInTheOutDirection", conformance.RunRelationsAnswersAWispTargetInTheOutDirection},
		{"RefusesTheZeroDirection", conformance.RunRelationsRefusesTheZeroDirection},
		{"SeparatesNoNeighborsFromNoSuchIssue", conformance.RunRelationsSeparatesNoNeighborsFromNoSuchIssue},
		{"ResolvesAWispAnchor", conformance.RunRelationsResolvesAWispAnchor},
		{"FiltersByAnOpenTypeVocabulary", conformance.RunRelationsFiltersByAnOpenTypeVocabulary},
		{"RefusesAnUnusableTypeFilter", conformance.RunRelationsRefusesAnUnusableTypeFilter},
		{"RefusesATypeFilterOverTheColumnLength", conformance.RunRelationsRefusesATypeFilterOverTheColumnLength},
		{"DirectionSelectsTheInverseGraph", conformance.RunRelationsDirectionSelectsTheInverseGraph},
		{"LeavesTheCallersRequestAlone", conformance.RunRelationsLeavesTheCallersRequestAlone},
		{"LeavesAnExternalTargetOutOfTheAnswer", conformance.RunRelationsLeavesAnExternalTargetOutOfTheAnswer},
		{"ResolvesTheAnchorIDExactly", conformance.RunRelationsResolvesTheAnchorIDExactly},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, ctx, fixture)
		})
	}
}

func newUOWRelationsFixture(t *testing.T, ctx context.Context, prefix string) conformance.RelationsFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	relations, err := NewIssueRelations(provider)
	if err != nil {
		t.Fatalf("NewIssueRelations: %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.RelationsFixture{
		IssuePrefix:   kit.IssuePrefix,
		Relations:     relations,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}
}
