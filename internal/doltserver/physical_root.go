package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
)

// PhysicalRoots describes the local physical database root(s) a store open
// for the given workspace will actually touch, so the workspace-gate wiring
// (cmd/bd chokepoint, bd init/migrate/restore, beads.OpenGated) can gate the
// directory the store will USE — not the directory any single existing
// resolver claims. That distinction is the whole reason this function exists:
// ResolveDoltDir reports .beads/dolt for embedded workspaces whose engine
// actually opens .beads/embeddeddolt, and central-config mode inheritance
// (~/.config/beads/server.json) is applied only deep inside the storage open
// path — a naive resolver that misses either would gate the wrong directory
// and let a migration replace a root out from under a live store.
type PhysicalRoots struct {
	// BeadsDir is the absolute path of the workspace .beads directory
	// the resolution was computed for.
	BeadsDir string
	// Roots are the absolute local physical DB roots to gate. Empty when
	// the backend is remote (RemoteBackend true): bd opens no local files
	// for a remote server, so there is nothing physical to gate — only
	// the workspace gate applies.
	Roots []string
	// Mode is the connection mode the open path will actually use:
	// "embedded" | "server" | "shared-server" | "proxied-server".
	Mode string
	// Provenance is a one-line human explanation of how Mode and Roots
	// were decided, for busy/diagnostic messages ("why is bd gating that
	// directory?").
	Provenance string
	// RemoteBackend is true when the backend is a server on a non-local
	// host: workspace gate only, no physical gate.
	RemoteBackend bool
}

// SharedDoltPath returns the dolt data directory path for the shared server
// (~/.beads/shared-server/dolt, or under BEADS_SHARED_SERVER_DIR) WITHOUT
// creating it. SharedDoltDir is the mkdir-on-first-use variant; resolution
// paths that must be side-effect free (gate planning, dry runs) use this one.
func SharedDoltPath() (string, error) {
	dir, err := SharedServerPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dolt"), nil
}

// projectDoltDirPath resolves the per-project dolt data directory without
// consulting shared-server mode and without side effects:
// BEADS_DOLT_DATA_DIR env (absolute as-is, relative joined to beadsDir) →
// metadata.json dolt_data_dir via Config.DatabasePath (stat-guarded so the
// legacy config.json→metadata.json migration write in configfile.Load can
// never fire from a pure path resolution) → default beadsDir/dolt.
func projectDoltDirPath(beadsDir string) string {
	if d := os.Getenv("BEADS_DOLT_DATA_DIR"); d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(beadsDir, d)
	}
	// Only load config if metadata.json exists (avoids legacy migration side effect)
	if _, err := os.Stat(configfile.ConfigPath(beadsDir)); err == nil {
		if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
			return cfg.DatabasePath(beadsDir)
		}
	}
	return filepath.Join(beadsDir, "dolt")
}

// DoltDirPath is the side-effect-free twin of ResolveDoltDir: identical path
// resolution (shared-server dir wins, then env, then metadata, then
// beadsDir/dolt) but it never creates the shared-server directories.
// ResolveDoltDir keeps its mkdir-on-first-use behavior for callers that are
// about to start a server there; pure resolution (gate planning, proxied
// root fallback) must use this one so that merely ASKING where the data
// lives cannot create ~/.beads trees.
func DoltDirPath(beadsDir string) string {
	if IsSharedServerMode() {
		if dir, err := SharedDoltPath(); err == nil {
			return dir
		}
		// Mirror ResolveDoltDir: an unresolvable shared dir (no home
		// directory) falls through to per-project resolution.
	}
	return projectDoltDirPath(beadsDir)
}

// ResolveProxiedServerRootPath resolves the physical root directory of a
// proxied-server workspace. This logic used to live unexported in
// cmd/bd/proxied_server.go; it is exported here so the workspace-gate
// resolver and the CLI proxy plumbing share ONE implementation and cannot
// drift (a divergent copy would gate a different directory than the proxy
// actually serves). Precedence:
//
//  1. BEADS_PROXIED_SERVER_ROOT_PATH env (absolute as-is, relative joined
//     to beadsDir);
//  2. the .beads/proxied_server_client_info.json sidecar's root_path
//     (which migrate_dolt_mode writes — it can legitimately be the shared
//     dir ~/.beads/shared-server/dolt or an arbitrary absolute path);
//  3. the default dolt data dir for the workspace (DoltDirPath).
//
// The fallback deliberately uses the side-effect-free DoltDirPath rather
// than ResolveDoltDir: the resolved path is identical, but resolution must
// not create shared-server directories as a side effect. Callers that start
// the proxy create the directory themselves when they actually need it.
func ResolveProxiedServerRootPath(beadsDir string) (string, error) {
	if p := os.Getenv("BEADS_PROXIED_SERVER_ROOT_PATH"); p != "" {
		if !filepath.IsAbs(p) {
			p = filepath.Join(beadsDir, p)
		}
		return p, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", err
	}
	if p := info.ResolvedRootPath(beadsDir); p != "" {
		return p, nil
	}
	return DoltDirPath(beadsDir), nil
}

// applyCentralModeDefaults mirrors the central-config merge the LIBRARY open
// path performs (internal/storage/dolt/open.go applyCentralConfigDefaults):
// load ~/.config/beads/server.json (or BEADS_CENTRAL_CONFIG) and apply its
// server fields — DoltMode, host, port, user, TLS, never data dirs — as
// defaults for fields the project config leaves empty.
//
// This is deliberately NOT part of ResolvePhysicalRoots: the CLI's
// hand-built store selection in cmd/bd/main.go never consults the central
// config, so CLI gate planning must not either (it would gate a server root
// the CLI would not open). It exists for LibraryOpenRootPath, which mirrors
// the library path where the merge DOES apply.
//
// A missing central config is a silent no-op, and a BROKEN central config is
// also skipped (the open path warns and proceeds without it; this pure
// resolver stays quiet and mirrors the "proceed without" behavior so both
// paths land on the same mode).
func applyCentralModeDefaults(cfg *configfile.Config) {
	centralPath := os.Getenv("BEADS_CENTRAL_CONFIG")
	if centralPath == "" {
		centralPath = configfile.DefaultCentralConfigPath()
	}
	if centralPath == "" {
		return
	}
	centralCfg, err := configfile.LoadCentralConfig(centralPath)
	if err != nil {
		return
	}
	configfile.ApplyCentralDefaults(cfg, centralCfg)
}

// isRemoteServerHost reports whether a dolt server host points off-machine.
// Local spellings (empty, localhost, loopback — including the bracketed
// IPv6 form a host:port split leaves behind) mean bd may still manage local
// files for the server; anything else means the data lives elsewhere and
// there is no local physical root to gate. The local set mirrors the
// storage layer's isLocalHost (internal/storage/dolt/store.go) so gate
// planning and connection handling agree on what "local" means.
func isRemoteServerHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return false
	}
	return true
}

// ResolvePhysicalRoots computes, WITHOUT side effects, the physical database
// root(s) that opening the given workspace would actually use, along with
// the effective connection mode. It exists for gate planning: workspace-gate
// acquisition must fence the same directory the store will open, and must be
// safe to call from paths (doctor, library consumers, dry runs) that have
// not consented to any writes.
//
// Side-effect freedom is load-bearing and non-obvious, because the obvious
// building blocks all write:
//
//   - configfile.Load performs a legacy config.json→metadata.json MIGRATION
//     WRITE when metadata.json is absent — so every Load here is guarded by
//     an os.Stat of metadata.json first, exactly as ResolveServerMode does;
//   - doltserver.SharedServerDir/SharedDoltDir MKDIR on first use — so the
//     shared root comes from SharedServerPath/SharedDoltPath instead.
//
// Parity target — this function mirrors the CLI's hand-built store
// selection in cmd/bd/main.go, EXACTLY and only that. The LIBRARY open path
// (internal/storage/dolt/open.go via beads.OpenFromConfig) is broader: it
// substitutes DefaultConfig when metadata.json is absent and applies
// central-config defaults (~/.config/beads/server.json) either way, so it
// can land on server mode where the CLI lands on embedded. Consumers gating
// the library path (beads.OpenGated) must union these roots with
// LibraryOpenRootPath. Three copies of mode selection now exist (main.go,
// open.go, here); collapsing them into one exported ResolveEffectiveMode is
// deliberate follow-up scope, not this function's job — until then, CLI
// parity here is the conservative choice because the CLI is what acquires
// gates through the chokepoint, and gating a root the CLI will not open
// would exclude maintenance from the wrong directory.
//
// Mode decision (ordering is significant and each step cites the CLI
// behavior it mirrors):
//
//  1. proxied-server: metadata dolt_mode=proxied-server wins outright —
//     main.go routes proxied workspaces to the proxy provider before the
//     shared-server rescue, which explicitly excludes ProxiedServer. Root
//     via ResolveProxiedServerRootPath.
//  2. shared-server: IsSharedServerMode (env BEADS_DOLT_SHARED_SERVER or
//     config.yaml dolt.shared-server) wins over remaining metadata — a
//     stale dolt_mode=embedded must not override active shared intent
//     (mirrors ResolveDoltDir and the main.go no-config rescue, GH#3817).
//     Root = SharedServerPath()/dolt, no mkdir. This is also the ONLY
//     rescue the CLI applies when metadata.json is absent.
//  3. server: configfile.IsDoltServerMode only (env BEADS_DOLT_SERVER_MODE,
//     metadata dolt_mode, config.yaml dolt.mode fallback). Deliberately NO
//     dolt_server_port>0 heuristic: `bd init --server-port N` without
//     --server writes dolt_mode=embedded AND a port, the CLI opens
//     embeddeddolt, and ResolveServerMode itself gives an explicit
//     dolt_mode=embedded precedence over the port. And deliberately NO
//     central-config promotion — the CLI never applies central config. A
//     remote host (dolt_server_host beyond the loopback/localhost set)
//     means RemoteBackend=true and NO local roots; a local host roots at
//     Config.DatabasePath semantics (BEADS_DOLT_DATA_DIR / dolt_data_dir /
//     beadsDir/dolt).
//  4. embedded (metadata present, GetDoltMode default): root is
//     beadsDir/embeddeddolt — hardcoded in the embedded engine
//     (internal/storage/embeddeddolt), which is precisely where
//     ResolveDoltDir is wrong (it reports beadsDir/dolt) and why this
//     function does not reuse it wholesale.
//  5. no metadata.json: after the shared-server rescue, mirror the open
//     path's discovery fallback (internal/beads findDatabaseInBeadsDir):
//     an embeddeddolt/ dir means embedded, else a dolt/ dir means a
//     server-layout workspace, else default to the embedded root (which
//     may not exist yet — gating a not-yet-existing root is fine, the gate
//     file lives beside it and workspacegate only requires the PARENT to
//     exist). The CLI's nil-config branch honors no other env rescue.
//
// Roots are returned absolute. Symlink canonicalization is deliberately NOT
// performed here: workspacegate.ForPhysicalRoot canonicalizes the gate
// file's parent itself (and refuses symlinked roots), and resolving here too
// would double-handle and could disagree with the gate's own rules.
func ResolvePhysicalRoots(beadsDir string) (PhysicalRoots, error) {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		return PhysicalRoots{}, fmt.Errorf("resolving beads dir %s: %w", beadsDir, err)
	}
	abs = filepath.Clean(abs)
	pr := PhysicalRoots{BeadsDir: abs}

	// Side-effect-free config load: never trigger the legacy config.json
	// migration. Absent metadata.json is treated as cfg == nil.
	var cfg *configfile.Config
	if _, statErr := os.Stat(configfile.ConfigPath(abs)); statErr == nil {
		loaded, loadErr := configfile.Load(abs)
		if loadErr != nil {
			// A present-but-broken metadata.json is authoritative: the open
			// path refuses to fall back, so gate planning refuses to guess.
			return PhysicalRoots{}, fmt.Errorf("loading config for gate resolution: %w", loadErr)
		}
		cfg = loaded
	}

	addRoot := func(root string) error {
		rootAbs, aerr := filepath.Abs(root)
		if aerr != nil {
			return fmt.Errorf("resolving physical root %s: %w", root, aerr)
		}
		pr.Roots = append(pr.Roots, filepath.Clean(rootAbs))
		return nil
	}

	switch {
	case cfg != nil && cfg.IsDoltProxiedServerMode():
		pr.Mode = "proxied-server"
		root, perr := ResolveProxiedServerRootPath(abs)
		if perr != nil {
			return PhysicalRoots{}, fmt.Errorf("resolving proxied-server root: %w", perr)
		}
		pr.Provenance = fmt.Sprintf("metadata.json dolt_mode=proxied-server; root %s via env/client-info/default", root)
		if err := addRoot(root); err != nil {
			return PhysicalRoots{}, err
		}

	case IsSharedServerMode():
		pr.Mode = "shared-server"
		root, serr := SharedDoltPath()
		if serr != nil {
			return PhysicalRoots{}, fmt.Errorf("resolving shared-server root: %w", serr)
		}
		pr.Provenance = fmt.Sprintf("shared-server mode active (BEADS_DOLT_SHARED_SERVER or config.yaml dolt.shared-server), overriding any metadata dolt_mode; root %s", root)
		if err := addRoot(root); err != nil {
			return PhysicalRoots{}, err
		}

	case cfg != nil && cfg.IsDoltServerMode():
		pr.Mode = "server"
		host := cfg.GetDoltServerHost()
		if isRemoteServerHost(host) {
			pr.RemoteBackend = true
			pr.Provenance = fmt.Sprintf("server mode with remote host %s; no local physical root", host)
			break
		}
		root := cfg.DatabasePath(abs)
		pr.Provenance = fmt.Sprintf("server mode (env/metadata/config.yaml, local host); root %s via DatabasePath semantics", root)
		if err := addRoot(root); err != nil {
			return PhysicalRoots{}, err
		}

	case cfg != nil:
		// GetDoltMode defaults empty to embedded; anything that is not one
		// of the server flavors above opens the embedded engine, whose data
		// dir is hardcoded to .beads/embeddeddolt.
		pr.Mode = "embedded"
		root := filepath.Join(abs, "embeddeddolt")
		pr.Provenance = fmt.Sprintf("metadata.json dolt_mode=%q -> embedded; root %s (embedded engine hardcodes embeddeddolt)", cfg.DoltMode, root)
		if err := addRoot(root); err != nil {
			return PhysicalRoots{}, err
		}

	default:
		// No metadata.json at all: mirror the open path's on-disk discovery.
		embedded := filepath.Join(abs, "embeddeddolt")
		doltDir := filepath.Join(abs, "dolt")
		if fi, serr := os.Stat(embedded); serr == nil && fi.IsDir() {
			pr.Mode = "embedded"
			pr.Provenance = fmt.Sprintf("no metadata.json; embeddeddolt/ directory present; root %s", embedded)
			if err := addRoot(embedded); err != nil {
				return PhysicalRoots{}, err
			}
		} else if fi, serr := os.Stat(doltDir); serr == nil && fi.IsDir() {
			pr.Mode = "server"
			pr.Provenance = fmt.Sprintf("no metadata.json; dolt/ server-layout directory present; root %s", doltDir)
			if err := addRoot(doltDir); err != nil {
				return PhysicalRoots{}, err
			}
		} else {
			// Nothing on disk yet: default to the embedded root the open
			// path would create. Gating a not-yet-existing root is fine —
			// the gate file lives beside it in .beads, and workspacegate
			// only requires the gate file's parent directory to exist.
			pr.Mode = "embedded"
			pr.Provenance = fmt.Sprintf("no metadata.json and no data directories; defaulting to embedded root %s", embedded)
			if err := addRoot(embedded); err != nil {
				return PhysicalRoots{}, err
			}
		}
	}

	return pr, nil
}

// LibraryOpenRootPath returns the physical root the LIBRARY open path
// (internal/storage/dolt/open.go, reached via beads.OpenFromConfig and
// beads.OpenGated) would use for this workspace. It exists because that path
// is broader than the CLI selection ResolvePhysicalRoots mirrors: open.go
// substitutes DefaultConfig when metadata.json is absent and applies
// central-config defaults (which can inherit dolt_mode=server from
// ~/.config/beads/server.json) either way, then derives the root from
// Config.DatabasePath — it never opens the embedded engine at all, so even
// an embedded-metadata workspace is opened server-style at DatabasePath
// (.beads/dolt or dolt_data_dir). beads.OpenGated gates the UNION of this
// root and the resolver's roots: over-gating an extra root under a SHARED
// hold is benign, under-gating is the bug.
//
// Side-effect free, unlike open.go itself: the config load is stat-guarded,
// so the legacy config.json→metadata.json migration write open.go would
// trigger cannot fire from gate planning; absent metadata uses DefaultConfig
// in memory only.
func LibraryOpenRootPath(beadsDir string) string {
	var cfg *configfile.Config
	if _, err := os.Stat(configfile.ConfigPath(beadsDir)); err == nil {
		if loaded, lerr := configfile.Load(beadsDir); lerr == nil && loaded != nil {
			// Copy before the central merge: callers may hold their own
			// loaded Config that must not observe inherited fields.
			cfgCopy := *loaded
			cfg = &cfgCopy
		}
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	applyCentralModeDefaults(cfg)
	return cfg.DatabasePath(beadsDir)
}
