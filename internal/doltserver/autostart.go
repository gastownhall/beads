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

// ResolveAutoStartForDir answers "would bd auto-start a server for THIS
// workspace?", reading beadsDir's own config.yaml first and falling back to the
// process-global value only when that file says nothing.
//
// The precedence is deliberately the opposite of ApplyCLIAutoStart's. That one
// answers for the ambient workspace, where the global value and the file are
// normally the same thing. This one answers for a named directory, and the
// process-global value is a snapshot taken when the process started — so a
// caller that has just WRITTEN the file would otherwise be told what the file
// used to say. A workspace whose owner set `dolt.auto-start: false` before
// handing it over reads as false forever, no matter what is written to it.
//
// Environment overrides still win, inside ResolveAutoStart: they are statements
// about this process, not about a directory.
func ResolveAutoStartForDir(beadsDir string) bool {
	autoStartCfg := config.GetStringFromDir(beadsDir, "dolt.auto-start")
	if autoStartCfg == "" {
		autoStartCfg = config.GetString("dolt.auto-start")
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
