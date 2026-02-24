package main

import (
	"encoding/json"
	"testing"
)

func TestApplyMetadataEdits_SetNewKey(t *testing.T) {
	t.Parallel()
	result, err := applyMetadataEdits(nil, []string{"team=platform"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"platform"` {
		t.Errorf("expected \"platform\", got %s", data["team"])
	}
}

func TestApplyMetadataEdits_SetOverwritesExisting(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"team":"old","sprint":"Q1"}`)
	result, err := applyMetadataEdits(existing, []string{"team=new"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"new"` {
		t.Errorf("expected \"new\", got %s", data["team"])
	}
	// sprint should be preserved
	if string(data["sprint"]) != `"Q1"` {
		t.Errorf("expected \"Q1\", got %s", data["sprint"])
	}
}

func TestApplyMetadataEdits_UnsetKey(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"team":"platform","sprint":"Q1"}`)
	result, err := applyMetadataEdits(existing, nil, []string{"team"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := data["team"]; ok {
		t.Error("expected team key to be removed")
	}
	if string(data["sprint"]) != `"Q1"` {
		t.Errorf("expected \"Q1\", got %s", data["sprint"])
	}
}

func TestApplyMetadataEdits_SetAndUnset(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"team":"platform","sprint":"Q1"}`)
	result, err := applyMetadataEdits(existing, []string{"env=prod"}, []string{"sprint"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"platform"` {
		t.Errorf("expected \"platform\", got %s", data["team"])
	}
	if string(data["env"]) != `"prod"` {
		t.Errorf("expected \"prod\", got %s", data["env"])
	}
	if _, ok := data["sprint"]; ok {
		t.Error("expected sprint key to be removed")
	}
}

func TestApplyMetadataEdits_NumericValue(t *testing.T) {
	t.Parallel()
	result, err := applyMetadataEdits(nil, []string{"story_points=5"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["story_points"]) != "5" {
		t.Errorf("expected 5, got %s", data["story_points"])
	}
}

func TestApplyMetadataEdits_BoolValue(t *testing.T) {
	t.Parallel()
	result, err := applyMetadataEdits(nil, []string{"urgent=true"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["urgent"]) != "true" {
		t.Errorf("expected true, got %s", data["urgent"])
	}
}

func TestApplyMetadataEdits_NullValue(t *testing.T) {
	t.Parallel()
	result, err := applyMetadataEdits(nil, []string{"cleared=null"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["cleared"]) != "null" {
		t.Errorf("expected null, got %s", data["cleared"])
	}
}

func TestApplyMetadataEdits_EmptyExisting(t *testing.T) {
	t.Parallel()
	// Empty metadata (nil)
	result, err := applyMetadataEdits(nil, []string{"team=platform"}, nil)
	if err != nil {
		t.Fatalf("nil metadata: %v", err)
	}
	if !json.Valid(result) {
		t.Errorf("result is not valid JSON: %s", result)
	}

	// Empty JSON object
	result, err = applyMetadataEdits(json.RawMessage(`{}`), []string{"team=platform"}, nil)
	if err != nil {
		t.Fatalf("empty object: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"platform"` {
		t.Errorf("expected \"platform\", got %s", data["team"])
	}
}

func TestApplyMetadataEdits_InvalidKey(t *testing.T) {
	t.Parallel()
	_, err := applyMetadataEdits(nil, []string{"bad key=val"}, nil)
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestApplyMetadataEdits_InvalidUnsetKey(t *testing.T) {
	t.Parallel()
	_, err := applyMetadataEdits(nil, nil, []string{"bad key"})
	if err == nil {
		t.Fatal("expected error for invalid unset key")
	}
}

func TestApplyMetadataEdits_InvalidFormat(t *testing.T) {
	t.Parallel()
	_, err := applyMetadataEdits(nil, []string{"noequalssign"}, nil)
	if err == nil {
		t.Fatal("expected error for missing =")
	}
}

func TestApplyMetadataEdits_NonObjectExisting(t *testing.T) {
	t.Parallel()
	_, err := applyMetadataEdits(json.RawMessage(`"just a string"`), []string{"team=platform"}, nil)
	if err == nil {
		t.Fatal("expected error for non-object metadata")
	}
}

func TestApplyMetadataEdits_MultipleSetFlags(t *testing.T) {
	t.Parallel()
	result, err := applyMetadataEdits(nil, []string{"team=platform", "sprint=Q1", "priority=2"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"platform"` {
		t.Errorf("expected \"platform\", got %s", data["team"])
	}
	if string(data["sprint"]) != `"Q1"` {
		t.Errorf("expected \"Q1\", got %s", data["sprint"])
	}
	if string(data["priority"]) != "2" {
		t.Errorf("expected 2, got %s", data["priority"])
	}
}

func TestApplyMetadataEdits_UnsetNonexistentKey(t *testing.T) {
	t.Parallel()
	existing := json.RawMessage(`{"team":"platform"}`)
	result, err := applyMetadataEdits(existing, nil, []string{"nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(data["team"]) != `"platform"` {
		t.Errorf("expected \"platform\", got %s", data["team"])
	}
}

func TestToJSONValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", `"hello"`},
		{"42", "42"},
		{"3.14", "3.14"},
		{"true", "true"},
		{"false", "false"},
		{"null", "null"},
		{"", `""`},
		{"hello world", `"hello world"`},
		{"0", "0"},
		{"-1", "-1"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := string(toJSONValue(tt.input))
			if got != tt.expected {
				t.Errorf("toJSONValue(%q) = %s, want %s", tt.input, got, tt.expected)
			}
		})
	}
}
