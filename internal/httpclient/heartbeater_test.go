package httpclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// The unit half of the Heartbeater client leg: the
// preflight, the request rules, the problem mapping and the replay, over the
// real wire client against a doctored server. The contract itself runs against
// a real bd serve in served_heartbeater_test.go.

// heartbeatStub is a doctored bd serve: a handshake advertising caps, and a
// :heartbeat route that answers each POST with the next canned reply.
type heartbeatStub struct {
	mu      sync.Mutex
	posts   []string // the raw bodies, in arrival order
	paths   []string
	replies []func(http.ResponseWriter)
}

func (h *heartbeatStub) store(t *testing.T, caps ...string) *Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == wire.PathContext {
			ctx := v0Context("proj-hb")
			ctx.Capabilities = caps
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ctx)
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, wire.MethodHeartbeat) {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.posts = append(h.posts, string(body))
		h.paths = append(h.paths, r.URL.Path)
		var reply func(http.ResponseWriter)
		if len(h.replies) > 0 {
			reply, h.replies = h.replies[0], h.replies[1:]
		}
		h.mu.Unlock()
		if reply == nil {
			t.Errorf("an unexpected heartbeat POST reached the server: %s", body)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		reply(w)
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := wire.New(base, nil, wire.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return New(Target{BaseURL: base}, client, nil)
}

func (h *heartbeatStub) postCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.posts)
}

func heartbeatOK(id, holder string, rearmed bool) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "holder": holder, "rearmed": rearmed,
			"heartbeat_at":     "2026-10-16T01:00:00Z",
			"lease_expires_at": "2026-10-16T01:05:00Z",
		})
	}
}

func heartbeatProblem(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func heartbeaterOf(t *testing.T, s *Store) issueops.Heartbeater {
	t.Helper()
	role, err := s.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	return role
}

// TestHeartbeatAgainstAServerWithoutTheTokenRefusesBeforeTheDial is the skew
// row for a new client on an old server: no issues.heartbeat in the handshake
// means a case-2 refusal NAMING the token, with nothing sent — never a
// fallback to a re-claim or a metadata stamp.
func TestHeartbeatAgainstAServerWithoutTheTokenRefusesBeforeTheDial(t *testing.T) {
	stub := &heartbeatStub{}
	s := stub.store(t, "issues.get", "issues.claim", "issues.release", "issues.reclaim")

	_, err := heartbeaterOf(t, s).Heartbeat(t.Context(), issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-1"})
	var capErr *wire.CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("Heartbeat on an old server = %v, want *wire.CapabilityError", err)
	}
	if capErr.Capability != "issues.heartbeat" {
		t.Errorf("CapabilityError.Capability = %q, want issues.heartbeat", capErr.Capability)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) || unsupported.Capability != "issues.heartbeat" {
		t.Errorf("Heartbeat on an old server = %v, want it to unwrap to *storage.ErrUnsupported{Capability: issues.heartbeat}", err)
	}
	if !strings.Contains(err.Error(), "issues.heartbeat") {
		t.Errorf("the refusal %q does not name the token", err)
	}
	if n := stub.postCount(); n != 0 {
		t.Errorf("%d heartbeat POSTs reached an old server, want 0", n)
	}

	// The legacy front door rides the same role, so it refuses the same way.
	if err := s.HeartbeatIssue(t.Context(), "bd-1", "worker"); !errors.As(err, &capErr) {
		t.Errorf("HeartbeatIssue on an old server = %v, want *wire.CapabilityError", err)
	}
}

// TestHeartbeatSendsTheActorVerbatimAndProjectsTheAnswer pins the request the
// client builds (the id in the path, the actor as the one body member, sent
// untrimmed: it is the fence's subject) and the result it reads back.
func TestHeartbeatSendsTheActorVerbatimAndProjectsTheAnswer(t *testing.T) {
	stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatOK("bd-7", " worker ", true)}}
	s := stub.store(t, "issues.heartbeat")
	req := issueops.HeartbeatRequest{Actor: " worker ", IssueID: "bd-7"}

	got, err := heartbeaterOf(t, s).Heartbeat(t.Context(), req)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if req.Actor != " worker " || req.IssueID != "bd-7" {
		t.Errorf("the request was mutated: %+v", req)
	}
	if stub.paths[0] != wire.PathIssues+"/bd-7"+wire.MethodHeartbeat {
		t.Errorf("dialed %q, want the issue's :heartbeat", stub.paths[0])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(stub.posts[0]), &body); err != nil {
		t.Fatalf("decode the sent body %q: %v", stub.posts[0], err)
	}
	if len(body) != 1 || body["actor"] != " worker " {
		t.Errorf("sent body %s, want exactly {\"actor\":\" worker \"}", stub.posts[0])
	}
	want := issueops.HeartbeatResult{
		IssueID: "bd-7", Holder: " worker ", Rearmed: true,
		HeartbeatAt:    time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC),
		LeaseExpiresAt: time.Date(2026, 10, 16, 1, 5, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("Heartbeat = %+v, want %+v", got, want)
	}
}

// TestHeartbeatMapsTheRefusalsOntoTheRolesTypes pins each documented refusal
// to the role's typed answer. already_claimed is the sharp one: the shared
// problem table rebuilds it as the CLAIM's conflict, and on this operation it
// must read as the heartbeat's, naming the holder in the native words.
func TestHeartbeatMapsTheRefusalsOntoTheRolesTypes(t *testing.T) {
	t.Run("already_claimed is a LeaseConflictError naming the holder", func(t *testing.T) {
		stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatProblem(http.StatusConflict,
			`{"status":409,"code":"already_claimed","title":"conflict","detail":"another actor holds this issue's claim; nothing on the issue was written","assignee":"alice","issue_status":"in_progress"}`)}}
		_, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
			issueops.HeartbeatRequest{Actor: "bob", IssueID: "bd-1"})
		var conflict *issueops.LeaseConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("Heartbeat = %v (%T), want *issueops.LeaseConflictError", err, err)
		}
		if conflict.Holder != "alice" || conflict.IssueID != "bd-1" || conflict.Status != "in_progress" {
			t.Errorf("LeaseConflictError = %+v, want Holder alice, IssueID bd-1, Status in_progress", conflict)
		}
		if !errors.Is(err, issueops.ErrAlreadyClaimed) || errors.Is(err, issueops.ErrLeaseNotHeld) {
			t.Errorf("Heartbeat = %v: want ErrAlreadyClaimed and not ErrLeaseNotHeld", err)
		}
		var claim *issueops.ClaimConflictError
		if errors.As(err, &claim) {
			t.Errorf("Heartbeat = %v still carries the claim's ClaimConflictError", err)
		}
		if !strings.Contains(err.Error(), "claimed by alice") {
			t.Errorf("Heartbeat error %q does not read as the native %q", err, "issue already claimed by alice")
		}
	})

	t.Run("lease_not_held wraps ErrLeaseNotHeld and ErrNotClaimable", func(t *testing.T) {
		stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatProblem(http.StatusConflict,
			`{"status":409,"code":"lease_not_held","title":"conflict","detail":"this actor holds no live claim on this issue: it is closed; nothing on the issue was written","issue_status":"closed"}`)}}
		_, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
			issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-2"})
		var conflict *issueops.LeaseConflictError
		if !errors.As(err, &conflict) || conflict.Status != "closed" || conflict.Holder != "" || conflict.Ephemeral {
			t.Fatalf("Heartbeat = %v, want *issueops.LeaseConflictError{Status: closed, Holder: \"\", Ephemeral: false}", err)
		}
		if !errors.Is(err, issueops.ErrLeaseNotHeld) || !errors.Is(err, issueops.ErrNotClaimable) || errors.Is(err, issueops.ErrAlreadyClaimed) {
			t.Errorf("Heartbeat = %v: want ErrLeaseNotHeld and ErrNotClaimable, not ErrAlreadyClaimed", err)
		}
		if !strings.Contains(conflict.Error(), "bd-2 status closed") || strings.Contains(err.Error(), "ephemeral") {
			t.Errorf("Heartbeat error %q (typed %q): want the native %q and no mention of a wisp", err, conflict, "bd-2 status closed")
		}
		if !strings.Contains(err.Error(), "it is closed") {
			t.Errorf("Heartbeat error %q drops the server's detail", err)
		}
	})

	t.Run("lease_not_held with reason ephemeral is a wisp", func(t *testing.T) {
		stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatProblem(http.StatusConflict,
			`{"status":409,"code":"lease_not_held","title":"conflict","detail":"this issue is an ephemeral wisp, which is never leased; nothing on the issue was written","issue_status":"in_progress","reason":"ephemeral"}`)}}
		_, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
			issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-w"})
		var conflict *issueops.LeaseConflictError
		if !errors.As(err, &conflict) || !conflict.Ephemeral || conflict.Status != "in_progress" {
			t.Fatalf("Heartbeat = %v, want *issueops.LeaseConflictError{Ephemeral: true, Status: in_progress}", err)
		}
		if !errors.Is(err, issueops.ErrLeaseNotHeld) || !strings.Contains(conflict.Error(), "bd-w is ephemeral") {
			t.Errorf("Heartbeat error %q (typed %q): want ErrLeaseNotHeld in the native %q", err, conflict, "bd-w is ephemeral")
		}
	})

	t.Run("not_found is ErrNotFound", func(t *testing.T) {
		stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatProblem(http.StatusNotFound,
			`{"status":404,"code":"not_found","title":"not found"}`)}}
		_, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
			issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-404"})
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Heartbeat = %v, want storage.ErrNotFound", err)
		}
	})

	t.Run("an invalid actor is the role's field error", func(t *testing.T) {
		stub := &heartbeatStub{replies: []func(http.ResponseWriter){heartbeatProblem(http.StatusBadRequest,
			`{"status":400,"code":"invalid_argument","title":"bad","param":"actor","reason":"invalid_value","detail":"actor must not contain control characters"}`)}}
		_, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
			issueops.HeartbeatRequest{Actor: "work\u0007er", IssueID: "bd-3"})
		var field *issueops.HeartbeatFieldError
		if !errors.As(err, &field) || field.Field != issueops.HeartbeatFieldActor || !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("Heartbeat = %v, want *issueops.HeartbeatFieldError{Field: actor} matching ErrValidation", err)
		}
	})
}

// TestHeartbeatReplaysABusyAnswer pins the replayable flag: the one write this
// client marks idempotent, because a repeat only moves the expiry later. A 503
// is retried, not handed to a worker that stops on any nonzero exit.
func TestHeartbeatReplaysABusyAnswer(t *testing.T) {
	stub := &heartbeatStub{replies: []func(http.ResponseWriter){
		func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "0")
			heartbeatProblem(http.StatusServiceUnavailable, `{"status":503,"code":"busy","title":"busy"}`)(w)
		},
		heartbeatOK("bd-9", "worker", false),
	}}
	got, err := heartbeaterOf(t, stub.store(t, "issues.heartbeat")).Heartbeat(t.Context(),
		issueops.HeartbeatRequest{Actor: "worker", IssueID: "bd-9"})
	if err != nil {
		t.Fatalf("Heartbeat after one busy answer = %v, want the replay's success", err)
	}
	if got.IssueID != "bd-9" {
		t.Errorf("Heartbeat = %+v, want bd-9", got)
	}
	if n := stub.postCount(); n != 2 {
		t.Errorf("%d heartbeat POSTs, want 2 (the busy one and its replay)", n)
	}
}

// TestHeartbeatRefusesBeforeTheDial pins the request rules: each is the role's
// *HeartbeatFieldError, and none reaches the server.
func TestHeartbeatRefusesBeforeTheDial(t *testing.T) {
	stub := &heartbeatStub{}
	role := heartbeaterOf(t, stub.store(t, "issues.heartbeat"))
	for name, tc := range map[string]struct {
		req   issueops.HeartbeatRequest
		field string
	}{
		"blank actor":     {issueops.HeartbeatRequest{Actor: " \t", IssueID: "bd-1"}, issueops.HeartbeatFieldActor},
		"over-long actor": {issueops.HeartbeatRequest{Actor: strings.Repeat("a", 256), IssueID: "bd-1"}, issueops.HeartbeatFieldActor},
		"empty id":        {issueops.HeartbeatRequest{Actor: "worker"}, issueops.HeartbeatFieldIssueID},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := role.Heartbeat(t.Context(), tc.req)
			var field *issueops.HeartbeatFieldError
			if !errors.As(err, &field) || field.Field != tc.field || !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("Heartbeat(%s) = %v, want *issueops.HeartbeatFieldError{Field: %s}", name, err, tc.field)
			}
		})
	}
	if n := stub.postCount(); n != 0 {
		t.Errorf("%d invalid heartbeats reached the server, want 0", n)
	}
}

// TestHeartbeatValidationMatchesTheSharedValidator binds heartbeater.go's
// restated rules to internal/storage/issueops.ValidateHeartbeatRequest, for
// TestReclaimValidationMatchesTheSharedValidator's reason.
func TestHeartbeatValidationMatchesTheSharedValidator(t *testing.T) {
	for name, req := range map[string]issueops.HeartbeatRequest{
		"valid":                 {Actor: "worker", IssueID: "bd-1"},
		"padded actor is valid": {Actor: " worker ", IssueID: "bd-1"},
		"blank actor":           {Actor: "  ", IssueID: "bd-1"},
		"absent actor":          {IssueID: "bd-1"},
		"at the column":         {Actor: strings.Repeat("a", 255), IssueID: "bd-1"},
		"over the column":       {Actor: strings.Repeat("a", 256), IssueID: "bd-1"},
		"empty id":              {Actor: "worker"},
		"blank actor and id":    {},
		"padded id is an id":    {Actor: "worker", IssueID: " bd-1 "},
	} {
		t.Run(name, func(t *testing.T) {
			mine := validateHeartbeatRequest(req)
			shared := storeops.ValidateHeartbeatRequest(req)
			if (mine == nil) != (shared == nil) {
				t.Fatalf("validateHeartbeatRequest = %v, storeops.ValidateHeartbeatRequest = %v: the client's restatement has drifted", mine, shared)
			}
			if mine == nil {
				return
			}
			var mineField, sharedField *issueops.HeartbeatFieldError
			if !errors.As(mine, &mineField) || !errors.As(shared, &sharedField) {
				t.Fatalf("validateHeartbeatRequest = %v, shared = %v: want both *issueops.HeartbeatFieldError", mine, shared)
			}
			if *mineField != *sharedField {
				t.Errorf("validateHeartbeatRequest = %+v, shared = %+v", *mineField, *sharedField)
			}
		})
	}
}

// TestLegacyHeartbeatIssueRidesTheRole pins the raw front door onto the role:
// one POST with the same body, and the role's typed refusal back.
func TestLegacyHeartbeatIssueRidesTheRole(t *testing.T) {
	stub := &heartbeatStub{replies: []func(http.ResponseWriter){
		heartbeatOK("bd-5", "worker", false),
		heartbeatProblem(http.StatusConflict,
			`{"status":409,"code":"already_claimed","title":"conflict","assignee":"alice","issue_status":"in_progress"}`),
	}}
	s := stub.store(t, "issues.heartbeat")
	if err := s.HeartbeatIssue(t.Context(), "bd-5", "worker"); err != nil {
		t.Fatalf("HeartbeatIssue: %v", err)
	}
	err := s.HeartbeatIssue(t.Context(), "bd-5", "worker")
	var conflict *issueops.LeaseConflictError
	if !errors.As(err, &conflict) || conflict.Holder != "alice" || !errors.Is(err, storage.ErrAlreadyClaimed) {
		t.Errorf("HeartbeatIssue against a foreign holder = %v, want *issueops.LeaseConflictError{Holder: alice}", err)
	}
	if err := s.HeartbeatIssue(t.Context(), "bd-5", ""); !errors.Is(err, issueops.ErrValidation) {
		t.Errorf("HeartbeatIssue with no actor = %v, want ErrValidation before the dial", err)
	}
	if n := stub.postCount(); n != 2 {
		t.Errorf("%d heartbeat POSTs, want 2", n)
	}
}
