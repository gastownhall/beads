//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_batch_getter_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The BatchGetter contract, run through the http client against a real bd
// serve.
//
// This is the role behind gc's ready veto and the CLI's own id-resolution
// passes once they move off one Get per id: a caller names up to
// issueops.MaxGetManyIDs ids and gets back every one the store holds,
// hydrated and in request order, plus a Missing sentinel for every id that
// named no row. The cap and the blank-id checks are refused CLIENT-SIDE, in
// checkGetManyIDs, before the dial — batch_get.go's failBatchGetErr collapses
// every validation failure on this surface into one generic,
// code-less InvalidArgument, so a round trip could never hand this client
// back the typed *issueops.TooManyIDsError the RefusesOverTheCap case
// requires.
func TestServedBatchGetterContract(t *testing.T) {
	e := newServedEnv(t, "bge")
	getter, err := e.subject.BatchGetter()
	if err != nil {
		t.Fatalf("BatchGetter(): %v", err)
	}
	fixture := conformance.BatchGetterFixture{
		IssuePrefix:  "bge",
		BatchGetter:  getter,
		CreateIssue:  e.createIssue,
		CreateWisp:   e.createWisp,
		CountHistory: e.countHistory,
	}

	for _, tc := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.BatchGetterFixture)
	}{
		{"FindsRequestedIssues", conformance.RunBatchGetterFindsRequestedIssues},
		{"ReportsMissingIDs", conformance.RunBatchGetterReportsMissingIDs},
		{"CollapsesRepeatedIDs", conformance.RunBatchGetterCollapsesRepeatedIDs},
		{"AnswersInRequestOrder", conformance.RunBatchGetterAnswersInRequestOrder},
		{"ResolvesIDsExactly", conformance.RunBatchGetterResolvesIDsExactly},
		{"AnswersAnEmptyRequest", conformance.RunBatchGetterAnswersAnEmptyRequest},
		{"RefusesAnUnusableRequest", conformance.RunBatchGetterRefusesAnUnusableRequest},
		{"RefusesOverTheCap", conformance.RunBatchGetterRefusesOverTheCap},
		{"AcceptsExactlyTheCap", conformance.RunBatchGetterAcceptsExactlyTheCap},
		{"HydratesLabels", conformance.RunBatchGetterHydratesLabels},
		{"LeavesTheRequestAlone", conformance.RunBatchGetterLeavesTheRequestAlone},
		{"SharesOneReadStructurally", conformance.RunBatchGetterSharesOneReadStructurally},
		{"CrossesBothPlanes", conformance.RunBatchGetterCrossesBothPlanes},
		{"WritesNothing", conformance.RunBatchGetterWritesNothing},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, t.Context(), fixture) })
	}
}
