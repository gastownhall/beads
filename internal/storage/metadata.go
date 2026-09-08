// Package storage defines the interface for issue storage backends.
package storage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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

// MetadataFieldType defines the type of a metadata field for schema validation.
type MetadataFieldType string

const (
	MetadataFieldString MetadataFieldType = "string"
	MetadataFieldInt    MetadataFieldType = "int"
	MetadataFieldFloat  MetadataFieldType = "float"
	MetadataFieldBool   MetadataFieldType = "bool"
	MetadataFieldEnum   MetadataFieldType = "enum"
)

// MetadataFieldSchema defines validation rules for a single metadata field.
type MetadataFieldSchema struct {
	Type     MetadataFieldType
	Values   []string // allowed values for enum type
	Required bool
	Min      *float64 // min value for int/float
	Max      *float64 // max value for int/float
}

// MetadataSchemaConfig holds the parsed metadata validation configuration.
type MetadataSchemaConfig struct {
	Mode   string                         // "none", "warn", "error"
	Fields map[string]MetadataFieldSchema // field name → schema
}

// MetadataValidationError describes a single schema violation.
type MetadataValidationError struct {
	Field   string
	Message string
}

func (e MetadataValidationError) Error() string {
	return fmt.Sprintf("metadata.%s: %s", e.Field, e.Message)
}

// ValidateMetadataSchema validates a metadata blob against a schema config.
// Returns a list of validation errors. An empty list means validation passed.
// If metadata is nil/empty and no fields are required, returns no errors.
func ValidateMetadataSchema(metadata json.RawMessage, schema MetadataSchemaConfig) []MetadataValidationError {
	if len(schema.Fields) == 0 {
		return nil
	}

	// Parse metadata into a map
	var parsed map[string]interface{}
	if len(metadata) == 0 || string(metadata) == "{}" || string(metadata) == "null" {
		parsed = map[string]interface{}{}
	} else {
		if err := json.Unmarshal(metadata, &parsed); err != nil {
			// Not a JSON object — can't validate fields
			return []MetadataValidationError{{Field: "(root)", Message: "metadata must be a JSON object for schema validation"}}
		}
	}

	var errs []MetadataValidationError

	for fieldName, fieldSchema := range schema.Fields {
		val, exists := parsed[fieldName]

		// Check required
		if fieldSchema.Required && !exists {
			errs = append(errs, MetadataValidationError{
				Field:   fieldName,
				Message: "required field is missing",
			})
			continue
		}

		if !exists {
			continue
		}

		// Type-check the value
		switch fieldSchema.Type {
		case MetadataFieldString:
			if _, ok := val.(string); !ok {
				errs = append(errs, MetadataValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("expected string, got %T", val),
				})
			}

		case MetadataFieldInt:
			num, ok := val.(float64)
			if !ok {
				errs = append(errs, MetadataValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("expected int, got %T", val),
				})
			} else {
				if num != float64(int64(num)) {
					errs = append(errs, MetadataValidationError{
						Field:   fieldName,
						Message: fmt.Sprintf("expected int, got float %v", num),
					})
				} else {
					if fieldSchema.Min != nil && num < *fieldSchema.Min {
						errs = append(errs, MetadataValidationError{
							Field:   fieldName,
							Message: fmt.Sprintf("value %v is below minimum %v", num, *fieldSchema.Min),
						})
					}
					if fieldSchema.Max != nil && num > *fieldSchema.Max {
						errs = append(errs, MetadataValidationError{
							Field:   fieldName,
							Message: fmt.Sprintf("value %v exceeds maximum %v", num, *fieldSchema.Max),
						})
					}
				}
			}

		case MetadataFieldFloat:
			num, ok := val.(float64)
			if !ok {
				errs = append(errs, MetadataValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("expected float, got %T", val),
				})
			} else {
				if fieldSchema.Min != nil && num < *fieldSchema.Min {
					errs = append(errs, MetadataValidationError{
						Field:   fieldName,
						Message: fmt.Sprintf("value %v is below minimum %v", num, *fieldSchema.Min),
					})
				}
				if fieldSchema.Max != nil && num > *fieldSchema.Max {
					errs = append(errs, MetadataValidationError{
						Field:   fieldName,
						Message: fmt.Sprintf("value %v exceeds maximum %v", num, *fieldSchema.Max),
					})
				}
			}

		case MetadataFieldBool:
			if _, ok := val.(bool); !ok {
				errs = append(errs, MetadataValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("expected bool, got %T", val),
				})
			}

		case MetadataFieldEnum:
			str, ok := val.(string)
			if !ok {
				errs = append(errs, MetadataValidationError{
					Field:   fieldName,
					Message: fmt.Sprintf("expected string (enum), got %T", val),
				})
			} else {
				found := false
				for _, allowed := range fieldSchema.Values {
					if str == allowed {
						found = true
						break
					}
				}
				if !found {
					errs = append(errs, MetadataValidationError{
						Field:   fieldName,
						Message: fmt.Sprintf("value %q is not one of: %v", str, fieldSchema.Values),
					})
				}
			}
		}
	}

	return errs
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

// JSONMetadataPath returns a MySQL/Dolt JSON path expression for the given
// metadata key. Keys containing dots are quoted so that "gc.routed_to"
// produces '$."gc.routed_to"' instead of '$.gc.routed_to' (which dolt
// interprets as a nested path: {gc: {routed_to: ...}}).
func JSONMetadataPath(key string) string {
	if strings.Contains(key, ".") {
		return `$."` + key + `"`
	}
	return "$." + key
}
