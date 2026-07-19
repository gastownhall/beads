package dolt

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// ServerMode is re-exported from doltserver for convenience.
type ServerMode = doltserver.ServerMode

// Re-export ServerMode constants for callers that import storage/dolt.
const (
	ServerModeOwned    = doltserver.ServerModeOwned
	ServerModeExternal = doltserver.ServerModeExternal
	ServerModeEmbedded = doltserver.ServerModeEmbedded
)

// ApplyCLIAutoStart sets the standalone auto-start policy used by the
// normal CLI path. Honors the actual server mode resolved from
// metadata.json + env: when External (e.g. metadata.json has explicit
// dolt_server_port), suppresses fallback auto-spawn — the user has
// configured an external server; if it's transiently unreachable, bd
// errors out rather than silently spawning a different server from
// .beads/dolt/ (the shadow database bug).
//
// Cold standalone setups remain unaffected: bd init writes the port and
// starts the server in one shot, so subsequent commands find a running
// server and don't need fallback auto-start. If the user explicitly
// stops the server, External mode's "you manage the lifecycle" semantics
// asks the user to run `bd dolt start`.
func ApplyCLIAutoStart(beadsDir string, cfg *Config) {
	if cfg.DisableAutoStart {
		cfg.AutoStart = false
		return
	}
	autoStartCfg := config.GetString("dolt.auto-start")
	if autoStartCfg == "" {
		autoStartCfg = config.GetStringFromDir(beadsDir, "dolt.auto-start")
	}
	mode := doltserver.ResolveServerMode(beadsDir)
	cfg.AutoStart = resolveAutoStart(true, autoStartCfg, mode)
}

// NewFromConfig creates a DoltStore based on the metadata.json configuration.
// beadsDir is the path to the .beads directory.
func NewFromConfig(ctx context.Context, beadsDir string) (*DoltStore, error) {
	return NewFromConfigWithOptions(ctx, beadsDir, nil)
}

// NewFromConfigWithCLIOptions creates a DoltStore using the standalone CLI
// auto-start policy from cmd/bd/main.go. This is for CLI helper paths like
// `bd doctor` that should behave the same way as normal top-level CLI commands
// while still honoring externally managed server mode.
func NewFromConfigWithCLIOptions(ctx context.Context, beadsDir string, cfg *Config) (*DoltStore, error) {
	fileCfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if fileCfg == nil {
		fileCfg = configfile.DefaultConfig()
	}

	// Apply central server config as defaults for any server fields not
	// set in the per-project metadata.json. This eliminates the need to
	// duplicate host/port/user across 30+ project configs.
	applyCentralConfigDefaults(fileCfg)

	if cfg == nil {
		cfg = &Config{}
	}
	if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
		return nil, err
	}
	ApplyCLIAutoStart(beadsDir, cfg)

	return New(ctx, cfg)
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

	// Apply central server config as defaults for any server fields not
	// set in the per-project metadata.json.
	applyCentralConfigDefaults(fileCfg)

	// Build config from metadata.json, allowing overrides from caller
	if cfg == nil {
		cfg = &Config{}
	}
	if err := applyResolvedConfig(beadsDir, fileCfg, cfg); err != nil {
		return nil, err
	}

	// Enable auto-start for standalone users (similar to main.go's auto-start
	// handling), with additional support for BEADS_TEST_MODE and a config.yaml
	// fallback for library consumers that never call config.Initialize().
	// Disabled under orchestrator (which manages its own server), by explicit config,
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
	// When the server is externally managed (explicit port in metadata.json,
	// shared server mode, etc.), suppress auto-start. This prevents bd from
	// launching a different server when the user's configured server is
	// temporarily unreachable — the root cause of the shadow database bug.
	mode := doltserver.ResolveServerMode(beadsDir)
	if cfg.DisableAutoStart {
		cfg.AutoStart = false
	} else {
		cfg.AutoStart = resolveAutoStart(cfg.AutoStart, autoStartCfg, mode)
	}

	return New(ctx, cfg)
}

// resolveAutoStart computes the effective AutoStart value, respecting a
// caller-provided value (current) while applying system-level overrides.
//
// Priority (highest to lowest):
//  1. BEADS_TEST_MODE=1                    → always false (tests own the server lifecycle)
//  2. BEADS_DOLT_AUTO_START=0              → always false (explicit env opt-out)
//  3. mode == ServerModeExternal           → always false (server is externally managed;
//     auto-starting a different server would create shadow databases)
//  4. doltAutoStartCfg == "false"/"0"/"off" → false (config.yaml explicit opt-out;
//     user intent to disable auto-start must be respected even when callers
//     like ApplyCLIAutoStart or bootstrap pass current=true — GH#autostart-bug)
//  5. current == true                      → true  (caller option wins over default)
//  6. default                              → true  (standalone user; safe default)
//
// doltAutoStartCfg is the raw value of the "dolt.auto-start" key from config.yaml
// (pass config.GetString("dolt.auto-start") at the call site).
//
// Note: because AutoStart is a plain bool, a zero value (false) cannot be
// distinguished from an explicit "opt-out" by the caller.  Callers that need
// to suppress auto-start should use one of the environment-variable or
// config-file overrides above.
func resolveAutoStart(current bool, doltAutoStartCfg string, mode ServerMode) bool {
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		return false
	}
	if os.Getenv("BEADS_DOLT_AUTO_START") == "0" {
		return false
	}
	// When the server is externally managed, never auto-start.
	// The user has configured a specific server — if it's down, error out
	// rather than silently starting a different server from .beads/dolt/.
	if mode == ServerModeExternal {
		return false
	}
	// Config.yaml explicit opt-out takes precedence over caller-provided
	// current=true. Without this, ApplyCLIAutoStart (which passes current=true)
	// and bootstrap paths (which hardcode AutoStart=true) would ignore the
	// user's dolt.auto-start: false setting, spawning rogue dolt servers that
	// overwrite port files and cause DB lock conflicts.
	if strings.EqualFold(doltAutoStartCfg, "false") || doltAutoStartCfg == "0" || strings.EqualFold(doltAutoStartCfg, "off") {
		return false
	}
	// Caller option wins over default.
	if current {
		return true
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
func applyResolvedConfig(beadsDir string, fileCfg *configfile.Config, cfg *Config) error {
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
	// Resolve the server-mode credential (the MySQL username) from the config and any
	// trusted credential helper.
	if err := resolveServerCredential(fileCfg, cfg); err != nil {
		return err
	}
	// Populate password and TLS the same way the CLI CRUD path does. Without
	// this, callers that rely on NewFromConfigWithOptions (e.g. doctor's
	// SharedStore) fail to reach externally-hosted Dolt servers that keep
	// credentials in ~/.config/beads/credentials, while bd create/list/close
	// succeed (bd-h5k7). GetDoltServerPasswordForPort checks BEADS_DOLT_PASSWORD
	// env first, then credentials file keyed by [host:resolved-port].
	if cfg.ServerPassword == "" {
		cfg.ServerPassword = fileCfg.GetDoltServerPasswordForPort(cfg.ServerPort)
	}
	if !cfg.ServerTLS {
		cfg.ServerTLS = fileCfg.GetDoltServerTLS()
	}

	// Pool size: env var > config.yaml > caller override > default (10).
	// Useful for shared-server setups with many worktrees (GH#3140).
	if cfg.MaxOpenConns == 0 {
		if v := os.Getenv("BEADS_DOLT_MAX_CONNS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cfg.MaxOpenConns = n
			}
		}
	}
	if cfg.MaxOpenConns == 0 {
		if v := config.GetString("dolt.max-conns"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				cfg.MaxOpenConns = n
			}
		}
	}
	return nil
}

// resolveServerCredential populates cfg.ServerUser (and, on the hosted credential path,
// cfg.CredentialCommand) when the caller left ServerUser unset. An explicitly pre-set
// ServerUser bypasses the helper entirely and keeps the pure static sql.Open path.
//
// SECURITY: the credential helper is executed as a shell command (sh -c), so it is accepted
// ONLY from a trusted user-local source (BEADS_DOLT_CREDENTIAL_COMMAND or the central per-user
// server config) — never from the project's tracked .beads/metadata.json, which travels with a
// git clone and would otherwise let a checked-out repository inject shell execution during
// store open (CWE-78). This single gate covers both the open-time eager resolve and the
// per-dial BeforeConnect path, because both key off cfg.CredentialCommand set here.
//
// A configured helper takes precedence over the static user and fails closed: if it errors,
// store construction aborts rather than falling back to the static/root identity. The eager
// token only SEEDS the DSN username so buildServerDSN/connStr stays parseable; every physical
// dial re-resolves a fresh cached token via BeforeConnect (see connector.go), so the baked
// username is inert and pooled connections survive token rotation.
func resolveServerCredential(fileCfg *configfile.Config, cfg *Config) error {
	if cfg.ServerUser != "" {
		return nil
	}
	helper := configfile.TrustedDoltCredentialCommand()
	if helper == "" {
		// A tracked metadata.json may name a credential command, but bd will not run it.
		// Warn so an operator who intended a helper moves it to a trusted source instead of
		// silently getting the static-user fallback.
		if fileCfg.DoltCredentialCommand != "" {
			fmt.Fprint(os.Stderr,
				"Warning: ignoring dolt_credential_command from project metadata.json for security.\n"+
					"A credential helper is executed as a shell command, so it must come from a trusted\n"+
					"user-local source: set BEADS_DOLT_CREDENTIAL_COMMAND or add dolt_credential_command to\n"+
					"the central server config (~/.config/beads/server.json). Falling back to the static user.\n")
		}
		cfg.ServerUser = fileCfg.GetDoltServerUser()
		return nil
	}
	// Open-time eager resolve: no dial context here, so use Background (the helper keeps its
	// full credCommandTimeout cap). Per-dial resolves on the connector path thread the dial
	// context so a deadline can abort the mint.
	tok, err := resolveCredentialToken(context.Background(), helper)
	if err != nil {
		return fmt.Errorf("resolving dolt credential command: %w", err)
	}
	cfg.ServerUser = tok
	cfg.CredentialCommand = helper
	return nil
}

// applyCentralConfigDefaults loads the central server config from
// ~/.config/beads/server.json (or BEADS_CENTRAL_CONFIG env var) and
// applies its server fields as defaults to the per-project config.
// A missing central config file is silently ignored.
func applyCentralConfigDefaults(fileCfg *configfile.Config) {
	centralPath := os.Getenv("BEADS_CENTRAL_CONFIG")
	if centralPath == "" {
		centralPath = configfile.DefaultCentralConfigPath()
	}
	if centralPath == "" {
		return
	}

	centralCfg, err := configfile.LoadCentralConfig(centralPath)
	if err != nil {
		// Log but don't fail — a broken central config shouldn't block operations.
		fmt.Fprintf(os.Stderr, "Warning: failed to load central config %s: %v\n", centralPath, err)
		return
	}

	configfile.ApplyCentralDefaults(fileCfg, centralCfg)
}
