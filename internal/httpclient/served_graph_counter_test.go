//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_graph_counter_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The GraphCounter contract, run through the http client against a real bd
// serve.
//
// NOTHING IS PARKED HERE. Every member of the request is a published parameter
// and both members of the answer are required ones, so the two facts the role
// is written around cross intact: the count SPANS BOTH DEPENDENCY PLANES — a
// durable anchor's inbound count includes the wisps that depend on it, which is
// one number the server sums over two tables and this client never recomputes —
// and Missing is a SENTINEL rather than a zero, which is the whole of the
// difference between a typo and an issue with no edges.
//
// The status filter's direction=out refusal is here too, and it is the one
// refusal on this role that a client could have quietly softened: the parameter
// is legal on the operation and illegal in one direction, so a client that
// simply forwarded it would earn a 400 with a param name and pass this case for
// the wrong reason. It does not forward it: the role's own validator refuses
// first, in the role's own order.
func TestServedGraphCounterContract(t *testing.T) {
	e := newServedEnv(t, "gcn")
	counter, err := e.subject.GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter(): %v", err)
	}
	fixture := conformance.GraphCounterFixture{
		IssuePrefix:   "gcn",
		GraphCounter:  counter,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		AddDependency: e.addDependency,
		CountHistory:  e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.GraphCounterFixture)
	}{
		{"CountsOutboundEdges", conformance.RunGraphCounterCountsOutboundEdges},
		{"CountsInboundEdges", conformance.RunGraphCounterCountsInboundEdges},
		{"AnswersOnePerAnchorInRequestOrder", conformance.RunGraphCounterAnswersOnePerAnchorInRequestOrder},
		{"DistinguishesNoEdgesFromNoAnchor", conformance.RunGraphCounterDistinguishesNoEdgesFromNoAnchor},
		{"CollapsesRepeatedAnchors", conformance.RunGraphCounterCollapsesRepeatedAnchors},
		{"FiltersEdgesNotAnchors", conformance.RunGraphCounterFiltersEdgesNotAnchors},
		{"NarrowsInboundByDependentStatus", conformance.RunGraphCounterNarrowsInboundByDependentStatus},
		{"CountsAcrossBothPlanes", conformance.RunGraphCounterCountsAcrossBothPlanes},
		{"NarrowsAWispDependentByStatus", conformance.RunGraphCounterNarrowsAWispDependentByStatus},
		{"ResolvesIDsExactly", conformance.RunGraphCounterResolvesIDsExactly},
		{"AnswersAnEmptyRequest", conformance.RunGraphCounterAnswersAnEmptyRequest},
		{"RefusesAnUnusableRequest", conformance.RunGraphCounterRefusesAnUnusableRequest},
		{"LeavesTheRequestAlone", conformance.RunGraphCounterLeavesTheRequestAlone},
		{"WritesNothing", conformance.RunGraphCounterWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}
