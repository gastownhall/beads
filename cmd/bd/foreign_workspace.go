package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
)

// A foreign workspace is a .beads bd was not launched in: an external:
// dependency's project, a routed or auto-routed target, a create target, a
// migrate-personal planning repo, or a repos.additional repo doctor reads.
// Command admission (guardLegacyUpgradeWorkspace) checks only the workspace bd
// runs in, so every path that opens a foreign workspace runs
// admitForeignWorkspace first. A path that opens it through the metadata
// store factories does so only through openForeignStore. Three opens do not
// use the factories: ensureBeadsDirForPath's and adoptTargetDatabase's
// newDoltStore open a metadata-less create or planning target, guarded by
// refuseUninitializableTarget, and doctor's readTypesFromDB opens a
// repos.additional repo with dolt.NewFromConfigWithCLIOptions, guarded by
// doctor.AdmitAdditionalRepo.
// The invariant both keep: bd writes nothing into a foreign workspace (no
// migrated config.json, no metadata.json, no database, sidecar or server
// files) until it has refused every layout bd refuses when run inside it, and
// it refuses with that text, except where the foreign classification admits a
// legacy Dolt layout bd run inside refuses: the launcher's dolt.shared-server
// fallback at guardForeignLegacyDoltWorkspace, and server intent set in a
// layer the foreign classification does not resolve for the target
// (user-level config, the BD_ environment, either workspace's .beads/.env; see
// targetSelectsServerMode). An open of an existing workspace refuses a .beads
// bd run inside finds no workspace in (openForeignStore); a create or
// planning target is initialized there instead, unless it holds a legacy
// config.json (a fresh clone of a SQLite-era project) or a non-empty
// issues.jsonl (a fresh clone of a project from before bd wrote config.json),
// which refuseUninitializableTarget refuses with the error openForeignStore
// returns for it. foreign_workspace_census_test.go pins that the store
// factories are reached from a foreign path only through openForeignStore.

// admitForeignWorkspace refuses, without writing anything, the layouts bd
// run inside beadsDir refuses from its contents: a workspace format this build
// cannot open, a historical SQLite workspace, a removed or unrecognized
// backend (checkDiscoveredBackend), and a legacy Dolt workspace
// (guardForeignLegacyDoltWorkspace, which admits some legacy Dolt layouts bd
// run inside refuses, and refuses some that bd run inside admits; see there
// and targetSelectsServerMode). It returns beadsDir's metadata as
// discovery reads it: nil when there is none, or when it does not load.
//
// Metadata that does not load is admitted here, not refused: it is safe only
// for a caller whose next step touching beadsDir is a configfile.Load, which
// fails on it without writing (openForeignStore and the factories behind it,
// bootstrap's ancestor walk and bd connect, which report the load error
// themselves). A caller that writes into beadsDir before any Load runs, such
// as a create or planning target being initialized, must refuse it first, as
// admission does (refuseUninitializableTarget runs loadAndCheckWorkspaceFormat).
func admitForeignWorkspace(beadsDir string) (*configfile.Config, error) {
	cfg, err := checkDiscoveredBackend(beadsDir)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		if _, loadErr := configfile.LoadForDiscovery(beadsDir); loadErr != nil {
			return nil, nil
		}
	}
	if err := guardForeignLegacyDoltWorkspace(beadsDir, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// admitForeignWorkspaceErr is admitForeignWorkspace for a caller that needs
// only its refusal: doctor's read of a repos.additional repository
// (doctor.AdmitAdditionalRepo).
func admitForeignWorkspaceErr(beadsDir string) error {
	_, err := admitForeignWorkspace(beadsDir)
	return err
}

func init() {
	doctor.AdmitAdditionalRepo = admitForeignWorkspaceErr
}

// guardForeignLegacyDoltWorkspace is admission's legacy Dolt classification
// (classifyLegacyDoltWorkspace) for a foreign workspace. Admission resolves
// the workspace's server intent from the process's bound configuration, which
// for a foreign workspace is the launcher's; this resolves dolt.mode from the
// workspace's own metadata and config.yaml (targetSelectsServerMode), so the
// refusal is the one bd run inside it prints. dolt.shared-server
// (sharedServerModeForWorkspace) is first enabled by this process's
// BEADS_DOLT_SHARED_SERVER, which can come from the launcher's .beads/.env;
// otherwise it is the workspace's own only when its
// config.yaml sets the key or its metadata names a dolt_mode; otherwise it
// falls back to the launcher's merged dolt.shared-server, the same fallback
// the store factories use to pick the server, so a shared-server launcher
// admits a legacy Dolt layout that bd run inside it refuses. When the metadata
// names a dolt_mode, the user-level and BD_DOLT_SHARED_SERVER
// dolt.shared-server that bd run inside also reads is not consulted, so a
// legacy Dolt layout bd run inside admits through it is refused here; nothing
// is written.
func guardForeignLegacyDoltWorkspace(beadsDir string, cfg *configfile.Config) error {
	return classifyLegacyDoltWorkspace(beadsDir, cfg, legacyDoltModes{
		serverMode:   targetSelectsServerMode(beadsDir, cfg),
		sharedServer: sharedServerModeForWorkspace(beadsDir, cfg),
	})
}

// targetSelectsServerMode reports whether bd run inside beadsDir, whose
// metadata is cfg (nil for none), opens it through a Dolt sql-server. It is
// configfile.Config.IsDoltServerMode's precedence with that resolver's
// config.yaml layer read from beadsDir's own config.yaml and the
// config.local.yaml beside it rather than from the launcher's: the
// environment, then a non-local host (env; then metadata's when config.yaml
// names no host or the same server, otherwise config.yaml's when it names no
// dolt.mode, GH#5961), then metadata's dolt_mode, then config.yaml's
// dolt.mode. A config.yaml that cannot be read selects nothing, leaving the
// stricter embedded classification.
//
// It approximates bd run inside beadsDir: it omits the user-level config.yaml
// layers and viper's BD_-prefixed environment (BD_DOLT_MODE, BD_DOLT_HOST)
// that IsDoltServerMode also merges there, and the BEADS_DOLT_* environment it
// reads is this process's, which can carry the launcher's .beads/.env and
// never carries beadsDir's, which bd run inside beadsDir loads before
// admission (loadBeadsEnvFile, without overriding variables already set).
// Those layers mostly decide which legacy Dolt refusal text is printed, but
// they can also decide whether beadsDir is refused, in both directions. When
// server mode comes only from the user-level or BD_ layers and the metadata
// names no dolt_mode: a .beads/dolt root that is empty
// with no version witness, or whose witness cannot be parsed, is admitted by
// bd run inside it and refused here; nothing is written. Conversely, a target
// bd run inside refuses as a legacy Dolt server workspace is admitted here: a
// .local_version naming bd 0.55-0.62 with no .beads/dolt root or beside an
// embedded repository, or an embedded repository beside a .beads/dolt root bd
// run inside refuses in server mode. The launcher merges the same layers, so
// the open that follows uses a Dolt server (effectiveServerMode), which can auto-start one inside the target or, for a
// write, run bd's schema migrations in its database, as on main. When server
// mode comes only from beadsDir's .beads/.env, the same legacy Dolt server
// layouts are admitted here, and the open that follows takes the launcher's
// mode (effectiveServerMode), so a write from an embedded launcher can create
// an embedded database in beadsDir, as on main. When it comes only from the
// launcher's .beads/.env, beadsDir is classified in server mode although bd
// run inside it is not.
func targetSelectsServerMode(beadsDir string, cfg *configfile.Config) bool {
	cfg = normalizeLoadedConfig(cfg)
	if cfg.GetBackend() != configfile.BackendDolt {
		return false
	}
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
		return true
	}
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	mode, _, modeErr := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.mode")
	if modeErr != nil {
		mode = ""
	}
	if !strings.EqualFold(cfg.DoltMode, configfile.DoltModeProxiedServer) {
		if host := os.Getenv("BEADS_DOLT_SERVER_HOST"); host != "" {
			if !configfile.IsLocalHostString(host) {
				return true
			}
		} else if cfg.DoltMode == "" {
			host, _, hostErr := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.host")
			if hostErr != nil {
				host = ""
			}
			if cfg.DoltServerHost != "" && (host == "" || configfile.SameServerHost(host, cfg.DoltServerHost)) {
				if !configfile.IsLocalHostString(cfg.DoltServerHost) {
					return true
				}
			} else if mode == "" && host != "" && !configfile.IsLocalHostString(host) {
				return true
			}
		}
	}
	if cfg.DoltMode != "" {
		return strings.EqualFold(cfg.DoltMode, configfile.DoltModeServer)
	}
	return strings.EqualFold(mode, configfile.DoltModeServer)
}

// foreignAccess selects how openForeignStore opens a foreign workspace.
type foreignAccess int

const (
	// foreignRead opens it read-only (newReadOnlyStoreFromConfig): an
	// external dependency, a routed or auto-routed read.
	foreignRead foreignAccess = iota
	// foreignPreview opens it for a preview's read (newPreviewStoreFromConfig).
	foreignPreview
	// foreignWrite opens its existing database writable, never creating one
	// (newExistingDoltStoreFromConfig): a routed or auto-routed write, a
	// migrate-personal planning repo.
	foreignWrite
	// foreignCreate opens it writable (newDoltStoreFromConfig): a routed
	// create's target, after ensureBeadsDirForPath.
	foreignCreate
)

// openForeignStore is the one way a command opens a foreign workspace's
// store through the metadata store factories (the three direct opens are
// listed at the top of this file): it refuses what admitForeignWorkspace
// refuses before the factory runs, so the factory writes nothing into a
// workspace admitForeignWorkspace refuses.
func openForeignStore(ctx context.Context, beadsDir string, access foreignAccess) (storage.DoltStorage, error) {
	if _, err := admitForeignWorkspace(beadsDir); err != nil {
		return nil, err
	}
	// An open of an existing workspace refuses a .beads bd run beside it
	// does not treat as a workspace at all, with the error bd prints there.
	// The most common one is a fresh clone of a SQLite-era project, which
	// keeps its tracked config.json and issues.jsonl but not the ignored
	// *.db: bd there finds no workspace and writes nothing, and the
	// historical SQLite check, which needs the database, does not refuse it.
	// A create's target has passed ensureBeadsDirForPath before it is opened,
	// which refuses that clone too (refuseUninitializableTarget) and may
	// leave an empty .beads for this open to initialize, so it is not
	// refused here.
	if access != foreignCreate && !beads.HasProjectFiles(beadsDir) {
		return nil, fmt.Errorf("%w at %s", ErrNoBeadsDatabase, remedy.PrintedValue(beadsDir))
	}
	switch access {
	case foreignRead:
		return newReadOnlyStoreFromConfig(ctx, beadsDir)
	case foreignPreview:
		return newPreviewStoreFromConfig(ctx, beadsDir)
	case foreignWrite:
		return newExistingDoltStoreFromConfig(ctx, beadsDir)
	case foreignCreate:
		return newDoltStoreFromConfig(ctx, beadsDir)
	default:
		return nil, fmt.Errorf("openForeignStore: unknown access %d", access)
	}
}
