package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// handleRemoteMigrateGateJSON renders the #4259 remote-migrate gate error as a
// structured JSON error block for agent consumption.
//
// The top-level "hint" is deliberately a non-runnable directive, NOT the
// `BD_ALLOW_REMOTE_MIGRATE=1 bd migrate` escape command: handing an agent a
// ready-to-run migrate as "the fix" is the footgun that forks shared remotes on
// multi-clone setups. The migrate command lives only inside
// remote_migrate_gate.options[migrate], gated on its "single designated
// migrator" precondition and annotated with its risk, so the agent surfaces a
// human decision instead of auto-running it.
func handleRemoteMigrateGateJSON(e *schema.RemoteMigrateGateError) {
	outer := buildJSONError(e.Error(), e.AgentDirective())
	if m, ok := outer.(map[string]interface{}); ok {
		opts := make([]map[string]interface{}, 0, len(e.Options()))
		for _, o := range e.Options() {
			opts = append(opts, map[string]interface{}{
				"id":       o.ID,
				"when":     o.When,
				"commands": o.Commands,
				"risk":     o.Risk,
			})
		}
		m["remote_migrate_gate"] = map[string]interface{}{
			"current_version":         e.CurrentVersion,
			"latest_version":          e.LatestVersion,
			"pending":                 e.Pending,
			"severity":                "blocking",
			"human_decision_required": true,
			"observed":                fmt.Sprintf("%d pending schema migration(s) and a configured remote", e.Pending),
			"expected":                "exactly one designated clone migrates and publishes; every other clone adopts the result",
			"options":                 opts,
			"docs":                    "https://github.com/gastownhall/beads/blob/main/website/docs/getting-started/upgrading.md#remote-backed-databases-and-multiple-clones",
		}
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
