package main

import (
	"encoding/json"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// handleMigrateConsentJSON renders the migration-consent refusal as a
// structured JSON error block for agent consumption.
//
// It follows handleRemoteMigrateGateJSON's convention, for the same reason:
// migrating is one-way, so neither "error" nor the top-level "hint" carries a
// runnable remedy. "error" states the refusal, "hint" is the non-runnable
// operator-decision directive, and the migrate command lives only inside
// migrate_consent.options[migrate], gated on its precondition and annotated
// with its risk. Handing an agent `bd migrate schema` (or the consent
// environment variable) as "the fix" lets it migrate a database that an older
// bd still has to open; the env var stays out of this payload entirely.
func handleMigrateConsentJSON(e *schema.MigrateConsentError) {
	outer := buildJSONError(e.Refusal(), e.AgentDirective())
	if m, ok := outer.(map[string]interface{}); ok {
		opts := make([]map[string]interface{}, 0, len(e.Options()))
		for _, o := range e.Options() {
			commands := make([]string, len(o.Commands))
			for i, c := range o.Commands {
				// Under --global the refused open aimed at beads_global, so the
				// project-scoped verb would migrate the WRONG database and leave
				// the refusal in place. Same retarget as the text path
				// (printGlobalDatabaseConsentHint) and the shared-store gate.
				if globalFlag && c == schema.SharedConsentCommand {
					c = schema.SharedConsentCommandGlobal
				}
				commands[i] = c
			}
			opts = append(opts, map[string]interface{}{
				"id":       o.ID,
				"when":     o.When,
				"commands": commands,
				"risk":     o.Risk,
			})
		}
		m["migrate_consent"] = map[string]interface{}{
			"current_version":         e.CurrentVersion,
			"required_version":        e.LatestVersion,
			"pending":                 e.Pending,
			"severity":                "blocking",
			"human_decision_required": true,
			"options":                 opts,
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
