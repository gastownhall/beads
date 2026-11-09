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

// handleCloneLocalBehindJSON is the --json form of a CloneLocalBehindError:
// the standard error object, the remedy as its hint, and the detail under
// clone_local_schema. command is the per-database step, omitted for a
// gateway, whose schema only its server administrator migrates.
func handleCloneLocalBehindJSON(e *schema.CloneLocalBehindError) {
	outer := buildJSONError(e.Error(), e.Remedy())
	if m, ok := outer.(map[string]interface{}); ok {
		detail := map[string]interface{}{
			"current_version":       e.IgnoredVersion,
			"required_version":      e.LatestIgnoredVersion,
			"table":                 e.Table,
			"missing_columns":       e.MissingColumns,
			"main_version":          e.MainVersion,
			"main_required_version": e.BinaryVersion,
			"gateway":               e.Gateway,
		}
		if !e.Gateway {
			command := schema.SharedConsentCommand
			if globalFlag {
				command = schema.SharedConsentCommandGlobal
			}
			detail["command"] = command
		}
		m["clone_local_schema"] = detail
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
