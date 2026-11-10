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
// Allows alphanumeric, underscore, dot (dotted keys like "jira.sprint"), and
// slash (path-style keys like "jira/sprint").
var validMetadataKeyRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_./]*$`)

// ValidateMetadataKey checks that a metadata key is safe for use in JSON path
// expressions. Keys must start with a letter or underscore and contain only
// alphanumeric characters, underscores, dots, and slashes.
func ValidateMetadataKey(key string) error {
	if !validMetadataKeyRe.MatchString(key) {
		return fmt.Errorf("invalid metadata key %q: must match %s", key, validMetadataKeyRe.String())
	}
	return nil
}

// JSONMetadataPath returns a MySQL/Dolt JSON path expression for the given
// metadata key. The key is always quoted so that "gc.routed_to" produces
// '$."gc.routed_to"' instead of '$.gc.routed_to' (which dolt interprets as a
// nested path: {gc: {routed_to: ...}}); quoting is valid for plain keys too,
// so no character list needs to stay in sync with validMetadataKeyRe. Slash
// and mixed-case keys are proven to round-trip through the real Dolt/
// go-mysql-server JSON path parser (see TestMetadataFilterSuite's
// MetadataFieldMatchSlashKey and MetadataFieldMatchMixedCaseKey subtests in
// cmd/bd/metadata_filter_test.go).
//
// Backslashes and quotes are also escaped, but every production caller
// (sqlbuild.AppendMetadataClauses and doltTransaction.SearchIssues) validates
// the key with ValidateMetadataKey first, which rejects `"` and `\`, so those escaping
// branches are unreachable in practice today and are exercised only by the
// string-level unit test in metadata_jsonpath_test.go, not against the real
// SQL engines. Treat that escaping as defense-in-depth, not a proven
// contract, unless a caller starts passing unvalidated keys here.
func JSONMetadataPath(key string) string {
	return `$."` + jsonPathEscaper.Replace(key) + `"`
}

var jsonPathEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// MergeMetadataJSON merges incoming metadata JSON into existing metadata.
// Top-level keys from incoming overwrite keys in existing; keys only in
// existing are preserved. Both inputs must be JSON objects (or empty/null).
func MergeMetadataJSON(existing, incoming json.RawMessage) (json.RawMessage, error) {
	base := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &base); err != nil {
				return nil, fmt.Errorf("existing metadata is not a JSON object: %w", err)
			}
		}
	}

	overlay := make(map[string]json.RawMessage)
	if err := json.Unmarshal(incoming, &overlay); err != nil {
		return nil, fmt.Errorf("new metadata is not a JSON object: %w", err)
	}

	for k, v := range overlay {
		base[k] = v
	}

	result, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged metadata: %w", err)
	}
	return json.RawMessage(result), nil
}

// ApplyMetadataEdits applies incremental set (key=value) and unset (key) edits
// to existing metadata and returns the merged JSON. Set values are typed via
// MetadataEditValue; keys are validated with ValidateMetadataKey.
func ApplyMetadataEdits(existing json.RawMessage, setFlags, unsetFlags []string) (json.RawMessage, error) {
	data := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &data); err != nil {
				return nil, fmt.Errorf("existing metadata is not a JSON object: %w", err)
			}
		}
	}

	for _, kv := range setFlags {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --set-metadata: expected key=value, got %q", kv)
		}
		if err := ValidateMetadataKey(k); err != nil {
			return nil, err
		}
		data[k] = MetadataEditValue(v)
	}

	for _, k := range unsetFlags {
		if err := ValidateMetadataKey(k); err != nil {
			return nil, err
		}
		delete(data, k)
	}

	result, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return json.RawMessage(result), nil
}

// MetadataEditValue converts a --set-metadata string value to JSON, inferring
// the scalar type from the value's spelling: "null" is JSON null, "true"/"false"
// are booleans, anything that scans as a float AND is valid JSON on its own is
// emitted as that literal (so 5 -> 5, 1e3 -> 1e3, while 05, +5 and NaN stay
// strings because they are not valid JSON), and everything else is a string.
//
// This is the shipped v1.2.2 CLI contract (v1.2.2's cmd/bd/update.go toJSONValue,
// copied body-for-body so the parity claim stays checkable). GH#4146 asked for
// string-always so that map[string]string Go consumers keep numeric-looking ids
// and versions like "1e3" intact; that briefly landed here and silently changed
// the CLI contract, so it is served instead by the explicitly typed routes —
// `--metadata` / `--metadata-json` (MergeMetadataJSON) on the CLI and the typed
// set map on the HTTP/Go path (issueops.applyTypedMetadataEdits).
func MetadataEditValue(s string) json.RawMessage {
	if s == "null" {
		return json.RawMessage("null")
	}
	if s == "true" || s == "false" {
		return json.RawMessage(s)
	}
	// Numbers (integer or float), but only when the spelling is also valid JSON
	// on its own — that rejects NaN, Inf, "+5" and leading-zero forms.
	if _, err := fmt.Sscanf(s, "%f", new(float64)); err == nil {
		if json.Valid([]byte(s)) {
			return json.RawMessage(s)
		}
	}
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}
