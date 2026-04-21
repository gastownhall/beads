package main

import (
	"encoding/json"
	"os"
	"reflect"
)

// JSONSchemaVersion is the current version of the bd JSON output schema.
// Consumers can check this field to detect format changes. Bump when
// fields are added, renamed, or removed from any --json output.
const JSONSchemaVersion = 1

// outputJSON outputs data as pretty-printed JSON to stdout.
//
// Object input: schema_version is injected as a top-level field so consumers
// (Jawnt MCP, BeadsX, gt mail, sync scripts) can detect format changes.
// Array/slice input: output as-is (no envelope wrapping) to preserve
// backwards compatibility with existing consumers that parse raw arrays.
func outputJSON(v interface{}) {
	wrapped := wrapWithSchemaVersion(v)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(wrapped); err != nil {
		FatalError("encoding JSON: %v", err)
	}
}

// outputJSONRaw outputs data without schema_version wrapping.
// Use for internal/machine-only output that should not be versioned.
func outputJSONRaw(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		FatalError("encoding JSON: %v", err)
	}
}

// wrapWithSchemaVersion adds schema_version to object output. Arrays
// and slices are returned unchanged to preserve backwards compatibility
// with existing consumers that parse raw JSON arrays.
func wrapWithSchemaVersion(v interface{}) interface{} {
	if v == nil {
		return map[string]interface{}{"schema_version": JSONSchemaVersion}
	}

	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	// Arrays/slices: return as-is (no envelope) for backwards compat.
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return v
	}

	// Objects: inject schema_version as a top-level field.
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return v
	}
	m["schema_version"] = JSONSchemaVersion
	return m
}

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
func outputJSONError(err error, code string) {
	errObj := map[string]interface{}{
		"error":          err.Error(),
		"schema_version": JSONSchemaVersion,
	}
	if code != "" {
		errObj["code"] = code
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
}
