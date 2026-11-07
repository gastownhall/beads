// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/metadatacas_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The compare-and-set role's own gates.
//
// Two things about this operation are unlike every other write on this seam,
// and both are asserted here rather than left to the served tier: a LOST RACE
// IS A 200, so the verdict is read out of the body of a success; and ABSENCE IS
// A VALUE on three members — `expected`, `value` and `current` — where an
// omitted member means "the key is absent on that side" and a member present
// holding `null` means the key exists and holds null.

func casRole(t *testing.T, w *stubWire) issueops.MetadataCAS {
	t.Helper()
	cas, err := stubStore(t, w).MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	return cas
}

func raw(s string) *json.RawMessage {
	value := json.RawMessage(s)
	return &value
}

// TestMetadataCASSendsTheTransitionAsAPair is the request half: Expected is the
// value before and Value the value after, and nil on either side means the key
// is ABSENT there — so a first-writer-wins acquire omits `expected` and a
// release omits `value`.
//
// A serializer that dropped an empty member would turn a conditional write into
// a delete, which is the trap the operation's own description opens with.
func TestMetadataCASSendsTheTransitionAsAPair(t *testing.T) {
	tests := []struct {
		name                    string
		request                 issueops.CompareAndSetKeyRequest
		wantExpected, wantValue string
	}{
		{
			name: "a first-writer-wins acquire omits expected",
			request: issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Value: raw(`"win44"`),
			},
			wantExpected: "", wantValue: `"win44"`,
		},
		{
			name: "a release omits value",
			request: issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Expected: raw(`"win44"`),
			},
			wantExpected: `"win44"`, wantValue: "",
		},
		{
			name: "a hand-off carries both",
			request: issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.lease",
				Expected: raw(`"win30"`), Value: raw(`"win44"`),
			},
			wantExpected: `"win30"`, wantValue: `"win44"`,
		},
		{
			name: "a stored null is a PRESENT value on both sides",
			request: issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.lease",
				Expected: raw(`null`), Value: raw(`null`),
			},
			wantExpected: `null`, wantValue: `null`,
		},
		{
			// A NUMBER TRAVELS AS THE CALLER'S OWN LITERAL, and on THIS
			// operation the stakes are a changed verdict rather than a changed
			// value: equality here is canonical but numbers compare as their
			// source literal, so `1` and `1.0` do NOT match. A client that
			// decoded either side through `any` and re-encoded it would send
			// `1` for a caller who wrote `1.0` — and the swap would land where
			// the role would have refused it, or be refused where the role
			// would have landed it.
			//
			// The big integer is the second half of the same rule: past 2^53 a
			// double cannot hold the value at all, so a re-encode hands the
			// server a token NEAR the caller's that is not it.
			name: "numbers travel as the caller's own literal, on both sides",
			request: issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.epoch",
				Expected: raw(`1.0`), Value: raw(`9007199254740993`),
			},
			wantExpected: `1.0`, wantValue: `9007199254740993`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := &stubWire{cas: &apigen.CompareAndSetMetadataResponse{Swapped: true}}
			if _, err := casRole(t, w).CompareAndSetKey(t.Context(), test.request); err != nil {
				t.Fatalf("CompareAndSetKey: %v", err)
			}
			if got := w.lastCAS.Actor; got != test.request.Actor {
				t.Errorf("actor = %q, want %q", got, test.request.Actor)
			}
			if got := w.lastCAS.Key; got != test.request.Key {
				t.Errorf("key = %q, want %q", got, test.request.Key)
			}
			// Through the marshaler: `omitempty` on a json.RawMessage omits the
			// EMPTY one, and what the server reads is the member set of the
			// encoded body, not the Go value.
			encoded, err := json.Marshal(w.lastCAS)
			if err != nil {
				t.Fatalf("marshal the request: %v", err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatalf("re-read the request: %v", err)
			}
			for member, want := range map[string]string{"expected": test.wantExpected, "value": test.wantValue} {
				got, present := body[member]
				switch {
				case want == "" && present:
					t.Errorf("the request carries %q = %s; a nil side of the transition is an ABSENT member", member, got)
				case want != "" && !present:
					t.Errorf("the request omits %q; omitting it means the key is absent there, which is a different request", member)
				case want != "" && string(got) != want:
					t.Errorf("%s = %s, want %s", member, got, want)
				}
			}
		})
	}
}

// TestMetadataCASReadsTheVerdictOutOfASuccess is the response half, and the
// reason this role could not be wired by analogy with its neighbors: a lost
// race is a 200 carrying `swapped: false` and the value that refused the swap.
// A client that dispatched on the status code would report a lost race as a
// won one.
func TestMetadataCASReadsTheVerdictOutOfASuccess(t *testing.T) {
	tests := []struct {
		name     string
		response apigen.CompareAndSetMetadataResponse
		wantSwap bool
		want     string
		wantNil  bool
	}{
		{
			name:     "a lost race is an answer, not a failure",
			response: apigen.CompareAndSetMetadataResponse{Swapped: false, Current: apigen.MetadataValue(`"win30"`)},
			wantSwap: false, want: `"win30"`,
		},
		{
			name:     "a landed swap reports the value the row now holds",
			response: apigen.CompareAndSetMetadataResponse{Swapped: true, Current: apigen.MetadataValue(`"win44"`)},
			wantSwap: true, want: `"win44"`,
		},
		{
			name:     "an absent current is an ABSENT key",
			response: apigen.CompareAndSetMetadataResponse{Swapped: true},
			wantSwap: true, wantNil: true,
		},
		{
			name:     "a present null is a value the key HOLDS",
			response: apigen.CompareAndSetMetadataResponse{Swapped: false, Current: apigen.MetadataValue(`null`)},
			wantSwap: false, want: `null`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := test.response
			w := &stubWire{cas: &response}
			result, err := casRole(t, w).CompareAndSetKey(t.Context(), issueops.CompareAndSetKeyRequest{
				Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Value: raw(`"win44"`),
			})
			if err != nil {
				t.Fatalf("CompareAndSetKey: %v", err)
			}
			if result.Swapped != test.wantSwap {
				t.Errorf("Swapped = %t, want %t", result.Swapped, test.wantSwap)
			}
			switch {
			case test.wantNil && result.Current != nil:
				t.Fatalf("Current = %s, want nil: an omitted member is an ABSENT key", *result.Current)
			case !test.wantNil && result.Current == nil:
				t.Fatalf("Current = nil, want %s: a client that cannot tell a present null from an absent key cannot converge on a null-valued key", test.want)
			case !test.wantNil && string(*result.Current) != test.want:
				t.Errorf("Current = %s, want %s", *result.Current, test.want)
			}
		})
	}
}

// TestMetadataCASRefusesAnUnusableRequestBeforeDialing covers the two refusals
// this layer owns rather than leaving to the server.
//
// Actor and issue id are the shared write rules — an empty id would join to the
// COLLECTION path and turn an operation on one resource into one on all of them
// — and a malformed JSON value cannot go on a JSON wire at all, so it would
// fail inside json.Marshal as a transport fault where the role's contract
// promises a deterministic validation failure.
//
// The KEY's syntax is deliberately NOT decided here: it is the workspace's own
// metadata-key rule, the server's role applies it inside the refusing
// transaction, and a second copy of it in this client would be a second
// definition of which keys a workspace may hold.
func TestMetadataCASRefusesAnUnusableRequestBeforeDialing(t *testing.T) {
	for name, request := range map[string]issueops.CompareAndSetKeyRequest{
		"empty actor":    {IssueID: "bd-1", Key: "gc.lease"},
		"blank actor":    {Actor: "  ", IssueID: "bd-1", Key: "gc.lease"},
		"empty issue id": {Actor: "win44", Key: "gc.lease"},
		"malformed expected": {
			Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Expected: raw(`{`),
		},
		"malformed value": {
			Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Value: raw(`nope`),
		},
		"an empty expected is not the empty string": {
			Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Expected: raw(``),
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{cas: &apigen.CompareAndSetMetadataResponse{Swapped: true}}
			result, err := casRole(t, w).CompareAndSetKey(t.Context(), request)
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("CompareAndSetKey(%s) = %v, want ErrValidation", name, err)
			}
			if result.Swapped {
				t.Error("a refused request reported Swapped")
			}
			if len(w.calls) != 0 {
				t.Errorf("the refused request dialed %v", w.calls)
			}
		})
	}
}

// TestMetadataCASDoesNotWriteThroughTheCallerRequest holds the promise every
// role here makes and this one states twice: "Implementations never mutate
// caller-owned request values: the two raw messages are read, never written
// through."
//
// The request travels as bytes into a marshaler, which is exactly the kind of
// borrow that becomes a write when a helper is added later.
func TestMetadataCASDoesNotWriteThroughTheCallerRequest(t *testing.T) {
	expected, value := raw(`"win30"`), raw(`"win44"`)
	request := issueops.CompareAndSetKeyRequest{
		Actor: "win44", IssueID: "bd-1", Key: "gc.lease", Expected: expected, Value: value,
	}
	before := reflect.DeepEqual(request, request)
	if !before {
		t.Fatal("the request is not comparable to itself")
	}

	w := &stubWire{cas: &apigen.CompareAndSetMetadataResponse{Swapped: true, Current: apigen.MetadataValue(`"win44"`)}}
	result, err := casRole(t, w).CompareAndSetKey(t.Context(), request)
	if err != nil {
		t.Fatalf("CompareAndSetKey: %v", err)
	}

	if string(*expected) != `"win30"` || string(*value) != `"win44"` {
		t.Errorf("the caller's values were written through: expected = %s, value = %s", *expected, *value)
	}
	if request.Expected != expected || request.Value != value {
		t.Error("the request's own pointers were replaced")
	}
	// And the answer is detached from the transport's buffer for the same
	// reason: a caller feeding Current back as the next Expected must not hold
	// a window onto a response body the client may reuse.
	if result.Current == nil {
		t.Fatal("Current is nil")
	}
	if &(*result.Current)[0] == &w.cas.Current[0] {
		t.Error("Current aliases the response body rather than copying it")
	}
}
