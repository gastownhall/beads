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
// command is the per-database step (with --global when the invocation
// targeted the global database), omitted for a gateway. The block sits beside
// error and hint, so under BD_JSON_ENVELOPE it goes inside data with them:
// an agent that unwraps the envelope keeps it.
func handleCloneLocalBehindJSON(e *schema.CloneLocalBehindError) {
	outer := buildJSONError(e.Error(), e.EscapeHint())
	payload, ok := outer.(map[string]interface{})
	if data, enveloped := payload["data"].(map[string]interface{}); enveloped {
		payload = data
	}
	if ok {
		detail := map[string]interface{}{
			"current_local_version": e.DBIgnoredVersion,
			"latest_local_version":  e.LatestIgnoredVersion,
			"missing_columns":       e.MissingColumns,
			"main_version":          e.MainVersion,
			"latest_main_version":   e.LatestMainVersion,
			"gateway":               e.Gateway,
			"severity":              "blocking",
			"docs":                  "https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md",
		}
		if !e.Gateway {
			command := schema.SharedConsentCommand
			if globalFlag {
				command = schema.SharedConsentCommandGlobal
			}
			detail["command"] = command
		}
		payload["clone_local_schema"] = detail
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
