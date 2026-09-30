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
	// dataBehindDocsURL covers the #6575 data-behind stop. It needs its own
	// anchor for the same reason sharedServersDocsURL does: the remote-backed
	// section's recipe is migrate-or-adopt coordination, and its ordering rule
	// says `bd dolt pull` is refused on every pending-migration open — which is
	// exactly the command this stop's options prescribe. An agent following the
	// default link would be steered off the one command that works.
	dataBehindDocsURL = "https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md#clone-behind-the-remote"
)

// humanDecisionRequired reports whether this refusal is one an agent must stop
// and hand to a human, rather than act on.
//
// Every arm of this gate but one is a coordination or data-loss decision the
// process cannot make — which clone is the designated migrator, whether it is
// safe to discard unpushed work, whether every co-resident client is upgraded
// — so the answer is true by default, and was hard-coded true before the
// #6575 data-behind stop existed.
//
// The data-behind stop is the exception, and only in its narrowest shape. It
// carries a single option whose command is `bd dolt pull`, whose When is
// unconditional ("always, for this stop") and whose Risk is "none — a pure
// fast-forward"; AgentDirective says the same in prose. Leaving the field true
// there made this payload contradict itself: an agent keying on it stopped to
// ask a human to approve a step the rest of the payload calls unconditional
// and riskless — and the cost of that stall is the operator reaching for
// `bd migrate --force` instead, which is the #6575 wedge.
//
// Both narrowing conditions are deliberate, and getting either backwards would
// be worse than leaving the field true:
//
//   - DataDiverged: the clone has commits of its own, so the pull MERGES.
//     Options() already says so — its Risk for this shape is conflicts "that
//     must be resolved before it completes", with a `--strategy ours|theirs`
//     choice behind them. A merge that can require conflict resolution, and a
//     strategy that decides whose rows win, is exactly a human decision.
//     AgentDirective agrees ("surface a conflict outcome to the operator
//     rather than forcing past it"). Stays true.
//
//   - Shared: the pull itself is still safe, but on a shared store the payload
//     carries a SECOND option, migrate-shared-after-pulling, whose When
//     requires the operator to confirm every co-resident bd client is upgraded
//     (#5920) — unobservable from this process. This field describes the
//     payload, not its first option, so one option needing a human makes the
//     answer true. AgentDirective agrees ("Do NOT auto-run it"). Stays true.
//
// What is NOT narrowed on: the When string, which is "always, for this stop"
// in both shapes and correctly so — the pull is the only way forward either
// way. It is the Risk, not the applicability, that the diverged shape changes.
func humanDecisionRequired(e *schema.RemoteMigrateGateError) bool {
	return !(e.IsDataBehind() && !e.DataDiverged && !e.Shared)
}

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
		// The #6575 data-behind stop on a SHARED store carries a consent verb in
		// its second option (migrate-shared-after-pulling) too — the forced one,
		// since that stop is always remote-backed — so it needs the same
		// retarget: under --global the project-scoped verb would consent the
		// wrong database and leave the refusal in place. It reaches here through
		// the default arm (empty Decision), so the Decision test alone would
		// miss it.
		retargetShared := globalFlag && (e.Decision == "shared-no-remote" || (e.IsDataBehind() && e.Shared))

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
					switch c {
					case schema.SharedConsentCommand:
						retargeted[i] = schema.SharedConsentCommandGlobal
					case schema.SharedConsentCommandForced:
						// The data-behind arm's consent step is the FORCED verb (that
						// stop is remote-backed by construction, where the bare verb's
						// consent is never read), so it needs its own global form —
						// matching only the bare verb would leave this option pointing
						// at the project database.
						retargeted[i] = schema.SharedConsentCommandForcedGlobal
					default:
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
			"human_decision_required": humanDecisionRequired(e),
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
			// gastownhall/beads#6575: the data-behind stop is a blunt stop with
			// a specific, verified, one-command remedy, and the blunt
			// observed/expected pair above describes a decision that does not
			// apply to it. Leaving them in place pointed an agent at
			// `bd migrate --force` (the bug) followed by a `bd dolt push` that
			// is guaranteed to be rejected non-fast-forward while the clone is
			// still behind. e.Options() already returns the pull-first option
			// for this reason, so options/hint are correct above; these two
			// fields are what was still lying. decision stays absent — the
			// stop has no Decision, and fallback_reason is the key an agent
			// matches on.
			if e.IsDataBehind() {
				observed := "this clone is level with the remote on schema but is BEHIND it in commits it has not pulled, so the pending migration would land on a history missing them (#6575, #6368)"
				if e.DataDiverged {
					observed += "; it also has commits of its own, so pulling merges rather than fast-forwards"
					gate["data_behind_shape"] = "diverged"
				} else {
					observed += "; it has no commits of its own, so pulling is a pure fast-forward"
					gate["data_behind_shape"] = "fast-forward"
				}
				gate["observed"] = observed
				gate["expected"] = "run `" + schema.DataBehindRemedyCommand + "` first; the migration is only allowed once this clone has nothing left to pull"
				// The docs pointer is set once above alongside the blunt
				// observed/expected pair, and has to move with them: the section it
				// names tells the reader `bd dolt pull` is refused on every
				// pending-migration open, which contradicts the single option this
				// payload carries. Same move, for the same reason, as
				// shared-no-remote's retarget above.
				gate["docs"] = dataBehindDocsURL
			}
		}
		m["remote_migrate_gate"] = gate
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(outer)
}
