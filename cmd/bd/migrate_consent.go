package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func handleMigrateConsentJSON(e *schema.MigrateConsentError) {
	outer := buildJSONError(e.Error(), "bd migrate schema  or  "+schema.AllowMigrateEnv+"=1 bd <command>")
	if m, ok := outer.(map[string]interface{}); ok {
		m["migrate_consent"] = map[string]interface{}{
			"current_version":  e.CurrentVersion,
			"required_version": e.LatestVersion,
			"pending":          e.Pending,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
