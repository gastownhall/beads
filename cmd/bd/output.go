package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/steveyegge/beads/internal/ui"
)

// JSONSchemaVersion is the current version of the bd JSON output schema.
// Consumers can check this field to detect format changes. Bump when
// fields are added, renamed, or removed from any --json output.
const JSONSchemaVersion = 1

// jsonEnvelopeEnabled returns true when BD_JSON_ENVELOPE=1 is set,
// opting into the uniform {"schema_version": N, "data": <payload>}
// envelope for all --json output. This will become the default in v2.0.
func jsonEnvelopeEnabled() bool {
	return os.Getenv("BD_JSON_ENVELOPE") == "1"
}

// outputJSON outputs data as pretty-printed JSON to stdout.
//
// When BD_JSON_ENVELOPE=1: all output is wrapped uniformly as
// {"schema_version": N, "data": <original>}. The original payload
// is untouched inside .data — no type corruption, no injection.
//
// Legacy mode (default): objects get schema_version injected as a
// top-level field; arrays pass through unchanged.
func outputJSON(v interface{}) {
	wrapped := wrapWithSchemaVersion(v)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(wrapped); err != nil {
		FatalError("encoding JSON: %v", err)
	}

	if !jsonEnvelopeEnabled() {
		emitEnvelopeDeprecation()
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

// wrapWithSchemaVersion wraps output with schema_version metadata.
//
// Envelope mode (BD_JSON_ENVELOPE=1): all output wrapped uniformly as
// {"schema_version": N, "data": <original>}. Type-safe for all payload
// types including map[string]string and slices.
//
// Legacy mode: objects get schema_version injected inline; arrays and
// slices pass through unchanged for backwards compatibility.
func wrapWithSchemaVersion(v interface{}) interface{} {
	if jsonEnvelopeEnabled() {
		return map[string]interface{}{
			"schema_version": JSONSchemaVersion,
			"data":           v,
		}
	}

	// Legacy mode: inline injection for objects, passthrough for arrays.
	if v == nil {
		return map[string]interface{}{"schema_version": JSONSchemaVersion}
	}

	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return v
	}

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

var envelopeDeprecationEmitted bool

// emitEnvelopeDeprecation prints a one-time deprecation notice to stderr
// when --json output is used without BD_JSON_ENVELOPE=1.
func emitEnvelopeDeprecation() {
	if envelopeDeprecationEmitted || !ui.IsStderrTerminal() {
		return
	}
	envelopeDeprecationEmitted = true
	fmt.Fprintf(os.Stderr,
		"NOTE: bd --json output format will change in v2.0. "+
			"Set BD_JSON_ENVELOPE=1 to opt in early. "+
			"See docs/JSON_SCHEMA.md for migration details.\n")
}

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
func outputJSONError(err error, code string) {
	var errObj interface{}
	base := map[string]interface{}{
		"error": err.Error(),
	}
	if code != "" {
		base["code"] = code
	}
	if jsonEnvelopeEnabled() {
		errObj = map[string]interface{}{
			"schema_version": JSONSchemaVersion,
			"data":           base,
		}
	} else {
		base["schema_version"] = JSONSchemaVersion
		errObj = base
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
}
