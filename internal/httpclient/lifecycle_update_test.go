// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/lifecycle_update_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The PATCH document's own gates, and the counterpart of
// lifecycle_create_test.go for the operation whose request is not a struct.
//
// updateIssue is the one write on this surface the client builds as a map, so
// nothing the compiler does can hold it to the wire's vocabulary: a typo is a
// 400 from a live server and a green run everywhere else. Three things are
// asserted here that no served case can show as cheaply — the members the wire
// wave added really reach the document, the metadata sub-document is the wire's
// own algebra rather than a second spelling of it, and a member the role adds
// tomorrow cannot arrive unclassified.

// TestUpdateSendsThePatchMembersTheWireNowPublishes is the round trip for the
// four members client wave ga-7i6by carries: `status`, `assignee`, `parent_id`
// and `metadata`. Each was a W-IssuePatch refusal until the wire published it,
// and each is asserted through the document the role built rather than through
// the result, because the result is the server's and would look identical if
// the member had been dropped.
func TestUpdateSendsThePatchMembersTheWireNowPublishes(t *testing.T) {
	w := &stubWire{update: &apigen.UpdateIssueResponse{Changed: true, Revision: "0"}}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
		Actor: "writer", IssueID: "bd-1",
		Patch: issueops.IssuePatch{
			Status:   set(issueops.StatusInProgress),
			Assignee: set("holder"),
			ParentID: set("bd-parent"),
			Metadata: issueops.MetadataPatch{
				Set: map[string]json.RawMessage{"gc.lease": json.RawMessage(`"win44"`)},
			},
		},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	patch := w.lastPatch
	for member, want := range map[string]any{
		"status":    string(issueops.StatusInProgress),
		"assignee":  "holder",
		"parent_id": "bd-parent",
	} {
		if got, ok := patch[member]; !ok || got != want {
			t.Errorf("patch[%s] = %v (present %t), want %v", member, got, ok, want)
		}
	}
	metadata, ok := patch["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("patch[metadata] = %#v, want the wire's own metadata document", patch["metadata"])
	}
	values, ok := metadata["set"].(map[string]json.RawMessage)
	if !ok {
		t.Fatalf("metadata[set] = %#v, want the raw values map", metadata["set"])
	}
	if got := string(values["gc.lease"]); got != `"win44"` {
		t.Errorf("metadata.set[gc.lease] = %s, want the caller's own bytes", got)
	}
}

// TestUpdateSendsTheEmptyStringsThatMeanSomething is the half of the three
// string members a nil check would lose. On this operation the empty string is
// not "no value": it UNASSIGNS on `assignee` and removes every parent-child
// edge on `parent_id`, so a builder that skipped a member because its value was
// empty would turn two real edits into no edit at all — silently, since the
// server answers `changed: false` and the caller reads a 200.
func TestUpdateSendsTheEmptyStringsThatMeanSomething(t *testing.T) {
	w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "0"}}
	lifecycle, err := stubStore(t, w).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
		Actor: "writer", IssueID: "bd-1",
		Patch: issueops.IssuePatch{Assignee: set(""), ParentID: set("")},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, member := range []string{"assignee", "parent_id"} {
		got, ok := w.lastPatch[member]
		if !ok {
			t.Errorf("patch omits %s, which was set to the empty string — the unassign and the unparent are edits, not omissions", member)
			continue
		}
		if got != "" {
			t.Errorf("patch[%s] = %v, want the empty string the caller set", member, got)
		}
	}
}

// TestUpdateEncodesTheMetadataAlgebra drives the sub-document one arm at a time.
//
// The algebra is the ROLE's — merge, then set in key order, then unset, with
// replace replacing the whole document — and the wire publishes it member for
// member, so this asserts a projection rather than a translation. The one place
// the two genuinely disagree is the CLEAR: the role spells it as a set Replace
// holding no bytes, and no bytes is not a JSON value at all.
func TestUpdateEncodesTheMetadataAlgebra(t *testing.T) {
	tests := []struct {
		name  string
		patch issueops.MetadataPatch
		want  map[string]string
	}{
		{
			name:  "replace carries the caller's document",
			patch: issueops.MetadataPatch{Replace: set(json.RawMessage(`{"a":1}`))},
			want:  map[string]string{"replace": `{"a":1}`},
		},
		{
			name:  "an empty replacement is the clear, spelled as the empty document",
			patch: issueops.MetadataPatch{Replace: set(json.RawMessage(nil))},
			want:  map[string]string{"replace": `{}`},
		},
		{
			name:  "merge carries the overlay",
			patch: issueops.MetadataPatch{Merge: set(json.RawMessage(`{"b":2}`))},
			want:  map[string]string{"merge": `{"b":2}`},
		},
		{
			name: "set and unset travel together",
			patch: issueops.MetadataPatch{
				Set:   map[string]json.RawMessage{"k": json.RawMessage(`{"nested":[1,2]}`)},
				Unset: []string{"gone"},
			},
			want: map[string]string{"set": `{"k":{"nested":[1,2]}}`, "unset": `["gone"]`},
		},
		{
			name:  "a key stored as JSON null is a value, not an absence",
			patch: issueops.MetadataPatch{Set: map[string]json.RawMessage{"k": json.RawMessage(`null`)}},
			want:  map[string]string{"set": `{"k":null}`},
		},
		{
			// EVERY NUMBER TRAVELS AS THE CALLER'S OWN LITERAL, and this is the
			// case that says why it must. The role compares metadata values by
			// their SOURCE LITERAL — `1` and `1.0` are not equal — so a client
			// that decoded a value through `any` and re-encoded it would change
			// the request: `1.0` would arrive as `1`, an integer past 2^53 would
			// arrive rounded, and both would be compared against a stored value
			// under a spelling the caller never wrote. On the compare-and-set
			// path that is a changed VERDICT, not a cosmetic difference.
			//
			// The store renormalizes numbers on the way in and says so; what
			// must not happen is a SECOND renormalization here, before the
			// store has seen what was actually sent.
			name: "numbers travel as the caller's own literal",
			patch: issueops.MetadataPatch{
				Set: map[string]json.RawMessage{
					"tenth": json.RawMessage(`1.0`),
					"big":   json.RawMessage(`9007199254740993`),
				},
			},
			want: map[string]string{"set": `{"big":9007199254740993,"tenth":1.0}`},
		},
		{
			name:  "and so does a replacement document",
			patch: issueops.MetadataPatch{Replace: set(json.RawMessage(`{"n":1.0}`))},
			want:  map[string]string{"replace": `{"n":1.0}`},
		},
		{
			name:  "and so does a merge overlay",
			patch: issueops.MetadataPatch{Merge: set(json.RawMessage(`{"n":1.0}`))},
			want:  map[string]string{"merge": `{"n":1.0}`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			if _, err := lifecycle.Update(t.Context(), issueops.UpdateRequest{
				Actor: "writer", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Metadata: test.patch},
			}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			// Through the marshaler, because what the server reads is the
			// BYTES: a map of json.RawMessage that looked right in Go and
			// marshaled to something else would pass a field-by-field check.
			document, ok := w.lastPatch["metadata"]
			if !ok {
				t.Fatal("the patch carries no metadata member")
			}
			encoded, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("marshal the metadata document: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("re-read the metadata document: %v", err)
			}
			if len(got) != len(test.want) {
				t.Errorf("metadata document = %s, want exactly %v", encoded, sortedStringKeys(test.want))
			}
			for member, want := range test.want {
				if string(got[member]) != want {
					t.Errorf("metadata[%s] = %s, want %s", member, got[member], want)
				}
			}
		})
	}
}

// TestUpdateRefusesMetadataThatIsNotJSON keeps a blob that cannot go on a JSON
// wire off it, the way the create's own gate does. The blob's CONTENT is the
// role's business and travels verbatim; its well-formedness is this layer's,
// because bytes that are not JSON fail inside json.Marshal and would reach the
// caller as a transport fault where every role contract promises a
// deterministic validation failure.
func TestUpdateRefusesMetadataThatIsNotJSON(t *testing.T) {
	for name, patch := range map[string]issueops.MetadataPatch{
		"replace": {Replace: set(json.RawMessage(`{"k":`))},
		"merge":   {Merge: set(json.RawMessage(`not json`))},
		"set":     {Set: map[string]json.RawMessage{"k": json.RawMessage(`{`)}},
	} {
		t.Run(name, func(t *testing.T) {
			w := &stubWire{update: &apigen.UpdateIssueResponse{Revision: "0"}}
			lifecycle, err := stubStore(t, w).IssueLifecycle()
			if err != nil {
				t.Fatalf("IssueLifecycle(): %v", err)
			}
			_, err = lifecycle.Update(t.Context(), issueops.UpdateRequest{
				Actor: "writer", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Metadata: patch},
			})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("a malformed %s = %v, want ErrValidation", name, err)
			}
			if len(w.calls) != 0 {
				t.Errorf("the malformed blob reached the wire: %v", w.calls)
			}
		})
	}
}

// TestTheEncodedMetadataDocumentUsesOnlyPublishedMembers is the patch
// document's own gate one level down.
//
// TestTheEncodedPatchDocumentUsesOnlyPublishedMembers holds the TOP level to
// apigen.IssuePatchBody; the metadata member is a nested document built the
// same untyped way, and until this ran nothing held it to
// apigen.ApplyMetadataPatch at all. Both directions matter: a member the wire
// does not publish is a 400, and a member it publishes that no arm of
// MetadataPatch drives is an edit this client cannot make.
func TestTheEncodedMetadataDocumentUsesOnlyPublishedMembers(t *testing.T) {
	published := bodyMembers(t, reflect.TypeOf(apigen.ApplyMetadataPatch{}))

	// Replace is deliberately NOT set beside the other three: the wire refuses
	// that combination with a 400 and so does the role, so a patch setting all
	// four at once is not a request either side would answer. It gets its own
	// pass.
	document, err := encodeMetadataPatch(issueops.MetadataPatch{
		Merge: set(json.RawMessage(`{"m":1}`)),
		Set:   map[string]json.RawMessage{"s": json.RawMessage(`1`)},
		Unset: []string{"u"},
	})
	if err != nil {
		t.Fatalf("encode the incremental metadata edits: %v", err)
	}
	replacement, err := encodeMetadataPatch(issueops.MetadataPatch{Replace: set(json.RawMessage(`{}`))})
	if err != nil {
		t.Fatalf("encode the replacement: %v", err)
	}
	for member := range replacement {
		document[member] = replacement[member]
	}

	for member := range document {
		if !published[member] {
			t.Errorf("the encoded metadata document carries %q, which ApplyMetadataPatch does not publish: %v",
				member, sortedKeys(published))
		}
	}
	for member := range published {
		if _, ok := document[member]; !ok {
			t.Errorf("ApplyMetadataPatch publishes %q and no arm of MetadataPatch drives it; that member is unreachable from this client",
				member)
		}
	}
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
