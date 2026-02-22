// Package storage defines the interface for issue storage backends.
package storage

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// validMetadataKeyRe validates metadata key names for use in JSON path expressions.
// Allows alphanumeric, underscore, and dot (for nested paths like "jira.sprint").
var validMetadataKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// ValidateMetadataKey checks that a metadata key is safe for use in JSON path
// expressions. Keys must start with a letter or underscore and contain only
// alphanumeric characters, underscores, and dots.
func ValidateMetadataKey(key string) error {
	if !validMetadataKeyRe.MatchString(key) {
		return fmt.Errorf("invalid metadata key %q: must match [a-zA-Z_][a-zA-Z0-9_.]*", key)
	}
	return nil
}
