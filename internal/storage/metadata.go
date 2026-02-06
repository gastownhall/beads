// Package storage defines the interface for issue storage backends.
package storage

import (
	"encoding/json"
	"fmt"
)

// NormalizeMetadataValue converts metadata values to a validated JSON string.
// Accepts string, []byte, or json.RawMessage and returns a validated JSON string.
// Returns an error if the value is not valid JSON or is an unsupported type.
//
// This supports GH#1417: allow UpdateIssue metadata updates via json.RawMessage/[]byte.
func NormalizeMetadataValue(value interface{}) (string, error) {
	var jsonStr string

	switch v := value.(type) {
	case string:
		jsonStr = v
	case []byte:
		jsonStr = string(v)
	case json.RawMessage:
		jsonStr = string(v)
	default:
		return "", fmt.Errorf("metadata must be string, []byte, or json.RawMessage, got %T", value)
	}

	// Validate that it's valid JSON
	if !json.Valid([]byte(jsonStr)) {
		return "", fmt.Errorf("metadata is not valid JSON")
	}

	return jsonStr, nil
}
