package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The pins for POST /v0/beads/issues/{id}:casMetadata. What is asserted here is
// the WIRE EDGE — that the handler carries the document's four members into the
// role's request faithfully, PRESERVES THE DIFFERENCE BETWEEN AN OMITTED AND A
// NULL VALUE MEMBER, and re-implements nothing the role owns.

const casMetadataPath = "/v0/beads/issues/bd-1:casMetadata"

func (ts *testServer) casMetadata(t *testing.T, path, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, path, "application/json", body)
}

// TestCASMetadataPathReachesItsHandler: a fourth custom method on the shared
// `/v0/beads/issues/{idop}` wildcard. A claim-shaped or reopen-shaped answer
// here would mean the dispatcher routed the segment to the wrong row.
func TestCASMetadataPathReachesItsHandler(t *testing.T) {
	cas := &roleMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{"actor":"alice","key":"gc.lease","value":"holder"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(cas.requests()) != 1 {
		t.Fatalf("the role was called %d times, want 1 — the path reached another handler", len(cas.requests()))
	}
}

// TestCASMetadataForwardsEveryDocumentedMember is the operation's central pin:
// the id comes off the PATH, the actor is trimmed, and both value members reach
// the role byte-for-byte rather than re-encoded.
//
// The re-encoding matters because the role's equality rule reads the bytes: a
// handler that decoded and re-marshaled a number would turn a caller's
// 9007199254740993 into a float and lose the swap it was asking for.
func TestCASMetadataForwardsEveryDocumentedMember(t *testing.T) {
	cas := &roleMetadataCAS{}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{
		"actor": "  alice  ",
		"key": "gc.lease",
		"expected": 9007199254740993,
		"value": {"holder":"alice","since":7}
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := cas.requests()
	if len(reqs) != 1 {
		t.Fatalf("the role was called %d times, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Actor != "alice" {
		t.Errorf("Actor = %q, want the trimmed value", got.Actor)
	}
	if got.IssueID != "bd-1" {
		t.Errorf("IssueID = %q, want the id off the path", got.IssueID)
	}
	if got.Key != "gc.lease" {
		t.Errorf("Key = %q, want the body's key", got.Key)
	}
	if got.Expected == nil || string(*got.Expected) != "9007199254740993" {
		t.Errorf("Expected = %v, want the caller's digits unaltered", got.Expected)
	}
	if got.Value == nil || string(*got.Value) != `{"holder":"alice","since":7}` {
		t.Errorf("Value = %v, want the caller's bytes unaltered", got.Value)
	}
}

// TestCASMetadataDistinguishesAnOmittedMemberFromANullOne is the pin the whole
// request shape rests on. An omitted member means the key is ABSENT on that
// side of the transition; a member present holding `null` means it holds JSON
// null. A handler that decoded into a value type would collapse the two and
// silently turn every acquire into an unguarded write.
func TestCASMetadataDistinguishesAnOmittedMemberFromANullOne(t *testing.T) {
	for _, test := range []struct {
		name         string
		body         string
		wantExpected *string
		wantValue    *string
	}{
		{
			name: "both omitted",
			body: `{"actor":"alice","key":"gc.lease"}`,
		},
		{
			name:         "expected null, value omitted",
			body:         `{"actor":"alice","key":"gc.lease","expected":null}`,
			wantExpected: strptr("null"),
		},
		{
			name:      "expected omitted, value null",
			body:      `{"actor":"alice","key":"gc.lease","value":null}`,
			wantValue: strptr("null"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cas := &roleMetadataCAS{}
			ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

			if resp := ts.casMetadata(t, casMetadataPath, test.body); resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			reqs := cas.requests()
			if len(reqs) != 1 {
				t.Fatalf("the role was called %d times, want 1", len(reqs))
			}
			assertRawMember(t, "Expected", reqs[0].Expected, test.wantExpected)
			assertRawMember(t, "Value", reqs[0].Value, test.wantValue)
		})
	}
}

// TestCASMetadataReportsALostRaceAsA200 pins the posture that makes this
// operation usable from a retry loop: a refused swap is the ANSWER, so it
// carries the current value on a 200 rather than arriving as a conflict.
func TestCASMetadataReportsALostRaceAsA200(t *testing.T) {
	current := json.RawMessage(`"holder-a"`)
	cas := &roleMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: false, Current: &current}}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{"actor":"alice","key":"gc.lease","value":"holder-b"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: a lost race is an answer, not an error", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["swapped"] != false {
		t.Errorf("swapped = %v, want false", body["swapped"])
	}
	if body["current"] != "holder-a" {
		t.Errorf("current = %v, want the value that refused the swap", body["current"])
	}
}

// TestCASMetadataOmitsCurrentWhenTheKeyIsAbsent pins the response side of the
// same distinction the request side makes: an absent key is an ABSENT member,
// not a null one, so a client can tell "the key is gone" from "the key holds
// null" without a second call.
func TestCASMetadataOmitsCurrentWhenTheKeyIsAbsent(t *testing.T) {
	cas := &roleMetadataCAS{result: issueops.CompareAndSetKeyResult{Swapped: true}}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{"actor":"alice","key":"gc.lease"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["swapped"] != true {
		t.Errorf("swapped = %v, want true", body["swapped"])
	}
	if _, present := body["current"]; present {
		t.Errorf("current is present as %v, want the member omitted for an absent key", body["current"])
	}
}

// TestCASMetadataRejectsTheShapesTheDocumentRefuses pins the 400s, each of them
// before the role is reached: a request the handler refuses must not become a
// call the role has to refuse again.
func TestCASMetadataRejectsTheShapesTheDocumentRefuses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"unknown member", `{"actor":"alice","key":"k","nope":1}`},
		{"missing actor", `{"key":"k"}`},
		{"null actor", `{"actor":null,"key":"k"}`},
		{"blank actor", `{"actor":"   ","key":"k"}`},
		{"missing key", `{"actor":"alice"}`},
		{"null key", `{"actor":"alice","key":null}`},
		{"empty key", `{"actor":"alice","key":""}`},
		{"non-object body", `[]`},
		{"unparseable body", `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cas := &roleMetadataCAS{}
			ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

			resp := ts.casMetadata(t, casMetadataPath, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if len(cas.requests()) != 0 {
				t.Errorf("the role was called %d times for a refused request, want 0", len(cas.requests()))
			}
		})
	}
}

// TestCASMetadataRefusesAQueryParameterAndAForeignMediaType keeps this
// operation on the surface's two shared refusals rather than letting a new
// handler quietly opt out of them.
func TestCASMetadataRefusesAQueryParameterAndAForeignMediaType(t *testing.T) {
	cas := &roleMetadataCAS{}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	body := `{"actor":"alice","key":"k"}`
	if resp := ts.casMetadata(t, casMetadataPath+"?verbose=1", body); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status with a query parameter = %d, want 400", resp.StatusCode)
	}
	// A foreign media type is this surface's 400 naming Content-Type, not a
	// 415: the document publishes one error shape and one code for it.
	if resp := ts.postBody(t, casMetadataPath, "text/plain", body); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status with a foreign media type = %d, want 400", resp.StatusCode)
	}
	if len(cas.requests()) != 0 {
		t.Errorf("the role was called %d times for refused requests, want 0", len(cas.requests()))
	}
}

// TestCASMetadataOfAnAbsentIssueIs404 pins the one refusal a caller cannot
// converge on, and the reason this operation documents a 404 at all.
func TestCASMetadataOfAnAbsentIssueIs404(t *testing.T) {
	cas := &roleMetadataCAS{err: issueops.ErrNotFound}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{"actor":"alice","key":"k"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestCASMetadataMapsARoleValidationRefusalToTheDocumented400 pins that the
// role's own refusals — a key outside the metadata-key syntax, a value that is
// not JSON — arrive as the document's 400 rather than as a 500.
func TestCASMetadataMapsARoleValidationRefusalToTheDocumented400(t *testing.T) {
	cas := &roleMetadataCAS{err: issueops.ErrValidation}
	ts := newTestServer(t, rolesConfig(Config{MetadataCAS: cas}))

	resp := ts.casMetadata(t, casMetadataPath, `{"actor":"alice","key":"k"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestCASMetadataResponseDecodesAPresentNull is the pin on the GENERATED
// CLIENT, and it is the only test here that decodes rather than asserting on
// the wire bytes — because the bug it exists to prevent is invisible at the
// wire.
//
// A `current` member declared as *json.RawMessage cannot READ a present null:
// encoding/json answers a JSON null against a pointer by setting the pointer to
// nil, before any UnmarshalJSON runs, so `{"current":null}` and a response with
// no `current` at all decode to the same value. On this operation those mean
// OPPOSITE things — the key holds null, versus the key is absent — and a retry
// loop that read the first as the second would swap with `expected` omitted,
// mismatch, and never converge. A livelock on a stream of 200s.
//
// The schema's x-go-type-skip-optional-pointer is what makes the member a bare
// json.RawMessage, which is an Unmarshaler and receives the literal. This test
// is what keeps the next `make api-gen` from regenerating that away.
func TestCASMetadataResponseDecodesAPresentNull(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want *string
	}{
		{"a present null is the literal", `{"swapped":false,"current":null}`, strptr("null")},
		{"an omitted member is nil", `{"swapped":false}`, nil},
		{"an ordinary value round-trips", `{"swapped":true,"current":{"a":1}}`, strptr(`{"a":1}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response apigen.CompareAndSetMetadataResponse
			if err := json.Unmarshal([]byte(test.body), &response); err != nil {
				t.Fatalf("decoding %s: %v", test.body, err)
			}
			switch {
			case test.want == nil && response.Current != nil:
				t.Fatalf("Current = %s, want nil: an omitted member is an ABSENT key", string(response.Current))
			case test.want != nil && response.Current == nil:
				t.Fatalf("Current = nil, want %s: a present null is a VALUE, and a client that "+
					"cannot tell it from an absent key cannot converge on a null-valued key", *test.want)
			case test.want != nil && string(response.Current) != *test.want:
				t.Fatalf("Current = %s, want %s", string(response.Current), *test.want)
			}
		})
	}
}

// TestAPointerMetadataValueCannotReadAPresentNull is the demonstration behind
// the test above, and it is here so that the compile error a regenerated
// pointer member produces has a companion that says WHY.
//
// It is not testing this package's code — it is testing the shape the schema
// must not generate — which is exactly what makes it worth keeping: nothing
// else in the tree records that the loss happens at decode time, silently, on a
// well-formed response.
func TestAPointerMetadataValueCannotReadAPresentNull(t *testing.T) {
	var pointerShaped struct {
		Current *json.RawMessage `json:"current,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"current":null}`), &pointerShaped); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if pointerShaped.Current != nil {
		t.Skip("encoding/json no longer nils a pointer on a JSON null; " +
			"x-go-type-skip-optional-pointer may no longer be load-bearing")
	}

	var valueShaped struct {
		Current json.RawMessage `json:"current,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"current":null}`), &valueShaped); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if string(valueShaped.Current) != "null" {
		t.Fatalf("the non-pointer shape read %q, want the literal null — the fix does not work either", valueShaped.Current)
	}
}

// TestCASMetadataRequestCarriesAPresentNull is the same pin on the request
// members, for a client that BUILDS its request through the generated type: an
// `expected` of null must reach the wire as null rather than being omitted,
// since omitting it asks a different question.
func TestCASMetadataRequestCarriesAPresentNull(t *testing.T) {
	encoded, err := json.Marshal(apigen.CompareAndSetMetadataRequest{
		Actor: "alice", Key: "gc.lease", Expected: json.RawMessage("null"),
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if got := string(encoded); got != `{"actor":"alice","expected":null,"key":"gc.lease"}` {
		t.Fatalf("encoded = %s, want a present null `expected` and no `value` member", got)
	}
	var decoded apigen.CompareAndSetMetadataRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if string(decoded.Expected) != "null" {
		t.Fatalf("Expected = %v, want the literal null back", decoded.Expected)
	}
	if decoded.Value != nil {
		t.Fatalf("Value = %s, want nil: the member was never sent", string(decoded.Value))
	}
}

func strptr(s string) *string { return &s }

func assertRawMember(t *testing.T, name string, got *json.RawMessage, want *string) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %s, want nil: an omitted member means the key is ABSENT", name, string(*got))
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %s: a member present holding null is a VALUE", name, *want)
	case want != nil && got != nil && string(*got) != *want:
		t.Errorf("%s = %s, want %s", name, string(*got), *want)
	}
}
