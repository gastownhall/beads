// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wave2b_roles_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of client wave 2b: the three roles' encoders and decoders,
// driven against a stub transport.
//
// WHAT THIS TIER IS FOR, given that served_*_test.go runs the whole conformance
// contract against a real server: the contracts assert what a BACKEND promises,
// and several of the decisions in this wave are about what this client SENDS or
// how it reads one member back — facts a contract cannot see because a correct
// server produces the same answer either way. The revision stitch is the sharp
// example: a client that dropped `revision` and left Issue.RowVersion at zero
// would fail no contract case that does not read the token back, and the failure
// would surface much later as a precondition_failed on somebody's NEXT request.

func releaserRole(t *testing.T, w *stubWire) issueops.Releaser {
	t.Helper()
	role, err := stubStore(t, w).Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	return role
}

func commenterRole(t *testing.T, w *stubWire) issueops.Commenter {
	t.Helper()
	role, err := stubStore(t, w).Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	return role
}

// relationsRole binds the neighbor read against the READ double rather than the
// write one: it is the only role of this wave that dispatches through the
// store's generic door, which is exactly what recordingWire records.
func relationsRole(t *testing.T) (issueops.Relations, *recordingWire) {
	t.Helper()
	store, w := recordingStore(t)
	role, err := store.IssueRelations()
	if err != nil {
		t.Fatalf("IssueRelations(): %v", err)
	}
	return role, w
}

// TestReleaseStitchesTheRevisionOntoTheRowItAnswersWith is the revision
// doctrine, pinned where it can fail.
//
// types.Issue.RowVersion is `json:"-"`, so the row the server sends carries NO
// token and the operation publishes it as a sibling member. The role's own leaf
// says the post-release token rides on ReleaseResult.Issue.RowVersion and that a
// second spelling of one token is how two spellings come to disagree — so the
// client has exactly one correct move, and both wrong ones are silent: dropping
// the member leaves a zero that reads as a real token (migration 0054 backfilled
// rows holding 0), and putting it in a field of its own leaves the documented
// compose-and-continue loop reading the wrong one.
//
// The value is deliberately past 2^53, which is the second half of the doctrine:
// an IEEE-754 double's ulp is already 64 up there, so a decode through a float
// anywhere on this path answers a number NEAR the token that is not it — which
// is why the wire spells it as a decimal STRING (types.RevisionToken) and this
// client parses it back with types.ParseRevisionToken.
func TestReleaseStitchesTheRevisionOntoTheRowItAnswersWith(t *testing.T) {
	const token int64 = 900719925474099234
	w := &stubWire{released: &apigen.ReleaseIssueResponse{
		Issue:    apigen.Issue{ID: "bd-1", Status: types.StatusOpen},
		Changed:  true,
		Revision: types.RevisionToken(token),
	}}

	res, err := releaserRole(t, w).Release(context.Background(), issueops.ReleaseRequest{
		Actor: "holder", IssueID: "bd-1",
	})
	if err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if res.Issue == nil {
		t.Fatal("Release() answered no row")
	}
	if res.Issue.RowVersion != token {
		t.Errorf("Issue.RowVersion = %d, want %d: the post-release token rides on the row and nowhere else",
			res.Issue.RowVersion, token)
	}
	if !res.Changed {
		t.Error("Changed = false on a 200; every answer this operation returns wrote the row")
	}
}

// TestReleasePostStateIsAnonymous is the Changed ruling, read off what the
// client does NOT do.
//
// A release leaves assignee cleared, status open and started_at gone — the same
// row whoever emptied it — so there is nothing on the post-state for a client to
// dispatch on, and any attempt to synthesize "was this mine" would be inventing
// a fact the wire never sent. This asserts the row is passed through as the
// server wrote it: no actor stamped back onto the assignee, no status rewritten,
// and the answer is a COPY rather than a window onto the response body.
func TestReleasePostStateIsAnonymous(t *testing.T) {
	body := &apigen.ReleaseIssueResponse{
		Issue:    apigen.Issue{ID: "bd-1", Status: types.StatusOpen, Assignee: ""},
		Changed:  true,
		Revision: "0",
	}
	w := &stubWire{released: body}

	res, err := releaserRole(t, w).Release(context.Background(), issueops.ReleaseRequest{
		Actor: "releaser-supervisor", IssueID: "bd-1", Force: true,
	})
	if err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if res.Issue.Assignee != "" {
		t.Errorf("Assignee = %q on a released row, want empty: the post-state names nobody, and an actor stamped back on would name the releaser",
			res.Issue.Assignee)
	}
	if res.Issue.Status != types.StatusOpen {
		t.Errorf("Status = %q, want %q", res.Issue.Status, types.StatusOpen)
	}
	// The copy, asserted by mutating the answer and re-reading the body: a
	// result aliasing the decoded response is a window a caller can write
	// through.
	res.Issue.ID = "mutated"
	if body.Issue.ID != "bd-1" {
		t.Error("the answer aliases the decoded response body; a caller writing to the row it was handed edits the transport's buffer")
	}
}

// TestReleaseSendsTheGuardUntrimmedAndForceOnlyWhenAsked pins the request half.
//
// THE EXPECTATION IS NOT TRIMMED, which is the role's own rule and the one a
// helpful client breaks: the comparison forgives separator runs and NOTHING
// else, so a padded expectation has to lose EVERY time rather than
// intermittently. A client that trimmed would turn a deterministic refusal into
// a release that lands — the exact opposite outcome, on a compare-and-set.
//
// FORCE TRAVELS ONLY WHEN TRUE, so a request that asked for neither conflict
// never puts both members on the wire at once.
func TestReleaseSendsTheGuardUntrimmedAndForceOnlyWhenAsked(t *testing.T) {
	padded := " holder"
	w := &stubWire{}
	if _, err := releaserRole(t, w).Release(context.Background(), issueops.ReleaseRequest{
		Actor: "supervisor", IssueID: "bd-1", ExpectedAssignee: &padded,
	}); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if w.lastRelease.ExpectedAssignee == nil {
		t.Fatal("the guard did not travel")
	}
	if got := *w.lastRelease.ExpectedAssignee; got != padded {
		t.Errorf("expected_assignee = %q, want %q verbatim: trimming turns a refusal into a release", got, padded)
	}
	if w.lastRelease.Force != nil {
		t.Errorf("force = %v beside a guard, want absent", *w.lastRelease.Force)
	}

	w = &stubWire{}
	if _, err := releaserRole(t, w).Release(context.Background(), issueops.ReleaseRequest{
		Actor: "reaper", IssueID: "bd-1", Force: true,
	}); err != nil {
		t.Fatalf("Release(force): %v", err)
	}
	if w.lastRelease.Force == nil || !*w.lastRelease.Force {
		t.Errorf("force = %v, want true", w.lastRelease.Force)
	}
	if w.lastRelease.ExpectedAssignee != nil {
		t.Errorf("expected_assignee = %q beside force, want absent", *w.lastRelease.ExpectedAssignee)
	}
}

// TestReleaseLeavesTheCallersExpectationAlone is the no-mutation promise at the
// one member a callee could write through.
func TestReleaseLeavesTheCallersExpectationAlone(t *testing.T) {
	expected := "holder"
	req := issueops.ReleaseRequest{Actor: "supervisor", IssueID: "bd-1", ExpectedAssignee: &expected}
	snapshot := req

	w := &stubWire{}
	if _, err := releaserRole(t, w).Release(context.Background(), req); err != nil {
		t.Fatalf("Release(): %v", err)
	}
	if expected != "holder" {
		t.Errorf("the caller's expectation changed across the call: %q", expected)
	}
	if req != snapshot {
		t.Errorf("the caller's request changed across the call: %+v, want %+v", req, snapshot)
	}
}

// TestReleaseRefusesBeforeTheDial is the seam's standing rule on this role.
//
// Every one of these is ALSO refused at the server's edge, so a client that
// skipped the check would still answer ErrValidation — from a problem body, one
// round trip later, and bound to a spelling a future server release may change.
// The assertion that makes the case real is therefore `calls`: the dial did not
// happen.
func TestReleaseRefusesBeforeTheDial(t *testing.T) {
	empty := ""
	holder := "holder"

	for name, req := range map[string]issueops.ReleaseRequest{
		"no actor":                          {IssueID: "bd-1"},
		"blank actor":                       {Actor: "   ", IssueID: "bd-1"},
		"no issue id":                       {Actor: "holder"},
		"empty expected assignee":           {Actor: "holder", IssueID: "bd-1", ExpectedAssignee: &empty},
		"force beside an expected assignee": {Actor: "holder", IssueID: "bd-1", ExpectedAssignee: &holder, Force: true},
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{}
			_, err := releaserRole(t, w).Release(context.Background(), req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("Release(%s) error = %v, want ErrValidation", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("a request the role calls invalid reached the server: %v", w.calls)
			}
		})
	}
}

// TestReleaseValidationMatchesTheSharedValidator binds the restated rules to
// internal/workapi's own, which is the answer to readyclaimer.go's warning that
// a restated rule is how a leg drifts.
//
// The import is legal HERE and nowhere else in this package: depguard denies
// internal/workapi to the client's non-test files, because a client that can
// build a filter is a client whose narrowing no server-side gate can observe. A
// test has no such power, so this is where the two definitions can be held to
// each other — the same arrangement TestDefaultListLimitMatchesTheSharedDefault
// uses for the list default.
//
// It compares VERDICTS over a table that covers both sides of every rule, which
// is what makes a drift in either direction fail: a rule dropped here, and a
// rule added upstream that this copy does not have.
func TestReleaseValidationMatchesTheSharedValidator(t *testing.T) {
	empty := ""
	blank := "   "
	holder := "holder"
	padded := " holder"

	for name, req := range map[string]issueops.ReleaseRequest{
		"valid unconditional": {Actor: "holder", IssueID: "bd-1"},
		"valid forced":        {Actor: "reaper", IssueID: "bd-1", Force: true},
		"valid conditional":   {Actor: "supervisor", IssueID: "bd-1", ExpectedAssignee: &holder},
		"valid padded expectation": {
			Actor: "supervisor", IssueID: "bd-1", ExpectedAssignee: &padded,
		},
		"no actor":          {IssueID: "bd-1"},
		"blank actor":       {Actor: blank, IssueID: "bd-1"},
		"no issue id":       {Actor: "holder"},
		"blank issue id":    {Actor: "holder", IssueID: blank},
		"empty expectation": {Actor: "holder", IssueID: "bd-1", ExpectedAssignee: &empty},
		"blank expectation": {Actor: "holder", IssueID: "bd-1", ExpectedAssignee: &blank},
		"force beside a guard": {
			Actor: "holder", IssueID: "bd-1", ExpectedAssignee: &holder, Force: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			mine := validateReleaseRequest(req) != nil
			shared := workapi.ValidateReleaseRequest(req) != nil
			if mine != shared {
				t.Errorf("validateReleaseRequest refuses = %v, workapi.ValidateReleaseRequest refuses = %v: "+
					"the client's restatement has drifted from the rule every other Releaser runs", mine, shared)
			}
		})
	}
}

// TestReleaseSentinelsTravelWhole is the transcription clause, and it is here
// because the value of this accessor to a downstream adapter is entirely in
// which refusal it gets.
//
// gc's ReleaseIfCurrent lane maps FOUR of these onto a quiet (false, nil) — an
// id that names nothing, a row holding no claim, a status that will not accept a
// release, and a guard that missed — and keeps the rest as errors. That mapping
// is only possible while the sentinels arrive distinguishable, so this pins that
// the role adds no swallowing of its own: every refusal the transport produced
// comes back matching the sentinel it arrived as.
//
// ErrNotClaimed is deliberately absent from the table and that absence is
// ledgered, not forgotten: the wire spells it and ErrNotReleasable with one
// code, so it cannot arrive over http at all (L-release-notclaimed).
func TestReleaseSentinelsTravelWhole(t *testing.T) {
	for name, sentinel := range map[string]error{
		"absent id":         issueops.ErrNotFound,
		"not releasable":    issueops.ErrNotReleasable,
		"guard missed":      issueops.ErrAssigneeMismatch,
		"foreign holder":    issueops.ErrAlreadyClaimed,
		"server validation": issueops.ErrValidation,
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{errs: []error{sentinel}}
			res, err := releaserRole(t, w).Release(context.Background(), issueops.ReleaseRequest{
				Actor: "holder", IssueID: "bd-1",
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("Release() error = %v, want %v to survive the role", err, sentinel)
			}
			if res.Issue != nil || res.Changed {
				t.Errorf("a refused release answered %+v, want the zero result", res)
			}
		})
	}
}

// TestCommentSendsTheAuthorVerbatim pins the caller-asserted half.
//
// The author is not the authenticated principal and not a configured actor: it
// lands IN the row and is read back by everyone who sees the thread. So the
// client applies no default and no normalization — a client that substituted the
// workspace's actor, or trimmed, would publish a claim the caller did not make
// under a name the caller did not choose.
func TestCommentSendsTheAuthorVerbatim(t *testing.T) {
	const author = "  Ada Lovelace  "
	const text = "\n  a body that keeps its own whitespace  \n"
	w := &stubWire{commented: &types.Comment{ID: "c-7", Author: author, Text: text}}

	res, err := commenterRole(t, w).AddComment(context.Background(), issueops.AddCommentRequest{
		Author: author, IssueID: "bd-1", Text: text,
	})
	if err != nil {
		t.Fatalf("AddComment(): %v", err)
	}
	if w.lastComment.Author != author {
		t.Errorf("author = %q, want %q verbatim", w.lastComment.Author, author)
	}
	if w.lastComment.Text != text {
		t.Errorf("text = %q, want %q verbatim: the column is a document and nothing here trims it", w.lastComment.Text, text)
	}
	if want := []string{"addComment:bd-1"}; !reflect.DeepEqual(w.calls, want) {
		t.Errorf("calls = %v, want %v", w.calls, want)
	}
	if res.Comment == nil || res.Comment.ID != "c-7" {
		t.Errorf("Comment = %+v, want the stored row the server answered with", res.Comment)
	}
}

// TestCommentAnswersTheStoredRowAsACopy: the result is the whole payload of this
// role, and it must not be a window onto a decoded response body — a caller
// keeps CreatedAt as a comment-page cursor.
func TestCommentAnswersTheStoredRowAsACopy(t *testing.T) {
	body := &types.Comment{ID: "c-7", Author: "ada", Text: "t"}
	w := &stubWire{commented: body}

	res, err := commenterRole(t, w).AddComment(context.Background(), issueops.AddCommentRequest{
		Author: "ada", IssueID: "bd-1", Text: "t",
	})
	if err != nil {
		t.Fatalf("AddComment(): %v", err)
	}
	res.Comment.Author = "mutated"
	if body.Author != "ada" {
		t.Error("the answer aliases the decoded response body")
	}
}

// TestCommentRefusesABodilessSuccess covers the one answer this role cannot
// pass through: a 200 with no comment in it.
//
// Reading that as a success would return a nil *Comment through a result whose
// only member is that pointer, moving the panic to whichever caller
// dereferenced it — and the two facts the role promises, the minted id and the
// stored created_at, would simply be absent.
func TestCommentRefusesABodilessSuccess(t *testing.T) {
	w := &stubWire{commented: nil}
	_, err := commenterRole(t, w).AddComment(context.Background(), issueops.AddCommentRequest{
		Author: "ada", IssueID: "bd-1", Text: "t",
	})
	if err == nil {
		t.Fatal("a 200 carrying no comment was read as a success")
	}
	if !strings.Contains(err.Error(), "no body") {
		t.Errorf("error = %v, want it to name the absent body", err)
	}
}

// TestCommentRefusesBeforeTheDial is the pre-dial rule on this role. All three
// refusals come from the SHARED validator, so this also pins that the client
// runs it rather than a restatement.
func TestCommentRefusesBeforeTheDial(t *testing.T) {
	for name, req := range map[string]issueops.AddCommentRequest{
		"no author":   {IssueID: "bd-1", Text: "t"},
		"no issue id": {Author: "ada", Text: "t"},
		"blank text":  {Author: "ada", IssueID: "bd-1", Text: "   \n  "},
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{}
			_, err := commenterRole(t, w).AddComment(context.Background(), req)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("AddComment(%s) error = %v, want ErrValidation", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("a request the role calls invalid reached the server: %v", w.calls)
			}
		})
	}
}

// TestRelationDirectionsMatchTheGeneratedEnum is the gotcha's gate.
//
// apigen carries TWO [out, in] enums — this operation's and
// countDependencyEdges' — and the arrival of the second renamed the bare In/Out
// constants a first draft would reach for. Both spell the same values today, so
// a cross-wired reference compiles and passes; what it would not survive is
// either enum's values changing, and by then the symptom is an inverse graph
// with the same shape and the same member names.
func TestRelationDirectionsMatchTheGeneratedEnum(t *testing.T) {
	for direction, want := range map[issueops.RelationDirection]apigen.ListRelatedIssuesParamsDirection{
		issueops.RelationOut: apigen.ListRelatedIssuesParamsDirectionOut,
		issueops.RelationIn:  apigen.ListRelatedIssuesParamsDirectionIn,
	} {
		if got := relationDirection(direction); got != string(want) {
			t.Errorf("relationDirection(%q) = %q, want %q", direction, got, want)
		}
		if !want.Valid() {
			t.Errorf("%q is not a member of ListRelatedIssuesParamsDirection", want)
		}
	}
	// The sibling enum is named here on purpose: this is the assertion that goes
	// red the day the two stop agreeing, which is the moment a cross-wired
	// constant stops being invisible. It is a Log rather than an Error because a
	// divergence between the two enums is upstream's to make — what this test
	// FAILS on is this client sending the wrong one.
	if string(apigen.ListRelatedIssuesParamsDirectionOut) != string(apigen.CountDependencyEdgesParamsDirectionOut) ||
		string(apigen.ListRelatedIssuesParamsDirectionIn) != string(apigen.CountDependencyEdgesParamsDirectionIn) {
		t.Log("the two direction enums have diverged; relationDirection must keep sending this operation's own constants")
	}
}

// TestRelatedRefusesBeforeTheDial covers the refusal that exists ONLY on this
// side of the wire.
//
// The server bounds the path id before the role is reached, so an empty anchor
// is a 404 there — meaning a client that forwarded one would answer ErrNotFound
// where every other backend answers ErrValidation. The direction and type
// refusals are reachable on both sides; this asserts all three are made here, in
// the shared validator's own order.
func TestRelatedRefusesBeforeTheDial(t *testing.T) {
	long := strings.Repeat("x", types.MaxDependencyTypeLen+1)

	for name, req := range map[string]issueops.RelatedRequest{
		"no anchor":         {Direction: issueops.RelationOut},
		"zero direction":    {ID: "bd-1"},
		"unknown direction": {ID: "bd-1", Direction: issueops.RelationDirection("sideways")},
		"empty type":        {ID: "bd-1", Direction: issueops.RelationOut, Types: []types.DependencyType{""}},
		"oversized type":    {ID: "bd-1", Direction: issueops.RelationOut, Types: []types.DependencyType{types.DependencyType(long)}},
	} {
		t.Run(name, func(t *testing.T) {
			role, w := relationsRole(t)
			if _, err := role.Related(context.Background(), req); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("Related(%s) error = %v, want ErrValidation", name, err)
			}
			if len(w.requests) != 0 {
				t.Errorf("a request the role calls invalid reached the server: %+v", w.requests)
			}
		})
	}
}

// TestRelatedSendsTheAnchorInThePathAndTheFiltersInTheQuery is the request
// shape.
//
// The anchor is a PATH segment, which is what makes this role's miss a 404
// rather than a per-anchor sentinel, and the type filter is REPEATED rather than
// comma-joined — the operation reads it with the repeatable list decoder and
// does no splitting, so a comma would travel into a type name.
func TestRelatedSendsTheAnchorInThePathAndTheFiltersInTheQuery(t *testing.T) {
	role, w := relationsRole(t)
	if _, err := role.Related(context.Background(), issueops.RelatedRequest{
		ID:        "ga-1:x",
		Direction: issueops.RelationIn,
		Types:     []types.DependencyType{types.DepBlocks, "parent-child"},
	}); err != nil {
		t.Fatalf("Related(): %v", err)
	}
	if len(w.requests) != 1 {
		t.Fatalf("dialed %d times, want 1", len(w.requests))
	}
	req := w.requests[0]
	if want := "/v0/beads/issues/ga-1%3Ax/related"; req.Path != want {
		t.Errorf("path = %q, want %q: the anchor is one escaped segment under a literal collection", req.Path, want)
	}
	if req.IssueID != "ga-1:x" {
		t.Errorf("IssueID = %q, want the anchor so a refusal names the row the request named", req.IssueID)
	}
	if got := req.Query.Get("direction"); got != "in" {
		t.Errorf("direction = %q, want %q", got, "in")
	}
	if got, want := req.Query["type"], []string{"blocks", "parent-child"}; !reflect.DeepEqual(got, want) {
		t.Errorf("type = %v, want %v repeated rather than joined", got, want)
	}
}

// TestRelatedAnswersAnEmptyPageWithoutNil is the never-nil promise, and it is
// the one shape a DECODE can produce that the document forbids: `items` is
// published as an empty array and never null, but an absent member unmarshals to
// a nil slice, and a caller ranges over the answer without checking.
func TestRelatedAnswersAnEmptyPageWithoutNil(t *testing.T) {
	role, _ := relationsRole(t)
	items, err := role.Related(context.Background(), issueops.RelatedRequest{
		ID: "bd-1", Direction: issueops.RelationOut,
	})
	if err != nil {
		t.Fatalf("Related(): %v", err)
	}
	if items == nil {
		t.Fatal("Related() answered nil on a successful call")
	}
	if len(items) != 0 {
		t.Errorf("Related() answered %d rows from an empty body", len(items))
	}
}

// TestRelatedLeavesTheCallersTypesAlone: RelatedRequest travels by value, so
// Types is the one member a body could write through — and reading it into a set
// is fine while sorting or de-duplicating it in place is not.
func TestRelatedLeavesTheCallersTypesAlone(t *testing.T) {
	types_ := []types.DependencyType{"related", types.DepBlocks, "related"}
	snapshot := append([]types.DependencyType(nil), types_...)

	role, _ := relationsRole(t)
	if _, err := role.Related(context.Background(), issueops.RelatedRequest{
		ID: "bd-1", Direction: issueops.RelationOut, Types: types_,
	}); err != nil {
		t.Fatalf("Related(): %v", err)
	}
	if !reflect.DeepEqual(types_, snapshot) {
		t.Errorf("the caller's Types changed across the call: %v, want %v", types_, snapshot)
	}
}
