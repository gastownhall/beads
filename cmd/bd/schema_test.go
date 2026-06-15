package main

import (
	"encoding/json"
	"testing"
)

// TestSchemaDocument verifies `bd schema` emits a valid JSON Schema document
// pinned to JSONSchemaVersion, with the issue/dependency record schemas and the
// enriched string enums sourced from internal/types. It exercises the pure
// schemaDocument() builder, so it needs no workspace or database.
func TestSchemaDocument(t *testing.T) {
	raw, err := json.Marshal(schemaDocument())
	if err != nil {
		t.Fatalf("marshal schema document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema document is not valid JSON: %v", err)
	}

	if got, ok := doc["schema_version"].(float64); !ok || int(got) != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", doc["schema_version"], JSONSchemaVersion)
	}

	recordTypes, ok := doc["types"].(map[string]any)
	if !ok {
		t.Fatalf("types missing or wrong shape: %T", doc["types"])
	}

	// --- issue record ---
	issue := objectField(t, recordTypes, "issue")
	mustContain(t, "issue.required", strSlice(issue["required"]), "id", "title")
	issueProps := objectField(t, issue, "properties")
	for _, p := range []string{"id", "title", "status", "priority", "issue_type"} {
		if _, ok := issueProps[p]; !ok {
			t.Errorf("issue schema missing property %q", p)
		}
	}
	mustContain(t, "issue.status.enum", propEnum(t, issueProps, "status"), "open", "closed")
	mustContain(t, "issue.issue_type.enum", propEnum(t, issueProps, "issue_type"), "bug", "task", "epic")

	// --- dependency record ---
	dep := objectField(t, recordTypes, "dependency")
	depProps := objectField(t, dep, "properties")
	for _, p := range []string{"issue_id", "depends_on_id", "type"} {
		if _, ok := depProps[p]; !ok {
			t.Errorf("dependency schema missing property %q", p)
		}
	}
	mustContain(t, "dependency.type.enum", propEnum(t, depProps, "type"), "blocks", "parent-child")
}

func objectField(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	obj, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q missing or not an object: %T", key, m[key])
	}
	return obj
}

func propEnum(t *testing.T, props map[string]any, name string) []string {
	t.Helper()
	return strSlice(objectField(t, props, name)["enum"])
}

func strSlice(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mustContain(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("%s = %v, missing %q", label, got, w)
		}
	}
}
