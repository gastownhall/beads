// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/brief_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// THE BRIEF DOOR: the projection marker, stamped client-side.
//
// `brief` is a request parameter on GET /v0/beads/issues and GET
// /v0/beads/ready (#5586) and this client sends it on both pages plus the ready
// bridge, so the server leaves the six heavy text columns unselected and the
// rows come back with them zero-valued. What does NOT come back is the fact that
// they were projected: types.Issue.IsLitePartial is `json:"-"`, so it never
// crosses, and a projected row is byte-identical to a genuinely textless one.
//
// THE ROW SAYS SO IN PROCESS AND THE WIRE SAYS SO BY HAVING BEEN ASKED, which is
// upstream's own resolution of the ambiguity (issueops/reader.go, ListRequest.
// Brief: "a wire consumer distinguishes them by having asked"). This client IS
// the consumer that asked, so it is the layer that can stamp — and it is the
// only one: the server cannot send a member the type does not marshal, and a
// caller above this seam has no way to know which of its rows came from a
// projected page.
//
// WHY IT IS NOT OPTIONAL. backend/conformance/reader_contract.go's
// assertReaderBriefRow hard-fails a projected row carrying IsLitePartial=false —
// "a blank body and a genuinely textless one are indistinguishable without it" —
// so the three Brief contracts cannot be adopted by any leg that does not stamp.
// That is why deferring the marker to this wave was safe: the contract forces
// it, loudly, at the moment the contract is wired.

// briefWire answers each page with one row whose heavy text is EMPTY, which is
// what a `brief` page really looks like on the wire — and is exactly why the
// marker cannot be inferred from the payload.
type briefWire struct {
	recordingWire
	lastQuery string
}

func (b *briefWire) Do(ctx context.Context, req wire.Request, out any) error {
	b.lastQuery = req.Query.Get("brief")
	row := apigen.IssueWithCounts{Issue: &types.Issue{ID: "http-brief-1", Title: "t"}}
	switch body := out.(type) {
	case *apigen.ReadyPage:
		*body = apigen.ReadyPage{Items: []apigen.IssueWithCounts{row}}
		return nil
	case *apigen.IssuesPage:
		*body = apigen.IssuesPage{Items: []apigen.IssueWithCounts{row}}
		return nil
	}
	return b.recordingWire.Do(ctx, req, out)
}

func briefStore(t *testing.T) (*Store, *briefWire) {
	t.Helper()
	w := &briefWire{}
	return New(testTarget(t), w, &apigen.ContextResponse{}), w
}

// TestBriefListingsStampTheProjectionMarker covers all THREE doors the parameter
// reaches: the listing, the ready page, and the off-role ready bridge whose
// legacy WorkFilter.Lite IS ReadyRequest.Brief under another name.
//
// The three are checked together rather than one per case because the bug this
// pins is asymmetric wiring: a client that stamped the listing and not the ready
// page would answer the same request two ways depending on which door it came
// in, which is the exact disagreement E-WorkFilter.Lite's ledger row warns about.
func TestBriefListingsStampTheProjectionMarker(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		brief bool
	}{
		{"projected", true},
		{"hydrated", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, w := briefStore(t)
			reader, err := store.IssueReader()
			if err != nil {
				t.Fatalf("IssueReader(): %v", err)
			}

			list, err := reader.List(ctx, issueops.ListRequest{Brief: tc.brief})
			if err != nil {
				t.Fatalf("List(): %v", err)
			}
			assertBriefMarker(t, "Reader.List", list.Items, tc.brief)
			assertBriefParam(t, "Reader.List", w.lastQuery, tc.brief)

			ready, err := reader.Ready(ctx, issueops.ReadyRequest{Brief: tc.brief})
			if err != nil {
				t.Fatalf("Ready(): %v", err)
			}
			assertBriefMarker(t, "Reader.Ready", ready.Items, tc.brief)
			assertBriefParam(t, "Reader.Ready", w.lastQuery, tc.brief)

			// StatusOpen is not decoration: the bridge's derived-default
			// inversion refuses any other status, because listReadyWork
			// publishes none and the server re-derives exactly this one. Every
			// real `bd ready` filter carries it for the same reason.
			bridged, err := store.GetReadyWorkWithCounts(ctx, types.WorkFilter{
				Status: types.StatusOpen,
				Lite:   tc.brief,
			})
			if err != nil {
				t.Fatalf("GetReadyWorkWithCounts(): %v", err)
			}
			assertBriefMarker(t, "the ready bridge", bridged, tc.brief)
			assertBriefParam(t, "the ready bridge", w.lastQuery, tc.brief)
		})
	}
}

// assertBriefMarker holds one page's rows to the marker the request earned.
//
// BOTH DIRECTIONS ARE ASSERTED, and the false one is not filler: a client that
// stamped unconditionally would tell every caller of an ordinary `bd list` that
// its rows are partial, which sends a consumer to refetch a body it already has
// — the same cost as the missing stamp, pointed the other way.
func assertBriefMarker(t *testing.T, what string, rows []*types.IssueWithCounts, want bool) {
	t.Helper()
	if len(rows) == 0 {
		t.Fatalf("%s answered no rows; this case cannot assert the marker on an empty page", what)
	}
	for _, row := range rows {
		if row == nil || row.Issue == nil {
			t.Fatalf("%s answered a row with no issue", what)
		}
		if row.IsLitePartial != want {
			t.Errorf("%s returned IsLitePartial = %v on %s, want %v: a projected row and a genuinely textless one are indistinguishable without it",
				what, row.IsLitePartial, row.ID, want)
		}
	}
}

// assertBriefParam is the other half of the same statement: the marker must
// describe a projection that was actually REQUESTED. Stamping a row the server
// hydrated in full would be this client inventing the fact rather than reporting
// it.
func assertBriefParam(t *testing.T, what, got string, want bool) {
	t.Helper()
	if want && got != "true" {
		t.Errorf("%s sent brief=%q, want %q — the marker has to describe a projection the request asked for", what, got, "true")
	}
	if !want && got != "" {
		t.Errorf("%s sent brief=%q on a hydrated read, want it absent", what, got)
	}
}
