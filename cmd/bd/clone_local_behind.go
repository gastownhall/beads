package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// handleCloneLocalBehindJSON renders CloneLocalBehindError under --json,
// following handleSchemaSkewJSON: the generic error/hint envelope plus a typed
// block an agent can dispatch on without parsing prose. The gateway variant's
// block omits the consent verbs the human path withholds too — bd never
// migrates a gateway's schema — and names the server administrator instead.
func handleCloneLocalBehindJSON(e *schema.CloneLocalBehindError) {
	outer := buildJSONError(e.Error(), e.EscapeHint())
	if m, ok := outer.(map[string]interface{}); ok {
		m["clone_local_schema"] = map[string]interface{}{
			"current_local_version": e.DBIgnoredVersion,
			"latest_local_version":  e.LatestIgnoredVersion,
			"missing_columns":       e.MissingColumns,
			"main_version":          e.MainVersion,
			"latest_main_version":   e.LatestMainVersion,
			"gateway":               e.Gateway,
			"severity":              "blocking",
			"docs":                  "https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md",
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
