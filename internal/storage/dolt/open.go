package dolt

import (
	"context"
	"fmt"
	"os"

	"github.com/gastownhall/beads/internal/config"
	"github.com/gastownhall/beads/internal/configfile"
	"github.com/gastownhall/beads/internal/doltserver"
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
	applyResolvedConfig(beadsDir, fileCfg, cfg)

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
	// When metadata.json specifies an explicit server port (raw field, not the
	// getter which falls back to DefaultDoltServerPort), suppress auto-start.
	// This prevents bd from launching a different server when the user's configured
	// server is temporarily unreachable — the root cause of the shadow database bug.
	explicitPort := fileCfg.DoltServerPort > 0
	cfg.AutoStart = resolveAutoStart(cfg.AutoStart, autoStartCfg, explicitPort)

	return New(ctx, cfg)
}

// resolveAutoStart computes the effective AutoStart value, respecting a
// caller-provided value (current) while applying system-level overrides.
//
// Priority (highest to lowest):
//  1. BEADS_TEST_MODE=1                    → always false (tests own the server lifecycle)
//  2. BEADS_DOLT_AUTO_START=0              → always false (explicit env opt-out)
//  3. explicitPort == true                 → always false (metadata.json has explicit port;
//     auto-starting a different server would create shadow databases)
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
func resolveAutoStart(current bool, doltAutoStartCfg string, explicitPort bool) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return false
	}
	if os.Getenv("BEADS_DOLT_AUTO_START") == "0" {
		return false
	}
	// When metadata.json specifies an explicit server port, never auto-start.
	// The user has configured a specific server — if it's down, error out
	// rather than silently starting a different server from .beads/dolt/.
	if explicitPort {
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

// applyResolvedConfig merges metadata.json-derived defaults into a store config.
// Server connection fields are always populated because the storage layer is
// server-backed even when older metadata.json files omit dolt_mode.
func applyResolvedConfig(beadsDir string, fileCfg *configfile.Config, cfg *Config) {
	cfg.Path = fileCfg.DatabasePath(beadsDir)
	if cfg.BeadsDir == "" {
		cfg.BeadsDir = beadsDir
	}

	// GH#2438: Warn if data-dir is set in server mode — it has no effect on
	// which database the server uses and can cause silent DB context switches.
	if fileCfg.DoltDataDir != "" && fileCfg.IsDoltServerMode() {
		fmt.Fprintf(os.Stderr, "Warning: dolt_data_dir is set (%s) but Dolt is in server mode.\n", fileCfg.DoltDataDir)
		fmt.Fprintf(os.Stderr, "In server mode, data-dir does not control which database is used.\n")
		fmt.Fprintf(os.Stderr, "This may cause commands to operate on the wrong database.\n")
		fmt.Fprintf(os.Stderr, "Fix: bd dolt set data-dir ''   (clear the data-dir setting)\n\n")
	}

	// Always apply database name from metadata.json (prefix-based naming, bd-u8rda).
	if cfg.Database == "" {
		cfg.Database = fileCfg.GetDoltDatabase()
	}

	if cfg.ServerHost == "" {
		cfg.ServerHost = fileCfg.GetDoltServerHost()
	}
	if cfg.ServerPort == 0 {
		// Use doltserver.DefaultConfig for port resolution (env > port file >
		// config.yaml > metadata > DerivePort). fileCfg.GetDoltServerPort()
		// falls back to 3307 which is wrong for standalone repos.
		cfg.ServerPort = doltserver.DefaultConfig(beadsDir).Port
	}
	if cfg.ServerUser == "" {
		cfg.ServerUser = fileCfg.GetDoltServerUser()
	}
}
