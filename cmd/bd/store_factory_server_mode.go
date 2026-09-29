package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
)

// This file carries NO build tag on purpose.
//
// Every factory call site with the GH#6551 gap lives in a build-tagged file:
// newDoltStoreFromConfig and its read-only sibling in store_factory.go
// (//go:build cgo), and their twins newDoltStoreFromConfig /
// newReadOnlyStoreFromConfig in store_factory_nocgo.go (//go:build !cgo). A
// helper placed behind either tag could only ever reach half of them, so
// "centralizing the check" would have been a claim the build tags falsify --
// and .goreleaser.yml ships three CGO_ENABLED=0 targets, the build where a
// shared server is the only usable backend. Keeping the helper untagged is
// what actually stops the four call sites from drifting apart again. Nothing
// here needs cgo: configfile, config and doltserver all build under
// CGO_ENABLED=0.

// effectiveServerMode reports whether the workspace at beadsDir selects Dolt
// server-mode storage, compensating for the same gap main.go's own resolution
// already compensates for (see the comment on doltCfg.ServerMode in main.go):
// configfile.IsDoltServerMode() deliberately does not read dolt.shared-server
// from config.yaml, to avoid a circular import with the doltserver package. A
// workspace (typically a linked git worktree) that has config.yaml but no
// metadata.json otherwise falls through to the embedded backend and silently
// creates a phantom database -- GH#6551, the same shape
// newDoltStoreFromConfig's caller in main.go was already fixed for under
// GH#3817.
func effectiveServerMode(beadsDir string, cfg *configfile.Config) bool {
	if cfg != nil && cfg.IsDoltServerMode() {
		return true
	}
	return sharedServerModeForWorkspace(beadsDir, cfg)
}

// sharedServerModeForWorkspace resolves dolt.shared-server for the workspace at
// beadsDir rather than for the directory bd happened to be launched from. The
// cross-workspace factories -- routed creates, remote-cache hydration,
// contributor init -- pass a beadsDir that is by construction NOT the bound
// target, and doltserver.IsSharedServerMode() reads process-global state, so
// consulting it directly would resolve activation from the launching workspace
// and contradict the contract documented on newDoltStoreFromConfig.
// legacy_upgrade_guard.go records the same hazard for the same key: "Do not
// rely on the binding from a caller that walks ancestors."
//
// Layering, highest priority first:
//
//  1. BEADS_DOLT_SHARED_SERVER. Machine-global by design, and already what
//     configfile.IsDoltServerMode honors ABOVE an explicit dolt_mode (step 2 of
//     its documented precedence chain), so honoring it for every workspace here
//     is base-equivalent.
//  2. beadsDir's own config.local.yaml, then its config.yaml -- the two
//     project-level layers config.Initialize merges, in Initialize's own order
//     (local last there, so local wins here) and under Initialize's own
//     condition (the local file counts only when a config.yaml sits beside it,
//     which is what Initialize hangs its local merge off; see
//     config.WorkspaceYamlValueStrictWithLocal). For the bound workspace these
//     are the same two files the process-wide merge already read, and config.yaml
//     is where the key is actually persisted: `bd init --shared-server`
//     (init.go) and `bd dolt shared-server on` (dolt.go) both route through
//     config.SetYamlConfig, which resolves to a .beads/config.yaml and never to
//     the user-global file, and the mode migrations write it with
//     SetYamlConfigInDir(beadsDir, ...). config.local.yaml has no bd writer; it
//     is the documented hand-authored escape hatch for holding machine-specific
//     state out of the tracked file, and dolt.shared-server is exactly that kind
//     of state, so a workspace can track the team's setting and opt this machine
//     out. Reading only config.yaml here would make these four factories answer
//     that shape differently from main.go, bootstrap, doctor and
//     migrate-dolt-mode -- the same resolver divergence GH#6551 is an instance
//     of. For a cross-workspace open this pair is the only config that answers
//     for the target. This also keeps shared-server mode winning over a stale
//     metadata.json that still pins dolt_mode="embedded", which main.go:1710 and
//     shouldUseExternalDoltStatus both rely on (GH#2946).
//  3. The process-wide merge, but only when the target declares no dolt_mode of
//     its own. That keeps machine-wide enablement working for a workspace that
//     has not spoken -- including the GH#6551 shape, where metadata.json is
//     absent entirely -- while preserving base behavior for one that has: at
//     base the config.yaml layer could not override an explicit dolt_mode
//     either, because IsDoltServerMode falls through to config.yaml only when
//     c.DoltMode == "". Without this gate a launcher whose config.yaml enables
//     shared-server would silently retarget a foreign workspace that explicitly
//     asked for embedded onto the shared server -- a connect failure or, worse,
//     a same-named database holding someone else's rows.
func sharedServerModeForWorkspace(beadsDir string, cfg *configfile.Config) bool {
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	raw, present, err := config.WorkspaceYamlValueStrictWithLocal(beadsDir, "dolt.shared-server")
	switch {
	case err != nil:
		// The target has a config.yaml (or config.local.yaml) we cannot read or
		// parse. Returning false here is a deliberate degrade to the embedded
		// default -- the same thing main.go's resolution does on the same
		// corrupt input, so it is parity rather than a new refusal. What it must
		// NOT be is silent: this is the one arm where the answer is "we do not
		// know", and an operator whose workspace does enable shared-server gets
		// the embedded store (or, under !cgo, the misleading "reinstall with
		// CGO" refusal) with the parse error otherwise discarded. Declining
		// rather than consulting layer 3 is still the right policy: a target
		// whose own configuration is unreadable must not be retargeted onto the
		// shared server by the LAUNCHER's configuration.
		debug.Logf("Debug: dolt.shared-server unresolved for %s, assuming embedded: %v\n", beadsDir, err)
		return false
	case present:
		// strconv.ParseBool is what the merged reader ends at for this key:
		// config.GetBool -> cast.ToBool, which for a string value is
		// strconv.ParseBool, so "1"/"t"/"T"/"TRUE" resolve here exactly as they
		// do everywhere else. An unparseable value is viper's false too, but
		// unlike viper we can say so. Same parse as migrationSharedTopology
		// applies to this key from this reader.
		parsed, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			debug.Logf("Debug: dolt.shared-server in %s is not a boolean (%q), assuming embedded\n", beadsDir, raw)
			return false
		}
		return parsed
	}
	// cfg.DoltMode, not cfg.GetDoltMode(): the getter substitutes the embedded
	// default for an absent mode, and this gate must fire only on an explicit
	// statement, exactly as IsDoltServerMode's own `c.DoltMode != ""` check does.
	if cfg != nil && cfg.DoltMode != "" {
		return false
	}
	return doltserver.IsSharedServerMode()
}
