package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// validateConfiguredBackend fails closed on metadata that selects a removed or
// unrecognized backend. beadsDir is the workspace whose metadata cfg came from;
// the rejection inspects it (read-only) so a workspace that already holds a Dolt
// database is told to fix the stale "backend" value rather than to export and
// reinitialize. Pass "" only where there is genuinely no workspace directory.
func validateConfiguredBackend(cfg *configfile.Config, beadsDir string) error {
	if cfg == nil {
		return nil
	}
	if backends.Registered(cfg.Backend) {
		return nil
	}
	switch cfg.Backend {
	case configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite:
		return configfile.RemovedBackendErrorAt(cfg.Backend, beadsDir, cfg)
	case "", configfile.BackendDolt:
		return nil
	default:
		return configfile.UnknownBackendErrorAt(cfg.Backend, beadsDir, cfg)
	}
}

// registeredBackendWorkspaceIsBeadsDir reports whether metadata selects a
// backend that has no separately discoverable local database.
func registeredBackendWorkspaceIsBeadsDir(cfg *configfile.Config) bool {
	if cfg == nil {
		return false
	}
	return backends.WorkspaceIsBeadsDir(cfg.GetBackend())
}

func requireDoltBackend(cfg *configfile.Config, beadsDir string) error {
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return err
	}
	if cfg != nil && cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("not using Dolt backend (configured backend %q)", cfg.GetBackend())
	}
	return nil
}

// normalizeLoadedConfig substitutes the default config for an absent
// metadata.json (cfg == nil) so mode inference still runs: a remote host
// supplied via BEADS_DOLT_SERVER_HOST or config.yaml dolt.host (GH#3545)
// must select server mode even when no metadata.json exists — otherwise
// the CLI silently opens the embedded store against a remote-host
// configuration.
func normalizeLoadedConfig(cfg *configfile.Config) *configfile.Config {
	if cfg == nil {
		return configfile.DefaultConfig()
	}
	return cfg
}

// openStoreErrorMessage frames a failed store-open for the operator. A
// registered remote backend (backends.IsRemote) is a pure network client of a
// bd serve with no local database: "failed to open database" misnames the
// failure, and the "database"-shaped diagnostics the generic path otherwise
// invites (checking .dolt/ files, a wrong data directory, ...) are the wrong
// advice for what is really a dial, auth, or protocol failure against a
// remote server. Every other backend — Dolt, or a registered backend that
// isn't Remote — keeps the original, unqualified wording so this is additive,
// not a behavior change for any existing workspace.
func openStoreErrorMessage(backendName string, err error) string {
	if backends.IsRemote(backendName) {
		return fmt.Sprintf("failed to reach remote backend %q: %v", backendName, err)
	}
	return fmt.Sprintf("failed to open database: %v", err)
}

// backendNameForErrorFraming best-effort loads metadata.json to learn which
// backend a failed open was for, so a caller that does not already have cfg
// in scope (for example direct mode's single-shot store-open) can still give
// openStoreErrorMessage the name it needs. A failure to even read the config
// degrades to "" — openStoreErrorMessage's default, Dolt-shaped framing —
// rather than compounding one error with another.
func backendNameForErrorFraming(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.GetBackend()
}

func loadDoltBackendConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if err := requireDoltBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	return cfg, nil
}
