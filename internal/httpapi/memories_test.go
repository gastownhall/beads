package httpapi

import (
	"net/http"
	"testing"

	"github.com/steveyegge/beads/memoryops"
)

// The pins for the memory operations. What is asserted here is the WIRE EDGE —
// that the handler decodes the document's members into the role's request
// faithfully, refuses what the document refuses, and does not re-implement
// anything the role owns. What a memory MEANS (the key derivation, the storage
// encoding, which plane a row belongs to) is the conformance contract's, and is
// deliberately not re-asserted here.

const memoriesPath = "/v0/beads/memories"

func (ts *testServer) remember(t *testing.T, body string) *http.Response {
	t.Helper()
	return ts.claimRequest(t, memoriesPath, "application/json", body)
}

// TestRememberForwardsBothDocumentedMembers is the operation's central pin:
// `content` and `key` reach the role's request unchanged, key VERBATIM.
//
// Asserted on the REQUEST the role received rather than on the response: a
// response echoing the right key says nothing about which bytes were stored,
// and a handler that trimmed the key would put the memory somewhere the client
// cannot name.
func TestRememberForwardsBothDocumentedMembers(t *testing.T) {
	memories := &roleMemories{remembered: memoryops.RememberResult{
		Key: "Has Spaces.✓", Value: "  keep\nme  ", Replaced: true,
	}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.remember(t, `{"content":"  keep\nme  ","key":"Has Spaces.✓"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := memories.rememberRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d remembers, want 1", len(reqs))
	}
	want := memoryops.RememberRequest{Content: "  keep\nme  ", Key: "Has Spaces.✓"}
	if reqs[0] != want {
		t.Errorf("role received %#v, want %#v", reqs[0], want)
	}

	body := decodeBody(t, resp)
	if got := body["key"]; got != "Has Spaces.✓" {
		t.Errorf("key = %v, want the stored key verbatim", got)
	}
	if got := body["value"]; got != "  keep\nme  " {
		t.Errorf("value = %q, want the content verbatim — this plane does not flatten what it stores", got)
	}
	if got := body["replaced"]; got != true {
		t.Errorf("replaced = %v, want true", got)
	}
}

// TestRememberWithoutAKeyLeavesTheDerivationToTheRole: an omitted `key` reaches
// the role as the empty string, which is what tells it to derive one, and the
// response is where the caller learns the answer.
//
// The handler must not derive: memoryapi.DeriveKey is importable from cmd/bd
// for the CLI's desire path, so a handler could call it — and then two places
// would decide where a memory lands.
func TestRememberWithoutAKeyLeavesTheDerivationToTheRole(t *testing.T) {
	memories := &roleMemories{remembered: memoryops.RememberResult{
		Key: "always-run-tests-with-race", Value: "always run tests with -race",
	}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.remember(t, `{"content":"always run tests with -race"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := memories.rememberRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d remembers, want 1", len(reqs))
	}
	if reqs[0].Key != "" {
		t.Errorf("role received key %q, want the empty string: the handler must not derive", reqs[0].Key)
	}

	body := decodeBody(t, resp)
	if got := body["key"]; got != "always-run-tests-with-race" {
		t.Errorf("key = %v, want the key the role reported", got)
	}
	if got := body["replaced"]; got != false {
		t.Errorf("replaced = %v, want false", got)
	}
}

// TestRememberRefusesWhatTheDocumentRefuses covers the body vocabulary. The two
// refusals that are the ROLE's — empty content, and content no key derives from
// — have their own case below, because the point of that one is that they come
// from BELOW the wire.
func TestRememberRefusesWhatTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantParam string
		reason    Reason
	}{
		{
			name:      "unknown member",
			body:      `{"content":"x","ttl":5}`,
			wantParam: "ttl",
			reason:    ReasonUnknownParameter,
		},
		{
			name:      "content missing",
			body:      `{"key":"k"}`,
			wantParam: "content",
			reason:    ReasonInvalidValue,
		},
		{
			name:      "content null",
			body:      `{"content":null}`,
			wantParam: "content",
			reason:    ReasonInvalidValue,
		},
		{
			name:      "content not a string",
			body:      `{"content":42}`,
			wantParam: "content",
			reason:    ReasonInvalidValue,
		},
		{
			name:      "key null",
			body:      `{"content":"x","key":null}`,
			wantParam: "key",
			reason:    ReasonInvalidValue,
		},
		{
			name:      "key not a string",
			body:      `{"content":"x","key":["k"]}`,
			wantParam: "key",
			reason:    ReasonInvalidValue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memories := &roleMemories{}
			ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

			resp := ts.remember(t, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["code"]; got != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
			}
			if got := body["param"]; got != tc.wantParam {
				t.Errorf("param = %v, want %q", got, tc.wantParam)
			}
			if got := body["reason"]; got != string(tc.reason) {
				t.Errorf("reason = %v, want %q", got, tc.reason)
			}
			if n := len(memories.rememberRequests()); n != 0 {
				t.Errorf("the role was called %d times for a refused request, want 0", n)
			}
		})
	}
}

// TestRememberSurfacesTheRolesRefusalAsABadRequest: the two refusals this
// operation has that the handler does NOT implement — empty content, and
// content from which no key can be derived — reach the client as the 400 the
// document promises, carrying the role's own sentence.
//
// It is the same line the sweep and delete handlers draw: the role validates,
// the handler classifies. Widening ClassifyError instead would change what
// every other operation returns for an error it has never produced.
func TestRememberSurfacesTheRolesRefusalAsABadRequest(t *testing.T) {
	memories := &roleMemories{err: memoryops.ErrValidation}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.remember(t, `{"content":"!!!"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["code"]; got != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
	}
	if _, ok := body["param"]; ok {
		t.Errorf("param = %v, want it absent: the role's refusals are about the request, not one member of it", body["param"])
	}
	if got := body["reason"]; got != string(ReasonInvalidValue) {
		t.Errorf("reason = %v, want %q", got, ReasonInvalidValue)
	}
	// The role's own sentence, not a rewrite of it: `bd remember` and this
	// endpoint refuse the same input with the same words.
	if got, _ := body["detail"].(string); got != memoryops.ErrValidation.Error() {
		t.Errorf("detail = %q, want the role's message", got)
	}
}

// TestRememberRefusesAQueryString: a write whose narrowing the server silently
// ignored is the failure the unknown-parameter rule exists to prevent, and this
// operation takes no parameters at all.
func TestRememberRefusesAQueryString(t *testing.T) {
	memories := &roleMemories{}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.claimRequest(t, memoriesPath+"?key=k", "application/json", `{"content":"x"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["param"]; got != "key" {
		t.Errorf("param = %v, want \"key\"", got)
	}
	if got := body["reason"]; got != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %q", got, ReasonUnknownParameter)
	}
	if n := len(memories.rememberRequests()); n != 0 {
		t.Errorf("the role was called %d times for a refused request, want 0", n)
	}
}
