// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wave2c_claimnext_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of client wave 2c's claimNext: which LEG the role takes, what
// the served one sends, and the one shape of answer a correct server never
// produces and this client must not pass on.
//
// The served-surface tier runs the whole eleven-case contract against the
// operation, so what is left here is what a correct server hides. Two things
// qualify. The DOWN-LEVEL leg cannot be reached through a real bd serve at all
// — the in-process server advertises every implemented operation — so a
// capability-gated fallback that had stopped working would be invisible to every
// test in this tree. And the FILTER travels as a query string, so a member the
// encoder dropped would widen the candidate set silently: the server would
// answer a correct claim for a question nobody asked.

// claimNextStore builds a store whose cached handshake advertises exactly the
// tokens given, which is how a case here chooses the leg under test.
//
// It is the store's SNAPSHOT rather than the wire's handshake because that is
// what the role consults: (*Store).snapshot is the one lazy handshake the store
// owns, and servesClaimNext reads the capability list off it.
func claimNextStore(t *testing.T, w *stubWire, capabilities ...string) *Store {
	t.Helper()
	return New(testTarget(t), w, &apigen.ContextResponse{BdVersion: "1.2.3", Capabilities: capabilities})
}

func claimNextRole(t *testing.T, s *Store) issueops.ReadyClaimer {
	t.Helper()
	role, err := s.ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	return role
}

// claimNextToken is the capability a server advertises for the operation. It is
// read from the client's own vocabulary rather than spelled, so a token renamed
// upstream reaches these cases through the same map the role consults.
func claimNextToken(t *testing.T) string {
	t.Helper()
	token, ok := wire.CapabilityFor(wire.OpClaimNextIssue)
	if !ok {
		t.Fatal("the client's vocabulary has no capability for claimNextIssue")
	}
	return token
}

// TestClaimNextTakesTheOperationWhereTheServerAdvertisesItAndComposesWhereItDoesNot
// is the leg choice, both arms, which is the whole reason the fallback is not
// dead code.
//
// A real bd serve advertises every operation it implements, so the served-surface
// tier can only ever exercise ONE of these two paths. Without this case the
// down-level leg would compile, would be the only thing standing between a
// pre-#5510 server and a broken `bd ready --claim`, and would be exercised by
// nothing at all.
func TestClaimNextTakesTheOperationWhereTheServerAdvertisesItAndComposesWhereItDoesNot(t *testing.T) {
	claimed := &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "ada"}}

	served := &stubWire{claimedNext: &apigen.ClaimNextResponse{Claimed: claimed}}
	res, err := claimNextRole(t, claimNextStore(t, served, claimNextToken(t))).
		ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "ada"})
	if err != nil {
		t.Fatalf("ClaimNext against a server that advertises the operation: %v", err)
	}
	if res.Claimed == nil || res.Claimed.ID != "bd-1" {
		t.Errorf("the served leg answered %v, want the row the operation claimed", res.Claimed)
	}
	if want := []string{"claimNextIssue"}; !equalCalls(served.calls, want) {
		t.Errorf("the served leg called %v, want %v: one operation, no listing beside it", served.calls, want)
	}

	// The SAME request against a server that does not advertise it. The stub's
	// ready page is what the composition walks; the claim answers the row.
	down := &stubWire{
		ready: []*apigen.ReadyPage{{Items: []apigen.IssueWithCounts{{Issue: &types.Issue{ID: "bd-2"}}}}},
		claim: &apigen.ClaimResponse{Issue: types.Issue{ID: "bd-2", Status: types.StatusInProgress, Assignee: "ada"}},
	}
	res, err = claimNextRole(t, claimNextStore(t, down)).
		ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "ada"})
	if err != nil {
		t.Fatalf("ClaimNext against a server too old for the operation: %v", err)
	}
	if res.Claimed == nil || res.Claimed.ID != "bd-2" {
		t.Errorf("the down-level leg answered %v, want the row the composition claimed", res.Claimed)
	}
	if want := []string{"listReadyWork", "claimIssue:bd-2"}; !equalCalls(down.calls, want) {
		t.Errorf("the down-level leg called %v, want %v: the composition L14 describes", down.calls, want)
	}
}

// TestClaimNextSendsTheFilterAsTheReadyQueryAndTheActorAsTheBody is the request,
// member for member.
//
// THE SPLIT IS THE OPERATION'S: the filter is `listReadyWork`'s vocabulary and
// goes through the same server-side decode, so it travels as a query string and
// a body object would be a second expression of one predicate. The actor is
// provenance that lands in a column.
//
// TWO PARAMETERS MUST BE ABSENT and their absence is asserted rather than
// assumed. `limit` is refused BY VALUE on this operation — any value at all is a
// 400, because the scan must stay unbounded — and `brief` has no spelling here,
// because the claim refetches its winner whole. A client that reused the
// listing's encoder verbatim would send both.
func TestClaimNextSendsTheFilterAsTheReadyQueryAndTheActorAsTheBody(t *testing.T) {
	w := &stubWire{claimedNext: &apigen.ClaimNextResponse{}}
	role := claimNextRole(t, claimNextStore(t, w, claimNextToken(t)))

	if _, err := role.ClaimNext(context.Background(), issueops.ClaimNextRequest{
		Actor: "ada",
		Filter: issueops.ReadyRequest{
			Labels:           []string{"lane", "urgent"},
			LabelsAny:        []string{"a", "b"},
			ExcludeLabels:    []string{"held"},
			IssueType:        "task",
			ParentID:         "bd-parent",
			IncludeEphemeral: true,
			Sort:             "oldest",
		},
	}); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}

	q := w.lastClaimNextParams
	for param, want := range map[string][]string{
		"label":             {"lane", "urgent"},
		"label_any":         {"a", "b"},
		"exclude_label":     {"held"},
		"type":              {"task"},
		"parent":            {"bd-parent"},
		"include_ephemeral": {"true"},
		"sort":              {"oldest"},
	} {
		if got := q[param]; !equalCalls(got, want) {
			t.Errorf("the claim sent %s=%v, want %v", param, got, want)
		}
	}
	for _, absent := range []string{"limit", "brief"} {
		if _, present := q[absent]; present {
			t.Errorf("the claim sent %s=%v; the operation refuses a limit by value and publishes no projection at all",
				absent, q[absent])
		}
	}
	if w.lastClaimNextBody.Actor != "ada" {
		t.Errorf("the claim body carried actor %q, want %q", w.lastClaimNextBody.Actor, "ada")
	}
}

// TestClaimNextSendsTheSortPolicyEvenWhenTheRequestNamesNone is the one filter
// member whose default decides which row is WRITTEN.
//
// An absent `sort` is `priority` to the handler and `hybrid` to the storage
// layer, and those two answer with different rows. The listing encoder sends the
// concrete policy an empty one means for exactly that reason, and a claim has
// more at stake than a listing does: the listing would print a different order,
// this mutates a different issue.
func TestClaimNextSendsTheSortPolicyEvenWhenTheRequestNamesNone(t *testing.T) {
	w := &stubWire{claimedNext: &apigen.ClaimNextResponse{}}
	role := claimNextRole(t, claimNextStore(t, w, claimNextToken(t)))
	if _, err := role.ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "ada"}); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if got := w.lastClaimNextParams.Get("sort"); got != "hybrid" {
		t.Errorf("the claim sent sort=%q for a request naming none, want %q: an absent parameter is the "+
			"handler's `priority` and the storage layer's `hybrid`, which claim different rows", got, "hybrid")
	}
}

// TestClaimNextRefusesAClaimCarryingNoRow is the served leg's one trust rule.
//
// types.IssueWithCounts embeds the row as a POINTER, so an object carrying the
// cardinalities and none of the issue's own members decodes to a non-nil claim
// whose Issue is nil — a shape nothing on the wire tells apart from a claim that
// landed. Passing it through is not a wrong answer but a PANIC in the caller:
// `bd ready --claim` dereferences the claimed row's ID with no nil check.
//
// THE ABSENT CLAIM IS THE CONTROL beside it, and it is the case that keeps the
// guard from being written as "refuse anything falsy": a drained front is a 200
// whose body is `{}`, which is the role's nil-Claimed nil-error answer and must
// stay one.
func TestClaimNextRefusesAClaimCarryingNoRow(t *testing.T) {
	rowless := &stubWire{claimedNext: &apigen.ClaimNextResponse{
		Claimed: &types.IssueWithCounts{DependencyCount: 2, CommentCount: 1},
	}}
	_, err := claimNextRole(t, claimNextStore(t, rowless, claimNextToken(t))).
		ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "ada"})
	if err == nil {
		t.Fatal("a claim carrying no issue was passed through; the caller dereferences it with no nil check")
	}
	if !strings.Contains(err.Error(), "no issue") {
		t.Errorf("the refusal reads %q, want it to name the missing row", err)
	}

	drained := &stubWire{claimedNext: &apigen.ClaimNextResponse{}}
	res, err := claimNextRole(t, claimNextStore(t, drained, claimNextToken(t))).
		ClaimNext(context.Background(), issueops.ClaimNextRequest{Actor: "ada"})
	if err != nil || res.Claimed != nil {
		t.Errorf("a drained front answered (%v, %v), want (nil, nil): an absent claim is the steady state of "+
			"a drained queue, not a broken server", res.Claimed, err)
	}
}

// TestClaimNextValidationRunsBeforeTheLegIsChosen pins that both legs refuse the
// same set, and that neither spends a round trip doing it.
//
// The rules are the shared validator's — Limit, Offset, Brief and an empty actor
// — and running them before the capability check is what makes them the ROLE's
// answer rather than a property of whichever server the caller happens to be
// pointed at. A validation refusal that reached the handshake would also mean a
// caller with no server at all could not be told its request was invalid.
func TestClaimNextValidationRunsBeforeTheLegIsChosen(t *testing.T) {
	limit := 5
	for _, tc := range []struct {
		name string
		req  issueops.ClaimNextRequest
	}{
		{"an empty actor", issueops.ClaimNextRequest{}},
		{"a limit", issueops.ClaimNextRequest{Actor: "ada", Filter: issueops.ReadyRequest{Limit: &limit}}},
		{"an offset", issueops.ClaimNextRequest{Actor: "ada", Filter: issueops.ReadyRequest{Offset: 3}}},
		{"a projection", issueops.ClaimNextRequest{Actor: "ada", Filter: issueops.ReadyRequest{Brief: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, leg := range []struct {
				name  string
				token []string
			}{{"served", []string{claimNextToken(t)}}, {"down-level", nil}} {
				w := &stubWire{claimedNext: &apigen.ClaimNextResponse{}}
				_, err := claimNextRole(t, claimNextStore(t, w, leg.token...)).ClaimNext(context.Background(), tc.req)
				if !errors.Is(err, issueops.ErrValidation) {
					t.Errorf("%s leg: refusal = %v, want ErrValidation", leg.name, err)
				}
				if len(w.calls) != 0 {
					t.Errorf("%s leg: the refused request still dialed %v", leg.name, w.calls)
				}
			}
		})
	}
}

// equalCalls compares two string slices element for element.
func equalCalls(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestUpdateSendsTheWholeOrderedLabelEdit is what replaced W-IssuePatch.Labels'
// refusal pin, and the shape of the replacement is the point: a retired refusal
// invites exactly one failure — a member that stopped being refused and never
// started being sent — which no negative assertion can see.
//
// THE PATCH IS A DOCUMENT, so the compiler checks nothing about these three
// names. A typo would produce a 400 from a live server and pass every test that
// does not dial one; this reads the document the encoder built.
//
// EACH ARM IS DRIVEN ALONE as well as together, because the three are one field
// on the role and an encoder that assembled them into a single member would pass
// a combined case while dropping two of them.
func TestUpdateSendsTheWholeOrderedLabelEdit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch issueops.LabelPatch
		want  map[string][]string
	}{
		{"an add alone", issueops.LabelPatch{Add: []string{"lane", "urgent"}},
			map[string][]string{"add_labels": {"lane", "urgent"}}},
		{"a remove alone", issueops.LabelPatch{Remove: []string{"held"}},
			map[string][]string{"remove_labels": {"held"}}},
		{"a replace alone", issueops.LabelPatch{Replace: issueops.Field[[]string]{Set: true, Value: []string{"only"}}},
			map[string][]string{"labels": {"only"}}},
		{"the clear", issueops.LabelPatch{Replace: issueops.Field[[]string]{Set: true}},
			map[string][]string{"labels": {}}},
		{"all three together", issueops.LabelPatch{
			Replace: issueops.Field[[]string]{Set: true, Value: []string{"base"}},
			Add:     []string{"added"},
			Remove:  []string{"base"},
		}, map[string][]string{"labels": {"base"}, "add_labels": {"added"}, "remove_labels": {"base"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document, err := encodeIssuePatch(issueops.IssuePatch{Labels: tc.patch})
			if err != nil {
				t.Fatalf("encodeIssuePatch: %v", err)
			}
			for member, want := range tc.want {
				got, present := document[member]
				if !present {
					t.Fatalf("the document carries no %q; it carries %v", member, sortedDocumentMembers(document))
				}
				values, ok := got.([]string)
				if !ok {
					t.Fatalf("%q is %T, want []string", member, got)
				}
				if !equalCalls(values, want) {
					t.Errorf("%q = %v, want %v", member, values, want)
				}
			}
			for member := range document {
				if _, wanted := tc.want[member]; !wanted {
					t.Errorf("the document carries %q, which this edit did not ask for: an empty add or remove "+
						"is one value with nil on the role, so sending it would put a member on the wire that says nothing", member)
				}
			}
		})
	}
}

// TestUpdateSendsNoLabelMemberForAnEmptyEdit is the other half of the emission
// rule, and it is separate because it is about a patch that is EMPTY.
//
// A LabelPatch carrying non-nil but empty slices expresses no edit — Add and
// Remove are bare slices on the role, so nil and empty are one value there — and
// emitting them would turn a patch the server refuses as empty into one it
// accepts and applies as nothing.
func TestUpdateSendsNoLabelMemberForAnEmptyEdit(t *testing.T) {
	document, err := encodeIssuePatch(issueops.IssuePatch{
		Labels: issueops.LabelPatch{Add: []string{}, Remove: []string{}},
	})
	if err != nil {
		t.Fatalf("encodeIssuePatch: %v", err)
	}
	if len(document) != 0 {
		t.Errorf("an edit carrying two empty slices encoded %v, want nothing", sortedDocumentMembers(document))
	}
}

// sortedDocumentMembers names what a patch document carries, for a failure that
// has to say what WAS there.
func sortedDocumentMembers(document map[string]any) []string {
	names := make([]string, 0, len(document))
	for name := range document {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
