package main

import (
	"encoding/json"
	"testing"
)

func TestJsonStderrError_StructuredOutput(t *testing.T) {
	tests := []struct {
		name    string
		message string
		hint    string
	}{
		{"message_only", "database not found", ""},
		{"message_with_hint", "database not found", "Run 'bd init' to create one"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := map[string]interface{}{
				"schema_version": JSONSchemaVersion,
				"error":          tt.message,
			}
			if tt.hint != "" {
				obj["hint"] = tt.hint
			}

			data, err := json.Marshal(obj)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if parsed["schema_version"] != float64(JSONSchemaVersion) {
				t.Errorf("schema_version = %v, want %d", parsed["schema_version"], JSONSchemaVersion)
			}
			if parsed["error"] != tt.message {
				t.Errorf("error = %v, want %s", parsed["error"], tt.message)
			}
			if tt.hint != "" {
				if parsed["hint"] != tt.hint {
					t.Errorf("hint = %v, want %s", parsed["hint"], tt.hint)
				}
			} else {
				if _, ok := parsed["hint"]; ok {
					t.Errorf("hint should not be present when empty")
				}
			}
		})
	}
}
