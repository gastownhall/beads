package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// handleSchemaEncodingDriftJSON renders the schema-encoding-drift refusal
// (gastownhall/beads#5699, dolthub/dolt#11131) as the standard JSON error
// envelope plus a schema_encoding_drift object carrying the migration
// context, the bounded symptom line, and the recovery URL — following the
// schema_skew pattern so every JSON consumer gets the same envelope shape.
//
// The top-level "hint" is deliberately operator-supervised with no bypass
// command: the affected rows are physically unrepairable via SQL, so no flag
// can safely push this refusal aside, and recovery happens out of band on a
// preserved copy.
func handleSchemaEncodingDriftJSON(e *schema.SchemaEncodingDriftError) {
	outer := buildJSONError(e.Error(),
		"operator-supervised recovery required; preserve a full copy of the database before any further write. "+
			"No flag bypasses this refusal.")
	if m, ok := outer.(map[string]interface{}); ok {
		m["schema_encoding_drift"] = map[string]interface{}{
			"migration":    e.Migration,
			"version":      e.Version,
			"cursor_table": e.CursorTable,
			"symptom":      e.Symptom(),
			"recovery_url": schema.SchemaEncodingDriftRecoveryURL,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}

// renderSchemaEncodingDriftRefusal prints the schema-encoding-drift refusal in
// the active output mode and reports whether it handled err. Both the store-
// open renderer (renderTypedOpenError) and the explicit `bd migrate schema`
// failure path call it, so a drift refusal renders identically wherever the
// migration pass runs; a false return means the caller falls back to its own
// generic handling.
func renderSchemaEncodingDriftRefusal(err error) bool {
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
