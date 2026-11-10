package issueops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestResolveMergeOpsTypedMetadataSetPreservesJSONValuesAndOrder(t *testing.T) {
	resolved, err := ResolveMergeOps(&types.Issue{Metadata: json.RawMessage(`{"keep":"yes","remove":"gone","overlap":"old"}`)}, map[string]any{
		OpMergeMetadata: json.RawMessage(`{"merged":{"source":"merge"},"overlap":"merged"}`),
		OpSetMetadata: map[string]json.RawMessage{
			"nested":  json.RawMessage(`{"enabled":true}`),
			"number":  json.RawMessage(`7`),
			"bool":    json.RawMessage(`true`),
			"overlap": json.RawMessage(`"set"`),
		},
		OpUnsetMetadata: []string{"overlap", "remove"},
	})
	if err != nil {
		t.Fatalf("ResolveMergeOps() error = %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(resolved["metadata"].(json.RawMessage), &metadata); err != nil {
		t.Fatalf("unmarshal resolved metadata: %v", err)
	}
	if metadata["keep"] != "yes" || metadata["number"] != float64(7) || metadata["bool"] != true {
		t.Fatalf("metadata = %#v", metadata)
	}
	if _, ok := metadata["overlap"]; ok {
		t.Fatalf("metadata overlap survived unset: %#v", metadata)
	}
	if _, ok := metadata["remove"]; ok {
		t.Fatalf("metadata remove survived unset: %#v", metadata)
	}
}

func TestResolveMergeOpsSetMetadataKeepsCLIListForms(t *testing.T) {
	for _, set := range []any{
		[]string{"first=one", "second=two"},
		[]any{"first=one", "second=two"},
	} {
		resolved, err := ResolveMergeOps(&types.Issue{Metadata: json.RawMessage(`{"keep":"yes"}`)}, map[string]any{OpSetMetadata: set})
		if err != nil {
			t.Fatalf("ResolveMergeOps(%T) error = %v", set, err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(resolved["metadata"].(json.RawMessage), &metadata); err != nil {
			t.Fatalf("unmarshal resolved metadata: %v", err)
		}
		if metadata["keep"] != "yes" || metadata["first"] != "one" || metadata["second"] != "two" {
			t.Fatalf("ResolveMergeOps(%T) metadata = %#v", set, metadata)
		}
	}
}

func TestResolveMergeOpsSurfacesUntypedSetMetadataFailure(t *testing.T) {
	for _, set := range []any{
		[]string{"missing-separator"},
		[]any{"missing-separator"},
	} {
		resolved, err := ResolveMergeOps(&types.Issue{Metadata: json.RawMessage(`{"keep":"yes"}`)}, map[string]any{OpSetMetadata: set})
		if err == nil {
			t.Fatalf("ResolveMergeOps(%T) error = nil, want a metadata edit failure; resolved = %#v", set, resolved)
		}
		if !strings.Contains(err.Error(), "metadata edit failed") {
			t.Fatalf("ResolveMergeOps(%T) error = %v, want it wrapped as a metadata edit failure", set, err)
		}
	}
}
