package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of POST /v0/beads/issues/{id}:heartbeat: the
// body shape, the refusals the handler raises itself, how the role's typed
// refusals reach the wire, and the projection of the role's result. Everything
// below the wire (the lease-only write, the in_progress fence, the re-arm) is
// issueops.Heartbeater's, pinned by backend/conformance/heartbeater_contract.go
// on every leg, the served one included.

const heartbeatPath = "/v0/beads/issues/bd-1:heartbeat"

func newHeartbeatServer(t *testing.T, heartbeater *roleHeartbeater) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Heartbeater: heartbeater}))
}

// TestHeartbeatMapsTheRequestAndProjectsTheLease pins both halves of a
// success: the path id and the trimmed actor reach the role, and the role's
// result reaches the wire member for member — with NO `revision` and no issue
// body, because the response reports the lease, not the issue: even after a
// re-arm, which reminted the revision, `rearmed` is how the caller learns to
// re-read the issue.
func TestHeartbeatMapsTheRequestAndProjectsTheLease(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	heartbeater := &roleHeartbeater{result: issueops.HeartbeatResult{
		IssueID:        "bd-1",
		Holder:         "worker",
		HeartbeatAt:    at,
		LeaseExpiresAt: at.Add(5 * time.Minute),
		Rearmed:        true,
	}}
	ts := newHeartbeatServer(t, heartbeater)

	resp := ts.claim(t, heartbeatPath, `{"actor":"  worker  "}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := heartbeater.heartbeatRequests()
	if len(calls) != 1 {
		t.Fatalf("role calls = %d, want 1", len(calls))
	}
	if want := (issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-1"}); calls[0] != want {
		t.Fatalf("role request = %+v, want %+v", calls[0], want)
	}

	body := decodeBody(t, resp)
	want := map[string]any{
		"id":               "bd-1",
		"holder":           "worker",
		"heartbeat_at":     "2026-10-16T12:00:00Z",
		"lease_expires_at": "2026-10-16T12:05:00Z",
		"rearmed":          true,
	}
	if len(body) != len(want) {
		t.Errorf("response members = %v, want exactly %v", body, want)
	}
	for member, value := range want {
		if body[member] != value {
			t.Errorf("%s = %v, want %v", member, body[member], value)
		}
	}
	for _, absent := range []string{"revision", "issue", "changed"} {
		if _, present := body[absent]; present {
			t.Errorf("response carries %q; a heartbeat answers the lease, not the issue", absent)
		}
	}
}

// TestHeartbeatRefusesABadRequestBeforeTheRole pins the edge's own refusals:
// each is a 400 naming the offender, and none reaches the role.
func TestHeartbeatRefusesABadRequestBeforeTheRole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		body  string
		param string
	}{
		{"a missing actor", heartbeatPath, `{}`, "actor"},
		{"a blank actor", heartbeatPath, `{"actor":"   "}`, "actor"},
		{"a null actor", heartbeatPath, `{"actor":null}`, "actor"},
		{"a non-string actor", heartbeatPath, `{"actor":7}`, "actor"},
		{"an actor with a newline", heartbeatPath, `{"actor":"a\nb"}`, "actor"},
		{"an actor one character over the column", heartbeatPath, `{"actor":"` + strings.Repeat("a", types.MaxFieldLen+1) + `"}`, "actor"},
		{"an unknown member", heartbeatPath, `{"actor":"worker","ttl_seconds":60}`, "ttl_seconds"},
		{"an expected_version member", heartbeatPath, `{"actor":"worker","expected_version":"3"}`, "expected_version"},
		{"a query parameter", heartbeatPath + "?force=true", `{"actor":"worker"}`, "force"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			heartbeater := &roleHeartbeater{}
			ts := newHeartbeatServer(t, heartbeater)
			resp := ts.claim(t, tc.path, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param {
				t.Fatalf("problem = %v, want invalid_argument naming %q", body, tc.param)
			}
			if calls := heartbeater.heartbeatRequests(); len(calls) != 0 {
				t.Fatalf("the role was called %d times for a request the edge refuses", len(calls))
			}
		})
	}
}

// TestHeartbeatAnswersTheRefusalsTheRoleRaises walks the role's typed refusals.
//
// The 409 arms are matched BEFORE the generic ones, and two rows here exist to
// pin that: ErrLeaseNotHeld also matches ErrNotClaimable, which the generic
// classifier would answer `not_claimable` — the code this operation must not
// answer — and a foreign holder must arrive in the claim's own conflict shape,
// naming the holder.
func TestHeartbeatAnswersTheRefusalsTheRoleRaises(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		code    Code
		param   string
		members map[string]any
		absent  []string
		// detail must appear in a lease_not_held detail, and none of
		// notDetail may: each refusal names its own state only.
		detail    string
		notDetail []string
	}{
		{
			name: "a foreign holder",
			err: &issueops.LeaseConflictError{
				IssueID: "bd-1", Holder: "alice", Status: types.StatusInProgress,
				Err: fmt.Errorf("%w: bd-1 is held by alice", issueops.ErrAlreadyClaimed),
			},
			status:  http.StatusConflict,
			code:    CodeAlreadyClaimed,
			members: map[string]any{"assignee": "alice", "issue_status": "in_progress"},
		},
		{
			name: "no live claim (closed)",
			err: &issueops.LeaseConflictError{
				IssueID: "bd-1", Status: types.StatusClosed, Err: issueops.ErrLeaseNotHeld,
			},
			status:    http.StatusConflict,
			code:      CodeLeaseNotHeld,
			members:   map[string]any{"issue_status": "closed"},
			absent:    []string{"assignee", "reason"},
			detail:    "it is closed",
			notDetail: []string{"ephemeral", "open"},
		},
		{
			name: "no live claim (released: open, unassigned)",
			err: &issueops.LeaseConflictError{
				IssueID: "bd-1", Status: types.StatusOpen, Err: issueops.ErrLeaseNotHeld,
			},
			status:    http.StatusConflict,
			code:      CodeLeaseNotHeld,
			members:   map[string]any{"issue_status": "open"},
			absent:    []string{"assignee", "reason"},
			detail:    "not claimed",
			notDetail: []string{"ephemeral", "closed"},
		},
		{
			name: "a wisp",
			err: &issueops.LeaseConflictError{
				IssueID: "bd-1", Status: types.StatusInProgress, Ephemeral: true,
				Err: fmt.Errorf("%w: bd-1 is ephemeral", issueops.ErrLeaseNotHeld),
			},
			status:    http.StatusConflict,
			code:      CodeLeaseNotHeld,
			members:   map[string]any{"issue_status": "in_progress", "reason": string(ReasonEphemeral)},
			absent:    []string{"assignee"},
			detail:    "ephemeral wisp",
			notDetail: []string{"closed", "open"},
		},
		{
			name:   "an absent id",
			err:    fmt.Errorf("%w: bd-1", issueops.ErrNotFound),
			status: http.StatusNotFound,
			code:   CodeNotFound,
		},
		{
			name:   "the role's own actor rule",
			err:    &issueops.HeartbeatFieldError{Field: issueops.HeartbeatFieldActor, Detail: "validation failed: actor is too long"},
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
			param:  "actor",
		},
		{
			name:   "some other validation refusal",
			err:    fmt.Errorf("%w: refused", issueops.ErrValidation),
			status: http.StatusBadRequest,
			code:   CodeInvalidArgument,
		},
		{
			name:   "a storage fault",
			err:    errors.New("dial tcp 10.0.0.1:3306: connection refused"),
			status: http.StatusInternalServerError,
			code:   CodeInternal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newHeartbeatServer(t, &roleHeartbeater{err: tc.err})
			resp := ts.claim(t, heartbeatPath, `{"actor":"worker"}`)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(tc.code) {
				t.Fatalf("code = %v, want %s (%v)", body["code"], tc.code, body)
			}
			if tc.param != "" && body["param"] != tc.param {
				t.Errorf("param = %v, want %q", body["param"], tc.param)
			}
			for member, value := range tc.members {
				if body[member] != value {
					t.Errorf("%s = %v, want %v", member, body[member], value)
				}
			}
			for _, member := range tc.absent {
				if _, present := body[member]; present {
					t.Errorf("%s carries %q; it is documented only beside already_claimed", tc.code, member)
				}
			}
			detail, _ := body["detail"].(string)
			if strings.Contains(detail, "10.0.0.1") {
				t.Errorf("detail leaks the storage error: %q", detail)
			}
			// The lease_not_held rows share the code, so the detail names the
			// one state the role observed, and nothing else.
			if tc.detail != "" && !strings.Contains(detail, tc.detail) {
				t.Errorf("detail = %q, want it to say %q", detail, tc.detail)
			}
			for _, not := range tc.notDetail {
				if strings.Contains(detail, not) {
					t.Errorf("detail = %q names %q; it must name only the state the role observed", detail, not)
				}
			}
		})
	}
}

// TestHeartbeatDocumentsItsOwnConflictVocabulary pins the operation's code
// row from the direction that would make it wrong: `lease_not_held` is minted
// for the unheld case, so the claim's `not_claimable` must not be documented
// here, and the fence is the holder, so there is no precondition_failed.
func TestHeartbeatDocumentsItsOwnConflictVocabulary(t *testing.T) {
	codes := operationCodes[OpHeartbeatIssue]
	for _, want := range []Code{CodeAlreadyClaimed, CodeLeaseNotHeld, CodeNotFound, CodeInvalidArgument} {
		if !containsCode(codes, want) {
			t.Errorf("heartbeatIssue does not document %s", want)
		}
	}
	for _, refused := range []Code{CodeNotClaimable, CodePreconditionFailed} {
		if containsCode(codes, refused) {
			t.Errorf("heartbeatIssue documents %s; see CodeLeaseNotHeld", refused)
		}
	}
	if got := CodeLeaseNotHeld.Status(); got != http.StatusConflict {
		t.Errorf("lease_not_held status = %d, want 409", got)
	}
}

// TestHeartbeatIsAdvertisedAsItsOwnCapability pins the token a client
// preflights on.
func TestHeartbeatIsAdvertisedAsItsOwnCapability(t *testing.T) {
	for _, token := range Capabilities() {
		if token == "issues.heartbeat" {
			return
		}
	}
	t.Fatalf("Capabilities() = %v, want it to advertise issues.heartbeat", Capabilities())
}
