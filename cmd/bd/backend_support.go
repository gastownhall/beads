package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
)

func validateConfiguredBackend(cfg *configfile.Config) error {
	if cfg == nil {
		return nil
	}
	if backends.Registered(cfg.Backend) {
		return nil
	}
	switch cfg.Backend {
	case configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite:
		return configfile.RemovedBackendError(cfg.Backend)
	case "", configfile.BackendDolt:
		return nil
	default:
		return configfile.UnknownBackendError(cfg.Backend)
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

func requireDoltBackend(cfg *configfile.Config) error {
	if err := validateConfiguredBackend(cfg); err != nil {
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

func loadDoltBackendConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if err := requireDoltBackend(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
