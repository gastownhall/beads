package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These are pure: the release path runs end to end over a real listener against
// a fake ROLE, so the wire edge — the path split, the media type, the body
// vocabulary, the guard pair, the response shape and every problem document —
// is covered on every pull request by the unconditional Go test job.
//
// What a fake cannot prove is that a REFUSED release left the claim standing,
// and that is the property the whole non-idempotence argument rests on. That
// lives in cmd/bd's proxied-server integration test against real Dolt
// (TestProxiedServerServeRelease). The role-level transition — what the row
// looks like afterwards, the lease, the event — is owned against a real store
// by internal/storage/uow/releaser_contract_test.go, which this slice cites
// rather than duplicates.

const releasePath = "/v0/beads/issues/bd-1:release"

func (ts *testServer) releaseIssue(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

// newReleaseServer wires a server over the store-shaped source with a releaser
// the case controls. Every other role is a placeholder: Listen refuses a
// partial source, and a release reaches none of them.
func newReleaseServer(t *testing.T, releaser *roleReleaser) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Releaser: releaser}))
}

// releasedIssue is the ANONYMOUS post-state: assignee cleared, status open. It
// is the same row no matter who emptied it, which is the whole reason this
// operation refuses rather than answering an idempotent 200.
func releasedIssue(id string, revision int64) *types.Issue {
	issue := seededIssue(id, "", types.StatusOpen)
	issue.RowVersion = revision
	return issue
}

// TestReleaseWritesOnceAndAnswersWithTheRowItWrote is the happy path and the
// three things a client depends on: the role receives exactly what the body
// asked for, the response carries the row as it stands after the release, and
// `revision` carries the REMINTED token off that same row.
func TestReleaseWritesOnceAndAnswersWithTheRowItWrote(t *testing.T) {
	releaser := &roleReleaser{result: issueops.ReleaseResult{Issue: releasedIssue("bd-1", 77), Changed: true}}
	ts := newReleaseServer(t, releaser)

	resp := ts.releaseIssue(t, releasePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := releaser.releaseRequests()
	if len(got) != 1 {
		t.Fatalf("the role received %+v, want exactly one request", got)
	}
	// The unconditional path: nil selects it, and nothing else does.
	if got[0].Actor != "alice" || got[0].IssueID != "bd-1" || got[0].ExpectedAssignee != nil || got[0].Force {
		t.Fatalf("the role received %+v, want an unconditional release of bd-1 by alice", got[0])
	}

	body := decodeBody(t, resp)
	if body["changed"] != true {
		t.Errorf("changed = %v, want true — the role reports it true on every answer it returns without an error", body["changed"])
	}
	if body["revision"] != float64(77) {
		t.Errorf("revision = %v, want the post-release token off the row the role answered with", body["revision"])
	}
	issue, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue = %#v, want an object", body["issue"])
	}
	// The ANONYMOUS post-state, as the wire spells it: status open, and no
	// `assignee` at all — types.Issue omits the member when it is empty, so a
	// client reads the release's outcome from the member's ABSENCE.
	if issue["status"] != string(types.StatusOpen) {
		t.Errorf("status = %v, want %s: %v", issue["status"], types.StatusOpen, issue)
	}
	if assignee, present := issue["assignee"]; present && assignee != "" {
		t.Errorf("assignee = %v; a released row holds no claim", assignee)
	}
}

// TestReleaseHasNoIdempotentAnswer is the operation's defining property, tested
// from the wire rather than assumed from the role: an unheld row is a REFUSAL,
// and it must not arrive as a 200 carrying `changed: false`.
//
// The distinction is not stylistic. A 200 here would report one answer for "I
// released this twice", "a reaper beat me to it" and "nothing ever claimed it",
// because the post-state is identical in all three and there is nothing left on
// the row to tell them apart. It would also be a value no role produces:
// ReleaseResult.Changed is true on every non-error answer.
func TestReleaseHasNoIdempotentAnswer(t *testing.T) {
	releaser := &roleReleaser{err: fmt.Errorf("%w: bd-1 has no assignee to release", issueops.ErrNotClaimed)}
	ts := newReleaseServer(t, releaser)

	resp := ts.releaseIssue(t, releasePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotReleasable) {
		t.Fatalf("code = %v, want %s", body["code"], CodeNotReleasable)
	}
	// No member distinguishes the two conditions this code covers, and a client
	// must not learn to dispatch on one appearing.
	for _, member := range []string{"assignee", "issue_status", "expected_assignee", "actual_assignee"} {
		if _, present := body[member]; present {
			t.Errorf("not_releasable carries %q; the code documents no discriminating member", member)
		}
	}
}

// TestReleaseAnswersTheRefusalsTheRoleRaises walks the whole 409 vocabulary and
// the 404, each from the sentinel the role actually returns.
//
// EVERY ARM IS MATCHED BEFORE ErrValidation AND ErrNotFound in failRelease, and
// that is what this table exists to pin: none of these sentinels is wrapped in
// ErrValidation by any leg, so an arm placed below a generic one would be a 500
// on every leg for a condition the document names by code. Deleting any single
// case from failRelease must fail a row here.
func TestReleaseAnswersTheRefusalsTheRoleRaises(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		err     error
		status  int
		code    Code
		param   string
		members map[string]any
	}{
		{
			name:   "a status that does not accept a release",
			body:   `{"actor":"alice"}`,
			err:    fmt.Errorf("%w: bd-1 has status %q, which is neither %q nor %q", issueops.ErrNotReleasable, "closed", "open", "in_progress"),
			status: http.StatusConflict,
			code:   CodeNotReleasable,
		},
		{
			name:   "a row that holds no claim",
			body:   `{"actor":"alice"}`,
			err:    fmt.Errorf("%w: bd-1 has no assignee to release", issueops.ErrNotClaimed),
			status: http.StatusConflict,
			code:   CodeNotReleasable,
		},
		{
			// The fence, answered with updateIssue's code for the identical
			// situation. No `assignee` member: the fence refuses without naming
			// the holder, so absence means "re-read the row".
			name:   "an unforced release by an actor that is not the holder",
			body:   `{"actor":"mallory"}`,
			err:    fmt.Errorf("%w: bd-1 is held by alice", issueops.ErrNotOwner),
			status: http.StatusConflict,
			code:   CodeAlreadyClaimed,
		},
		{
			// The guard, carrying the REQUEST's expectation and no observation.
			name:    "a guard that missed",
			body:    `{"actor":"supervisor","expected_assignee":"alice"}`,
			err:     fmt.Errorf("%w: bd-1 is held by %q, expected %q", issueops.ErrAssigneeMismatch, "bob", "alice"),
			status:  http.StatusConflict,
			code:    CodePreconditionFailed,
			param:   releaseExpectedAssigneeMember,
			members: map[string]any{"expected_assignee": "alice"},
		},
		{
			name:   "an id naming no row on either plane",
			body:   `{"actor":"alice"}`,
			err:    fmt.Errorf("get bd-1: %w", issueops.ErrNotFound),
			status: http.StatusNotFound,
			code:   CodeNotFound,
		},
		{
			// Defensive: every validation refusal the role has is refused at the
			// edge. It must still be a 400 rather than a 500 if that stops
			// being true.
			name:   "the role's own validation, which the edge should have caught",
			body:   `{"actor":"alice"}`,
			err:    fmt.Errorf("%w: release requires an actor", issueops.ErrValidation),
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newReleaseServer(t, &roleReleaser{err: tc.err})

			resp := ts.releaseIssue(t, releasePath, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(tc.code) {
				t.Fatalf("code = %v, want %s", body["code"], tc.code)
			}
			if tc.param != "" && body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			for member, want := range tc.members {
				if body[member] != want {
					t.Errorf("%s = %v, want %v", member, body[member], want)
				}
			}
			// The refusals replace the role's prose with the server's own
			// words, so the role's message must not travel on the wire: it
			// names holders and statuses this surface publishes typed or not
			// at all.
			if detail, _ := body["detail"].(string); detail != "" && tc.err != nil {
				if detail == tc.err.Error() {
					t.Errorf("detail quotes the role's message verbatim: %q", detail)
				}
			}
			// `actual_assignee` is declared on the envelope for an operation
			// whose role can report what it found. This one cannot.
			if _, present := body["actual_assignee"]; present {
				t.Errorf("the refusal carries actual_assignee, which no release refusal can honestly fill")
			}
		})
	}
}

// TestReleaseRefusesTheGuardPair pins the one refusal this handler owns rather
// than forwards. `force` and `expected_assignee` are answers to the same
// question and they disagree, so honoring either would be the server picking
// which half of the request the caller meant.
//
// It is refused at the EDGE, before any database work, which is what lets the
// 400 name a member.
func TestReleaseRefusesTheGuardPair(t *testing.T) {
	releaser := &roleReleaser{result: issueops.ReleaseResult{Issue: releasedIssue("bd-1", 1), Changed: true}}
	ts := newReleaseServer(t, releaser)

	resp := ts.releaseIssue(t, releasePath, `{"actor":"alice","expected_assignee":"bob","force":true}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["param"] != releaseForceMember {
		t.Errorf("code/param = %v/%v, want %s on `%s`", body["code"], body["param"], CodeInvalidArgument, releaseForceMember)
	}
	if got := releaser.releaseRequests(); len(got) != 0 {
		t.Errorf("the pair reached the role: %+v", got)
	}
}

// TestReleaseExpectedAssigneeDistinguishesAbsentFromEmpty is the member's whole
// contract, and the one place it disagrees with updateIssue's same-named guard.
//
// Absent selects the unconditional path. The empty string — and anything blank
// after trimming — is a 400 rather than a guard meaning "expected unassigned",
// because "release a row nobody holds" describes no release at all. A caller
// asking whether a row is unheld is asking a READER a question.
func TestReleaseExpectedAssigneeDistinguishesAbsentFromEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		status   int
		expected *string
	}{
		{name: "absent selects the unconditional path", body: `{"actor":"alice"}`, status: http.StatusOK},
		{name: "the empty string is refused", body: `{"actor":"alice","expected_assignee":""}`, status: http.StatusBadRequest},
		{name: "blank after trimming is refused", body: `{"actor":"alice","expected_assignee":"   "}`, status: http.StatusBadRequest},
		{name: "explicit null is refused", body: `{"actor":"alice","expected_assignee":null}`, status: http.StatusBadRequest},
		{name: "a non-string is refused", body: `{"actor":"alice","expected_assignee":7}`, status: http.StatusBadRequest},
		{
			name:     "a real holder guards the release",
			body:     `{"actor":"supervisor","expected_assignee":"alice"}`,
			status:   http.StatusOK,
			expected: strPtr("alice"),
		},
		{
			// The value travels UNTRIMMED, matching the role: a padded
			// expectation must lose every time rather than intermittently.
			name:     "a padded holder reaches the role padded",
			body:     `{"actor":"supervisor","expected_assignee":" alice"}`,
			status:   http.StatusOK,
			expected: strPtr(" alice"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releaser := &roleReleaser{result: issueops.ReleaseResult{Issue: releasedIssue("bd-1", 1), Changed: true}}
			ts := newReleaseServer(t, releaser)

			resp := ts.releaseIssue(t, releasePath, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, readAll(t, resp))
			}
			if tc.status != http.StatusOK {
				if body := decodeBody(t, resp); body["param"] != releaseExpectedAssigneeMember {
					t.Errorf("param = %v, want %q", body["param"], releaseExpectedAssigneeMember)
				}
				if got := releaser.releaseRequests(); len(got) != 0 {
					t.Errorf("a refused guard reached the role: %+v", got)
				}
				return
			}
			got := releaser.releaseRequests()
			if len(got) != 1 {
				t.Fatalf("the role received %+v, want exactly one request", got)
			}
			switch {
			case tc.expected == nil && got[0].ExpectedAssignee != nil:
				t.Errorf("ExpectedAssignee = %q, want nil — only absence selects the unconditional path", *got[0].ExpectedAssignee)
			case tc.expected != nil && got[0].ExpectedAssignee == nil:
				t.Errorf("ExpectedAssignee = nil, want %q", *tc.expected)
			case tc.expected != nil && *got[0].ExpectedAssignee != *tc.expected:
				t.Errorf("ExpectedAssignee = %q, want %q", *got[0].ExpectedAssignee, *tc.expected)
			}
		})
	}
}

// TestReleaseForwardsForce: the fence is the ROLE's, so the handler's whole job
// is to pass the bypass through unexamined.
func TestReleaseForwardsForce(t *testing.T) {
	releaser := &roleReleaser{result: issueops.ReleaseResult{Issue: releasedIssue("bd-1", 2), Changed: true}}
	ts := newReleaseServer(t, releaser)

	resp := ts.releaseIssue(t, releasePath, `{"actor":"supervisor","force":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := releaser.releaseRequests()
	if len(got) != 1 || !got[0].Force {
		t.Fatalf("the role received %+v, want one forced release", got)
	}
}

// TestReleaseRefusesUnknownAndMalformedBodies is the body vocabulary, refused BY
// NAME. The schema is additionalProperties: false, and a client that has stopped
// parsing prose can only enforce that if the server names the offender.
func TestReleaseRefusesUnknownAndMalformedBodies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		param string
	}{
		{name: "an unknown member", body: `{"actor":"alice","expected_version":3}`, param: "expected_version"},
		{name: "a missing actor", body: `{"force":true}`, param: claimActorMember},
		{name: "an actor that is blank after trimming", body: `{"actor":"  "}`, param: claimActorMember},
		{name: "an actor carrying a newline", body: `{"actor":"alice\nbd: released by mallory"}`, param: claimActorMember},
		{name: "a non-boolean force", body: `{"actor":"alice","force":"yes"}`, param: releaseForceMember},
		{name: "an explicitly null force", body: `{"actor":"alice","force":null}`, param: releaseForceMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releaser := &roleReleaser{}
			ts := newReleaseServer(t, releaser)

			resp := ts.releaseIssue(t, releasePath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			if got := releaser.releaseRequests(); len(got) != 0 {
				t.Errorf("a refused body reached the role: %+v", got)
			}
		})
	}
}

// TestReleaseRefusesAReleaserThatAnswersWithNothing pins checkedReleaser. The
// handler dereferences the result's issue and reads its RowVersion, so a
// caller-supplied role reporting success without a row is a nil dereference on
// a live server — and there is no wire code that honestly describes it.
func TestReleaseRefusesAReleaserThatAnswersWithNothing(t *testing.T) {
	ts := newReleaseServer(t, &roleReleaser{result: issueops.ReleaseResult{Changed: true}})

	resp := ts.releaseIssue(t, releasePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	// THE STATUS ALONE PROVES NOTHING, which is why this asserts the log. The
	// server recovers panics into the same 500 with the same code, so a test
	// that stopped at the status would pass with the wrapper deleted and the
	// handler dereferencing nil on a live server. What the wrapper buys is the
	// fault ARRIVING AS AN ERROR NAMING ITSELF, which is exactly what the panic
	// it replaces did not produce.
	line := findLogLine(t, ts.stderr.String(), "the releaser reported success without an issue")
	if !strings.Contains(line, "bd-1") {
		t.Errorf("the logged fault does not name the issue it was asked about:\n%s", line)
	}
}

// TestReleasePathReachesItsHandler drives the path the DOCUMENT spells, which
// is the one thing route parity cannot check for a custom-method row: it
// declares its spec path instead of deriving it from the pattern. The parity
// test bounds the shape of that exception; only a request proves the pattern
// serves the documented path — and, on a shared dispatcher, that this verb is
// not answered by whichever row happens to be first.
func TestReleasePathReachesItsHandler(t *testing.T) {
	releaser := &roleReleaser{result: issueops.ReleaseResult{Issue: releasedIssue("bd-1", 1), Changed: true}}
	ts := newReleaseServer(t, releaser)

	resp := ts.releaseIssue(t, releasePath, `{"actor":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented release path: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	line := findLogLine(t, ts.stderr.String(), "path="+releasePath)
	if !strings.Contains(line, "op="+OpReleaseIssue) {
		t.Errorf("the documented release path is served by another operation:\n%s", line)
	}
}

// TestReleaseIsNotReachableThroughTheClaimsErrorVocabulary guards the reuse
// decision from the direction that would make it wrong. `already_claimed` is
// borrowed for the ownership fence; the claim's OTHER conflict, not_claimable,
// is deliberately absent from this operation, because an open unassigned row is
// the most claimable row a workspace has and answering that about a release
// refusal would send a client somewhere there is nothing to find.
func TestReleaseIsNotReachableThroughTheClaimsErrorVocabulary(t *testing.T) {
	for _, code := range operationCodes[OpReleaseIssue] {
		if code == CodeNotClaimable {
			t.Fatalf("releaseIssue documents %s; see CodeNotReleasable for why that reuse was refused", code)
		}
	}
	// And the borrowed one is really borrowed rather than re-minted.
	if !containsCode(operationCodes[OpReleaseIssue], CodeAlreadyClaimed) {
		t.Errorf("releaseIssue does not document %s; the ownership fence has no other code", CodeAlreadyClaimed)
	}
}

func containsCode(codes []Code, want Code) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }

// releaseSentinels is the set failRelease must classify, asserted as a set so a
// sentinel added to the role without a wire arm fails here rather than reaching
// a client as a 500.
func TestEveryReleaseSentinelIsClassified(t *testing.T) {
	for _, sentinel := range []error{
		issueops.ErrNotClaimed,
		issueops.ErrNotReleasable,
		issueops.ErrNotOwner,
		issueops.ErrAssigneeMismatch,
		issueops.ErrNotFound,
	} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			ts := newReleaseServer(t, &roleReleaser{err: fmt.Errorf("release bd-1: %w", sentinel)})

			resp := ts.releaseIssue(t, releasePath, `{"actor":"alice","expected_assignee":"bob"}`)
			if resp.StatusCode >= http.StatusInternalServerError {
				t.Fatalf("%v reached the client as a %d; it must be classified in failRelease: %s",
					sentinel, resp.StatusCode, readAll(t, resp))
			}
			code := Code(decodeBody(t, resp)["code"].(string))
			if !containsCode(operationCodes[OpReleaseIssue], code) {
				t.Errorf("%v produced %s, which this operation does not document", sentinel, code)
			}
			if errors.Is(sentinel, issueops.ErrValidation) {
				t.Fatalf("%v is wrapped in ErrValidation; failRelease's ordering assumes it is not", sentinel)
			}
		})
	}
}
