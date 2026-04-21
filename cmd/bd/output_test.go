package main

import (
	"encoding/json"
	"testing"
)

func TestWrapWithSchemaVersion_Object(t *testing.T) {
	input := map[string]string{"id": "beads-123", "title": "Test"}
	result := wrapWithSchemaVersion(input)

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
	if m["id"] != "beads-123" {
		t.Errorf("id = %v, want beads-123", m["id"])
	}
}

func TestWrapWithSchemaVersion_Struct(t *testing.T) {
	type issue struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	input := &issue{ID: "beads-456", Title: "Struct test"}
	result := wrapWithSchemaVersion(input)

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
	if m["id"] != "beads-456" {
		t.Errorf("id = %v, want beads-456", m["id"])
	}
}

func TestWrapWithSchemaVersion_Slice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := wrapWithSchemaVersion(input)

	// Arrays pass through unchanged for backwards compatibility.
	arr, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string (passthrough), got %T", result)
	}
	if len(arr) != 3 {
		t.Errorf("slice length = %d, want 3", len(arr))
	}
}

func TestWrapWithSchemaVersion_Nil(t *testing.T) {
	result := wrapWithSchemaVersion(nil)
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
}

func TestWrapWithSchemaVersion_RoundTrip(t *testing.T) {
	input := map[string]interface{}{"count": 42, "name": "test"}
	result := wrapWithSchemaVersion(input)

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	sv, ok := parsed["schema_version"]
	if !ok {
		t.Fatal("schema_version missing after round-trip")
	}
	if sv != float64(JSONSchemaVersion) {
		t.Errorf("schema_version = %v, want %v", sv, float64(JSONSchemaVersion))
	}
}
