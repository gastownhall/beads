package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The wire edge of the two settings WRITES, on a fake role.
//
// Which keys the plane refuses, what the projection of status.custom does, and
// whether a removal removed anything are issueops.WorkspaceConfig's, held to on
// three legs by its own contract and shown against real Dolt in cmd/bd. What
// only these cases can show is that the request a caller SENDS becomes the
// request the role RECEIVES, that the response is projected by the SAME rule the
// read is projected by — which is the whole of what stops a write leaking what a
// read withholds — and that the removal has no 404 to give.

const settingPath = "/v0/beads/config/status.custom"

func (ts *testServer) setSetting(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.putBody(t, path, "application/json", body)
}

func (ts *testServer) putBody(t *testing.T, path, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (ts *testServer) unsetSetting(t *testing.T, path string) *http.Response {
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

func newSettingsServer(t *testing.T, settings *roleSettings) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Settings: settings}))
}

// TestSetSettingProjectsTheWholeRequest drives the two halves of the request,
// which come from two different places: the key off the path and the value off
// the body. The verbatim cases are the ones with teeth — this plane's keys and
// values are both stored as sent, so a trim at this edge would produce a write
// the caller can never find again under the name it used.
func TestSetSettingProjectsTheWholeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
		want issueops.SetSettingRequest
	}{
		{
			name: "the key comes from the path and the value from the body",
			path: "status.custom",
			body: `{"value":"awaiting_review:active"}`,
			want: issueops.SetSettingRequest{Key: "status.custom", Value: "awaiting_review:active"},
		},
		{
			// One path segment, percent-decoded once. Keys routinely carry dots
			// and this surface walks no namespace.
			name: "a percent-escaped key is decoded once",
			path: "custom%2Fslash",
			body: `{"value":"v"}`,
			want: issueops.SetSettingRequest{Key: "custom/slash", Value: "v"},
		},
		{
			// The EMPTY STRING is a legal value and reaches the role. A caller
			// that meant "remove it" has DELETE; conflating the two here would
			// make one request mean whichever the handler guessed.
			name: "the empty value is stored, not treated as a removal",
			path: "notes",
			body: `{"value":""}`,
			want: issueops.SetSettingRequest{Key: "notes", Value: ""},
		},
		{
			// Neither trimmed nor filtered: the column is TEXT and two of this
			// plane's keys carry structured configuration.
			name: "the value keeps its newlines and its surrounding space",
			path: "notes",
			body: `{"value":"  one\ntwo  "}`,
			want: issueops.SetSettingRequest{Key: "notes", Value: "  one\ntwo  "},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &roleSettings{}
			ts := newSettingsServer(t, settings)

			resp := ts.setSetting(t, "/v0/beads/config/"+tc.path, tc.body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := settings.setRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d writes ran, want 1", len(reqs))
			}
			if !reflect.DeepEqual(reqs[0], tc.want) {
				t.Errorf("SetSettingRequest = %+v, want %+v", reqs[0], tc.want)
			}
		})
	}
}

// TestSetSettingAnswersWhatTheReadWouldAnswer is the redaction property, stated
// as an equality rather than as two assertions that could drift: the body of a
// write is byte-for-byte the body of the read that follows it.
//
// THIS IS THE WHOLE NO-ECHO GUARANTEE. Redaction on this surface decides on the
// KEY and never on the caller, so a write that handed a credential-bearing value
// back would be one operation exempting itself from a rule the schema states in
// one place — and `Setting.redacted` would mean two different things depending on
// which operation produced it. Comparing the two responses is what makes a second
// projection impossible to add without failing here.
func TestSetSettingAnswersWhatTheReadWouldAnswer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "an ordinary key", key: "status.custom", value: "awaiting_review:active"},
		{name: "a credential-bearing key", key: "notion.token", value: "shhh-real-secret"},
		{name: "a key stored empty", key: "notes", value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The two servers answer the SAME stored value, so any difference in
			// the bodies is the projection's rather than the fixture's.
			writer := newSettingsServer(t, &roleSettings{})
			reader := newSettingsServer(t, &roleSettings{value: tc.value})

			wrote := writer.setSetting(t, "/v0/beads/config/"+tc.key, fmt.Sprintf(`{"value":%q}`, tc.value))
			if wrote.StatusCode != http.StatusOK {
				t.Fatalf("PUT status = %d, want 200: %s", wrote.StatusCode, readAll(t, wrote))
			}
			read := reader.get(t, "/v0/beads/config/"+tc.key)
			if read.StatusCode != http.StatusOK {
				t.Fatalf("GET status = %d, want 200: %s", read.StatusCode, readAll(t, read))
			}
			wroteBody, readBody := decodeBody(t, wrote), decodeBody(t, read)
			if !reflect.DeepEqual(wroteBody, readBody) {
				t.Errorf("PUT answered %v, the GET beside it answers %v; the write must be projected by the same rule",
					wroteBody, readBody)
			}
		})
	}
}

// TestSetSettingWithholdsACredentialItJustAccepted is the doctrine, asserted
// directly rather than only through the equality above: the write is PERMITTED
// and the value does not come back.
//
// PERMITTED because redaction is a rule about DISCLOSURE. The role refuses no
// credential-bearing key — `bd config set`'s own secret guard is about writing
// one into a git-tracked config.yaml, a different plane with a different hazard —
// and a writer already holds the value, so refusing here would protect nothing
// while leaving a workspace's credentials visible as PRESENT and permanently
// unconfigurable through this door.
func TestSetSettingWithholdsACredentialItJustAccepted(t *testing.T) {
	const key = "notion.token"
	if !config.IsSecretKey(key) {
		t.Fatalf("%q is no longer a credential-bearing key; this test proves nothing", key)
	}
	settings := &roleSettings{}
	ts := newSettingsServer(t, settings)

	resp := ts.setSetting(t, "/v0/beads/config/"+key, `{"value":"shhh-real-secret"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a credential-bearing key is writable: %s", resp.StatusCode, readAll(t, resp))
	}
	// The role got the value: the refusal is not smuggled in as a silent drop.
	if reqs := settings.setRequests(); len(reqs) != 1 || reqs[0].Value != "shhh-real-secret" {
		t.Fatalf("the role received %+v, want the value the caller sent", reqs)
	}
	raw := readAll(t, resp)
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if body["redacted"] != true {
		t.Errorf("redacted = %v, want true", body["redacted"])
	}
	if _, present := body["value"]; present {
		t.Errorf("the write handed the credential back: %v", body)
	}
	// THE RAW RESPONSE BYTES, not the decoded map. A decoded-map grep only sees
	// members this test thought to look at — an extension member, a duplicated
	// key, an echo inside a `detail` string would all pass it — and what a
	// client, a proxy log and an access log actually receive is the bytes.
	if strings.Contains(raw, "shhh-real-secret") {
		t.Errorf("the response bytes carry the stored credential: %s", raw)
	}
}

// TestSetSettingAnswersTheStoredValueNotTheRequest is the other half of the
// projection: the body is built from what the ROLE reported, so a role whose
// answer differed from the request would be reported honestly rather than
// papered over by an echo.
func TestSetSettingAnswersTheStoredValueNotTheRequest(t *testing.T) {
	ts := newSettingsServer(t, &roleSettings{stored: "what-the-plane-holds"})

	resp := ts.setSetting(t, settingPath, `{"value":"what-the-caller-sent"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["value"] != "what-the-plane-holds" {
		t.Errorf("value = %v, want the STORED value; this response is echoing the request", body["value"])
	}
}

// TestSetSettingRefusesTheRequest walks every refusal this edge owns, each named
// by the member it is about.
func TestSetSettingRefusesTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		body  string
		param string
	}{
		{name: "an unknown member", path: "status.custom", body: `{"value":"v","key":"status.custom"}`, param: "key"},
		{name: "a missing value", path: "status.custom", body: `{}`, param: "value"},
		{name: "a null value", path: "status.custom", body: `{"value":null}`, param: "value"},
		{name: "a non-string value", path: "status.custom", body: `{"value":7}`, param: "value"},
		{name: "a key that is empty after trimming", path: "%20%20", body: `{"value":"v"}`, param: "key"},
		{name: "a key carrying a control character", path: "a%0Ab", body: `{"value":"v"}`, param: "key"},
		{
			// The bound the WRITE needs and the read does not: this operation
			// inserts into a VARCHAR(255) primary key, so an over-long key would
			// otherwise be a 500 from the column.
			name:  "a key past the storage column",
			path:  strings.Repeat("k", types.MaxFieldLen+1),
			body:  `{"value":"v"}`,
			param: "key",
		},
		{
			// The SAME hazard on the other half of the row: config.value is a
			// TEXT column, so one byte past it is an error from the column — a
			// generic 500 for a request the caller could have fixed, which is
			// precisely what the key's bound above exists to prevent.
			name:  "a value past the storage column",
			path:  "notes",
			body:  `{"value":"` + strings.Repeat("v", types.MaxTextBytes+1) + `"}`,
			param: "value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &roleSettings{}
			ts := newSettingsServer(t, settings)

			resp := ts.setSetting(t, "/v0/beads/config/"+tc.path, tc.body)
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
			if got := settings.setRequests(); len(got) != 0 {
				t.Errorf("%d writes ran on a refused request", len(got))
			}
		})
	}
}

// TestSetSettingAcceptsAValueAtTheColumnsCeiling is the other direction of the
// bound, and it is the half that stops a refusal from quietly becoming stricter
// than the column it is keyed on: exactly MaxTextBytes must be ACCEPTED and
// reach the role, because that value fits.
//
// The multi-byte case is what pins BYTES rather than characters. 40000 two-byte
// characters are 80000 bytes — well inside any rune count and well past the
// column — so a bound that counted runes would accept it and hand the 500 back
// to the caller, which is the failure this whole check exists to prevent.
func TestSetSettingAcceptsAValueAtTheColumnsCeiling(t *testing.T) {
	t.Run("exactly the ceiling is accepted", func(t *testing.T) {
		settings := &roleSettings{}
		ts := newSettingsServer(t, settings)

		value := strings.Repeat("v", types.MaxTextBytes)
		raw, err := json.Marshal(map[string]string{"value": value})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		resp := ts.setSetting(t, "/v0/beads/config/notes", string(raw))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 — this value fits the column: %s", resp.StatusCode, readAll(t, resp))
		}
		if reqs := settings.setRequests(); len(reqs) != 1 || len(reqs[0].Value) != types.MaxTextBytes {
			t.Errorf("the role received %d bytes, want the %d sent", len(reqs[0].Value), types.MaxTextBytes)
		}
	})

	t.Run("multi-byte is counted in bytes", func(t *testing.T) {
		settings := &roleSettings{}
		ts := newSettingsServer(t, settings)

		// Two bytes per character, so this is inside any rune-based bound and
		// past the column.
		value := strings.Repeat("é", types.MaxTextBytes/2+1)
		if len(value) <= types.MaxTextBytes {
			t.Fatalf("the fixture is %d bytes, which the column accepts; this test proves nothing", len(value))
		}
		raw, err := json.Marshal(map[string]string{"value": value})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		resp := ts.setSetting(t, "/v0/beads/config/notes", string(raw))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 — the column counts bytes: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["param"] != "value" {
			t.Errorf("param = %v, want value", body["param"])
		}
		if got := settings.setRequests(); len(got) != 0 {
			t.Errorf("%d writes ran on a refused request", len(got))
		}
	})
}

// TestSetSettingTwiceIsTheSameAnswerAndTheSameState is PUT's defining property,
// pinned rather than assumed: the method is idempotent, so the second request
// must answer byte-for-byte what the first did and leave the plane where the
// first left it.
//
// It is worth a case of its own because nothing else here would catch a handler
// that grew a "first write wins" or an "already set" member — both of which are
// shapes this surface has elsewhere (claimIssue's `already_claimed`,
// closeIssue's `already_closed`) and neither of which belongs on a PUT.
func TestSetSettingTwiceIsTheSameAnswerAndTheSameState(t *testing.T) {
	settings := &roleSettings{}
	ts := newSettingsServer(t, settings)

	first := ts.setSetting(t, settingPath, `{"value":"awaiting_review:active"}`)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first PUT status = %d, want 200: %s", first.StatusCode, readAll(t, first))
	}
	firstBody := decodeBody(t, first)

	second := ts.setSetting(t, settingPath, `{"value":"awaiting_review:active"}`)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d, want 200: %s", second.StatusCode, readAll(t, second))
	}
	if secondBody := decodeBody(t, second); !reflect.DeepEqual(firstBody, secondBody) {
		t.Errorf("the second PUT answered %v, the first answered %v; this method is idempotent", secondBody, firstBody)
	}

	// BOTH reached the role, which is the state half: the role performs the
	// write and its projection unconditionally — a no-op detection would make
	// repairing a drifted table depend on the row having changed, which is
	// exactly the state that needs repairing — so this surface must not
	// short-circuit the second call either.
	reqs := settings.setRequests()
	if len(reqs) != 2 {
		t.Fatalf("%d writes reached the role, want 2", len(reqs))
	}
	if !reflect.DeepEqual(reqs[0], reqs[1]) {
		t.Errorf("the two writes reached the role as %+v and %+v; want the same request twice", reqs[0], reqs[1])
	}
}

// TestSetSettingRefusesEverythingButAJSONBody covers the two document-level
// rules on this operation, because both are enforced per handler.
func TestSetSettingRefusesEverythingButAJSONBody(t *testing.T) {
	t.Run("a query parameter", func(t *testing.T) {
		ts := newSettingsServer(t, &roleSettings{})
		resp := ts.setSetting(t, settingPath+"?force=true", `{"value":"v"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["reason"] != string(ReasonUnknownParameter) {
			t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
		}
	})

	t.Run("a form encoding", func(t *testing.T) {
		ts := newSettingsServer(t, &roleSettings{})
		resp := ts.putBody(t, settingPath, "application/x-www-form-urlencoded", `value=v`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
		}
		if body := decodeBody(t, resp); body["param"] != "Content-Type" {
			t.Errorf("param = %v, want Content-Type", body["param"])
		}
	})
}

// TestSetSettingCarriesTheRolesRefusal is the ROLE's 400 reaching the wire as
// the 400 it is, with the role's own sentence.
//
// It carries NO `param`, deliberately. The two reachable refusals are about
// different members — the protected key, and a status.custom that does not parse
// — and telling them apart needs the role's protected-key vocabulary, which this
// package cannot hold: depguard denies internal/workapi from every non-test file
// here, precisely so a second copy of a shared rule cannot exist to drift.
func TestSetSettingCarriesTheRolesRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  string
	}{
		{
			name: "the protected prefix key",
			err:  `"issue_prefix" is set by bd init --prefix, bd bootstrap or bd rename-prefix, not by a config write`,
		},
		{
			name: "a status.custom value that does not parse",
			err:  "invalid status.custom value: bad shape",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newSettingsServer(t, &roleSettings{
				writeErr: fmt.Errorf("%w: %s", issueops.ErrValidation, tc.err),
			})

			resp := ts.setSetting(t, settingPath, `{"value":"whatever"}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if _, present := body["param"]; present {
				t.Errorf("the role's refusal names a member this surface cannot know: %v", body)
			}
			if detail, _ := body["detail"].(string); !strings.Contains(detail, tc.err) {
				t.Errorf("detail = %q, want the role's own sentence", detail)
			}
		})
	}
}

// TestUnsetSettingRemovesTheNamedKey is the removal's happy path, and the
// response shape is the assertion: the key and nothing else.
func TestUnsetSettingRemovesTheNamedKey(t *testing.T) {
	settings := &roleSettings{}
	ts := newSettingsServer(t, settings)

	resp := ts.unsetSetting(t, settingPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := settings.unsetRequests()
	if len(reqs) != 1 || reqs[0].Key != "status.custom" {
		t.Fatalf("unset requests = %+v, want one for status.custom", reqs)
	}
	body := decodeBody(t, resp)
	if body["key"] != "status.custom" {
		t.Errorf("key = %v, want the key the path named", body["key"])
	}
	// No `removed` flag: the storage seam discards the affected-row count, so
	// the member would be a value one implementation had to invent. No `value`
	// either — on the one operation that withholds nothing, reporting what was
	// there would publish exactly the credential the read redacts.
	for _, member := range []string{"removed", "value", "redacted"} {
		if _, present := body[member]; present {
			t.Errorf("the removal publishes %q: %v", member, body)
		}
	}
}

// TestUnsetSettingHasNo404 is the divergence from DELETE
// /v0/beads/memories/{key}, which answers 404 for a key it held nothing under.
// This role reports no affected-row count, so the operation states an intended
// END STATE: removing a key nothing set succeeds, and removing the same key
// twice is 200 and then 200.
func TestUnsetSettingHasNo404(t *testing.T) {
	settings := &roleSettings{}
	ts := newSettingsServer(t, settings)

	for i := range 2 {
		resp := ts.unsetSetting(t, "/v0/beads/config/never.set")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("removal %d: status = %d, want 200 — this operation has no miss to report: %s",
				i+1, resp.StatusCode, readAll(t, resp))
		}
	}
	if got := settings.unsetRequests(); len(got) != 2 {
		t.Errorf("%d removals reached the role, want 2", len(got))
	}
}

// TestUnsetSettingRefusesTheKeyTheReadRefuses pins the deliberate SAMENESS: the
// removal takes the read's parameter and judges it the read's way.
//
// The over-long key is the case that pins the deliberate DIFFERENCE from the
// write. A removal is a comparison and never an insert, so a key no column could
// hold matches nothing and already has an answer; refusing it would be a refusal
// the role does not have — releaseExpectedAssignee's rule, on the other keyed
// plane.
func TestUnsetSettingRefusesTheKeyTheReadRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{name: "a key that is empty after trimming", path: "%20%20", want: http.StatusBadRequest},
		{name: "a key carrying a control character", path: "a%0Ab", want: http.StatusBadRequest},
		{
			name: "a key past the storage column is not refused",
			path: strings.Repeat("k", types.MaxFieldLen+1),
			want: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &roleSettings{}
			ts := newSettingsServer(t, settings)

			removed := ts.unsetSetting(t, "/v0/beads/config/"+tc.path)
			if removed.StatusCode != tc.want {
				t.Fatalf("DELETE status = %d, want %d: %s", removed.StatusCode, tc.want, readAll(t, removed))
			}
			// The read answers the same way, which is what "the read's parameter,
			// judged the read's way" means.
			read := ts.get(t, "/v0/beads/config/"+tc.path)
			if read.StatusCode != tc.want {
				t.Errorf("GET status = %d, DELETE answered %d; the two must judge one parameter one way",
					read.StatusCode, tc.want)
			}
		})
	}
}

// TestSettingWritesRefuseAQueryParameterOnTheRemoval keeps the document-level
// rule true on the one write with no body to carry it.
func TestSettingWritesRefuseAQueryParameterOnTheRemoval(t *testing.T) {
	ts := newSettingsServer(t, &roleSettings{})

	resp := ts.unsetSetting(t, settingPath+"?force=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
	}
}

// TestSettingPathReachesEachMethodsHandler drives all three methods on the one
// pattern against the real router. ServeMux registers method and pattern
// together, so this is what proves the three rows are three operations rather
// than one shadowing the others.
func TestSettingPathReachesEachMethodsHandler(t *testing.T) {
	settings := &roleSettings{value: "stored"}
	ts := newSettingsServer(t, settings)

	if resp := ts.get(t, settingPath); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if resp := ts.setSetting(t, settingPath, `{"value":"v"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if resp := ts.unsetSetting(t, settingPath); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(settings.getRequests()) != 1 || len(settings.setRequests()) != 1 || len(settings.unsetRequests()) != 1 {
		t.Errorf("the three methods reached %d reads, %d writes and %d removals; want one each",
			len(settings.getRequests()), len(settings.setRequests()), len(settings.unsetRequests()))
	}
}
