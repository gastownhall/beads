package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func handleSchemaSkewJSON(e *schema.SchemaSkewError) {
	outer := buildJSONError(e.Error(), e.EscapeHint())
	if m, ok := outer.(map[string]interface{}); ok {
		m["schema_skew"] = map[string]interface{}{
			"current_version":  e.DBVersion,
			"required_version": e.BinaryVersion,
			"delta":            e.DBVersion - e.BinaryVersion,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
