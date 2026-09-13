package doltserver

import (
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/config"
)

// ResolveAutoStart computes the effective auto-start decision, respecting a
// caller-provided current value, the config.yaml opt-out, the environment
// overrides, and the resolved server mode:
//
//	BEADS_TEST_MODE=1                       → false
//	BEADS_DOLT_AUTO_START=0                 → false
//	mode == ServerModeExternal              → false  (user manages the server)
//	dolt.auto-start false/0/off             → false  (config beats the caller)
//	current == true                         → true
//	default                                 → true   (standalone user)
//
// doltAutoStartCfg is the raw "dolt.auto-start" value from config.yaml.
//
// This lives here rather than in storage/dolt because it is a statement about
// who owns the server's lifecycle, which is this package's subject — and
// because the ownership-handoff commit gate has to ask the same question
// without importing the storage layer that fences on its journal.
//
// Note: because AutoStart is a plain bool, a zero value cannot be
// distinguished from an explicit opt-out by the caller. Callers needing to
// suppress auto-start should use an environment or config-file override.
func ResolveAutoStart(current bool, doltAutoStartCfg string, mode ServerMode) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return false
	}
	if os.Getenv("BEADS_DOLT_AUTO_START") == "0" {
		return false
	}
	// When the server is externally managed, never auto-start. The user has
	// configured a specific server — if it's down, error out rather than
	// silently starting a different server from .beads/dolt/.
	if mode == ServerModeExternal {
		return false
	}
	// Config.yaml explicit opt-out takes precedence over caller-provided
	// current=true. Without this, ApplyCLIAutoStart (which passes current=true)
	// and bootstrap paths (which hardcode AutoStart=true) would ignore the
	// user's dolt.auto-start: false setting, spawning rogue dolt servers that
	// overwrite port files and cause DB lock conflicts.
	if strings.EqualFold(doltAutoStartCfg, "false") || doltAutoStartCfg == "0" || strings.EqualFold(doltAutoStartCfg, "off") {
		return false
	}
	if current {
		return true
	}
	return true
}

// ResolveAutoStartForDir answers "would bd auto-start a server for this
// workspace?" using the same precedence the CLI path uses: the process-global
// dolt.auto-start value if one is loaded, otherwise the one in beadsDir's own
// config.yaml, resolved against beadsDir's server mode.
//
// It exists for callers that must ask about a specific workspace rather than
// the ambient one — IsAutoStartDisabled reads only process-global config, so it
// cannot answer for a root the current process was not launched in.
func ResolveAutoStartForDir(beadsDir string) bool {
	autoStartCfg := config.GetString("dolt.auto-start")
	if autoStartCfg == "" {
		autoStartCfg = config.GetStringFromDir(beadsDir, "dolt.auto-start")
	}
	return ResolveAutoStart(true, autoStartCfg, ResolveServerMode(beadsDir))
}

// EnsureDoltInit makes doltDir a Dolt database directory, initializing it with
// `dolt init` when it has no .dolt subtree and seeding the compatibility marker
// when it does. It is the exported form of beads' own "is this a Dolt root?"
// predicate-and-repair, for the ownership handoff, which must use bd's
// definition rather than a second opinion about what counts as initialized.
//
// It is not idempotent in the reversible sense: on a fresh directory it creates
// the .dolt subtree. Callers that may have to undo it must reserve the
// intention durably before calling.
func EnsureDoltInit(doltDir string) error { return ensureDoltInit(doltDir) }
