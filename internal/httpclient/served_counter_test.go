//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_counter_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The Counter contract, run through the http client against a real bd serve.
//
// NOTHING IS PARKED HERE, and on this role that is a statement rather than a
// happy accident. Every case scopes itself with IDFilter — the count has
// always published `id` (the listing gained it, behind issues.list.ids, with
// S15b) — and every filter the
// contract reaches for is a parameter this operation carries. The plane cases in
// particular cross whole: `include_infra` is one flag on the wire and one flag
// on the request, and what it MEANS is four decisions the server's own role
// makes from the workspace's infra vocabulary, which is a config load this
// client could not perform and does not have to.
func servedCounterFixture(t *testing.T, e *servedEnv, prefix string) conformance.CounterFixture {
	t.Helper()
	counter, err := e.subject.Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	return conformance.CounterFixture{
		IssuePrefix:   prefix,
		Counter:       counter,
		CreateIssue:   e.createIssue,
		CreateWisp:    e.createWisp,
		CountHistory:  e.countHistory,
		AddDependency: e.addDependency,
		// List is deliberately left nil (unlike the dolt leg's wiring in
		// internal/storage/dolt/counter_contract_test.go). NoParent and
		// ExcludeTypes now ride the listing behind issues.list.filters, but the
		// cardinality cases list with IncludeAllTypes, which this client's
		// ListRequest encoder still refuses (E-ListRequest.IncludeAllTypes), so
		// wiring List here would make RunCounter*MatchesListCardinality FAIL
		// rather than take the requireCounterList skip this fixture's own
		// documented design intends for a leg that cannot express the
		// comparison.
	}
}

func TestServedCounterContract(t *testing.T) {
	e := newServedEnv(t, "cnt")
	fixture := servedCounterFixture(t, e, "cnt")

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.CounterFixture)
	}{
		{"CountsTheDurablePlaneByDefault", conformance.RunCounterCountsTheDurablePlaneByDefault},
		{"IncludeInfraMergesTheWispTier", conformance.RunCounterIncludeInfraMergesTheWispTier},
		{"IncludeInfraExcludesGates", conformance.RunCounterIncludeInfraExcludesGates},
		{"CountsClosedRows", conformance.RunCounterCountsClosedRows},
		{"AnUnknownStatusMatchesNothing", conformance.RunCounterAnUnknownStatusMatchesNothing},
		{"GroupsPartitionTheScalarSet", conformance.RunCounterGroupsPartitionTheScalarSet},
		{"LabelBucketsOverlapSoTotalIsNotTheirSum", conformance.RunCounterLabelBucketsOverlapSoTotalIsNotTheirSum},
		{"NamesTheEmptyBuckets", conformance.RunCounterNamesTheEmptyBuckets},
		{"PrefixesPriorityBuckets", conformance.RunCounterPrefixesPriorityBuckets},
		{"PriorityBucketsCountZeroAndCountEveryRow", conformance.RunCounterPriorityBucketsCountZeroAndCountEveryRow},
		{"TheNoLabelBucketIsAbsentWhenEveryRowIsLabeled", conformance.RunCounterTheNoLabelBucketIsAbsentWhenEveryRowIsLabeled},
		{"TypeBucketsAreTheRawTypeNames", conformance.RunCounterTypeBucketsAreTheRawTypeNames},
		{"RefusesAnUnknownGroup", conformance.RunCounterRefusesAnUnknownGroup},
		{"NormalizesLabelsAndLeavesTheRequestAlone", conformance.RunCounterNormalizesLabelsAndLeavesTheRequestAlone},
		{"WritesNothing", conformance.RunCounterWritesNothing},

		// S8 (#7199) count scope, served for real against a running bd serve:
		// ParentID, NoParent, ExcludeTypes and ExcludeStatus each narrow the
		// predicate over the http wire, exercising the client's encoding
		// (counts_test.go covers the unit-level encoding and the pre-dial
		// skew refusal; these confirm the served round trip). The three
		// *MatchesListCardinality cases SKIP here (requireCounterList) rather
		// than fail, since this fixture leaves List unwired — see
		// servedCounterFixture's comment.
		{"ParentIDScopesToChildren", conformance.RunCounterParentIDScopesToChildren},
		{"NoParentExcludesChildren", conformance.RunCounterNoParentExcludesChildren},
		{"ExcludeTypesNarrowsThePredicate", conformance.RunCounterExcludeTypesNarrowsThePredicate},
		{"ExcludeStatusNarrowsThePredicate", conformance.RunCounterExcludeStatusNarrowsThePredicate},
		{"ParentIDMatchesListCardinality", conformance.RunCounterParentIDMatchesListCardinality},
		{"NoParentMatchesListCardinality", conformance.RunCounterNoParentMatchesListCardinality},
		{"ExcludeTypesMatchesListCardinality", conformance.RunCounterExcludeTypesMatchesListCardinality},
		{"ParentIDIncludesAWispChild", conformance.RunCounterParentIDIncludesAWispChild},
		{"ParentIDAndExcludeStatusComposeOnAClosedChild", conformance.RunCounterParentIDAndExcludeStatusComposeOnAClosedChild},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}
