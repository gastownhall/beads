package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
)

// NewFromConfig creates a DoltStore based on the metadata.json configuration.
// beadsDir is the path to the .beads directory.
func NewFromConfig(ctx context.Context, beadsDir string) (*DoltStore, error) {
	return NewFromConfigWithOptions(ctx, beadsDir, nil)
}

// NewFromConfigWithOptions creates a DoltStore with options from metadata.json.
// Options in cfg override those from the config file. Pass nil for default options.
func NewFromConfigWithOptions(ctx context.Context, beadsDir string, cfg *Config) (*DoltStore, error) {
	fileCfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if fileCfg == nil {
		fileCfg = configfile.DefaultConfig()
	}

	// Build config from metadata.json, allowing overrides from caller
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.Path = fileCfg.DatabasePath(beadsDir)

	// Always apply database name from metadata.json (prefix-based naming, bd-u8rda).
	// This must happen for both embedded and server modes; previously it was
	// gated on IsDoltServerMode(), causing embedded-mode opens to fall back
	// to the default "beads" database and miss the prefix-specific one.
	if cfg.Database == "" {
		cfg.Database = fileCfg.GetDoltDatabase()
	}

	// Merge Dolt server mode config (config provides defaults, caller can override)
	if fileCfg.IsDoltServerMode() {
		cfg.ServerMode = true
		if cfg.ServerHost == "" {
			cfg.ServerHost = fileCfg.GetDoltServerHost()
		}
		if cfg.ServerPort == 0 {
			cfg.ServerPort = fileCfg.GetDoltServerPort()
		}
		if cfg.ServerUser == "" {
			cfg.ServerUser = fileCfg.GetDoltServerUser()
		}
	}

	return New(ctx, cfg)
}

// GetBackendFromConfig returns the backend type from metadata.json.
// Returns "dolt" if no config exists or backend is not specified.
func GetBackendFromConfig(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return configfile.BackendDolt
	}
	return cfg.GetBackend()
}
