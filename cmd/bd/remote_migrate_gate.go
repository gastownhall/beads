package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/schema"
)

const (
	// remoteBackedDocsURL covers the #4259 migrate-or-adopt decisions: the
	// database syncs with a remote and the question is which clone migrates.
	remoteBackedDocsURL = "https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md#remote-backed-databases-and-multiple-clones"
	// sharedServersDocsURL covers the #5920 shared-store decisions: one
	// database, many clients of one server, and the question is whether they
	// are all upgraded.
	sharedServersDocsURL = "https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md#shared-servers"
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
		// The shared-no-remote consent verb is the one command in this block
		// that is target-scoped: under --global the refused open aimed at
		// beads_global, so the project-scoped `bd migrate schema` would consent
		// the WRONG database and leave the refusal in place. Mirror the
		// human/text path (printGlobalDatabaseConsentHint,
		// noticeSharedMigrateRefusal) and name the --global verb when this
		// invocation targeted the global database. Only shared-no-remote is
		// retargeted; the remote-backed arms coordinate through bd bootstrap /
		// bd migrate --force, which --global does not rewrite.
		sharedConsent := schema.SharedConsentCommand
		if globalFlag {
			sharedConsent = schema.SharedConsentCommandGlobal
		}
		retargetShared := globalFlag && e.Decision == "shared-no-remote"

		opts := make([]map[string]interface{}, 0, len(e.Options()))
		for _, o := range e.Options() {
			commands := o.Commands
			if retargetShared {
				// The runnable command deliberately lives inside the option, not
				// in the top-level hint, so an agent that extracts it
				// programmatically must get the --global verb too — fixing only
				// "expected" would still hand it the wrong-target command.
				retargeted := make([]string, len(commands))
				for i, c := range commands {
					if c == schema.SharedConsentCommand {
						retargeted[i] = schema.SharedConsentCommandGlobal
					} else {
						retargeted[i] = c
					}
				}
				commands = retargeted
			}
			opts = append(opts, map[string]interface{}{
				"id":       o.ID,
				"when":     o.When,
				"commands": commands,
				"risk":     o.Risk,
			})
		}
		gate := map[string]interface{}{
			"current_version":         e.CurrentVersion,
			"latest_version":          e.LatestVersion,
			"pending":                 e.Pending,
			"severity":                "blocking",
			"human_decision_required": true,
			"observed":                fmt.Sprintf("%d pending schema migration(s) and a configured remote", e.Pending),
			"expected":                "exactly one designated clone migrates and publishes; every other clone adopts the result",
			"options":                 opts,
			"docs":                    remoteBackedDocsURL,
		}
		// Smart gate (#4516): when a state-aware decision narrowed the stop,
		// tell the agent which case it is and (for a fork) which versions skewed.
		switch e.Decision {
		case "adopt":
			gate["decision"] = "adopt"
			gate["observed"] = "the remote is already migrated; migrating here would fork it"
			gate["expected"] = "adopt the remote's migrated database (destructive re-clone — operator decision)"
		case "shared-no-remote":
			// #5920: no remote at all, so the migrate-or-adopt framing above
			// does not apply — there is one shared copy and the only question
			// is whether every client of it is upgraded. The base docs link
			// goes with that framing, so it has to move too: an agent
			// following it would brief the operator on designated-migrator
			// and bd bootstrap coordination that does not exist here.
			gate["decision"] = "shared-no-remote"
			gate["observed"] = fmt.Sprintf("%d pending schema migration(s) on a shared server database, no consent", e.Pending)
			gate["expected"] = "operator upgrades co-resident clients, then consents once via " + sharedConsent
			gate["docs"] = sharedServersDocsURL
		case "adopt-ff":
			// A strict refinement of adopt, and on a shared store now a
			// routine outcome rather than something auto-executed — so it
			// needs its own tailoring instead of inheriting the default
			// "exactly one designated clone migrates and publishes", which
			// contradicts the adopt-only options this decision carries.
			gate["decision"] = "adopt-ff"
			gate["observed"] = "the remote is already migrated and this clone can fast-forward to it losslessly (no unpushed commits, clean working set)"
			gate["expected"] = "adopt the remote's migrated database; nothing local is discarded"
		case "fork-skew":
			gate["decision"] = "fork-skew"
			gate["observed"] = fmt.Sprintf("this clone and the remote applied different content for migration(s) %s — already forked", schema.FormatMigrationVersions(e.SkewVersions))
			gate["expected"] = "pick one canonical clone and re-bootstrap the others (data-loss decision)"
			gate["skew_versions"] = e.SkewVersions
		default:
			// Blunt #4515 stop — name WHY the smart gate (#4516) could not do
			// better (gastownhall/beads#4551 follow-up), so an agent/operator can
			// tell "unreadable remote state" apart from "below the convergence
			// floor" apart from "opted out" apart from "unparseable BD_SMART_GATE".
			if e.FallbackReason != "" {
				gate["fallback_reason"] = e.FallbackReason
			}
		}
		m["remote_migrate_gate"] = gate
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
