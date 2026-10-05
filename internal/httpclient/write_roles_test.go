// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/write_roles_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
)

// TestTheWireClientSatisfiesTheWriteSeam is the only compile-time proof that the
// interface the store declares and the methods the transport grew are the same
// set.
//
// Production never makes this assertion: the store is deliberately transport-
// free — a build links one through RegisterWireDialer — so nothing in the
// dependency graph forces the two to agree. A signature that drifted would
// otherwise surface as a failed type assertion at the first dial, in the
// activation bead's code, with neither side's author present.
func TestTheWireClientSatisfiesTheWriteSeam(t *testing.T) {
	var _ WriteWire = (*wire.Client)(nil)
}

// stubWire answers the write surface from canned values and records what it was
// asked. Every method that a test does not arm answers a zero value, which is
// what makes "this role dialed something it should have refused" visible: the
// recorded call list is empty on a correct refusal.
type stubWire struct {
	res *apigen.ContextResponse

	calls []string

	claim      *apigen.ClaimResponse
	close      *apigen.CloseIssueResponse
	batchClose *apigen.BatchCloseResponse
	reopen     *apigen.ReopenIssueResponse
	update     *apigen.UpdateIssueResponse
	added      *apigen.AddDependenciesResponse
	removed    *apigen.RemoveDependencyResponse
	remember   *apigen.RememberedMemory
	memory     *apigen.Memory
	page       *apigen.MemoriesPage
	ready      []*apigen.ReadyPage
	swept      *apigen.SweepResult
	deleted    *apigen.DeleteIssuesResult
	created    *apigen.BatchCreateResponse
	created1   *apigen.Issue
	cas        *apigen.CompareAndSetMetadataResponse
	applied    *apigen.ApplyBatchResponse
	released   *apigen.ReleaseIssueResponse
	// commented is the appended comment the stub answers with. A nil one is
	// answered as a nil BODY rather than as a synthesized row, unlike the
	// created/applied defaults above: "the server answered 200 with no comment"
	// is a state the Commenter role has to have an answer for, and a stub that
	// invented a row could not drive it.
	commented *apigen.Comment
	// claimedNext is the served claimNext answer. A nil one is answered as a nil
	// BODY, for commented's reason.
	claimedNext *apigen.ClaimNextResponse

	// err is returned by the next operation of any kind, then cleared, so a
	// test can arm one refusal in the middle of a loop.
	errs []error

	// lastPatch is the document the update role built, which is the one thing
	// about that role no result can show.
	lastClaimNextParams url.Values
	lastClaimNextBody   apigen.ClaimNextRequest

	lastPatch       map[string]any
	lastGuards      wire.UpdateGuards
	lastClose       apigen.CloseIssueRequest
	lastReopen      apigen.ReopenIssueRequest
	lastBatchClose  apigen.BatchCloseRequest
	lastAdd         apigen.AddDependenciesRequest
	lastSearch      string
	lastSweep       apigen.SweepRequest
	lastDelete      apigen.DeleteIssuesRequest
	lastBatchCreate apigen.BatchCreateRequest
	lastCreate      apigen.CreateIssueRequest
	lastCAS         apigen.CompareAndSetMetadataRequest
	lastApply       wire.ApplyBatchRequest
	lastRelease     apigen.ReleaseIssueRequest
	lastComment     apigen.AddCommentRequest
}

func (s *stubWire) ServerContext(context.Context) (*apigen.ContextResponse, error) {
	if s.res == nil {
		return &apigen.ContextResponse{}, nil
	}
	return s.res, nil
}

// The read half of the seam, which the WRITE roles never touch: they dispatch
// through operation-shaped methods above, not through the generic pair. Both
// fail loudly rather than answering, so a write role that started routing
// through the read path would say so instead of quietly recording no call.
func (s *stubWire) Preflight(_ context.Context, op string) error {
	return fmt.Errorf("stubWire: the write seam does not pre-flight %q", op)
}

func (s *stubWire) Do(_ context.Context, req wire.Request, _ any) error {
	return fmt.Errorf("stubWire: the write seam does not dispatch %q generically", req.Op)
}

// WatchEvents is the stream half of the seam, which the write roles never touch.
// It fails loudly rather than answering, so a write role that started streaming
// would say so instead of quietly opening nothing.
func (s *stubWire) WatchEvents(_ context.Context, since int64) (*wire.EventStream, error) {
	return nil, fmt.Errorf("stubWire: the write seam does not watch (since=%d)", since)
}

func (s *stubWire) record(op string) error {
	s.calls = append(s.calls, op)
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return err
	}
	return nil
}

func (s *stubWire) ClaimIssue(_ context.Context, id string, _ apigen.ClaimRequest) (*apigen.ClaimResponse, error) {
	if err := s.record("claimIssue:" + id); err != nil {
		return nil, err
	}
	return s.claim, nil
}

// ClaimNextIssue answers the prepared claim and records the QUERY, which is the
// half of this operation no result can show: the filter travels as a query
// string, so what the client asked for is only visible here.
//
// A nil claimedNext is answered as a nil BODY rather than as a synthesized
// response, like commented above: "the server answered 200 with nothing" is a
// state this role has to have an answer for.
func (s *stubWire) ClaimNextIssue(_ context.Context, params url.Values, body apigen.ClaimNextRequest) (*apigen.ClaimNextResponse, error) {
	s.lastClaimNextParams = params
	s.lastClaimNextBody = body
	if err := s.record("claimNextIssue"); err != nil {
		return nil, err
	}
	return s.claimedNext, nil
}

func (s *stubWire) CloseIssue(_ context.Context, id string, body apigen.CloseIssueRequest) (*apigen.CloseIssueResponse, error) {
	s.lastClose = body
	if err := s.record("closeIssue:" + id); err != nil {
		return nil, err
	}
	return s.close, nil
}

func (s *stubWire) BatchCloseIssues(_ context.Context, body apigen.BatchCloseRequest) (*apigen.BatchCloseResponse, error) {
	s.lastBatchClose = body
	if err := s.record("batchCloseIssues"); err != nil {
		return nil, err
	}
	return s.batchClose, nil
}

func (s *stubWire) ReopenIssue(_ context.Context, id string, body apigen.ReopenIssueRequest) (*apigen.ReopenIssueResponse, error) {
	s.lastReopen = body
	if err := s.record("reopenIssue:" + id); err != nil {
		return nil, err
	}
	return s.reopen, nil
}

func (s *stubWire) UpdateIssue(_ context.Context, id, _ string, patch map[string]any, guards wire.UpdateGuards) (*apigen.UpdateIssueResponse, error) {
	s.lastPatch = patch
	s.lastGuards = guards
	if err := s.record("updateIssue:" + id); err != nil {
		return nil, err
	}
	return s.update, nil
}

func (s *stubWire) ReleaseIssue(_ context.Context, id string, body apigen.ReleaseIssueRequest) (*apigen.ReleaseIssueResponse, error) {
	s.lastRelease = body
	if err := s.record("releaseIssue:" + id); err != nil {
		return nil, err
	}
	if s.released == nil {
		// The shape every 200 on this operation has: the post-release row,
		// `changed` true because the role refuses every shape that would not
		// write, and the reminted token BESIDE the row rather than on it —
		// types.Issue.RowVersion is `json:"-"`, so a real decode never carries
		// one and a stub that put it on the issue would hide the stitch.
		return &apigen.ReleaseIssueResponse{
			Issue:    apigen.Issue{ID: id, Status: types.StatusOpen},
			Changed:  true,
			Revision: "0",
		}, nil
	}
	return s.released, nil
}

func (s *stubWire) AddComment(_ context.Context, id string, body apigen.AddCommentRequest) (*apigen.Comment, error) {
	s.lastComment = body
	if err := s.record("addComment:" + id); err != nil {
		return nil, err
	}
	return s.commented, nil
}

func (s *stubWire) CompareAndSetMetadata(_ context.Context, id string, body apigen.CompareAndSetMetadataRequest) (*apigen.CompareAndSetMetadataResponse, error) {
	s.lastCAS = body
	if err := s.record("compareAndSetMetadata:" + id); err != nil {
		return nil, err
	}
	if s.cas == nil {
		return &apigen.CompareAndSetMetadataResponse{}, nil
	}
	return s.cas, nil
}

func (s *stubWire) ApplyBatch(_ context.Context, body wire.ApplyBatchRequest) (*apigen.ApplyBatchResponse, error) {
	s.lastApply = body
	if err := s.record("applyBatch"); err != nil {
		return nil, err
	}
	if s.applied == nil {
		// The shape the operation promises: one result per requested item, in
		// request order, echoing the kind the item carried, with every NAMED
		// create bound in `keys` to the same id its own result carries. A stub
		// that answered a shorter array, or left a declared key unbound, would
		// be exercising the role's own guards rather than whatever the case is
		// about.
		items := make([]apigen.ApplyItemResult, 0, len(body.Items))
		keys := map[string]string{}
		for i, item := range body.Items {
			id := "stub-applied"
			if item.Create != nil && item.Create.Key != nil && *item.Create.Key != "" {
				id = fmt.Sprintf("stub-applied-%d", i)
				keys[*item.Create.Key] = id
			}
			// `revision` is required on every result and is a decimal string
			// (types.RevisionToken); "0" is the legacy token a real row holds.
			items = append(items, apigen.ApplyItemResult{
				Kind: apigen.ApplyItemResultKind(item.Kind), IssueId: id, Changed: true, Revision: "0",
			})
		}
		return &apigen.ApplyBatchResponse{Keys: keys, Items: items}, nil
	}
	return s.applied, nil
}

func (s *stubWire) CreateIssue(_ context.Context, body apigen.CreateIssueRequest) (*apigen.Issue, error) {
	s.lastCreate = body
	if err := s.record("createIssue"); err != nil {
		return nil, err
	}
	if s.created1 == nil {
		// The row as STORED: the id the server minted when the request named
		// none, echoed back the way the operation's own response does.
		id := derefOr(body.Id, "stub-created")
		return &apigen.Issue{ID: id, Title: body.Title}, nil
	}
	return s.created1, nil
}

func derefOr(v *string, fallback string) string {
	if v == nil || *v == "" {
		return fallback
	}
	return *v
}

func (s *stubWire) AddDependencies(_ context.Context, body apigen.AddDependenciesRequest) (*apigen.AddDependenciesResponse, error) {
	s.lastAdd = body
	if err := s.record("addDependencies"); err != nil {
		return nil, err
	}
	return s.added, nil
}

func (s *stubWire) RemoveDependency(_ context.Context, _ apigen.RemoveDependencyRequest) (*apigen.RemoveDependencyResponse, error) {
	if err := s.record("removeDependency"); err != nil {
		return nil, err
	}
	return s.removed, nil
}

func (s *stubWire) SweepIssues(_ context.Context, body apigen.SweepRequest) (*apigen.SweepResult, error) {
	s.lastSweep = body
	if err := s.record("sweepIssues"); err != nil {
		return nil, err
	}
	if s.swept == nil {
		return &apigen.SweepResult{}, nil
	}
	return s.swept, nil
}

func (s *stubWire) DeleteIssues(_ context.Context, body apigen.DeleteIssuesRequest) (*apigen.DeleteIssuesResult, error) {
	s.lastDelete = body
	if err := s.record("deleteIssues"); err != nil {
		return nil, err
	}
	if s.deleted == nil {
		return &apigen.DeleteIssuesResult{}, nil
	}
	return s.deleted, nil
}

func (s *stubWire) BatchCreateIssues(_ context.Context, body apigen.BatchCreateRequest) (*apigen.BatchCreateResponse, error) {
	s.lastBatchCreate = body
	if err := s.record("batchCreateIssues"); err != nil {
		return nil, err
	}
	if s.created == nil {
		// One echoed issue per requested item, which is the only answer the
		// role accepts: a length mismatch is a server fault it reports rather
		// than indexes into.
		items := make([]apigen.Issue, len(body.Items))
		for i, item := range body.Items {
			items[i] = types.Issue{ID: fmt.Sprintf("stub-%d", i), Title: item.Title}
		}
		return &apigen.BatchCreateResponse{Items: items}, nil
	}
	return s.created, nil
}

func (s *stubWire) RememberMemory(_ context.Context, _ apigen.RememberRequest) (*apigen.RememberedMemory, error) {
	if err := s.record("rememberMemory"); err != nil {
		return nil, err
	}
	return s.remember, nil
}

func (s *stubWire) RecallMemory(_ context.Context, key string) (*apigen.Memory, error) {
	if err := s.record("getMemory:" + key); err != nil {
		return nil, err
	}
	return s.memory, nil
}

func (s *stubWire) ForgetMemory(_ context.Context, key string) (*apigen.Memory, error) {
	if err := s.record("forgetMemory:" + key); err != nil {
		return nil, err
	}
	return s.memory, nil
}

func (s *stubWire) ListMemories(_ context.Context, search string) (*apigen.MemoriesPage, error) {
	s.lastSearch = search
	if err := s.record("listMemories"); err != nil {
		return nil, err
	}
	return s.page, nil
}

func (s *stubWire) ListReadyWork(_ context.Context, _ url.Values) (*apigen.ReadyPage, error) {
	if err := s.record("listReadyWork"); err != nil {
		return nil, err
	}
	if len(s.ready) == 0 {
		return &apigen.ReadyPage{}, nil
	}
	page := s.ready[0]
	if len(s.ready) > 1 {
		s.ready = s.ready[1:]
	}
	return page, nil
}

func stubStore(t *testing.T, w *stubWire) *Store {
	t.Helper()
	return New(testTarget(t), w, &apigen.ContextResponse{BdVersion: "1.2.3"})
}

func set[T any](v T) issueops.Field[T] { return issueops.Field[T]{Set: true, Value: v} }

// TestUpdateRefusesEveryMemberTheWireExcludes is the refuse-not-drop pin for the
// update path: every UpdateRequest member and every IssuePatch member the wire's
// issuePatchMembers list leaves out fails the request, and — the half that
// matters — fails it WITHOUT dialing. A refusal that reached the server would be
// a request the server answered on the members it did understand.
func TestUpdateRefusesEveryMemberTheWireExcludes(t *testing.T) {
	minutes := 30

	cases := map[string]issueops.UpdateRequest{
		"Claim":                 {Claim: true},
		"ForceAssigneeTransfer": {ForceAssigneeTransfer: true},
		"ForceClosePolicy":      {ForceClosePolicy: true},
		"IssuePlaneOnly":        {IssuePlaneOnly: true},
		"Provenance":            {Provenance: "bd: update"},

		"Patch.Owner":           {Patch: issueops.IssuePatch{Owner: set("someone")}},
		"Patch.ClosedBySession": {Patch: issueops.IssuePatch{ClosedBySession: set("s1")}},
		"Patch.SpecID":          {Patch: issueops.IssuePatch{SpecID: set("spec")}},
		"Patch.AwaitID":         {Patch: issueops.IssuePatch{AwaitID: set("await")}},
		"Patch.Persistence":     {Patch: issueops.IssuePatch{Persistence: set(issueops.PersistenceModeEphemeral)}},

		// Patch.Labels.Add and Patch.Labels.Remove WERE here, and their absence
		// is the flip rather than an oversight: upstream #5510 published
		// add_labels and remove_labels, client wave ga-jpywb emits both, and
		// W-IssuePatch.Labels is retired. What replaced these two rows is a
		// positive assertion — TestUpdateSendsTheWholeOrderedLabelEdit — because
		// the failure a retired refusal invites is a member that stopped being
		// refused and never started being sent.
	}

	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}

			// Every case carries a wire-expressible member too, so a refusal
			// cannot be mistaken for the empty-patch validation failure.
			req.Actor, req.IssueID = "writer", "bd-1"
			req.Patch.Title = set("a title")
			req.Patch.EstimatedMinutes = set(&minutes)

			if _, err := lifecycle.Update(t.Context(), req); !errors.Is(err, encode.ErrRefused) {
				t.Fatalf("Update with %s = %v, want a refuse-not-drop refusal", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the refusal dialed %v; a refused member must never reach the server", w.calls)
			}
		})
	}
}

// TestClaimOnlyUpdateRefusesEveryNonClaimMember is claimOnlyUpdate's own
// refuse-not-drop pin, one level below TestUpdateRefusesEveryMemberTheWireExcludes
// above: that test combines Claim with a Patch (the one shape every case in it
// shares, to distinguish the refusal from the empty-patch validation failure),
// which proves Claim+Patch refuses but says nothing about Claim combined with
// any of isClaimOnlyUpdate's other six guarded members individually — each one
// is its own branch of that function's boolean, and a mutation that dropped any
// single one from the check would still pass every existing test here.
//
// issueops.UpdateRequest's own doc comments are explicit that most of these
// guards are ordinarily INDEPENDENT of Claim at the domain level —
// ExpectedVersion "may be combined with Claim" as its own precondition, for
// instance — so this refusal is a property of THIS wire specifically:
// claimIssue's request is `{actor}` alone (apigen.ClaimRequest), so this
// backend cannot express any of these combined with a claim atomically, and
// refuses rather than silently dropping the extra member or serving it as two
// non-atomic calls.
func TestClaimOnlyUpdateRefusesEveryNonClaimMember(t *testing.T) {
	someVersion := int64(7)
	someAssignee := "someone"
	someStatus := issueops.StatusOpen

	cases := map[string]issueops.UpdateRequest{
		"ExpectedVersion":       {ExpectedVersion: &someVersion},
		"ForceClosePolicy":      {ForceClosePolicy: true},
		"IssuePlaneOnly":        {IssuePlaneOnly: true},
		"Provenance":            {Provenance: "bd: update"},
		"ExpectedAssignee":      {ExpectedAssignee: &someAssignee},
		"ExpectedStatus":        {ExpectedStatus: &someStatus},
		"ForceAssigneeTransfer": {ForceAssigneeTransfer: true},
	}

	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{claim: &apigen.ClaimResponse{Issue: apigen.Issue{ID: "bd-1"}}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}

			req.Claim = true
			req.Actor, req.IssueID = "writer", "bd-1"

			_, err = lifecycle.Update(t.Context(), req)
			var refused *encode.RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Update with Claim+%s = %v, want an *encode.RefusedError", name, err)
			}
			if refused.Row.ID != "W-UpdateRequest.Claim" {
				t.Errorf("Update with Claim+%s refused on row %q, want %q", name, refused.Row.ID, "W-UpdateRequest.Claim")
			}
			for _, call := range w.calls {
				if strings.HasPrefix(call, "claimIssue:") {
					t.Errorf("Update with Claim+%s dialed %v; a refused claim must never call claimIssue", name, w.calls)
				}
			}
			if len(w.calls) != 0 {
				t.Errorf("Update with Claim+%s dialed %v; a refused member must never reach the server", name, w.calls)
			}
		})
	}
}

// TestUpdateEncodesPresenceAndTheNullableClears pins the one thing about the
// patch document that a result cannot show: an unset member is absent, a set
// member is present, and a set-but-nil nullable member is a literal null, which
// is the CLEAR. Collapsing the last two — which is what apigen's pointer members
// under `omitempty` would do — turns "clear the due date" into "leave it alone".
func TestUpdateEncodesPresenceAndTheNullableClears(t *testing.T) {
	w := &stubWire{update: &apigen.UpdateIssueResponse{Changed: true, Revision: "0"}}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	due := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	res, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
		Actor: "writer", IssueID: "bd-1",
		Patch: issueops.IssuePatch{
			Title:            set("new title"),
			Priority:         set(1),
			IssueType:        set(types.TypeTask),
			DueAt:            set(&due),
			DeferUntil:       set[*time.Time](nil),
			EstimatedMinutes: set[*int](nil),
			Labels:           issueops.LabelPatch{Replace: set([]string{"x", "y"})},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Changed {
		t.Error("Changed = false, want the server's own answer")
	}

	patch := w.lastPatch
	if got, ok := patch["title"]; !ok || got != "new title" {
		t.Errorf("patch[title] = %v (present %t), want the set value", got, ok)
	}
	if _, ok := patch["description"]; ok {
		t.Error("patch carries description, which was never set — an unset member must be absent")
	}
	for _, member := range []string{"defer_until", "estimated_minutes"} {
		got, ok := patch[member]
		if !ok {
			t.Errorf("patch omits %s, which was set to nil — the clear must reach the server as null", member)
			continue
		}
		if got != nil {
			t.Errorf("patch[%s] = %v, want a literal null", member, got)
		}
	}
	if got := patch["due_at"]; got != due.Format(time.RFC3339Nano) {
		t.Errorf("patch[due_at] = %v, want RFC 3339", got)
	}
	if got, want := patch["labels"], []string{"x", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("patch[labels] = %v, want the replacement set %v", got, want)
	}
}

// TestUpdateRefusesARequestThatWritesNothing keeps an empty patch off the wire:
// the server answers it with a 400 and so would we, and a write that writes
// nothing is a client bug worth naming before it costs a round trip.
func TestUpdateRefusesARequestThatWritesNothing(t *testing.T) {
	w := &stubWire{}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{Actor: "writer", IssueID: "bd-1"})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("empty-patch Update = %v, want ErrValidation", err)
	}
	if len(w.calls) != 0 {
		t.Errorf("the empty patch dialed %v", w.calls)
	}
}

// TestReopenRefusesTheMembersTheWireExcludes is the reopen half of
// refuse-not-drop. Provenance is not in the design's own enumeration — the page
// lists the close half of ExpectedVersion and nothing about Provenance — and it
// is a ledger row for the same reason that one was.
//
// ExpectedVersion left this population when the client learned to send it; its
// round trip is TestReopenSendsTheRowVersionPrecondition below.
func TestReopenRefusesTheMembersTheWireExcludes(t *testing.T) {
	cases := map[string]issueops.ReopenRequest{
		"Provenance": {Provenance: "bd: reopen"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{reopen: &apigen.ReopenIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			req.Actor, req.IssueID = "writer", "bd-1"
			if _, err := lifecycle.Reopen(t.Context(), req); !errors.Is(err, encode.ErrRefused) {
				t.Fatalf("Reopen with %s = %v, want a refuse-not-drop refusal", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the refusal dialed %v", w.calls)
			}
		})
	}
}

// The compare-and-set preconditions, on the four verbs that publish them.
//
// Each case drives ONE guard and reads the body the role put on the wire, and
// each asserts the same two things: the member is sent, and — the half that
// catches the mistake nobody sees — a request that names NO guard leaves the
// member ABSENT rather than sending a zero.
//
// ABSENT IS NOT ZERO on any of them. `expected_version` 0 is a legal token: the
// migration-0054 backfill left rows holding it, so a client that encoded "no
// guard" as 0 would guard every unguarded write against those rows and only
// those. `expected_assignee` "" is a legal guard too — it is how a caller says
// "only if nobody holds it" — so the two states cannot be collapsed in the other
// direction either. The role models both as POINTERS for exactly this reason and
// the wire body does too, so the encoding is a nil check and nothing else. No
// sentinel is encoded anywhere: not 0, and not the -1 the tree carried before
// the wire wave retired it.

func TestCloseSendsTheRowVersionPrecondition(t *testing.T) {
	version := int64(9)
	for _, tc := range []struct {
		name string
		want *int64
		req  issueops.CloseRequest
	}{
		{name: "guarded", want: &version, req: issueops.CloseRequest{ExpectedVersion: &version}},
		{name: "zero is a real token", want: new(int64), req: issueops.CloseRequest{ExpectedVersion: new(int64)}},
		{name: "unguarded", want: nil, req: issueops.CloseRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{close: &apigen.CloseIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			req := tc.req
			req.Actor, req.IssueID = "writer", "bd-1"
			if _, err := lifecycle.Close(t.Context(), req); err != nil {
				t.Fatalf("Close: %v", err)
			}
			assertExpectedVersion(t, "closeIssue", w.lastClose.ExpectedVersion, tc.want)
		})
	}
}

func TestReopenSendsTheRowVersionPrecondition(t *testing.T) {
	version := int64(11)
	for _, tc := range []struct {
		name string
		want *int64
		req  issueops.ReopenRequest
	}{
		{name: "guarded", want: &version, req: issueops.ReopenRequest{ExpectedVersion: &version}},
		{name: "zero is a real token", want: new(int64), req: issueops.ReopenRequest{ExpectedVersion: new(int64)}},
		{name: "unguarded", want: nil, req: issueops.ReopenRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{reopen: &apigen.ReopenIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			req := tc.req
			req.Actor, req.IssueID = "writer", "bd-1"
			if _, err := lifecycle.Reopen(t.Context(), req); err != nil {
				t.Fatalf("Reopen: %v", err)
			}
			assertExpectedVersion(t, "reopenIssue", w.lastReopen.ExpectedVersion, tc.want)
		})
	}
}

// TestLifecycleResultsStitchTheRevisionOntoTheRow is the update/close/reopen
// twin of TestReleaseStitchesTheRevisionOntoTheRowItAnswersWith.
//
// types.Issue.RowVersion is `json:"-"`, so the decoded row carries no token and
// each verb publishes the post-write revision as a SIBLING member. The client's
// one correct move is to ride it back onto Issue.RowVersion; both wrong ones are
// silent — dropping the member leaves a zero that reads as a real token (the
// migration-0054 backfill wrote 0s), and a second result field leaves the
// compose-and-continue loop reading the wrong one. The token is deliberately past
// 2^53: a decode through an IEEE-754 double answers a number NEAR it that is not
// it. The stub answers the revision BESIDE the row precisely so a stub that put
// it on the issue would hide the stitch.
func TestLifecycleResultsStitchTheRevisionOntoTheRow(t *testing.T) {
	const token int64 = 900719925474099234

	t.Run("update", func(t *testing.T) {
		w := &stubWire{update: &apigen.UpdateIssueResponse{
			Issue: types.Issue{ID: "bd-1", Status: types.StatusOpen}, Changed: true, Revision: types.RevisionToken(token),
		}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		res, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
			Actor: "writer", IssueID: "bd-1", Patch: issueops.IssuePatch{Priority: set(1)},
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if res.Issue == nil || res.Issue.RowVersion != token {
			t.Errorf("UpdateResult.Issue.RowVersion = %v, want %d off the wire's revision", res.Issue, token)
		}
	})

	t.Run("close", func(t *testing.T) {
		w := &stubWire{close: &apigen.CloseIssueResponse{
			Issue: types.Issue{ID: "bd-1", Status: types.StatusClosed}, Revision: types.RevisionToken(token),
		}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		res, err := lifecycle.Close(t.Context(), issueops.CloseRequest{Actor: "writer", IssueID: "bd-1"})
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if res.Issue == nil || res.Issue.RowVersion != token {
			t.Errorf("CloseResult.Issue.RowVersion = %v, want %d off the wire's revision", res.Issue, token)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		w := &stubWire{reopen: &apigen.ReopenIssueResponse{
			Issue: types.Issue{ID: "bd-1", Status: types.StatusOpen}, Revision: types.RevisionToken(token),
		}}
		lifecycle, err := stubStore(t, w).IssueLifecycle()
		if err != nil {
			t.Fatalf("IssueLifecycle(): %v", err)
		}
		res, err := lifecycle.Reopen(t.Context(), issueops.ReopenRequest{Actor: "writer", IssueID: "bd-1"})
		if err != nil {
			t.Fatalf("Reopen: %v", err)
		}
		if res.Issue == nil || res.Issue.RowVersion != token {
			t.Errorf("ReopenResult.Issue.RowVersion = %v, want %d off the wire's revision", res.Issue, token)
		}
	})
}

// TestUpdateSendsTheGuardTrio drives all three of updateIssue's preconditions,
// including the two whose EMPTY value is a real guard.
func TestUpdateSendsTheGuardTrio(t *testing.T) {
	version := int64(7)
	unassigned := ""
	holder := "gastown.mayor"
	open := issueops.StatusOpen

	patch := issueops.IssuePatch{Priority: set(1)}
	for _, tc := range []struct {
		name string
		req  issueops.UpdateRequest
		want wire.UpdateGuards
	}{
		{
			name: "unguarded leaves every member absent",
			req:  issueops.UpdateRequest{},
			want: wire.UpdateGuards{},
		},
		{
			name: "version",
			req:  issueops.UpdateRequest{ExpectedVersion: &version},
			want: wire.UpdateGuards{ExpectedVersion: ptr(types.RevisionToken(version))},
		},
		{
			name: "version zero is a real token",
			req:  issueops.UpdateRequest{ExpectedVersion: new(int64)},
			want: wire.UpdateGuards{ExpectedVersion: ptr("0")},
		},
		{
			name: "the empty assignee is a real guard",
			req:  issueops.UpdateRequest{ExpectedAssignee: &unassigned},
			want: wire.UpdateGuards{ExpectedAssignee: &unassigned},
		},
		{
			name: "assignee",
			req:  issueops.UpdateRequest{ExpectedAssignee: &holder},
			want: wire.UpdateGuards{ExpectedAssignee: &holder},
		},
		{
			name: "status",
			req:  issueops.UpdateRequest{ExpectedStatus: &open},
			want: wire.UpdateGuards{ExpectedStatus: ptr(string(open))},
		},
		{
			name: "all three together",
			req: issueops.UpdateRequest{
				ExpectedVersion: &version, ExpectedAssignee: &holder, ExpectedStatus: &open,
			},
			want: wire.UpdateGuards{
				ExpectedVersion: ptr(types.RevisionToken(version)), ExpectedAssignee: &holder, ExpectedStatus: ptr(string(open)),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			req := tc.req
			req.Actor, req.IssueID, req.Patch = "writer", "bd-1", patch
			if _, err := lifecycle.Update(t.Context(), req); err != nil {
				t.Fatalf("Update: %v", err)
			}
			// The version guard reaches the wire as the token's decimal string
			// (types.RevisionToken), so it is compared as the text it became.
			assertGuardText(t, "expected_version", w.lastGuards.ExpectedVersion, tc.want.ExpectedVersion)
			assertGuardText(t, "expected_assignee", w.lastGuards.ExpectedAssignee, tc.want.ExpectedAssignee)
			assertGuardText(t, "expected_status", w.lastGuards.ExpectedStatus, tc.want.ExpectedStatus)
			// The guards ride BESIDE the patch document, never inside it: the
			// server reads them off the body's top level and an
			// `expected_version` smuggled into `patch` would be an unknown
			// member and a 400.
			for _, guard := range []string{"expected_version", "expected_status", "expected_assignee"} {
				if _, in := w.lastPatch[guard]; in {
					t.Errorf("the patch document carries %q; the guards are top-level members of the body", guard)
				}
			}
		})
	}
}

// assertExpectedVersion compares a row-version guard THROUGH the pointer, so
// absent and zero are two different failures with two different messages.
//
// `got` is what reached the wire: the token's decimal STRING, spelled by
// types.RevisionToken (upstream #6053 — a JSON number on this member is a 400).
// `want` is the int64 the role was handed, so the assertion pins the encoding
// too: a guard on 0 has to arrive as "0", never as an absent member.
func assertExpectedVersion(t *testing.T, op string, got *string, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s sent expected_version %q for a request that named no guard; absent means absent, never zero", op, *got)
	case want != nil && got == nil:
		t.Errorf("%s omitted expected_version; the guard was %d", op, *want)
	case want != nil && got != nil && *got != types.RevisionToken(*want):
		t.Errorf("%s sent expected_version %q, want %q", op, *got, types.RevisionToken(*want))
	}
}

func assertGuardText(t *testing.T, member string, got, want *string) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s was sent as %q for a request that named no guard; absent means absent, never the empty string", member, *got)
	case want != nil && got == nil:
		t.Errorf("%s was omitted; the guard was %q", member, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %q, want %q", member, *got, *want)
	}
}

func ptr[T any](v T) *T { return &v }

// TestCreateRefusesARequestWithNoIssue keeps the role's own precondition off the
// wire: a create with nothing to create is a client bug, not a round trip.
func TestCreateRefusesARequestWithNoIssue(t *testing.T) {
	w := &stubWire{}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{Actor: "writer"}); !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("Create with no issue = %v, want ErrValidation", err)
	}
	if len(w.calls) != 0 {
		t.Errorf("the refusal dialed %v", w.calls)
	}
}

// TestCreateSendsTheWholeRequestVocabulary drives one create carrying every
// member the wire publishes and reads the body back.
//
// It is the create's answer to TestBatchCreateSendsTheItemMembersTheWireCarries,
// and it is a wider table for a reason the two operations do not share:
// createIssue publishes the WHOLE create vocabulary at the top level of one flat
// body, where batchCreateIssues' item spells nine members of an issue. So every
// member here is a member some caller of `bd create` fills in.
func TestCreateSendsTheWholeRequestVocabulary(t *testing.T) {
	minutes := 45
	externalRef := "gh-9"
	dueAt := time.Date(2033, 5, 6, 7, 8, 9, 0, time.UTC)
	deferUntil := time.Date(2033, 4, 5, 6, 7, 8, 0, time.UTC)

	w := &stubWire{}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{
		Actor:                   "writer",
		ParentID:                "bd-parent",
		InheritLabelsFromParent: true,
		ForceIDPrefix:           true,
		Dependencies: []issueops.CreateDependency{
			{TargetID: "bd-blocker", Type: types.DepBlocks},
			{TargetID: "bd-reverse", Type: types.DepBlocks, Reverse: true, Metadata: `{"k":"v"}`},
		},
		WaitsFor: &issueops.WaitsFor{SpawnerID: "bd-spawner", Gate: "any-children"},
		Issue: &issueops.Issue{
			ID: "bd-9", Title: "created title", Description: "created description",
			Design: "created design", AcceptanceCriteria: "created acceptance",
			Notes: "created notes", Status: issueops.StatusInProgress, Priority: 1,
			IssueType: types.TypeBug, Assignee: "created-assignee", Owner: "created-owner",
			EstimatedMinutes: &minutes, ExternalRef: &externalRef,
			DueAt: &dueAt, DeferUntil: &deferUntil, Sender: "created-sender",
			Metadata: json.RawMessage(`{"m":1}`), Labels: []string{"a", "b"},
			Ephemeral: true,
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := w.calls; len(got) != 1 || got[0] != "createIssue" {
		t.Fatalf("Create dialed %v, want one createIssue", got)
	}

	body := w.lastCreate
	for member, got := range map[string]string{
		"actor":               body.Actor,
		"title":               body.Title,
		"id":                  deref(body.Id),
		"description":         deref(body.Description),
		"design":              deref(body.Design),
		"acceptance_criteria": deref(body.AcceptanceCriteria),
		"notes":               deref(body.Notes),
		"status":              deref(body.Status),
		"issue_type":          deref(body.IssueType),
		"assignee":            deref(body.Assignee),
		"owner":               deref(body.Owner),
		"external_ref":        deref(body.ExternalRef),
		"sender":              deref(body.Sender),
		"parent_id":           deref(body.ParentId),
	} {
		if got == "" {
			t.Errorf("createIssue left %q empty; every member of the request reached the wire or refused", member)
		}
	}
	if body.Priority == nil || *body.Priority != 1 {
		t.Errorf("priority = %v, want 1 — 0 is P0 and a real request, so the member is always sent", body.Priority)
	}
	if body.EstimatedMinutes == nil || *body.EstimatedMinutes != minutes {
		t.Errorf("estimated_minutes = %v, want %d", body.EstimatedMinutes, minutes)
	}
	if body.DueAt == nil || !body.DueAt.Equal(dueAt) {
		t.Errorf("due_at = %v, want %v", body.DueAt, dueAt)
	}
	if body.DeferUntil == nil || !body.DeferUntil.Equal(deferUntil) {
		t.Errorf("defer_until = %v, want %v", body.DeferUntil, deferUntil)
	}
	if body.Labels == nil || strings.Join(*body.Labels, ",") != "a,b" {
		t.Errorf("labels = %v, want [a b]", body.Labels)
	}
	if string(body.Metadata) != `{"m":1}` {
		t.Errorf("metadata = %s, want the caller's document verbatim", body.Metadata)
	}
	for member, got := range map[string]*bool{
		"ephemeral":                  body.Ephemeral,
		"inherit_labels_from_parent": body.InheritLabelsFromParent,
		"force_id_prefix":            body.ForceIdPrefix,
	} {
		if got == nil || !*got {
			t.Errorf("%s = %v, want true", member, got)
		}
	}
	if body.WaitsFor == nil || body.WaitsFor.SpawnerId != "bd-spawner" || deref(body.WaitsFor.Gate) != "any-children" {
		t.Errorf("waits_for = %+v, want the spawner and its gate", body.WaitsFor)
	}
	if body.Dependencies == nil || len(*body.Dependencies) != 2 {
		t.Fatalf("dependencies = %v, want two edges", body.Dependencies)
	}
	edges := *body.Dependencies
	if edges[0].TargetId != "bd-blocker" || edges[0].Type != string(types.DepBlocks) {
		t.Errorf("dependencies[0] = %+v, want the blocking edge", edges[0])
	}
	// `reverse` and `metadata` ARE on this operation's edge, unlike
	// batchCreateIssues' — the create has an id for a target to point back at.
	if edges[1].Reverse == nil || !*edges[1].Reverse {
		t.Errorf("dependencies[1].reverse = %v, want true", edges[1].Reverse)
	}
	if string(edges[1].Metadata) != `{"k":"v"}` {
		t.Errorf("dependencies[1].metadata = %s, want the caller's blob verbatim", edges[1].Metadata)
	}
}

// TestCreateOmitsTheMembersTheRequestLeftEmpty is the other half: a bare create
// sends a bare body.
//
// It is not tidiness. `ephemeral` and `no_history` select a PLANE, `status` and
// `issue_type` are checked against the workspace's own vocabulary, and `id` is
// create-only — so a client that sent every member at its zero value would
// route a durable create onto the wisp plane, refuse on an empty type, and claim
// the empty id. Only `priority` and `actor` are unconditional, and priority for
// the reason batchCreateIssues gives: 0 is P0.
func TestCreateOmitsTheMembersTheRequestLeftEmpty(t *testing.T) {
	w := &stubWire{}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	if _, err := lifecycle.Create(t.Context(), issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{Title: "bare", IssueType: types.TypeTask},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	body := w.lastCreate
	for member, got := range map[string]any{
		"id":                         body.Id,
		"description":                body.Description,
		"design":                     body.Design,
		"acceptance_criteria":        body.AcceptanceCriteria,
		"notes":                      body.Notes,
		"status":                     body.Status,
		"assignee":                   body.Assignee,
		"owner":                      body.Owner,
		"external_ref":               body.ExternalRef,
		"sender":                     body.Sender,
		"parent_id":                  body.ParentId,
		"estimated_minutes":          body.EstimatedMinutes,
		"due_at":                     body.DueAt,
		"defer_until":                body.DeferUntil,
		"labels":                     body.Labels,
		"ephemeral":                  body.Ephemeral,
		"no_history":                 body.NoHistory,
		"inherit_labels_from_parent": body.InheritLabelsFromParent,
		"force_id_prefix":            body.ForceIdPrefix,
		"dependencies":               body.Dependencies,
		"waits_for":                  body.WaitsFor,
	} {
		if !reflect.ValueOf(got).IsNil() {
			t.Errorf("a bare create sent %q; an omitted member is the workspace default and a zero one is a request", member)
		}
	}
	if body.Metadata != nil {
		t.Errorf("a bare create sent metadata %s", body.Metadata)
	}
	if body.Priority == nil || *body.Priority != 0 {
		t.Errorf("priority = %v, want 0 sent explicitly — 0 is P0, not an absence", body.Priority)
	}
}

// TestCreateNamesTheOccupiedIDItWasRefusedFor: the wire's already_exists carries
// `param: "id"` and no id, because the request already said it. The role puts it
// back, so `bd create --id X` over http tells the user WHICH id — the same fact a
// local backend's refusal carries in its own message.
func TestCreateNamesTheOccupiedIDItWasRefusedFor(t *testing.T) {
	w := &stubWire{errs: []error{fmt.Errorf("bd serve answered 409: %w", issueops.ErrAlreadyExists)}}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	_, err = lifecycle.Create(t.Context(), issueops.CreateRequest{
		Actor: "writer",
		Issue: &issueops.Issue{ID: "bd-taken", Title: "t", IssueType: types.TypeTask},
	})
	if !errors.Is(err, issueops.ErrAlreadyExists) {
		t.Fatalf("Create over an occupied id = %v, want ErrAlreadyExists", err)
	}
	if !strings.Contains(err.Error(), "bd-taken") {
		t.Errorf("Create error = %v, want it to name the occupied id", err)
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// TestBatchCloseServesTheSingleItemShapeAndRefusesTheRest pins the DOWN-LEVEL
// leg: a server that does not advertise issues.batchClose (the stub store carries
// no capabilities), against which the role composes the single-item, no-ClaimNext
// shape onto closeIssue and refuses every shape that cannot compose. Where the
// capability IS present the whole batch is served on one wire call — the served
// conformance lane pins that leg, all nineteen cases.
func TestBatchCloseServesTheSingleItemShapeAndRefusesTheRest(t *testing.T) {
	t.Run("single item composes onto closeIssue", func(t *testing.T) {
		w := &stubWire{close: &apigen.CloseIssueResponse{
			Issue: types.Issue{ID: "bd-1", Status: types.StatusClosed}, OpenChildren: 2, Revision: "0",
		}}
		closer, err := stubStore(t, w).BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser(): %v", err)
		}
		res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
			Actor:   "writer",
			Items:   []issueops.BatchCloseItem{{IssueID: "bd-1", Reason: "done"}},
			Session: "s1",
			Force:   true,
		})
		if err != nil {
			t.Fatalf("CloseBatch: %v", err)
		}
		if len(res.Outcomes) != 1 || res.Outcomes[0].IssueID != "bd-1" {
			t.Fatalf("Outcomes = %+v, want one entry for bd-1", res.Outcomes)
		}
		if !res.Outcomes[0].Changed || res.Outcomes[0].OpenChildren != 2 {
			t.Errorf("outcome = %+v, want the close's own Changed and OpenChildren", res.Outcomes[0])
		}
		// The per-item reason and the request-wide session and force all reach
		// the one operation, because losing any of them would close the issue
		// with a record of why that is not the one the caller gave.
		if w.lastClose.Reason == nil || *w.lastClose.Reason != "done" {
			t.Errorf("close body carried reason %v, want the item's", w.lastClose.Reason)
		}
		if w.lastClose.Session == nil || *w.lastClose.Session != "s1" {
			t.Errorf("close body carried session %v, want the request's", w.lastClose.Session)
		}
		if w.lastClose.Force == nil || !*w.lastClose.Force {
			t.Errorf("close body carried force %v, want true", w.lastClose.Force)
		}
	})

	t.Run("a per-item refusal is a result", func(t *testing.T) {
		w := &stubWire{errs: []error{&issueops.CloseOpenChildrenError{IssueID: "bd-1", OpenChildren: 3}}}
		closer, err := stubStore(t, w).BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser(): %v", err)
		}
		res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
			Actor: "writer", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}},
		})
		if err != nil {
			t.Fatalf("CloseBatch returned the refusal as the METHOD's error: %v", err)
		}
		if len(res.Outcomes) != 1 || res.Outcomes[0].Err == nil {
			t.Fatalf("Outcomes = %+v, want the refusal carried per item", res.Outcomes)
		}
		var openChildren *issueops.CloseOpenChildrenError
		if !errors.As(res.Outcomes[0].Err, &openChildren) || openChildren.OpenChildren != 3 {
			t.Errorf("per-item error = %v, want the typed open-children refusal", res.Outcomes[0].Err)
		}
	})

	t.Run("the unserved shapes refuse without dialing", func(t *testing.T) {
		claim := issueops.ReadyRequest{}
		cases := map[string]issueops.CloseBatchRequest{
			"multi-item": {Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}, {IssueID: "bd-2"}}},
			"claim-next": {Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, ClaimNext: &claim},
		}
		for name, req := range cases {
			t.Run(name, func(t *testing.T) {
				w := &stubWire{close: &apigen.CloseIssueResponse{Revision: "0"}}
				closer, err := stubStore(t, w).BatchCloser()
				if err != nil {
					t.Fatalf("BatchCloser(): %v", err)
				}
				res, err := closer.CloseBatch(t.Context(), req)
				var unsup *ErrHTTPUnsupported
				if !errors.As(err, &unsup) {
					t.Fatalf("CloseBatch(%s) = %v, want the typed unsupported sentinel", name, err)
				}
				if len(res.Outcomes) != 0 {
					t.Errorf("a refused request carried outcomes: %+v", res.Outcomes)
				}
				if len(w.calls) != 0 {
					t.Errorf("the refusal dialed %v; a shape that cannot be served must not half-serve it", w.calls)
				}
			})
		}
	})

	t.Run("an invalid request is ErrValidation, not unsupported", func(t *testing.T) {
		w := &stubWire{}
		closer, err := stubStore(t, w).BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser(): %v", err)
		}
		for name, req := range map[string]issueops.CloseBatchRequest{
			"no actor": {Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}},
			"no items": {Actor: "w"},
			"blank id": {Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: ""}}},
		} {
			if _, err := closer.CloseBatch(t.Context(), req); !errors.Is(err, issueops.ErrValidation) {
				t.Errorf("CloseBatch(%s) = %v, want ErrValidation", name, err)
			}
		}
		if len(w.calls) != 0 {
			t.Errorf("an invalid request dialed %v", w.calls)
		}
	})
}

// batchCloseStore is a stub store whose server advertises issues.batchClose, so
// the BatchCloser takes the served (whole-batch) leg rather than the down-level
// composed one.
func batchCloseStore(t *testing.T, w *stubWire) *Store {
	t.Helper()
	return New(testTarget(t), w, &apigen.ContextResponse{
		BdVersion:    "1.2.3",
		Capabilities: []string{"issues.close", "issues.batchClose"},
	})
}

// TestBatchCloseRefusesWhatTheWireCannotCarry is L-close-cap's pin, L16's
// sibling: a batch over the wire's item cap refuses naming the bound and NEVER
// dials — the assertion that matters is that it is not chunked or half-sent — and
// a batch exactly at the bound reaches the batchClose operation.
func TestBatchCloseRefusesWhatTheWireCannotCarry(t *testing.T) {
	items := func(n int) []issueops.BatchCloseItem {
		out := make([]issueops.BatchCloseItem, n)
		for i := range out {
			out[i] = issueops.BatchCloseItem{IssueID: fmt.Sprintf("bd-%d", i)}
		}
		return out
	}
	dialed := func(calls []string, op string) bool {
		for _, c := range calls {
			if c == op {
				return true
			}
		}
		return false
	}

	t.Run("over the wire bound refuses without dialing", func(t *testing.T) {
		w := &stubWire{}
		closer, err := batchCloseStore(t, w).BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser(): %v", err)
		}
		res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
			Actor: "w", Items: items(maxWireBatchCloseItems + 1),
		})
		if !errors.Is(err, encode.ErrRefused) {
			t.Fatalf("CloseBatch(%d items) = %v, want the ledgered refusal", maxWireBatchCloseItems+1, err)
		}
		if len(res.Outcomes) != 0 {
			t.Errorf("a refused over-cap batch carried outcomes: %+v", res.Outcomes)
		}
		if len(w.calls) != 0 {
			t.Errorf("the over-cap refusal dialed %v; a batch over the bound must not be chunked or sent", w.calls)
		}
	})

	t.Run("exactly the wire bound serves", func(t *testing.T) {
		reqItems := items(maxWireBatchCloseItems)
		outcomes := make([]apigen.CloseOutcome, maxWireBatchCloseItems)
		for i := range outcomes {
			outcomes[i] = apigen.CloseOutcome{IssueId: reqItems[i].IssueID, Issue: &types.Issue{ID: reqItems[i].IssueID}}
		}
		w := &stubWire{batchClose: &apigen.BatchCloseResponse{Outcomes: outcomes}}
		closer, err := batchCloseStore(t, w).BatchCloser()
		if err != nil {
			t.Fatalf("BatchCloser(): %v", err)
		}
		res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{Actor: "w", Items: reqItems})
		if err != nil {
			t.Fatalf("CloseBatch at the bound: %v", err)
		}
		if len(res.Outcomes) != maxWireBatchCloseItems {
			t.Fatalf("Outcomes = %d, want one per item at the bound", len(res.Outcomes))
		}
		if !dialed(w.calls, "batchCloseIssues") {
			t.Errorf("a batch at the bound did not reach the batchClose operation: %v", w.calls)
		}
	})
}

// TestBatchCloseRefusesAServedClaimCarryingNoIssue is the outcome-count
// refusal's twin, one member over.
//
// types.IssueWithCounts carries the row as an EMBEDDED POINTER, so a
// `claimed_next` object that carries the cardinalities and none of the issue's
// own members decodes to a NON-NIL claim whose Issue is nil. Nothing on the
// wire distinguishes it from a claim that landed, and passing it through is not
// a wrong answer but a client-side PANIC: httpstore is a registered
// storage.DoltStorage, so `bd close --claim-next` takes the direct arm and
// dereferences the claimed row's ID with no nil check. A faulty or rogue server
// would crash the caller instead of being told it is broken.
//
// So it refuses at the decode, on the count's terms exactly: it is the METHOD's
// failure, and it carries no outcomes rather than an answer the caller might
// trust.
func TestBatchCloseRefusesAServedClaimCarryingNoIssue(t *testing.T) {
	w := &stubWire{batchClose: &apigen.BatchCloseResponse{
		Outcomes: []apigen.CloseOutcome{{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}}},
		// The counts are present and the row is not — the exact shape a
		// `"claimed_next": {}` object decodes to.
		ClaimedNext: &types.IssueWithCounts{DependencyCount: 1},
	}}
	closer, err := batchCloseStore(t, w).BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}

	claim := issueops.ReadyRequest{}
	res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
		Actor:     "w",
		Items:     []issueops.BatchCloseItem{{IssueID: "bd-1"}},
		ClaimNext: &claim,
	})
	if err == nil {
		t.Fatalf("CloseBatch passed a rowless claim through: ClaimedNext = %+v; the caller dereferences it", res.ClaimedNext)
	}
	if !strings.Contains(err.Error(), "bd serve returned") {
		t.Errorf("CloseBatch = %v, want the broken-server diagnosis the outcome count uses", err)
	}
	if len(res.Outcomes) != 0 || res.ClaimedNext != nil {
		t.Errorf("a refused result carried an answer: %+v", res)
	}
}

// TestBatchCloseRefusesAServedClaimTheRequestNeverEarned is the rowless
// refusal's sibling and the same trust class: a server whose answer breaks the
// role contract is broken, and this client says so rather than passing the
// answer up.
//
// A CLAIM IS A WRITE. The row `claimed_next` names is assigned to this actor,
// so a claim the request never earned either reports an assignment that never
// happened or hides one that did — and either way `bd close` prints it and
// hands it to the agent as its next piece of work. The rule it has to obey is
// the role contract's, and it is the SAME one `bd serve` now holds its own
// closer to: a claim requires that the REQUEST asked for one, and that at least
// one item LANDED. Changed is the test for landed, so a batch of idempotent
// re-closes earns nothing.
//
// THE HOOK IS WHY THE REFUSAL HAS TO HAPPEN HERE. The client's decorator chain
// puts HookFiringStore above this store, and hookBatchCloser fires the update
// hook on ClaimedNext's PRESENCE alone — it re-derives nothing. Its two
// preconditions are a nil error and a non-nil claim, and both assertions below
// deny it one: the refusal returns before any claim value escapes this decode.
// TestAnUnearnedClaimNeverReachesTheUpdateHook drives the real decorator and
// proves it end to end.
func TestBatchCloseRefusesAServedClaimTheRequestNeverEarned(t *testing.T) {
	claim := issueops.ReadyRequest{}
	landed := apigen.CloseOutcome{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}}
	reclosed := apigen.CloseOutcome{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}, AlreadyClosed: ptr(true)}
	served := &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-next"}}

	for _, tc := range []struct {
		name    string
		request issueops.CloseBatchRequest
		outcome apigen.CloseOutcome
		fault   string
	}{
		{
			// The request carried no claim_next at all, so the encoder sent
			// none: a claim in the answer is one this caller never asked for.
			name:    "a claim the request never asked for",
			request: issueops.CloseBatchRequest{Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}},
			outcome: landed,
			fault:   "asked for none",
		},
		{
			// already_closed: a per-item SUCCESS that persisted nothing, which
			// is exactly the batch the contract says earns no claim. This is
			// the case the two rules have to agree on, or a legitimate server
			// would trip the guard.
			name: "a claim for a batch that landed nothing",
			request: issueops.CloseBatchRequest{
				Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, ClaimNext: &claim,
			},
			outcome: reclosed,
			fault:   "closed nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &stubWire{batchClose: &apigen.BatchCloseResponse{
				Outcomes:    []apigen.CloseOutcome{tc.outcome},
				ClaimedNext: served,
			}}
			closer, err := batchCloseStore(t, w).BatchCloser()
			if err != nil {
				t.Fatalf("BatchCloser(): %v", err)
			}

			res, err := closer.CloseBatch(t.Context(), tc.request)
			if err == nil {
				t.Fatalf("CloseBatch passed an unearned claim through: ClaimedNext = %+v", res.ClaimedNext)
			}
			if !strings.Contains(err.Error(), "bd serve returned") {
				t.Errorf("CloseBatch = %v, want the broken-server diagnosis the outcome count uses", err)
			}
			if !strings.Contains(err.Error(), tc.fault) {
				t.Errorf("CloseBatch = %v, want it to say which rule the server broke (%q)", err, tc.fault)
			}
			// The hook decorator's two preconditions, denied: a nil error and a
			// non-nil claim. Nothing it fires on escapes this decode.
			if res.ClaimedNext != nil {
				t.Errorf("a refused result still carried the claim the update hook fires on: %+v", res.ClaimedNext)
			}
			if len(res.Outcomes) != 0 {
				t.Errorf("a refused result carried outcomes: %+v", res.Outcomes)
			}
		})
	}
}

// TestBatchCloseKeepsTheClaimItEarned is the guard's other half: the shape a
// correct server sends must still arrive. Without it the two refusals above
// would be satisfied by a decode that refused every claim.
func TestBatchCloseKeepsTheClaimItEarned(t *testing.T) {
	claim := issueops.ReadyRequest{}
	w := &stubWire{batchClose: &apigen.BatchCloseResponse{
		Outcomes:    []apigen.CloseOutcome{{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}}},
		ClaimedNext: &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-next"}, DependencyCount: 2},
	}}
	closer, err := batchCloseStore(t, w).BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}

	res, err := closer.CloseBatch(t.Context(), issueops.CloseBatchRequest{
		Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, ClaimNext: &claim,
	})
	if err != nil {
		t.Fatalf("CloseBatch refused a claim the batch earned: %v", err)
	}
	if res.ClaimedNext == nil || res.ClaimedNext.ID != "bd-next" {
		t.Fatalf("ClaimedNext = %+v, want the served row bd-next", res.ClaimedNext)
	}
	if res.ClaimedNext.DependencyCount != 2 {
		t.Errorf("the claim lost its cardinalities: %+v", res.ClaimedNext)
	}
}

// TestClaimReportsTheIdempotentReclaim: already_claimed on a 200 is the SAME
// actor re-claiming, and the role spells that Changed false. Reading it as a
// conflict would make an agent polling its own claim fire the update hook on
// every poll.
func TestClaimReportsTheIdempotentReclaim(t *testing.T) {
	w := &stubWire{claim: &apigen.ClaimResponse{
		AlreadyClaimed: true, Issue: types.Issue{ID: "bd-1", Assignee: "writer"},
	}}
	claimer, err := stubStore(t, w).IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	res, err := claimer.Claim(t.Context(), issueops.ClaimRequest{Actor: "writer", IssueID: "bd-1"})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.Changed {
		t.Error("Changed = true for an idempotent re-claim")
	}
	if res.Issue == nil || res.Issue.ID != "bd-1" {
		t.Errorf("Issue = %+v, want the post-claim row", res.Issue)
	}
}

func readyRow(id string, deps int) apigen.IssueWithCounts {
	issue := &types.Issue{ID: id, Status: types.StatusOpen}
	return apigen.IssueWithCounts{Issue: issue, DependencyCount: deps}
}

// TestReadyClaimWalksPastLostRacesAndHydratesFromThePage covers the composition
// D8 row 4 specifies: a taken row is walked past, and the row that lands carries
// the claim's post-state issue with the listing's cardinalities.
func TestReadyClaimWalksPastLostRacesAndHydratesFromThePage(t *testing.T) {
	w := &stubWire{
		ready: []*apigen.ReadyPage{{Items: []apigen.IssueWithCounts{readyRow("bd-1", 4), readyRow("bd-2", 7)}}},
		claim: &apigen.ClaimResponse{Issue: types.Issue{ID: "bd-2", Assignee: "writer", Status: types.StatusInProgress}},
		errs: []error{
			nil, // the ready listing
			&issueops.ClaimConflictError{IssueID: "bd-1", Assignee: "other", Err: issueops.ErrAlreadyClaimed},
		},
	}
	claimer, err := stubStore(t, w).ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	res, err := claimer.ClaimNext(t.Context(), issueops.ClaimNextRequest{Actor: "writer"})
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if res.Claimed == nil || res.Claimed.ID != "bd-2" {
		t.Fatalf("Claimed = %+v, want the second row", res.Claimed)
	}
	if res.Claimed.Status != types.StatusInProgress {
		t.Errorf("Claimed.Status = %q, want the POST-claim status", res.Claimed.Status)
	}
	if res.Claimed.DependencyCount != 7 {
		t.Errorf("DependencyCount = %d, want the listing's %d", res.Claimed.DependencyCount, 7)
	}
}

// TestReadyClaimSeparatesAnEmptyFrontFromLostRaces is L14's second residue made
// visible: a drained queue is a nil-Claimed success, and a front that existed
// but was taken is an error. Reporting the second as the first would tell a
// polling agent there is no work when there is.
func TestReadyClaimSeparatesAnEmptyFrontFromLostRaces(t *testing.T) {
	t.Run("empty front", func(t *testing.T) {
		w := &stubWire{ready: []*apigen.ReadyPage{{}}}
		claimer, err := stubStore(t, w).ReadyClaimer()
		if err != nil {
			t.Fatalf("ReadyClaimer(): %v", err)
		}
		res, err := claimer.ClaimNext(t.Context(), issueops.ClaimNextRequest{Actor: "writer"})
		if err != nil {
			t.Fatalf("ClaimNext on a drained queue = %v, want nil", err)
		}
		if res.Claimed != nil {
			t.Errorf("Claimed = %+v, want nil", res.Claimed)
		}
	})

	t.Run("every candidate lost", func(t *testing.T) {
		w := &stubWire{
			ready: []*apigen.ReadyPage{{Items: []apigen.IssueWithCounts{readyRow("bd-1", 0)}}},
			errs:  []error{nil, issueops.ErrNotClaimable},
		}
		claimer, err := stubStore(t, w).ReadyClaimer()
		if err != nil {
			t.Fatalf("ReadyClaimer(): %v", err)
		}
		res, err := claimer.ClaimNext(t.Context(), issueops.ClaimNextRequest{Actor: "writer"})
		if !errors.Is(err, ErrClaimRacesLost) {
			t.Fatalf("ClaimNext = %v, want the lost-races refusal", err)
		}
		if res.Claimed != nil {
			t.Errorf("Claimed = %+v, want nil", res.Claimed)
		}
		var lost *ClaimRacesLostError
		if !errors.As(err, &lost) || lost.Attempts != 1 {
			t.Errorf("lost-races error = %v, want one recorded attempt", err)
		}
	})
}

// TestReadyClaimRefusesTheRequestMembersTheRoleForbids: Limit and Offset are the
// role's own refusals, not the encoder's, and a bounded window would report an
// empty front whenever that window happened to be unclaimable.
func TestReadyClaimRefusesTheRequestMembersTheRoleForbids(t *testing.T) {
	limit := 5
	cases := map[string]issueops.ClaimNextRequest{
		"no actor": {Filter: issueops.ReadyRequest{}},
		"limit":    {Actor: "w", Filter: issueops.ReadyRequest{Limit: &limit}},
		"offset":   {Actor: "w", Filter: issueops.ReadyRequest{Offset: 3}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{}
			claimer, err := stubStore(t, w).ReadyClaimer()
			if err != nil {
				t.Fatalf("ReadyClaimer(): %v", err)
			}
			if _, err := claimer.ClaimNext(t.Context(), req); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("ClaimNext(%s) = %v, want ErrValidation", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the refusal dialed %v", w.calls)
			}
		})
	}
}

// TestReadyClaimDoesNotMutateTheCallerFilter: the page size is ours and the
// filter is the caller's, and writing one into the other would leave a Limit on
// a request the role has just refused Limits on.
func TestReadyClaimDoesNotMutateTheCallerFilter(t *testing.T) {
	w := &stubWire{ready: []*apigen.ReadyPage{{}}}
	claimer, err := stubStore(t, w).ReadyClaimer()
	if err != nil {
		t.Fatalf("ReadyClaimer(): %v", err)
	}
	req := issueops.ClaimNextRequest{Actor: "writer", Filter: issueops.ReadyRequest{Labels: []string{"a"}}}
	before := req
	if _, err := claimer.ClaimNext(t.Context(), req); err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !reflect.DeepEqual(req, before) {
		t.Errorf("the caller's request changed: %+v, want %+v", req, before)
	}
}

// TestDependencyAddRefusesWhatTheWireCannotCarry covers the three request-level
// refusals: the unpublished skip flag, the wire's 100-edge bound (never chunked,
// L16), and the self-dependency the role names its own sentinel for.
func TestDependencyAddRefusesWhatTheWireCannotCarry(t *testing.T) {
	edge := issueops.DependencyEdge{IssueID: "bd-1", DependsOnID: "bd-2", Type: issueops.DepBlocks}

	t.Run("SkipPerEdgeCycleCheck", func(t *testing.T) {
		w := &stubWire{}
		editor, err := stubStore(t, w).DependencyEditor()
		if err != nil {
			t.Fatalf("DependencyEditor(): %v", err)
		}
		_, err = editor.AddDependencies(t.Context(), issueops.AddDependenciesRequest{
			Actor: "w", Edges: []issueops.DependencyEdge{edge}, SkipPerEdgeCycleCheck: true,
		})
		if !errors.Is(err, encode.ErrRefused) {
			t.Fatalf("AddDependencies with the skip flag = %v, want a refusal", err)
		}
		if len(w.calls) != 0 {
			t.Errorf("the refusal dialed %v", w.calls)
		}
	})

	t.Run("past the wire bound", func(t *testing.T) {
		w := &stubWire{added: &apigen.AddDependenciesResponse{}}
		editor, err := stubStore(t, w).DependencyEditor()
		if err != nil {
			t.Fatalf("DependencyEditor(): %v", err)
		}
		edges := make([]issueops.DependencyEdge, 0, maxAddDependencyEdges+1)
		for i := 0; i <= maxAddDependencyEdges; i++ {
			edges = append(edges, issueops.DependencyEdge{
				IssueID: "bd-src", DependsOnID: "bd-" + strconv.Itoa(i), Type: issueops.DepBlocks,
			})
		}
		if _, err := editor.AddDependencies(t.Context(), issueops.AddDependenciesRequest{Actor: "w", Edges: edges}); !errors.Is(err, encode.ErrRefused) {
			t.Fatalf("AddDependencies with %d edges = %v, want the bound's refusal", len(edges), err)
		}
		if len(w.calls) != 0 {
			t.Errorf("the over-bound request was dialed (%v); chunking is exactly what L16 forbids", w.calls)
		}

		// The boundary itself serves.
		if _, err := editor.AddDependencies(t.Context(), issueops.AddDependenciesRequest{Actor: "w", Edges: edges[:maxAddDependencyEdges]}); err != nil {
			t.Fatalf("AddDependencies with exactly %d edges: %v", maxAddDependencyEdges, err)
		}
	})

	t.Run("self dependency", func(t *testing.T) {
		w := &stubWire{}
		editor, err := stubStore(t, w).DependencyEditor()
		if err != nil {
			t.Fatalf("DependencyEditor(): %v", err)
		}
		_, err = editor.AddDependencies(t.Context(), issueops.AddDependenciesRequest{
			Actor: "w", Edges: []issueops.DependencyEdge{{IssueID: "bd-1", DependsOnID: "bd-1", Type: issueops.DepBlocks}},
		})
		if !errors.Is(err, issueops.ErrSelfDependency) {
			t.Fatalf("self-dependency = %v, want ErrSelfDependency", err)
		}
	})
}

// TestDependencyAddEchoesTheServersOrder reads the response back rather than
// re-echoing the request, which is the only way a server that ever answered
// something else would be caught.
func TestDependencyAddEchoesTheServersOrder(t *testing.T) {
	w := &stubWire{added: &apigen.AddDependenciesResponse{Added: []apigen.DependencyEdge{
		{IssueId: "bd-1", DependsOnId: "bd-2", Type: "blocks"},
	}}}
	editor, err := stubStore(t, w).DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	res, err := editor.AddDependencies(t.Context(), issueops.AddDependenciesRequest{
		Actor: "w", Edges: []issueops.DependencyEdge{{IssueID: "bd-1", DependsOnID: "bd-2", Type: issueops.DepBlocks}},
	})
	if err != nil {
		t.Fatalf("AddDependencies: %v", err)
	}
	want := []issueops.DependencyEdge{{IssueID: "bd-1", DependsOnID: "bd-2", Type: issueops.DepBlocks}}
	if !reflect.DeepEqual(res.Added, want) {
		t.Errorf("Added = %+v, want %+v", res.Added, want)
	}
	if w.lastAdd.Actor != "w" || len(w.lastAdd.Edges) != 1 {
		t.Errorf("request body = %+v, want the actor and the one edge", w.lastAdd)
	}
}

// TestMemoryMissesAreResultsNotErrors is the one real translation in the memory
// role: memoryops declares no ErrNotFound on purpose, and the wire spells a miss
// as a 404. Leaking that through would make `bd recall` of an unknown key an
// error where every other backend prints "not found".
func TestMemoryMissesAreResultsNotErrors(t *testing.T) {
	t.Run("recall", func(t *testing.T) {
		w := &stubWire{errs: []error{issueops.ErrNotFound}}
		memories, err := stubStore(t, w).Memories()
		if err != nil {
			t.Fatalf("Memories(): %v", err)
		}
		res, err := memories.Recall(t.Context(), memoryops.RecallRequest{Key: "nope"})
		if err != nil {
			t.Fatalf("Recall of an absent key = %v, want nil", err)
		}
		if res.Found || res.Key != "nope" {
			t.Errorf("RecallResult = %+v, want Found false with the key echoed", res)
		}
	})

	t.Run("forget", func(t *testing.T) {
		w := &stubWire{errs: []error{issueops.ErrNotFound}}
		memories, err := stubStore(t, w).Memories()
		if err != nil {
			t.Fatalf("Memories(): %v", err)
		}
		res, err := memories.Forget(t.Context(), memoryops.ForgetRequest{Key: "nope"})
		if err != nil {
			t.Fatalf("Forget of an absent key = %v, want nil", err)
		}
		if res.Found {
			t.Errorf("ForgetResult = %+v, want Found false", res)
		}
	})
}

// TestMemoryRememberDerivesItsKeyServerSide: an omitted key is a different
// request from an empty one, and the derived key comes back in the result.
func TestMemoryRememberDerivesItsKeyServerSide(t *testing.T) {
	w := &stubWire{remember: &apigen.RememberedMemory{Key: "derived", Value: "content", Replaced: true}}
	memories, err := stubStore(t, w).Memories()
	if err != nil {
		t.Fatalf("Memories(): %v", err)
	}
	res, err := memories.Remember(t.Context(), memoryops.RememberRequest{Content: "content"})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if res.Key != "derived" || !res.Replaced {
		t.Errorf("RememberResult = %+v, want the server's derived key and its Replaced", res)
	}

	if _, err := memories.Remember(t.Context(), memoryops.RememberRequest{Content: "   "}); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("Remember of blank content = %v, want ErrValidation", err)
	}
}

// TestMemoryListAnswersAnEmptyMapNeverNil: a caller ranges over the answer, and
// "the plane is empty" is not something to spell as an absent one.
func TestMemoryListAnswersAnEmptyMapNeverNil(t *testing.T) {
	w := &stubWire{page: &apigen.MemoriesPage{}}
	memories, err := stubStore(t, w).Memories()
	if err != nil {
		t.Fatalf("Memories(): %v", err)
	}
	res, err := memories.List(t.Context(), memoryops.ListRequest{Search: "Term"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Memories == nil {
		t.Error("Memories is nil, want an empty map")
	}
	// The folding is the role's and happens server-side; sending a lowercased
	// term would be a second implementation of a rule that has one.
	if w.lastSearch != "Term" {
		t.Errorf("search parameter = %q, want the caller's term verbatim", w.lastSearch)
	}
}

// TestWriteAccessorsRefuseWithoutATransport: a build that registered the backend
// without linking a wire client is a wiring fault, and it must be named rather
// than reached as a nil dereference three frames into a role.
func TestWriteAccessorsRefuseWithoutATransport(t *testing.T) {
	s := New(testTarget(t), nil, nil)
	accessors := map[string]func() error{
		"IssueClaimer":     func() error { _, err := s.IssueClaimer(); return err },
		"ReadyClaimer":     func() error { _, err := s.ReadyClaimer(); return err },
		"IssueLifecycle":   func() error { _, err := s.IssueLifecycle(); return err },
		"DependencyEditor": func() error { _, err := s.DependencyEditor(); return err },
		"Memories":         func() error { _, err := s.Memories(); return err },
		"BatchCloser":      func() error { _, err := s.BatchCloser(); return err },
	}
	for name, open := range accessors {
		if err := open(); !errors.Is(err, ErrNoTransport) {
			t.Errorf("%s() without a transport = %v, want ErrNoTransport", name, err)
		}
	}
	if err := s.CloseIssue(t.Context(), "bd-1", "", "w", ""); !errors.Is(err, ErrNoTransport) {
		t.Errorf("CloseIssue without a transport = %v, want ErrNoTransport", err)
	}
}
