package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
)

func validateConfiguredBackend(cfg *configfile.Config) error {
	if cfg == nil {
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

func requireDoltBackend(cfg *configfile.Config) error {
	if err := validateConfiguredBackend(cfg); err != nil {
		return err
	}
	if cfg != nil && cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("not using Dolt backend (configured backend %q)", cfg.GetBackend())
	}
	return nil
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
