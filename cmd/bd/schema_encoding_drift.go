package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// renderSchemaEncodingDrift renders a schema migration that stopped on
// schema-encoding drift (gastownhall/beads#5699) and reports whether err
// carried one. The store-open paths reach it through renderTypedOpenError and
// `bd migrate schema` calls it directly, so both print the same refusal.
func renderSchemaEncodingDrift(err error) bool {
	var driftErr *schema.SchemaEncodingDriftError
	if !errors.As(err, &driftErr) {
		return false
	}
	if jsonOutput {
		handleSchemaEncodingDriftJSON(driftErr)
	} else {
		fmt.Fprint(os.Stderr, driftErr.UserMessage())
	}
	return true
}

// handleSchemaEncodingDriftJSON writes the refusal as a JSON error whose
// schema_encoding_drift object sits beside error and hint, so with
// BD_JSON_ENVELOPE=1 it is inside data, where the envelope puts the error
// payload (docs/reference/json-schema.md).
func handleSchemaEncodingDriftJSON(e *schema.SchemaEncodingDriftError) {
	outer := buildJSONError(e.Error(), e.Hint())
	if m, ok := outer.(map[string]interface{}); ok {
		if inner, ok := m["data"].(map[string]interface{}); ok {
			m = inner
		}
		m["schema_encoding_drift"] = map[string]interface{}{
			"migration":     e.Migration,
			"version":       e.Version,
			"cursor_source": e.CursorTable,
			"symptom":       e.Symptom(),
			"recovery_url":  schema.SchemaEncodingDriftRecoveryURL,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
