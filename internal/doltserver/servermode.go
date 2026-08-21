package doltserver

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// ServerMode describes who owns and manages the dolt sql-server lifecycle.
type ServerMode int

const (
	// ServerModeOwned means beads auto-starts and manages the server.
	// This is the default for standalone users with no explicit port config.
	ServerModeOwned ServerMode = iota

	// ServerModeExternal means the user manages the server lifecycle
	// (e.g., systemd, Docker, Hosted Dolt, VPS). Beads never starts or
	// stops the server. Determined when metadata.json has an explicit
	// dolt_server_port or BEADS_DOLT_SHARED_SERVER is set.
	ServerModeExternal

	// ServerModeEmbedded is the legacy in-process embedded dolt path.
	// Determined when metadata.json dolt_mode is "embedded".
	ServerModeEmbedded
)

// String returns a human-readable label for the server mode.
func (m ServerMode) String() string {
	switch m {
	case ServerModeOwned:
		return "owned"
	case ServerModeExternal:
		return "external"
	case ServerModeEmbedded:
		return "embedded"
	default:
		return fmt.Sprintf("ServerMode(%d)", int(m))
	}
}

// ResolveServerMode determines the server mode from the given beadsDir.
// This is the single source of truth for how the server lifecycle is managed.
//
// Decision logic (checked in order):
//  1. BEADS_DOLT_SERVER_MODE=1 env var             -> ServerModeExternal
//  2. BEADS_DOLT_SHARED_SERVER env var is set       -> ServerModeExternal
//     2b. host-based inference (HostImpliesServerMode) -> ServerModeExternal
//     2c. BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT env var -> ServerModeExternal
//  3. metadata.json dolt_mode == "embedded"         -> ServerModeEmbedded
//  4. metadata.json has explicit dolt_server_port   -> ServerModeExternal
//  5. default                                       -> ServerModeOwned
//
// Runtime env vars (1, 2, 2c) take precedence over persisted metadata.json
// to prevent stale dolt_mode=embedded from silently overriding an active
// shared-server, server-mode, or env-supplied-port configuration (GH#2949).
//
// The function loads metadata.json only if the file exists, to avoid
// triggering the legacy config.json -> metadata.json migration side effect.
func ResolveServerMode(beadsDir string) ServerMode {
	return resolveServerMode(beadsDir, true)
}

// resolveServerModeIgnoringPortEnv is ResolveServerMode with check 2c
// (BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT) skipped.
//
// Those env vars are also set ambiently on multi-agent rigs purely to route
// bd's own client connections to a shared coordination server; setting them
// does not disclaim beads' ownership of beadsDir's own local dolt server
// process the way checks 1/2/2b (and metadata.json check 4) genuinely do.
// Callers asking "should I auto-start a server" or "which server should I
// talk to" want the full ResolveServerMode — an ambient port var correctly
// means "route there instead of spawning a redundant one". Callers asking
// "am I allowed to manage/kill OS processes for this directory's server
// lifecycle" — currently only killStaleServersForDir's orphan-cleanup guard
// (GH#2430) — must use this instead, or the ambient routing var falsely
// disables cleanup for a directory beads still owns.
func resolveServerModeIgnoringPortEnv(beadsDir string) ServerMode {
	return resolveServerMode(beadsDir, false)
}

// resolveServerMode is the shared implementation behind ResolveServerMode
// and resolveServerModeIgnoringPortEnv. honorPortEnv controls whether check
// 2c (BEADS_DOLT_SERVER_PORT / BEADS_DOLT_PORT) participates; see
// resolveServerModeIgnoringPortEnv for why a caller would want it excluded.
func resolveServerMode(beadsDir string, honorPortEnv bool) ServerMode {
	// 1. BEADS_DOLT_SERVER_MODE=1 env var -> external (explicit server mode)
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
		return ServerModeExternal
	}

	// 2. Shared server mode (env var or config.yaml) -> external.
	// Must be checked before metadata.json so that a stale
	// dolt_mode=embedded cannot override active shared-server intent.
	if IsSharedServerMode() {
		return ServerModeExternal
	}

	var fileCfg *configfile.Config

	// Only load config if metadata.json exists (avoids legacy migration side effect)
	metadataPath := configfile.ConfigPath(beadsDir)
	if _, err := os.Stat(metadataPath); err == nil {
		if cfg, loadErr := configfile.Load(beadsDir); loadErr == nil && cfg != nil {
			fileCfg = cfg
		}
	}

	// 2b. Host-based inference (GH#3545) -> external. Uses the same
	// centralized rule as IsDoltServerMode (explicit-mode gates, env
	// suppression, effective-host precedence, proxied exemption) so the
	// storage-mode and lifecycle resolvers cannot disagree: a server
	// that lives on another machine cannot have a bd-owned lifecycle.
	// Runs before the embedded-metadata check because a runtime env
	// host beats stale metadata (GH#2949).
	hostCfg := fileCfg
	if hostCfg == nil {
		hostCfg = &configfile.Config{}
	}
	if hostCfg.HostImpliesServerMode() {
		return ServerModeExternal
	}

	// 2c. Explicit server port via env var (BEADS_DOLT_SERVER_PORT, then
	// the legacy BEADS_DOLT_PORT) -> external. Mirrors the deprecated
	// GetDoltServerPort()'s own env precedence: the orchestrator sets
	// these to signal a server address it manages, port included, so
	// ResolveServerMode must agree instead of defaulting a
	// port-only deployment to owned (ensureDoltInit would never run for
	// it). Runs before the metadata.json checks for the same GH#2949
	// reason as 1/2/2b: a runtime env var beats stale persisted metadata.
	//
	// Skipped when honorPortEnv is false — see resolveServerModeIgnoringPortEnv.
	if honorPortEnv && doltServerPortFromEnv() > 0 {
		return ServerModeExternal
	}

	// 3. Explicit embedded mode in metadata.json
	if fileCfg != nil && strings.ToLower(fileCfg.DoltMode) == configfile.DoltModeEmbedded &&
		fileCfg.DoltMode != "" { // empty defaults to embedded in GetDoltMode, but we treat empty as "unset"
		return ServerModeEmbedded
	}

	// 4. Explicit server port in metadata.json -> external
	if fileCfg != nil && fileCfg.DoltServerPort > 0 {
		return ServerModeExternal
	}

	// 5. Default: beads owns the server
	return ServerModeOwned
}

// doltServerPortFromEnv returns the server port from BEADS_DOLT_SERVER_PORT
// or BEADS_DOLT_PORT (in that order), or 0 if neither is set to a valid
// positive integer. Mirrors configfile.Config.GetDoltServerPort()'s env-var
// precedence without requiring a loaded Config.
func doltServerPortFromEnv() int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	return 0
}
