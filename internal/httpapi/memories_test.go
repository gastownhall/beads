package httpapi

import (
	"net/http"
	"slices"
	"strings"
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

func (ts *testServer) forget(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.base+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
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

// TestGetMemoryAnswersTheStoredValue: the path segment reaches the role
// verbatim — percent-decoded once and not otherwise touched — and the answer is
// the stored bytes.
//
// The key here carries a space, a dot and a non-ASCII rune on purpose:
// `bd remember --key` accepts any string, so a handler that slugged, folded or
// trimmed the segment would answer about a different memory than the one asked
// for.
func TestGetMemoryAnswersTheStoredValue(t *testing.T) {
	memories := &roleMemories{recalled: memoryops.RecallResult{
		Key: "Has Spaces.✓", Value: "  the\nstored bytes  ", Found: true,
	}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.get(t, memoriesPath+"/Has%20Spaces.%E2%9C%93")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := memories.recallRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d recalls, want 1", len(reqs))
	}
	if reqs[0].Key != "Has Spaces.✓" {
		t.Errorf("role received key %q, want the decoded segment verbatim", reqs[0].Key)
	}

	body := decodeBody(t, resp)
	if got := body["value"]; got != "  the\nstored bytes  " {
		t.Errorf("value = %q, want the stored content verbatim", got)
	}
	// No redaction on this plane, deliberately: there is no member to carry it
	// and no value to withhold.
	if _, ok := body["redacted"]; ok {
		t.Error("the response carries `redacted`; this plane has no redaction, and a member that says otherwise is a promise the key-name heuristic cannot keep")
	}
}

// TestGetMemoryAnswersAMissWithA404 is the deliberate divergence from the
// settings surface's no-404 doctrine.
//
// Both legs are the SAME answer from the role — Found false — and both are a
// 404: a row stored as the empty string is a miss here because the storage seam
// cannot tell it from an absent row, and the wire does not claim to see what
// the role cannot. `GET /v0/beads/memories` enumerating such a row is the one
// way a client tells them apart.
func TestGetMemoryAnswersAMissWithA404(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result memoryops.RecallResult
	}{
		{name: "nothing stored", result: memoryops.RecallResult{Key: "gone"}},
		{name: "stored as the empty string", result: memoryops.RecallResult{Key: "gone", Value: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, rolesConfig(Config{Memories: &roleMemories{recalled: tc.result}}))

			resp := ts.get(t, memoriesPath+"/gone")
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["code"]; got != string(CodeNotFound) {
				t.Errorf("code = %v, want %q", got, CodeNotFound)
			}
			if body["request_id"] == nil {
				t.Error("no request_id on the problem body")
			}
			// The detail is about the MEMORY plane. Reusing the issue-id
			// sentence would tell a client its memory key was an issue id.
			if got, _ := body["detail"].(string); !strings.Contains(got, "memory") {
				t.Errorf("detail = %q, want it to name the memory plane", got)
			}
		})
	}
}

// TestGetMemoryRefusesAKeyItCannotAddress covers the two 400s, and the second
// one is the documented consequence worth pinning: a control character is
// refused at the door rather than looked up, so a memory stored under such a
// key is unreachable by path while the ROLE stays verbatim and the CLI still
// recalls it.
//
// The refusal is a 400 and not the 404 beside it, because a 404 would be a
// claim about storage that nothing here asked storage about.
func TestGetMemoryRefusesAKeyItCannotAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "empty after trimming", path: memoriesPath + "/%20%20"},
		{name: "control character", path: memoriesPath + "/bad%0Akey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memories := &roleMemories{recalled: memoryops.RecallResult{Found: true, Value: "v"}}
			ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

			resp := ts.get(t, tc.path)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["param"]; got != "key" {
				t.Errorf("param = %v, want \"key\"", got)
			}
			if got := body["reason"]; got != string(ReasonInvalidValue) {
				t.Errorf("reason = %v, want %q", got, ReasonInvalidValue)
			}
			if n := len(memories.recallRequests()); n != 0 {
				t.Errorf("the role was called %d times for a key the door refused, want 0", n)
			}
		})
	}
}

// TestGetMemoryRefusesAQueryString: this operation takes no parameters, and an
// ignored one is a client's silently unanswered question.
func TestGetMemoryRefusesAQueryString(t *testing.T) {
	memories := &roleMemories{recalled: memoryops.RecallResult{Found: true, Value: "v"}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.get(t, memoriesPath+"/k?verbose=1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["param"]; got != "verbose" {
		t.Errorf("param = %v, want \"verbose\"", got)
	}
	if got := body["reason"]; got != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %q", got, ReasonUnknownParameter)
	}
	if n := len(memories.recallRequests()); n != 0 {
		t.Errorf("the role was called %d times for a refused request, want 0", n)
	}
}

// TestForgetMemoryRemovesTheNamedKeyAndReportsWhatItHeld: the surface's first
// DELETE. The path segment reaches the role verbatim and the 200 body carries
// what was removed, which is what `bd forget` prints.
//
// WHICH ROW WAS REMOVED IS NOT ASSERTED HERE and must not be: the memory plane
// shares one table with the workspace's settings and the generic kv namespace,
// and "exactly the named row" is the role's promise, pinned by its conformance
// contract against three backends. A handler test with a fake role could only
// restate what the fake was told to do.
func TestForgetMemoryRemovesTheNamedKeyAndReportsWhatItHeld(t *testing.T) {
	memories := &roleMemories{forgotten: memoryops.ForgetResult{
		Key: "Has Spaces.✓", Value: "  what it held  ", Found: true,
	}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.forget(t, memoriesPath+"/Has%20Spaces.%E2%9C%93")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := memories.forgetRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d forgets, want 1", len(reqs))
	}
	if reqs[0].Key != "Has Spaces.✓" {
		t.Errorf("role received key %q, want the decoded segment verbatim", reqs[0].Key)
	}

	body := decodeBody(t, resp)
	if got := body["key"]; got != "Has Spaces.✓" {
		t.Errorf("key = %v, want the forgotten key", got)
	}
	if got := body["value"]; got != "  what it held  " {
		t.Errorf("value = %q, want what the memory held, verbatim", got)
	}
}

// TestForgetMemoryOfAnAbsentKeyIs404AndRemovesNothing: Found false is the
// role's answer and NOTHING WAS REMOVED, which is a 404 rather than a 200 with
// an empty body — a client that retried a forget has to be able to tell "I
// removed this" from "there was nothing to remove".
func TestForgetMemoryOfAnAbsentKeyIs404AndRemovesNothing(t *testing.T) {
	memories := &roleMemories{forgotten: memoryops.ForgetResult{Key: "gone"}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.forget(t, memoriesPath+"/gone")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["code"]; got != string(CodeNotFound) {
		t.Errorf("code = %v, want %q", got, CodeNotFound)
	}
	if body["request_id"] == nil {
		t.Error("no request_id on the problem body")
	}
	// The role was still called: whether a key holds a memory is a question for
	// storage, and a handler that answered 404 without asking would be guessing.
	if n := len(memories.forgetRequests()); n != 1 {
		t.Errorf("the role was called %d times, want 1", n)
	}
}

// TestForgetMemoryRefusesAKeyItCannotAddress: the door refuses the same keys
// the read refuses, and the role is never reached — which on a DESTRUCTIVE
// operation is the half that matters. A stored key carrying a control
// character is therefore forgettable from the CLI and not through this
// operation, with the role still verbatim.
func TestForgetMemoryRefusesAKeyItCannotAddress(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "empty after trimming", path: memoriesPath + "/%20%20"},
		{name: "control character", path: memoriesPath + "/bad%0Akey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memories := &roleMemories{forgotten: memoryops.ForgetResult{Found: true}}
			ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

			resp := ts.forget(t, tc.path)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["param"]; got != "key" {
				t.Errorf("param = %v, want \"key\"", got)
			}
			if n := len(memories.forgetRequests()); n != 0 {
				t.Errorf("the role was called %d times for a key the door refused, want 0 — this operation deletes", n)
			}
		})
	}
}

// TestForgetMemoryRefusesAQueryString: this operation takes no parameters, and
// on a destructive one an ignored parameter is the shape where a client
// believes it narrowed what it erased.
func TestForgetMemoryRefusesAQueryString(t *testing.T) {
	memories := &roleMemories{forgotten: memoryops.ForgetResult{Found: true}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.forget(t, memoriesPath+"/k?dry_run=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if got := body["param"]; got != "dry_run" {
		t.Errorf("param = %v, want \"dry_run\"", got)
	}
	if got := body["reason"]; got != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %q", got, ReasonUnknownParameter)
	}
	if n := len(memories.forgetRequests()); n != 0 {
		t.Errorf("the role was called %d times for a refused request, want 0", n)
	}
}

// TestDeleteOnTheMemoryCollectionIsNotRouted: the DELETE row is registered for
// `/v0/beads/memories/{key}`, and ServeMux requires the separating slash — so
// the collection path has no DELETE and answers the catch-all's 404.
//
// Worth a case because this is the surface's first DELETE method: a pattern
// that had swallowed the collection would be an undocumented way to ask for a
// bulk erase, and the parity test compares path STRINGS rather than probing
// what the router matches.
func TestDeleteOnTheMemoryCollectionIsNotRouted(t *testing.T) {
	memories := &roleMemories{forgotten: memoryops.ForgetResult{Found: true}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.forget(t, memoriesPath)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(memories.forgetRequests()); n != 0 {
		t.Errorf("the role was called %d times for the collection path, want 0", n)
	}
}

// TestListMemoriesAnswersThePlaneOrderedByKey: the collection read. The order
// is what makes the paginated envelope honest — a keyset cursor over it is
// expressible later without changing what a client already receives — and
// `has_more` is always false because the whole plane comes back in one page.
func TestListMemoriesAnswersThePlaneOrderedByKey(t *testing.T) {
	memories := &roleMemories{listed: memoryops.ListResult{Memories: map[string]string{
		"zebra":        "last by key",
		"alpha":        "first by key",
		"Has Spaces.✓": "an explicit key",
		// A row written out of band with an empty value. It is enumerated here
		// — its KEY exists — while GET /v0/beads/memories/{key} answers 404 for
		// it, and that asymmetry is the one way a client tells a row stored
		// empty from a row that is not there.
		"stored-empty": "",
	}}}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.get(t, memoriesPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	body := decodeBody(t, resp)
	if got := body["has_more"]; got != false {
		t.Errorf("has_more = %v, want false: the whole plane comes back in one page", got)
	}
	if _, ok := body["next_cursor"]; ok {
		t.Error("next_cursor is present; it is documented as present if and only if has_more is true")
	}
	items, _ := body["items"].([]any)
	var gotKeys []string
	for _, item := range items {
		entry, _ := item.(map[string]any)
		key, _ := entry["key"].(string)
		gotKeys = append(gotKeys, key)
	}
	wantKeys := []string{"Has Spaces.✓", "alpha", "stored-empty", "zebra"}
	if !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("keys = %v, want %v (ordered by key)", gotKeys, wantKeys)
	}

	values := map[string]any{}
	for _, item := range items {
		entry, _ := item.(map[string]any)
		key, _ := entry["key"].(string)
		values[key] = entry["value"]
	}
	if got := values["stored-empty"]; got != "" {
		t.Errorf("the empty-valued row's value = %v, want the empty string: `value` is required on Memory", got)
	}
	if got := values["alpha"]; got != "first by key" {
		t.Errorf("value = %v, want the stored content", got)
	}
}

// TestListMemoriesOfAnEmptyPlaneIsAnEmptyArray: never null. A client must not
// have to tell an absent array from an empty one to learn that nothing is
// stored.
func TestListMemoriesOfAnEmptyPlaneIsAnEmptyArray(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Memories: &roleMemories{}}))

	resp := ts.get(t, memoriesPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := readAll(t, resp)
	if !strings.Contains(raw, `"items":[]`) {
		t.Errorf("body = %s, want an empty items array rather than null", raw)
	}
}

// TestListMemoriesPassesTheSearchTermUnfolded: the term reaches the role as the
// client sent it.
//
// Case folding is the ROLE's — it owns what matching means, so that this
// surface and `bd memories` cannot come to disagree — and a handler that
// lowercased on the way past would be the second definition. Asserted on the
// REQUEST the role received for that reason: a response says nothing about
// which term was searched for.
func TestListMemoriesPassesTheSearchTermUnfolded(t *testing.T) {
	memories := &roleMemories{}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	resp := ts.get(t, memoriesPath+"?search=Dolt%20PHANTOMS")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := memories.listRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d lists, want 1", len(reqs))
	}
	if reqs[0].Search != "Dolt PHANTOMS" {
		t.Errorf("role received search %q, want the term verbatim", reqs[0].Search)
	}
}

// TestListMemoriesTreatsAnAbsentTermAsEverything: absent `q` is the empty
// search, which the role reads as "everything" — not as a filter that matches
// only memories containing "".
func TestListMemoriesTreatsAnAbsentTermAsEverything(t *testing.T) {
	memories := &roleMemories{}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	if resp := ts.get(t, memoriesPath); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := memories.listRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d lists, want 1", len(reqs))
	}
	if reqs[0].Search != "" {
		t.Errorf("role received search %q for an absent parameter, want the empty string", reqs[0].Search)
	}
}

// TestListMemoriesRefusesAnUnknownQueryParameter is the pin the design asked
// for by name: it is the only operation on the memory plane that takes a
// parameter at all, so it is the only one that goes through the query DECODER
// rather than through requireNoQuery — and that decoder's allowlist is the set
// of names the handler actually read, so a handler reading nothing accepts
// nothing while one reading `search` accepts exactly `search`.
//
// Silently ignoring an unrecognized parameter is what this prevents. On a
// filtering operation it WIDENS the answer, so a client one version ahead of
// the server would receive memories it believed it had filtered out — and on
// this plane a widened answer is a disclosure. It is also a client's only
// per-parameter capability probe, since `capabilities` is operation-level.
func TestListMemoriesRefusesAnUnknownQueryParameter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		wantParam string
		reason    Reason
	}{
		{
			// `q` SPECIFICALLY, which is the whole reason this operation's
			// parameter is not called that. On GET /v0/beads/issues:query, `q`
			// is a boolean expression over issue fields; a client carrying that
			// habit here has asked a question this operation does not answer,
			// and the one useful reply is that the name is unknown. Were the
			// parameter spelled `q`, `?q=status:open` would come back as a
			// literal substring search over memory text — a wrong answer with
			// a 200 on it.
			name:      "the other operation's q",
			query:     "?q=status:open",
			wantParam: "q",
			reason:    ReasonUnknownParameter,
		},
		{
			// The paging vocabulary of the issue reads. This operation has no
			// limit and no cursor, and accepting one silently would promise
			// paging that does not happen.
			name:      "a parameter another operation does have",
			query:     "?search=dolt&limit=10",
			wantParam: "limit",
			reason:    ReasonUnknownParameter,
		},
		{
			// A repeated `search` is two search terms, which is a question this
			// operation cannot answer. Resolving it to one of them silently
			// would answer a different question from the one asked.
			name:      "a repeated search",
			query:     "?search=one&search=two",
			wantParam: "search",
			reason:    ReasonInvalidValue,
		},
		{
			// The degenerate spelling: a parameter whose NAME is empty. The
			// document promises `param` on every 400 but an unparseable body,
			// so it is named rather than omitted.
			name:      "the empty parameter name",
			query:     "?=1",
			wantParam: "",
			reason:    ReasonUnknownParameter,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			memories := &roleMemories{}
			ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

			resp := ts.get(t, memoriesPath+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if got := body["code"]; got != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", got, CodeInvalidArgument)
			}
			param, ok := body["param"]
			if !ok {
				t.Fatalf("no `param` on the refusal; the document promises one on every 400 but an unparseable body")
			}
			if param != tc.wantParam {
				t.Errorf("param = %v, want %q", param, tc.wantParam)
			}
			if got := body["reason"]; got != string(tc.reason) {
				t.Errorf("reason = %v, want %q", got, tc.reason)
			}
			if n := len(memories.listRequests()); n != 0 {
				t.Errorf("the role was called %d times for a refused request, want 0", n)
			}
		})
	}
}

// TestListMemoriesAcceptsSearchAndNothingElse states the other half of the rule
// as a positive: `search` is accepted, so the refusals above are about the
// OTHER names rather than about a decoder that refuses everything.
func TestListMemoriesAcceptsSearchAndNothingElse(t *testing.T) {
	memories := &roleMemories{}
	ts := newTestServer(t, rolesConfig(Config{Memories: memories}))

	if resp := ts.get(t, memoriesPath+"?search=dolt"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d for the one parameter this operation takes, want 200: %s",
			resp.StatusCode, readAll(t, resp))
	}
	if n := len(memories.listRequests()); n != 1 {
		t.Errorf("the role was called %d times, want 1", n)
	}
}
