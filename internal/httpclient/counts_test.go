// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/counts_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The two counting roles' own gates: the properties a served run cannot show,
// because a served run answers with the RIGHT number and the failure mode here
// is a number that looks equally right.
//
// Three of them recur and each has its own test below: a cardinality is 64 bits
// wide and must not be read through a float; a bucket map that arrives absent is
// not a bucket map that arrived empty; and an anchor's Missing flag is a
// SENTINEL rather than a zero, which is the only thing separating a typo from
// an issue that genuinely has no edges.

// countingWire records the request each role dialed and answers with a body the
// test supplied. It answers the READ half of the seam only, which is all either
// role touches.
type countingWire struct {
	fakeWire
	dialed []wire.Request
	body   string
	err    error
}

func newCountingWire(body string) *countingWire {
	w := &countingWire{body: body}
	w.preflight = func(context.Context, string) error { return nil }
	w.do = func(_ context.Context, req wire.Request, out any) error {
		w.dialed = append(w.dialed, req)
		if w.err != nil {
			return w.err
		}
		return json.Unmarshal([]byte(w.body), out)
	}
	return w
}

func (w *countingWire) query(t *testing.T) url.Values {
	t.Helper()
	if len(w.dialed) != 1 {
		t.Fatalf("dialed %d times, want exactly 1", len(w.dialed))
	}
	return w.dialed[0].Query
}

func countRole(t *testing.T, w *countingWire) issueops.Counter {
	t.Helper()
	counter, err := New(testTarget(t), w, nil).Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	return counter
}

func graphCountRole(t *testing.T, w *countingWire) issueops.GraphCounter {
	t.Helper()
	counter, err := New(testTarget(t), w, nil).GraphCounter()
	if err != nil {
		t.Fatalf("GraphCounter(): %v", err)
	}
	return counter
}

// TestACountIsReadAsSixtyFourBits is the decode convention, on both roles at
// once.
//
// The value is chosen so a float64 round trip is VISIBLE: 2^53+1 is the first
// integer a double cannot represent, and a lossy read answers 2^53 — a number
// near the cardinality, which on a count is worse than an error because nothing
// downstream can tell. The wire says `format: int64` on both members and the
// role's results are int64; this is what holds the client to it.
func TestACountIsReadAsSixtyFourBits(t *testing.T) {
	const beyondFloat64 = int64(1)<<53 + 1

	t.Run("the issue count's total", func(t *testing.T) {
		w := newCountingWire(fmt.Sprintf(`{"total":%d}`, beyondFloat64))
		got, err := countRole(t, w).Count(t.Context(), issueops.CountRequest{})
		if err != nil {
			t.Fatalf("Count: %v", err)
		}
		if got.Total != beyondFloat64 {
			t.Errorf("Total = %d, want %d", got.Total, beyondFloat64)
		}
	})

	t.Run("the grouped total", func(t *testing.T) {
		w := newCountingWire(fmt.Sprintf(`{"total":%d,"groups":{"open":3}}`, beyondFloat64))
		got, err := countRole(t, w).CountByGroup(t.Context(), issueops.CountByGroupRequest{
			GroupBy: issueops.CountGroupStatus,
		})
		if err != nil {
			t.Fatalf("CountByGroup: %v", err)
		}
		if got.Total != beyondFloat64 {
			t.Errorf("Total = %d, want %d", got.Total, beyondFloat64)
		}
	})

	t.Run("an anchor's edge count", func(t *testing.T) {
		w := newCountingWire(fmt.Sprintf(`{"anchors":[{"id":"bd-1","count":%d,"missing":false}]}`, beyondFloat64))
		got, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{
			IDs: []string{"bd-1"}, Direction: issueops.EdgeDirectionOut,
		})
		if err != nil {
			t.Fatalf("CountEdges: %v", err)
		}
		if got.Anchors[0].Count != beyondFloat64 {
			t.Errorf("Count = %d, want %d", got.Anchors[0].Count, beyondFloat64)
		}
	})
}

// TestTheCountsQueryCarriesTheWholePredicate drives the role rather than the
// encoder, which is the difference that matters: the encoder is gated against
// the document, and this is what proves the ROLE hands it the caller's request
// instead of a request of its own.
func TestTheCountsQueryCarriesTheWholePredicate(t *testing.T) {
	w := newCountingWire(`{"total":7}`)
	if _, err := countRole(t, w).Count(t.Context(), issueops.CountRequest{
		Status:       "closed",
		IssueType:    "bug",
		IDFilter:     "bd-1,bd-2",
		Labels:       []string{"alpha"},
		IncludeInfra: true,
	}); err != nil {
		t.Fatalf("Count: %v", err)
	}

	q := w.query(t)
	for param, want := range map[string]string{
		"status": "closed", "type": "bug", "id": "bd-1,bd-2", "label": "alpha", "include_infra": "true",
	} {
		if got := q.Get(param); got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}
	// A scalar count must not ask for buckets: `groups` would come back, and a
	// caller that asked for a number would be paying for a second query.
	if q.Has("group_by") {
		t.Errorf("a scalar count sent group_by=%q", q.Get("group_by"))
	}
	if op := w.dialed[0].Op; op != wire.OpCountIssues {
		t.Errorf("dialed %q, want %q", op, wire.OpCountIssues)
	}
	if path := w.dialed[0].Path; path != wire.PathIssuesCount {
		t.Errorf("dialed %q, want %q", path, wire.PathIssuesCount)
	}
}

// TestAGroupedCountAsksTheSamePredicateAsTheScalarOne is the identity the role
// is built around, asserted where it can actually break: the two methods must
// send the SAME query but for `group_by`.
//
// A grouped count that narrowed differently would answer buckets over a set the
// caller never asked about, and every bucket in it would look plausible — the
// total beside them would even be self-consistent, because the server computes
// it from the same filter it bucketed.
func TestAGroupedCountAsksTheSamePredicateAsTheScalarOne(t *testing.T) {
	predicate := issueops.CountRequest{
		Status: "open", Assignee: "agent-7", Labels: []string{"alpha", "beta"},
		IDFilter: "bd-1,bd-2", NoLabels: true, IncludeInfra: true,
	}

	scalar := newCountingWire(`{"total":2}`)
	if _, err := countRole(t, scalar).Count(t.Context(), predicate); err != nil {
		t.Fatalf("Count: %v", err)
	}
	grouped := newCountingWire(`{"total":2,"groups":{"open":2}}`)
	if _, err := countRole(t, grouped).CountByGroup(t.Context(), issueops.CountByGroupRequest{
		Filter: predicate, GroupBy: issueops.CountGroupStatus,
	}); err != nil {
		t.Fatalf("CountByGroup: %v", err)
	}

	want, got := scalar.query(t), grouped.query(t)
	if got.Get("group_by") != string(issueops.CountGroupStatus) {
		t.Errorf("group_by = %q, want %q", got.Get("group_by"), issueops.CountGroupStatus)
	}
	got.Del("group_by")
	if got.Encode() != want.Encode() {
		t.Errorf("the grouped count asked %q and the scalar one asked %q; the two must ask the same predicate", got.Encode(), want.Encode())
	}
}

// TestAGroupedCountWithoutBucketsRefusesRatherThanReadingZero is the presence
// rule, and it is the one decode decision on this role that had a plausible
// wrong answer.
//
// `groups` is published as present exactly when the request carried `group_by`,
// and this client always sends it — so an absent member is a server that did not
// bucket. Substituting an empty map would answer "nothing matched" to a question
// that may have matched thousands, with a correct-looking total sitting beside
// it. A present-but-null member is the same fact in a different spelling and is
// refused with it.
func TestAGroupedCountWithoutBucketsRefusesRatherThanReadingZero(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"absent", `{"total":9}`},
		{"present but null", `{"total":9,"groups":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCountingWire(tc.body)
			got, err := countRole(t, w).CountByGroup(t.Context(), issueops.CountByGroupRequest{
				GroupBy: issueops.CountGroupLabel,
			})
			if err == nil {
				t.Fatalf("CountByGroup answered %+v for a body carrying no buckets", got)
			}
			if !strings.Contains(err.Error(), "groups") {
				t.Errorf("the refusal does not name the member: %v", err)
			}
		})
	}

	// The other side of the same rule: an EMPTY object is a real answer — the
	// predicate matched nothing — and must not be refused with it.
	w := newCountingWire(`{"total":0,"groups":{}}`)
	got, err := countRole(t, w).CountByGroup(t.Context(), issueops.CountByGroupRequest{GroupBy: issueops.CountGroupLabel})
	if err != nil {
		t.Fatalf("an empty bucket set refused: %v", err)
	}
	if got.Groups == nil || len(got.Groups) != 0 {
		t.Errorf("Groups = %v, want an empty non-nil map", got.Groups)
	}
}

// TestAnUnknownCountDimensionRefusesWithoutDialing pins the role's own closed
// vocabulary, and pins that the refusal costs no round trip.
//
// The EMPTY dimension is the sharp one. The encoder omits an empty `group_by`,
// so a client that let it through would dial the SCALAR shape, read a body with
// no `groups`, and — but for the presence rule above — hand a caller who asked
// for buckets an answer that had none.
func TestAnUnknownCountDimensionRefusesWithoutDialing(t *testing.T) {
	for _, group := range []issueops.CountGroup{"", "owner", "Status", "label "} {
		w := newCountingWire(`{"total":0,"groups":{}}`)
		_, err := countRole(t, w).CountByGroup(t.Context(), issueops.CountByGroupRequest{GroupBy: group})
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("CountByGroup(%q) error = %v, want ErrValidation", group, err)
		}
		if len(w.dialed) != 0 {
			t.Errorf("CountByGroup(%q) dialed %d times before refusing", group, len(w.dialed))
		}
	}
}

// TestTheCountGroupVocabularyMatchesTheSharedOne pins this package's redeclared
// dimension list in BOTH directions, and the second direction is the one that
// needed an authority outside this file.
//
// internal/workapi is denied to this package by depguard — a client that built
// filters would be answering questions the wire is supposed to answer — so the
// list is redeclared here, and something has to keep the copy honest. It runs
// from a test file, where the rule does not apply, exactly as
// TestDefaultListLimitMatchesTheSharedDefault does for the list default.
//
// The DOCUMENT is what the second direction reads, because it is the only side
// of this that a client can be wrong about without being wrong on its own
// terms. A dimension this package carries and the shared validator refuses is
// caught by asking the validator; a dimension the WIRE publishes and this
// package does not carry is invisible to any check written out of this file's
// own constants — the client would refuse a value the server would have
// answered, on its own authority, and every test in this package would agree
// with it. So the enum comes off internal/httpapi/spec/openapi.v0.yaml, which
// the encoder's own bijection gate already treats as the surface authority,
// and each published value is put back through the shared validator so the
// document and the role vocabulary are held together too.
func TestTheCountGroupVocabularyMatchesTheSharedOne(t *testing.T) {
	for _, group := range countGroups {
		if _, err := workapi.ValidateCountGroup(group); err != nil {
			t.Errorf("this package accepts %q, which the shared validator refuses: %v", group, err)
		}
	}

	published := publishedCountGroups(t)
	if len(published) == 0 {
		t.Fatal("the document published no group_by enum; this direction would assert nothing")
	}
	for _, group := range published {
		if err := validateCountGroup(group); err != nil {
			t.Errorf("the wire publishes group_by=%q and this package refuses it: %v.\n"+
				"A dimension added upstream must be carried, not turned down on this client's own authority.", group, err)
		}
		if _, err := workapi.ValidateCountGroup(group); err != nil {
			t.Errorf("the wire publishes group_by=%q and the shared validator refuses it: %v", group, err)
		}
	}
	for _, group := range countGroups {
		if !slices.Contains(published, group) {
			t.Errorf("this package carries %q, which the document does not publish: the server would answer 400 for it", group)
		}
	}
}

// publishedCountGroups reads countIssues' `group_by` enum out of the wire
// contract itself, rather than out of a second copy of the five names.
func publishedCountGroups(t *testing.T) []issueops.CountGroup {
	t.Helper()
	published := publishedParamEnum(t, wire.OpCountIssues, "group_by")
	out := make([]issueops.CountGroup, 0, len(published))
	for _, value := range published {
		out = append(out, issueops.CountGroup(value))
	}
	return out
}

// TestTheEdgeCountQueryCarriesEveryMember drives the graph count and reads its
// query back.
//
// The direction is the member worth naming: it is REQUIRED and has no default,
// so a client that omitted it would earn a 400 — and a client that defaulted it
// would count the other end of every edge and never say so.
func TestTheEdgeCountQueryCarriesEveryMember(t *testing.T) {
	w := newCountingWire(`{"anchors":[{"id":"bd-1","count":2,"missing":false},{"id":"bd-2","count":0,"missing":true}]}`)
	if _, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs:       []string{"bd-1", "bd-2"},
		Direction: issueops.EdgeDirectionIn,
		Types:     []issueops.DependencyType{"blocks", "related"},
		Status:    "open",
	}); err != nil {
		t.Fatalf("CountEdges: %v", err)
	}

	q := w.query(t)
	if got := q["issue_id"]; !reflect.DeepEqual(got, []string{"bd-1", "bd-2"}) {
		t.Errorf("issue_id = %v, want the anchors in first-mention order", got)
	}
	// Repeated, never comma-joined: the operation reads `type` with the
	// repeatable decoder and does no splitting, so a comma would travel into a
	// type name and match no edge.
	if got := q["type"]; !reflect.DeepEqual(got, []string{"blocks", "related"}) {
		t.Errorf("type = %v, want two repeated values", got)
	}
	if got := q.Get("direction"); got != string(issueops.EdgeDirectionIn) {
		t.Errorf("direction = %q, want %q", got, issueops.EdgeDirectionIn)
	}
	if got := q.Get("status"); got != "open" {
		t.Errorf("status = %q, want open", got)
	}
	if op, path := w.dialed[0].Op, w.dialed[0].Path; op != wire.OpCountDependencyEdges || path != wire.PathDependenciesCount {
		t.Errorf("dialed %q %q, want %q %q", op, path, wire.OpCountDependencyEdges, wire.PathDependenciesCount)
	}
}

// TestEdgeCountRequestSendsEveryMember is what the graph count has instead of an
// encoder table: a reflective sweep over the request shape.
//
// The three anchored graph reads beside it build their queries inline for the
// same reason this one does — an anchored request is not a predicate — but
// inline building has the encoder's own failure mode, which is SILENCE: a member
// added upstream tomorrow is a member nothing sends, and a count that dropped a
// type filter answers a bigger number that looks exactly like a count.
//
// So the classification is enumerated here and checked against reflection, and
// each entry is driven through the role to prove the wire member it names really
// moves when the member does.
func TestEdgeCountRequestSendsEveryMember(t *testing.T) {
	// Every populatable member of the request, with the parameter it becomes
	// and a value that changes the query.
	sends := map[string]struct {
		param string
		set   func(*issueops.EdgeCountRequest)
	}{
		"IDs":       {"issue_id", func(r *issueops.EdgeCountRequest) { r.IDs = []string{"bd-1", "bd-9"} }},
		"Direction": {"direction", func(r *issueops.EdgeCountRequest) { r.Direction = issueops.EdgeDirectionOut }},
		"Types":     {"type", func(r *issueops.EdgeCountRequest) { r.Types = []issueops.DependencyType{"blocks"} }},
		"Status":    {"status", func(r *issueops.EdgeCountRequest) { r.Status = "in_progress" }},
	}

	rt := reflect.TypeOf(issueops.EdgeCountRequest{})
	for i := range rt.NumField() {
		name := rt.Field(i).Name
		if !rt.Field(i).IsExported() {
			continue
		}
		if _, ok := sends[name]; !ok {
			t.Errorf("EdgeCountRequest.%s is populatable and UNCLASSIFIED.\n"+
				"The graph count builds its query inline, so a member nobody sends is a member silently dropped — and a dropped filter on a COUNT is a bigger number with nothing to show for it.", name)
		}
	}

	for name, entry := range sends {
		t.Run(name, func(t *testing.T) {
			// The baseline is the smallest request this role accepts, and it
			// is the INBOUND direction because `status` is legal only there —
			// the one member whose validity depends on another. The direction
			// case flips it; every other case leaves it alone.
			base := issueops.EdgeCountRequest{IDs: []string{"bd-1"}, Direction: issueops.EdgeDirectionIn}
			body := `{"anchors":[{"id":"bd-1","count":0,"missing":false},{"id":"bd-9","count":0,"missing":false}]}`

			before := newCountingWire(body)
			if _, err := graphCountRole(t, before).CountEdges(t.Context(), base); err != nil {
				t.Fatalf("the baseline refused: %v", err)
			}
			populated := base
			entry.set(&populated)
			after := newCountingWire(body)
			if _, err := graphCountRole(t, after).CountEdges(t.Context(), populated); err != nil {
				t.Fatalf("a populated %s refused: %v", name, err)
			}
			if reflect.DeepEqual(before.query(t)[entry.param], after.query(t)[entry.param]) {
				t.Errorf("a populated %s left %s at %v; the member never reached the wire",
					name, entry.param, after.query(t)[entry.param])
			}
		})
	}
}

// TestEdgeCountRefusesMoreAnchorsThanTheWireCarries is L-edgecount-bound's pin,
// both sides of the boundary, plus the fact that the refusal never dials.
//
// IT IS A UNIT PIN AND THAT IS THE WHOLE POINT, not a gap the served tier will
// close later: the refusal is PRE-DIAL BY DESIGN, so a served run would prove
// only that a request this client never sends would also have been refused by
// the server. What has to be true is that the client refuses first and reaches
// no server at all, and a fake wire is the only thing that can witness the
// second half. No conformance case reaches this bound either — the contract
// sets no cap, because the role sets none.
//
// The bound is measured AFTER the collapse, which is the half a naive check
// would get wrong: a caller naming one id a hundred and one times is asking
// about ONE anchor, and refusing it for a size the answer would never have had
// is a refusal about nothing.
func TestEdgeCountRefusesMoreAnchorsThanTheWireCarries(t *testing.T) {
	anchors := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("bd-%04d", i))
		}
		return out
	}
	body := func(ids []string) string {
		entries := make([]string, 0, len(ids))
		for _, id := range ids {
			entries = append(entries, fmt.Sprintf(`{"id":%q,"count":0,"missing":true}`, id))
		}
		return `{"anchors":[` + strings.Join(entries, ",") + `]}`
	}

	exactly := anchors(maxEdgeCountAnchors)
	w := newCountingWire(body(exactly))
	got, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs: exactly, Direction: issueops.EdgeDirectionOut,
	})
	if err != nil {
		t.Fatalf("exactly %d anchors refused: %v", maxEdgeCountAnchors, err)
	}
	if len(got.Anchors) != maxEdgeCountAnchors {
		t.Errorf("answered %d anchors, want %d", len(got.Anchors), maxEdgeCountAnchors)
	}

	over := newCountingWire(`{"anchors":[]}`)
	_, err = graphCountRole(t, over).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs: append(anchors(maxEdgeCountAnchors), "bd-over"), Direction: issueops.EdgeDirectionOut,
	})
	if !errors.Is(err, encode.ErrRefused) {
		t.Fatalf("%d anchors encoded without a refusal: %v", maxEdgeCountAnchors+1, err)
	}
	var refusal *encode.RefusedError
	if !errors.As(err, &refusal) || refusal.Row.ID != "L-edgecount-bound" {
		t.Errorf("refusal cites %+v, want ledger row L-edgecount-bound", err)
	}
	if len(over.dialed) != 0 {
		t.Errorf("the refusal dialed %d times; it must never reach the server", len(over.dialed))
	}

	// The collapse comes first: a hundred and one mentions of ONE anchor is one
	// anchor, and it serves.
	repeated := make([]string, maxEdgeCountAnchors+1)
	for i := range repeated {
		repeated[i] = "bd-1"
	}
	collapsed := newCountingWire(`{"anchors":[{"id":"bd-1","count":3,"missing":false}]}`)
	result, err := graphCountRole(t, collapsed).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs: repeated, Direction: issueops.EdgeDirectionOut,
	})
	if err != nil {
		t.Fatalf("%d mentions of one anchor refused: %v", len(repeated), err)
	}
	if len(result.Anchors) != 1 || result.Anchors[0].Count != 3 {
		t.Errorf("answered %+v, want one anchor counting 3", result.Anchors)
	}
	if got := collapsed.query(t)["issue_id"]; !reflect.DeepEqual(got, []string{"bd-1"}) {
		t.Errorf("sent issue_id=%v, want the collapsed single anchor", got)
	}
}

// TestAnUnansweredAnchorRefusesRatherThanCountingZero is the Missing sentinel's
// other half.
//
// The operation carries one entry per distinct requested id. A body missing one
// leaves a client two plausible substitutions — count 0, or missing — and those
// are the exact two answers this role exists to tell apart, so it makes neither.
func TestAnUnansweredAnchorRefusesRatherThanCountingZero(t *testing.T) {
	w := newCountingWire(`{"anchors":[{"id":"bd-1","count":4,"missing":false}]}`)
	_, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs: []string{"bd-1", "bd-2"}, Direction: issueops.EdgeDirectionOut,
	})
	if err == nil {
		t.Fatal("an answer missing an anchor was accepted")
	}
	if !strings.Contains(err.Error(), "bd-2") {
		t.Errorf("the refusal does not name the unanswered anchor: %v", err)
	}
}

// TestTheMissingSentinelSurvivesTheProjection is the decode convention that
// matters most on this role: Missing is a FACT, and 0 is the common answer.
//
// A projection that read the count and forgot the flag would answer 0/false for
// a typo — indistinguishable from an issue with no edges in that direction, and
// therefore a mistake a caller could not find.
func TestTheMissingSentinelSurvivesTheProjection(t *testing.T) {
	w := newCountingWire(`{"anchors":[
		{"id":"bd-present","count":0,"missing":false},
		{"id":"bd-typo","count":0,"missing":true}
	]}`)
	got, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{
		IDs: []string{"bd-present", "bd-typo"}, Direction: issueops.EdgeDirectionOut,
	})
	if err != nil {
		t.Fatalf("CountEdges: %v", err)
	}
	want := []issueops.AnchorEdgeCount{
		{ID: "bd-present", Count: 0, Missing: false},
		{ID: "bd-typo", Count: 0, Missing: true},
	}
	if !reflect.DeepEqual(got.Anchors, want) {
		t.Errorf("anchors = %+v, want %+v", got.Anchors, want)
	}
}

// TestAnEmptyEdgeCountAnswersWithoutDialing pins the two shapes that must not
// reach a server, in the role's own order.
//
// The direction is checked FIRST, so EdgeCountRequest{} is a refusal ABOUT THE
// DIRECTION rather than an empty answer — a caller who forgot it would otherwise
// get a plausible response forever. Only then is an empty anchor list a success
// with no anchors, which the operation itself could not answer: it requires at
// least one `issue_id`.
func TestAnEmptyEdgeCountAnswersWithoutDialing(t *testing.T) {
	w := newCountingWire(`{"anchors":[]}`)
	if _, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{}); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("the empty request error = %v, want ErrValidation about the direction", err)
	}

	got, err := graphCountRole(t, w).CountEdges(t.Context(), issueops.EdgeCountRequest{Direction: issueops.EdgeDirectionOut})
	if err != nil {
		t.Fatalf("an empty anchor list refused: %v", err)
	}
	if got.Anchors == nil || len(got.Anchors) != 0 {
		t.Errorf("Anchors = %+v, want an empty non-nil slice", got.Anchors)
	}
	if len(w.dialed) != 0 {
		t.Errorf("dialed %d times for requests that need no server", len(w.dialed))
	}
}

// TestNeitherCountingRoleWritesThroughTheCallerRequest pins the snapshot promise
// both roles carry, on the members that would be written through: the count's
// two label slices and the graph count's anchors and types.
//
// Reusing one request for several counts is the ordinary way to hold either
// role, so a normalization that sorted or de-duplicated in place would change
// the caller's next question rather than this one's answer.
func TestNeitherCountingRoleWritesThroughTheCallerRequest(t *testing.T) {
	count := issueops.CountRequest{Labels: []string{"  beta ", "alpha", "alpha"}, LabelsAny: []string{"gamma", ""}}
	before := fmt.Sprint(count.Labels, count.LabelsAny)
	w := newCountingWire(`{"total":1}`)
	if _, err := countRole(t, w).Count(t.Context(), count); err != nil {
		t.Fatalf("Count: %v", err)
	}
	if after := fmt.Sprint(count.Labels, count.LabelsAny); after != before {
		t.Errorf("the caller's label slices became %s, want %s", after, before)
	}

	edges := issueops.EdgeCountRequest{
		IDs:       []string{"bd-2", "bd-1", "bd-2"},
		Types:     []issueops.DependencyType{"related", "blocks"},
		Direction: issueops.EdgeDirectionOut,
	}
	edgesBefore := fmt.Sprint(edges.IDs, edges.Types)
	g := newCountingWire(`{"anchors":[{"id":"bd-2","count":1,"missing":false},{"id":"bd-1","count":0,"missing":false}]}`)
	if _, err := graphCountRole(t, g).CountEdges(t.Context(), edges); err != nil {
		t.Fatalf("CountEdges: %v", err)
	}
	if after := fmt.Sprint(edges.IDs, edges.Types); after != edgesBefore {
		t.Errorf("the caller's anchors became %s, want %s", after, edgesBefore)
	}
}

// countRoleWithSnapshot is countRole's twin for the S8 (#7199) count-scope
// tests below, which need to pin exactly what the handshake advertises
// (CapCountScope present or absent) rather than accept countRole's bare nil —
// passing a snapshot straight through New is the same idiom
// wave2c_claimnext_test.go uses to pin a capability set without a live
// handshake dial.
func countRoleWithSnapshot(t *testing.T, w *countingWire, snap *apigen.ContextResponse) issueops.Counter {
	t.Helper()
	counter, err := New(testTarget(t), w, snap).Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}
	return counter
}

// TestCountScopeFieldsEncodeOntoTheQuery is S8's client half, the encoding
// side: each of the four count-scope fields (ParentID, NoParent, ExcludeTypes,
// ExcludeStatus — internal/httpapi/routes.go's issues.count.scope) reaches the
// wire once the handshake advertises the token.
//
// ParentID and NoParent are exercised in separate cases rather than together:
// the ROLE (issueops/counter.go) refuses that combination as ErrValidation on
// every backend, this one included, so a case that set both would be testing a
// refusal rather than an encoding.
func TestCountScopeFieldsEncodeOntoTheQuery(t *testing.T) {
	served := &apigen.ContextResponse{Capabilities: []string{wire.CapCountScope}}

	for _, tc := range []struct {
		name string
		req  issueops.CountRequest
		want url.Values
	}{
		{"ParentID", issueops.CountRequest{ParentID: "bd-1"}, url.Values{"parent": {"bd-1"}}},
		{"NoParent", issueops.CountRequest{NoParent: true}, url.Values{"no_parent": {"true"}}},
		{"ExcludeTypes", issueops.CountRequest{ExcludeTypes: []string{"wisp", "gate"}}, url.Values{"exclude_type": {"wisp", "gate"}}},
		{"ExcludeStatus", issueops.CountRequest{ExcludeStatus: []string{"closed", "archived"}}, url.Values{"exclude_status": {"closed", "archived"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCountingWire(`{"total":3}`)
			if _, err := countRoleWithSnapshot(t, w, served).Count(t.Context(), tc.req); err != nil {
				t.Fatalf("Count: %v", err)
			}
			q := w.query(t)
			for param, want := range tc.want {
				if got := q[param]; !slices.Equal(got, want) {
					t.Errorf("query[%q] = %v, want %v (full query: %v)", param, got, want, q)
				}
			}
		})
	}
}

// TestCountScopeRefusesLocallyWhenTheServerLacksTheCapability is S8's other
// half, the skew side: internal/httpapi/routes.go's CLIENT-SKEW NOTE beside
// CapIssuesCountScope requires that a request setting any of the four scope
// fields refuse BEFORE dialing when the handshake does not advertise
// issues.count.scope — never a round trip for a guaranteed 400, and never a
// silent drop that answers a wider count than asked for.
func TestCountScopeRefusesLocallyWhenTheServerLacksTheCapability(t *testing.T) {
	masked := &apigen.ContextResponse{Capabilities: []string{"issues.count"}}

	for _, tc := range []struct {
		name string
		req  issueops.CountRequest
	}{
		{"ParentID", issueops.CountRequest{ParentID: "bd-1"}},
		{"NoParent", issueops.CountRequest{NoParent: true}},
		{"ExcludeTypes", issueops.CountRequest{ExcludeTypes: []string{"wisp"}}},
		{"ExcludeStatus", issueops.CountRequest{ExcludeStatus: []string{"closed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCountingWire(`{"total":0}`)
			_, err := countRoleWithSnapshot(t, w, masked).Count(t.Context(), tc.req)
			if err == nil {
				t.Fatal("Count returned no error, want a pre-dial capability refusal")
			}
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
			}
			if unsup.Capability != wire.CapCountScope {
				t.Errorf("Capability = %q, want %q", unsup.Capability, wire.CapCountScope)
			}
			if unsup.Op != "Counter.Count" {
				t.Errorf("Op = %q, want %q", unsup.Op, "Counter.Count")
			}
			if len(w.dialed) != 0 {
				t.Errorf("dialed %d times, want 0: a pre-dial refusal must never reach the wire", len(w.dialed))
			}
		})

		t.Run(tc.name+"/CountByGroup", func(t *testing.T) {
			w := newCountingWire(`{"total":0,"groups":{}}`)
			counter, err := New(testTarget(t), w, masked).Counter()
			if err != nil {
				t.Fatalf("Counter(): %v", err)
			}
			_, err = counter.CountByGroup(t.Context(), issueops.CountByGroupRequest{
				Filter: tc.req, GroupBy: issueops.CountGroupStatus,
			})
			if err == nil {
				t.Fatal("CountByGroup returned no error, want a pre-dial capability refusal")
			}
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
			}
			if unsup.Capability != wire.CapCountScope {
				t.Errorf("Capability = %q, want %q", unsup.Capability, wire.CapCountScope)
			}
			if len(w.dialed) != 0 {
				t.Errorf("dialed %d times, want 0", len(w.dialed))
			}
		})
	}

	// The converse: a scope-free count against the SAME masked server dials
	// normally. The gate guards the four fields, not the operation — a plain
	// `bd count` must keep working against a server that predates S8.
	t.Run("no scope field set dials normally", func(t *testing.T) {
		w := newCountingWire(`{"total":5}`)
		if _, err := countRoleWithSnapshot(t, w, masked).Count(t.Context(), issueops.CountRequest{Status: "open"}); err != nil {
			t.Fatalf("Count: %v", err)
		}
		if len(w.dialed) != 1 {
			t.Errorf("dialed %d times, want exactly 1", len(w.dialed))
		}
	})
}

// TestCounterRefusesScopeGracefullyWithNoTransportAndNoSnapshot is the 2026-10
// Opus-review LOW-6 finding: Store.snapshot returns (nil, nil) when a Store
// carries neither a wire NOR a cached handshake, and Store.Counter() — unlike
// Sweeper() and BatchApplier() — never calls roleWire first, so a Counter
// built this way is reachable through the ordinary public accessor. Before
// the nil guard in refuseUnservedScope, a scoped count against such a Counter
// paniced on snap.Capabilities rather than refusing.
//
// Passing a nil snapshot through New is the direct, minimal reproduction:
// Store.Counter() builds on whatever New was given, with no roleWire gate to
// go through first.
func TestCounterRefusesScopeGracefullyWithNoTransportAndNoSnapshot(t *testing.T) {
	counter, err := New(testTarget(t), nil, nil).Counter()
	if err != nil {
		t.Fatalf("Counter(): %v", err)
	}

	_, err = counter.Count(t.Context(), issueops.CountRequest{ParentID: "bd-1"})
	if err == nil {
		t.Fatal("Count returned no error, want a graceful capability refusal rather than a panic")
	}
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
	}
	if unsup.Capability != wire.CapCountScope {
		t.Errorf("Capability = %q, want %q", unsup.Capability, wire.CapCountScope)
	}

	_, err = counter.CountByGroup(t.Context(), issueops.CountByGroupRequest{
		Filter: issueops.CountRequest{NoParent: true}, GroupBy: issueops.CountGroupStatus,
	})
	if err == nil {
		t.Fatal("CountByGroup returned no error, want a graceful capability refusal rather than a panic")
	}
	if !errors.As(err, &unsup) {
		t.Fatalf("errors.As to *storage.ErrUnsupported failed for %v", err)
	}
	if unsup.Capability != wire.CapCountScope {
		t.Errorf("Capability = %q, want %q", unsup.Capability, wire.CapCountScope)
	}
}
