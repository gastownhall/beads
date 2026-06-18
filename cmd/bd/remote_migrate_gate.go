package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// handleRemoteMigrateGateJSON renders the #4259 remote-migrate gate error as a
// structured JSON error block, mirroring handleSchemaSkewJSON.
func handleRemoteMigrateGateJSON(e *schema.RemoteMigrateGateError) {
	outer := buildJSONError(e.Error(), e.EscapeHint())
	if m, ok := outer.(map[string]interface{}); ok {
		m["remote_migrate_gate"] = map[string]interface{}{
			"current_version": e.CurrentVersion,
			"latest_version":  e.LatestVersion,
			"pending":         e.Pending,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
