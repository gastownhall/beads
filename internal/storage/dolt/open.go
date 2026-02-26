package dolt

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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
	if cfg.Database == "" {
		cfg.Database = fileCfg.GetDoltDatabase()
	}

	// Merge server connection config (config provides defaults, caller can override)
	if fileCfg.IsDoltServerMode() {
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

	// Enable auto-start for standalone users (similar to main.go's auto-start
	// handling), with additional support for BEADS_TEST_MODE and a config.yaml
	// fallback for library consumers that never call config.Initialize().
	// Disabled under Gas Town (which manages its own server), by explicit config,
	// or in test mode (tests manage their own server lifecycle via testdoltserver).
	// Note: cfg.ReadOnly refers to the store's read-only mode, not the server —
	// the server must be running regardless of whether the store is read-only.
	//
	// Prefer the global viper config (populated when config.Initialize() has been
	// called, i.e. all CLI paths). Fall back to a direct read of the project
	// config.yaml for library consumers that never call config.Initialize().
	autoStartCfg := config.GetString("dolt.auto-start")
	if autoStartCfg == "" {
		autoStartCfg = config.GetStringFromDir(beadsDir, "dolt.auto-start")
	}
	cfg.AutoStart = resolveAutoStart(cfg.AutoStart, autoStartCfg)

	return New(ctx, cfg)
}

// resolveAutoStart computes the effective AutoStart value, respecting a
// caller-provided value (current) while applying system-level overrides.
//
// Priority (highest to lowest):
//  1. BEADS_TEST_MODE=1                    → always false (tests own the server lifecycle)
//  2. IsDaemonManaged()                    → always false (Gas Town manages the server)
//  3. BEADS_DOLT_AUTO_START=0              → always false (explicit env opt-out)
//  4. current == true                      → true  (caller option wins over config file,
//     per NewFromConfigWithOptions contract)
//  5. doltAutoStartCfg == "false"/"0"/"off" → false (config.yaml opt-out)
//  6. default                              → true  (standalone user; safe default)
//
// doltAutoStartCfg is the raw value of the "dolt.auto-start" key from config.yaml
// (pass config.GetString("dolt.auto-start") at the call site).
//
// Note: because AutoStart is a plain bool, a zero value (false) cannot be
// distinguished from an explicit "opt-out" by the caller.  Callers that need
// to suppress auto-start should use one of the environment-variable or
// config-file overrides above.
func resolveAutoStart(current bool, doltAutoStartCfg string) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return false
	}
	if doltserver.IsDaemonManaged() {
		return false
	}
	if os.Getenv("BEADS_DOLT_AUTO_START") == "0" {
		return false
	}
	// Caller option wins over config.yaml (NewFromConfigWithOptions contract).
	if current {
		return true
	}
	if doltAutoStartCfg == "false" || doltAutoStartCfg == "0" || doltAutoStartCfg == "off" {
		return false
	}
	// Default: auto-start for standalone users.
	return true
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
