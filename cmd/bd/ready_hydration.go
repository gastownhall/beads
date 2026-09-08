package main

import (
	"fmt"
	"os"
	"strings"
)

// EnvReadyHydration names the projection `bd ready` uses when the command line
// asks for neither --brief nor --full. It is THE ROLLBACK for the lite default:
// an operator who needs the old fully-hydrated ready listing back sets
//
//	BEADS_READY_HYDRATION=full
//
// in the environment the bd/gc processes are started with, and gets it at the
// next restart with no rebuild and no flag change anywhere. "lite" (or unset)
// is the default.
//
// It governs the DEFAULT only. An explicit --brief or --full on the command
// line always wins, in both directions, so a caller that states its projection
// is never surprised by an environment it did not set.
//
// It deliberately does NOT govern the claim scan (issueops.ClaimReadyIssueInTx).
// That projection has no observable effect to roll back: the scan's rows are
// read for their ID alone and the row a claim wins is refetched whole before it
// is returned, so there is no output that can differ. A knob offered there
// would be a knob that does nothing.
const EnvReadyHydration = "BEADS_READY_HYDRATION"

// readyHydrationDefaultLite reports the projection default for `bd ready`,
// and a warning line when the environment named something unrecognized.
//
// AN UNREADABLE VALUE FALLS BACK TO FULL HYDRATION AND SAYS SO. Every other
// direction fails toward the reassuring answer: a typo'd BEADS_READY_HYDRATION
// that silently kept the lite default would leave an operator who believed they
// had rolled back reading projected rows, which is the one state this knob
// exists to let them leave.
func readyHydrationDefaultLite(env func(string) string) (lite bool, warning string) {
	raw := strings.TrimSpace(env(EnvReadyHydration))
	switch strings.ToLower(raw) {
	case "":
		return true, ""
	case "lite":
		return true, ""
	case "full":
		return false, ""
	default:
		return false, fmt.Sprintf(
			"warning: %s=%q is not a recognized value (want \"lite\" or \"full\"); using full hydration",
			EnvReadyHydration, raw)
	}
}

// resolveReadyProjection decides whether this `bd ready` invocation runs the
// lite projection, given the two flags and the four modes that reach a
// different query.
//
// THE MODES ARE NOT REFUSED HERE, THEY FALL BACK. briefModeConflict refuses
// --brief against --claim, --gated, --mol, --explain and the text renderings,
// because a flag the user TYPED that no route can honor is a silent no-op and
// must be reported. A DEFAULT is a different thing: the same combination just
// means this invocation is not on the projected route, and refusing `bd ready
// --claim` because of an environment default nobody typed would break every
// claiming caller in the town on the day the default flipped.
func resolveReadyProjection(brief, full, claim, gated, explain bool, molID string, jsonOut bool, env func(string) string) (lite bool, warning string) {
	if full {
		return false, ""
	}
	if brief {
		// Already validated by briefModeConflict, which refuses every
		// combination the projection cannot reach.
		return true, ""
	}
	// The counts mega-query is the only ready query that carries a projection,
	// and `bd ready` runs it for --json alone; --claim, --gated, --mol and
	// --explain each answer with a shape of their own. Defaulting lite on those
	// routes would set a field nothing reads.
	if !jsonOut || claim || gated || explain || molID != "" {
		return false, ""
	}
	return readyHydrationDefaultLite(env)
}

// osEnv is resolveReadyProjection's production environment reader.
func osEnv(key string) string { return os.Getenv(key) }
