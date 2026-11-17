//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_relations_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Relations contract, run through the http client against a real bd serve.
//
// NOTHING IS PARKED HERE. The request has three members — the anchor in the
// path, a required direction and a repeatable type filter — and every one is a
// published parameter, so there is no shape for a divergence to live in and no
// W- row. The refusal vocabulary crosses too: the role's three ErrValidation
// refusals are raised client-side in the shared validator's own order, and the
// anchor's miss is a 404 the problem mapper has already turned into
// issueops.ErrNotFound.
//
// WHAT THIS TIER PROVES that no unit test can, and it is the whole reason the
// role's answer is worth checking end to end:
//
//   - THE MISS IS SEPARATED FROM THE EMPTY ANSWER by the SERVER, and both
//     answers travel. `SeparatesNoNeighborsFromNoSuchIssue` asks for the
//     neighbors of a real issue that has none and gets an empty list, then asks
//     about an id that names nothing and gets ErrNotFound. This is a single-anchor
//     operation, so there is no per-anchor Missing sentinel to fall back on —
//     which is exactly why the 404 has to arrive as a refusal rather than as an
//     empty page.
//   - BOTH HALVES ARE TWO-PLANE. `OrdersNeighborsFromBothPlanesTogether` seeds a
//     graph that straddles `dependencies` and `wisp_dependencies` and asserts the
//     merged order; `ResolvesAWispAnchor` uses a wisp id AS the anchor, and
//     `AnswersAWispTargetInTheOutDirection` puts one on the far end. Every one of
//     those is decided inside the server's transaction, and no member of the
//     answer says which plane a row came from.
//   - THE ORDER IS THE SERVER'S. `AnswersInThePinnedOrder` seeds its edges in
//     DESCENDING id order, so a client that re-sorted — or that trusted the
//     transport's arrival order — is told apart from one that receives the pinned
//     order and leaves it alone. This client re-sorts nothing on purpose: the
//     ordering is FinishRelatedPage's, and a second copy of it here is precisely
//     the drift that shared body exists to prevent.
//   - THE FILTER NARROWS EDGES, NEVER THE ANCHOR. `FiltersByAnOpenTypeVocabulary`
//     drives a workspace's own type through the repeatable parameter, which is
//     what says the vocabulary really is open on this path: a client that
//     validated against a known-types list would refuse a filter the server
//     accepts, and one that comma-joined would send a type name that matches
//     nothing.
//
// THE ANCHOR-ID EXACTNESS CASE is worth naming separately because its refusal
// changes shape on this leg without changing meaning: `ResolvesTheAnchorIDExactly`
// drives case variants, padding, prefixes and suffixes, and over http several of
// them are turned into the 404 by the SERVER's path bound before the role is
// reached. The answer a caller sees is the same ErrNotFound either way, which is
// what the case asserts.

func newServedRelationsFixture(t *testing.T, prefix string) conformance.RelationsFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	relations, err := env.subject.IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations(): %v", err)
	}
	return conformance.RelationsFixture{
		IssuePrefix:   env.prefix,
		Relations:     relations,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		AddDependency: env.addDependency,
		QueryScalar:   env.queryScalar,
	}
}

func TestServedRelationsContract(t *testing.T) {
	fixture := newServedRelationsFixture(t, "hrel")

	for _, tc := range []struct {
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
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}
