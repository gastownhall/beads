package main

import (
	"encoding/json"
	"testing"
)

func TestWrapWithSchemaVersion_Legacy_Object(t *testing.T) {
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

func TestWrapWithSchemaVersion_Legacy_Slice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := wrapWithSchemaVersion(input)

	arr, ok := result.([]string)
	if !ok {
		t.Fatalf("expected []string (passthrough), got %T", result)
	}
	if len(arr) != 3 {
		t.Errorf("slice length = %d, want 3", len(arr))
	}
}

func TestWrapWithSchemaVersion_Legacy_Nil(t *testing.T) {
	result := wrapWithSchemaVersion(nil)
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
}

func TestWrapWithSchemaVersion_Envelope_Object(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "1")

	input := map[string]string{"id": "beads-123", "title": "Test"}
	result := wrapWithSchemaVersion(input)

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{}, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
	data, ok := m["data"]
	if !ok {
		t.Fatal("missing 'data' key in envelope")
	}
	inner, ok := data.(map[string]string)
	if !ok {
		t.Fatalf("data type = %T, want map[string]string", data)
	}
	if inner["id"] != "beads-123" {
		t.Errorf("data.id = %v, want beads-123", inner["id"])
	}
}

func TestWrapWithSchemaVersion_Envelope_Slice(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "1")

	input := []string{"a", "b", "c"}
	result := wrapWithSchemaVersion(input)

	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected envelope map, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
	data, ok := m["data"]
	if !ok {
		t.Fatal("missing 'data' key in envelope")
	}
	arr, ok := data.([]string)
	if !ok {
		t.Fatalf("data type = %T, want []string", data)
	}
	if len(arr) != 3 {
		t.Errorf("data length = %d, want 3", len(arr))
	}
}

func TestWrapWithSchemaVersion_Envelope_Nil(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "1")

	result := wrapWithSchemaVersion(nil)
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected envelope map, got %T", result)
	}
	if m["schema_version"] != JSONSchemaVersion {
		t.Errorf("schema_version = %v, want %d", m["schema_version"], JSONSchemaVersion)
	}
	if m["data"] != nil {
		t.Errorf("data = %v, want nil", m["data"])
	}
}

func TestWrapWithSchemaVersion_Envelope_RoundTrip(t *testing.T) {
	t.Setenv("BD_JSON_ENVELOPE", "1")

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

	if parsed["schema_version"] != float64(JSONSchemaVersion) {
		t.Errorf("schema_version = %v, want %v", parsed["schema_version"], float64(JSONSchemaVersion))
	}
	innerData, ok := parsed["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data type = %T, want map[string]interface{}", parsed["data"])
	}
	if innerData["count"] != float64(42) {
		t.Errorf("data.count = %v, want 42", innerData["count"])
	}
}
