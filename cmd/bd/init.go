package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/setup"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/templates/agents"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/term"
)

// applyInitGatewayCredential routes the hand-built init Dolt config through the
// gateway credential machinery. When BEADS_DOLT_CREDENTIAL_COMMAND is configured,
// its short-lived token becomes the connection username, the config is marked as
// targeting an authenticating gateway server (so the store skips schema init and
// the SHOW/CREATE DATABASE probe), and local auto-start is disabled — a gateway
// server is externally managed, and spawning a local dolt server would shadow it.
//
// It runs only when the config targets a server (ServerMode). An embedded init
// never presents a username, so an ambient credential command must not run — or
// fail — an embedded open. This mirrors the canonical open path
// (internal/storage/dolt/open.go), which gates the command on
// IsDoltServerMode()||IsSharedServerMode(). Without this gate, a plain embedded
// `bd init` on a host that exports BEADS_DOLT_CREDENTIAL_COMMAND would drag the
// gateway machinery into an ordinary local init and abort it with a misleading
// provisioning-contract error, leaving a half-initialized .beads/. Gateway init
// always reaches here with ServerMode set: init forces server mode for a shared
// server and hard-fails a remote dolt host that lacks server mode.
//
// The main command path applies this via applyResolvedConfig; init builds its own
// config and must mirror the behavior. Within server mode it is still a no-op when
// no command is configured or when a --server-user flag already preset the
// username. It fails closed: a configured-but-failing command aborts init.
func applyInitGatewayCredential(ctx context.Context, beadsDir string, doltCfg *dolt.Config) error {
	if !doltCfg.ServerMode {
		return nil
	}
	fileCfg, _ := configfile.Load(beadsDir)
	if fileCfg == nil {
		fileCfg = configfile.DefaultConfig()
	}
	applied, err := dolt.ApplyGatewayCredential(ctx, fileCfg, doltCfg)
	if err != nil {
		return err
	}
	if applied {
		// ApplyGatewayCredential sets DisableAutoStart, but the hand-built init
		// config path never runs ApplyCLIAutoStart to translate that into the
		// AutoStart field the store's auto-start block actually consults.
		doltCfg.AutoStart = false
	}
	return nil
}

// resolveInitIssuePrefix decides how init sets the issue_prefix. readErr is the
// error (if any) from reading the current issue_prefix out of the database.
//
// Non-gateway (legacy, unchanged): if none is configured yet, set the sanitized
// prefix (dots -> underscores so issue IDs stay valid identifiers); if one exists,
// leave it (avoid clobbering a shared database). readErr is ignored here, exactly
// as legacy init ignored it. Gateway: the prefix is server-provisioned, so an
// existing value is adopted (no write). A missing value is a provisioning-contract
// violation — bd will not choose a prefix for a hosted database — but only when the
// read genuinely succeeded and returned empty: a read error means we could not
// consult the server, so it is surfaced as the transient failure it is rather than
// misdiagnosed as an unprovisioned database.
func resolveInitIssuePrefix(gateway bool, existing, dbName, prefix string, readErr error) (value string, write bool, err error) {
	if existing != "" {
		return "", false, nil
	}
	if gateway {
		if readErr != nil {
			return "", false, fmt.Errorf(
				"reading issue_prefix from hosted database %q: %w", dbName, readErr)
		}
		return "", false, fmt.Errorf(
			"hosted database %q has no issue_prefix -- provisioning-contract violation; "+
				"bd will not choose one for a hosted database (re-provision server-side, then re-run init)",
			dbName)
	}
	return strings.ReplaceAll(prefix, ".", "_"), true, nil
}

// resolveInitProjectID decides init's project identity by reconciling the local
// metadata.json id (localID; "" when none is set yet) with the _project_id read
// from the database (adoptedFromDB; "" when absent or not consulted). readErr is
// the error, if any, from that read. changed reports whether the resolved id
// differs from localID, so the caller can surface the reconciliation.
//
// Gateway: the hosted database's identity is server-authoritative, so an adopted
// server id always wins and is reconciled onto local even when localID is already
// set. A re-init or orchestrator-preseeded workspace must not keep a stale local
// id: init opens with CreateIfMissing, which skips the storage identity verifier
// (store.go verifyProjectIdentity), so a stale id would be saved as success and
// every later normal open would then hard-fail with PROJECT IDENTITY MISMATCH. A
// missing server id is a provisioning-contract violation bd will not mint over —
// even when a local id already exists — and a read error is surfaced as the
// transient failure it is, so a flaky connection is not misdiagnosed as an
// unprovisioned database.
//
// Non-gateway (legacy, unchanged): a non-empty localID is kept as-is (readErr
// ignored, exactly as before); otherwise an adopted id wins (another rig already
// chose it; minting a new one would break cross-project verification), else a
// fresh identity is generated.
func resolveInitProjectID(gateway bool, localID, adoptedFromDB, dbName string, readErr error) (value string, changed bool, err error) {
	if gateway {
		if adoptedFromDB != "" {
			return adoptedFromDB, adoptedFromDB != localID, nil
		}
		if readErr != nil {
			return "", false, fmt.Errorf(
				"reading project identity (_project_id) from hosted database %q: %w", dbName, readErr)
		}
		return "", false, fmt.Errorf(
			"hosted database %q has no provisioned project identity (_project_id) -- "+
				"provisioning-contract violation; bd will not mint an identity for a hosted database",
			dbName)
	}
	if localID != "" {
		return localID, false, nil
	}
	if adoptedFromDB != "" {
		return adoptedFromDB, true, nil
	}
	return configfile.GenerateProjectID(), true, nil
}

// shouldConsultInitProjectID reports whether init must read _project_id from the
// database before resolving the local identity.
//
// Gateway: always — the hosted server owns the identity, so init reconciles it on
// every run, including a re-init or preseeded metadata.json that already carries a
// project_id. Skipping the read when a local id was already set was the bug that
// let init save a stale id and made every later normal open hard-fail PROJECT
// IDENTITY MISMATCH.
//
// Non-gateway (legacy, unchanged): only when no local id exists yet and the
// database is a pre-existing shared/bootstrapped one worth adopting from
// (--database set or bootstrapped-from-remote). A fresh local-only init mints its
// own id without a read.
func shouldConsultInitProjectID(gateway bool, localID, database string, bootstrappedFromRemote bool) bool {
	if gateway {
		return true
	}
	return localID == "" && (database != "" || bootstrappedFromRemote)
}

// shouldWriteProjectIDLocally reports whether init should write _project_id back
// to the database. Non-gateway writes it for cross-project verification; gateway
// does not — the identity is server-authoritative, and the credential may be
// read-only.
func shouldWriteProjectIDLocally(gateway bool, projectID string) bool {
	return !gateway && projectID != ""
}

// shouldWriteInitStateToDB reports whether init may write clone-local tracking
// state into the database — the bd_version / repo_id / clone_id / last_import_time
// metadata and the initial-state Dolt commit.
//
// Non-gateway (legacy, unchanged): true — these writes give diagnostics accurate
// data and leave a clean, committed working set.
//
// Gateway: false. The database is a shared, server-owned store: repo_id and
// clone_id are per-clone fingerprints that cross-project verification (doctor,
// migrate) reads back, so a last-init-wins overwrite there produces false mismatch
// diagnostics for every other clone; and the credential may be read-only, in which
// case each write and the DOLT_COMMIT would merely spew per-init warnings. In
// gateway mode bd adopts and verifies server state instead of writing it, exactly
// as shouldWriteProjectIDLocally already suppresses the _project_id write-back.
func shouldWriteInitStateToDB(gateway bool) bool {
	return !gateway
}

// shouldInitSharedGlobalDB reports whether init should manage the local shared
// Dolt server and provision its beads_global database — starting the server,
// creating the global database, initializing its schema, and seeding its config.
//
// Shared-server mode owns that local infrastructure, so it does. Gateway mode
// does not: a gateway connects to a remote authenticating server that provisions
// databases server-side under no-create/no-schema/no-write semantics. Running the
// shared-global path there would either start a shadow local server or drive
// create/schema/write operations against the gateway — the global initializer
// rebuilds its own dolt.Config without the Gateway flag and with CreateIfMissing
// set, so it cannot preserve gateway semantics. init therefore skips the whole
// shared-global path whenever the connection is gateway-managed, mirroring how
// shouldWriteInitStateToDB / shouldWriteProjectIDLocally already suppress
// server-owned writes.
func shouldInitSharedGlobalDB(sharedServer, sharedServerMode, gateway bool) bool {
	return (sharedServer || sharedServerMode) && !gateway
}

var initCmd = &cobra.Command{
	Use:           "init",
	GroupID:       "setup",
	SilenceUsage:  true,
	SilenceErrors: true,
	Short:         "Initialize bd in the current directory",
	Long: `Initialize bd in the current directory by creating a .beads/ directory
and its storage (a Dolt database by default). Optionally specify a custom issue prefix.

Dolt is the default and only supported storage backend, with full version
control (history, branching, sync).

Use --database to specify an existing server database name, overriding the
default prefix-based naming. This is useful when an external tool (e.g. an orchestrator)
has already created the database.

With --stealth: configures per-repository git settings for invisible beads usage:
  • .git/info/exclude to prevent beads files from being committed
  Perfect for personal use without affecting repo collaborators.
  To set up a specific AI tool, run: bd setup <claude|cursor|aider|...> --stealth

By default, beads uses an embedded Dolt engine (no external server needed).
Pass --server to use an external dolt sql-server instead. In server mode,
set connection details with --server-host, --server-port, and --server-user.
Password should be set via BEADS_DOLT_PASSWORD environment variable.

Auto-export is optional. When enabled, bd exports issues to
.beads/issues.jsonl after write commands (throttled to once per 60s). This is
for viewers (bv), interchange, and issue-level migration; not backup.
Cross-machine sync and backups use Dolt remotes/backups, not JSONL import/export.
To enable: bd config set export.auto true

Non-interactive mode (--non-interactive or BD_NON_INTERACTIVE=1):
  Skips all interactive prompts, using sensible defaults:
  • Role defaults to "maintainer" (override with --role)
  • Fork exclude auto-configured when fork detected
  • Auto-export left at default (disabled)
  • --contributor and --team flags are rejected (wizards require interaction)
  Also auto-detected when stdin is not a terminal or CI=true is set.`,
	RunE: func(cmd *cobra.Command, _ []string) (retErr error) {
		prefix, _ := cmd.Flags().GetString("prefix")
		quiet, _ := cmd.Flags().GetBool("quiet")
		contributor, _ := cmd.Flags().GetBool("contributor")
		team, _ := cmd.Flags().GetBool("team")
		stealth, _ := cmd.Flags().GetBool("stealth")
		skipHooks, _ := cmd.Flags().GetBool("skip-hooks")
		skipAgents, _ := cmd.Flags().GetBool("skip-agents")
		force, _ := cmd.Flags().GetBool("force")
		reinitLocal, _ := cmd.Flags().GetBool("reinit-local")
		initIfMissing, _ := cmd.Flags().GetBool("init-if-missing")
		discardRemote, _ := cmd.Flags().GetBool("discard-remote")
		nonInteractiveFlag, _ := cmd.Flags().GetBool("non-interactive")
		roleFlag, _ := cmd.Flags().GetString("role")
		fromJSONL, _ := cmd.Flags().GetBool("from-jsonl")
		initRemote, _ := cmd.Flags().GetString("remote")
		initRemoteChanged := cmd.Flags().Changed("remote")
		// Dolt server connection flags
		backendFlag, _ := cmd.Flags().GetString("backend")
		initServerMode, _ := cmd.Flags().GetBool("server")
		serverHost, _ := cmd.Flags().GetString("server-host")
		serverPort, _ := cmd.Flags().GetInt("server-port")
		serverSocket, _ := cmd.Flags().GetString("server-socket")
		serverUser, _ := cmd.Flags().GetString("server-user")
		database, _ := cmd.Flags().GetString("database")
		destroyToken, _ := cmd.Flags().GetString("destroy-token")

		// --force is a deprecated alias for --reinit-local. They share
		// semantics for the local data-safety guard; both refuse remote
		// divergence unless --discard-remote is also passed. See
		// engdocs/adr/0002-init-safety-invariants.md.
		if force && !reinitLocal {
			fmt.Fprintf(os.Stderr, "%s --force is deprecated; use --reinit-local instead.\n", ui.RenderWarn("DeprecationWarning:"))
			fmt.Fprintf(os.Stderr, "  See 'bd help init-safety' for the init flag surface.\n\n")
			reinitLocal = true
		}
		sharedServer, _ := cmd.Flags().GetBool("shared-server")
		externalServer, _ := cmd.Flags().GetBool("external")
		debugMode, _ := cmd.Flags().GetBool("debug")
		initProxiedServer, _ := cmd.Flags().GetBool("proxied-server")
		serverConfigPath, _ := cmd.Flags().GetString("proxied-server-config-path")
		serverLogPath, _ := cmd.Flags().GetString("proxied-server-log-path")
		serverRootPath, _ := cmd.Flags().GetString("proxied-server-root-path")
		serverProxyPort, _ := cmd.Flags().GetInt("proxied-server-port")
		serverProxyIdleTimeout, _ := cmd.Flags().GetDuration("proxied-server-idle-timeout")
		idleTimeoutSet := cmd.Flags().Changed("proxied-server-idle-timeout")
		externalHost, _ := cmd.Flags().GetString("proxied-server-external-host")
		externalPort, _ := cmd.Flags().GetInt("proxied-server-external-port")
		externalSocketPath, _ := cmd.Flags().GetString("proxied-server-external-socket-path")
		externalUser, _ := cmd.Flags().GetString("proxied-server-external-user")
		externalTLS, _ := cmd.Flags().GetBool("proxied-server-external-tls")
		externalTLSCACertPath, _ := cmd.Flags().GetString("proxied-server-external-tls-ca-cert-path")
		externalTLSCertPath, _ := cmd.Flags().GetString("proxied-server-external-tls-cert-path")
		externalTLSKeyPath, _ := cmd.Flags().GetString("proxied-server-external-tls-key-path")
		externalTLSServerName, _ := cmd.Flags().GetString("proxied-server-external-tls-server-name")
		externalTLSSkipVerify, _ := cmd.Flags().GetBool("proxied-server-external-tls-skip-verify")
		externalKeepAlive, _ := cmd.Flags().GetDuration("proxied-server-external-keep-alive")
		if os.Getenv("BEADS_DOLT_PROXIED_SERVER") == "1" {
			initProxiedServer = true
		}

		initEvt := metrics.NewCommandEvent("init-" + resolveInitDoltMode(initProxiedServer, sharedServer, initServerMode))
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(initEvt)
			}
		}()

		if initProxiedServer && initServerMode {
			return fmt.Errorf("--server and --proxied-server are mutually exclusive")
		}
		if initProxiedServer {
			if sharedServer || externalServer ||
				serverHost != "" || serverPort != 0 || serverSocket != "" || serverUser != "" {
				return fmt.Errorf("--proxied-server cannot be combined with --shared-server, --external, or any --server-* flag")
			}
		}
		if serverConfigPath != "" {
			if !initProxiedServer {
				return fmt.Errorf("--proxied-server-config-path requires --proxied-server")
			}
			if !filepath.IsAbs(serverConfigPath) {
				return fmt.Errorf("--proxied-server-config-path must be an absolute path, got %q", serverConfigPath)
			}
			if err := validateProxiedServerConfig(serverConfigPath); err != nil {
				return fmt.Errorf("--proxied-server-config-path %v", err)
			}
		}
		if serverLogPath != "" {
			if !initProxiedServer {
				return fmt.Errorf("--proxied-server-log-path requires --proxied-server")
			}
			if !filepath.IsAbs(serverLogPath) {
				return fmt.Errorf("--proxied-server-log-path must be an absolute path, got %q", serverLogPath)
			}
			if err := validateProxiedServerLogPath(serverLogPath); err != nil {
				return fmt.Errorf("--proxied-server-log-path %v", err)
			}
		}
		if serverRootPath != "" {
			if !initProxiedServer {
				return fmt.Errorf("--proxied-server-root-path requires --proxied-server")
			}
			if !filepath.IsAbs(serverRootPath) {
				return fmt.Errorf("--proxied-server-root-path must be an absolute path, got %q", serverRootPath)
			}
			if err := validateProxiedServerRootPath(serverRootPath); err != nil {
				return fmt.Errorf("--proxied-server-root-path %v", err)
			}
		}
		if serverProxyPort != 0 {
			if !initProxiedServer {
				return fmt.Errorf("--proxied-server-port requires --proxied-server")
			}
			if serverProxyPort < 1 || serverProxyPort > 65535 {
				return fmt.Errorf("--proxied-server-port must be between 1 and 65535, got %d", serverProxyPort)
			}
		}
		if idleTimeoutSet {
			if !initProxiedServer {
				return fmt.Errorf("--proxied-server-idle-timeout requires --proxied-server")
			}
			if serverProxyIdleTimeout < 0 {
				return fmt.Errorf("--proxied-server-idle-timeout must be 0 (never) or a positive duration, got %s", serverProxyIdleTimeout)
			}
			if serverProxyIdleTimeout == 0 {
				serverProxyIdleTimeout = proxy.IdleTimeoutNever
			}
		}

		externalProvided := externalHost != "" || externalPort != 0 || externalSocketPath != "" ||
			externalUser != "" ||
			externalTLS || externalTLSCACertPath != "" || externalTLSCertPath != "" || externalTLSKeyPath != "" ||
			externalTLSServerName != "" || externalTLSSkipVerify || externalKeepAlive != 0
		if externalProvided && !initProxiedServer {
			return fmt.Errorf("--proxied-server-external-* flags require --proxied-server")
		}
		if externalProvided && serverConfigPath != "" {
			return fmt.Errorf("--proxied-server-external-* flags cannot be combined with --proxied-server-config-path (external mode has no managed dolt sql-server to configure)")
		}
		if externalProvided && debugMode {
			return fmt.Errorf("--debug cannot be combined with --proxied-server-external-* (debug applies to the managed dolt sql-server only)")
		}
		var externalConfig *configfile.ExternalDoltConfig
		if externalProvided {
			cfg := configfile.ExternalDoltConfig{
				Host:            externalHost,
				Port:            externalPort,
				Socket:          externalSocketPath,
				User:            externalUser,
				TLSRequired:     externalTLS,
				TLSCACert:       externalTLSCACertPath,
				TLSCert:         externalTLSCertPath,
				TLSKey:          externalTLSKeyPath,
				TLSServerName:   externalTLSServerName,
				TLSSkipVerify:   externalTLSSkipVerify,
				KeepAlivePeriod: externalKeepAlive,
			}
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("--proxied-server-external-*: %v", err)
			}
			externalConfig = &cfg
		}

		// Backend selection: Dolt is the only supported backend.
		if !configfile.IsSupportedBackend(backendFlag) {
			switch backendFlag {
			case configfile.BackendPostgres, configfile.BackendMySQL:
				return fmt.Errorf("storage backend %q is no longer supported: %s; the supported backend is \"dolt\" (default)", backendFlag, configfile.RemovedBackendRationale)
			case configfile.BackendSQLite:
				return fmt.Errorf("storage backend %q is no longer supported: %s; the supported backend is \"dolt\" (default)", backendFlag, configfile.RemovedSQLiteRationale)
			}
			return fmt.Errorf("unknown backend %q: the supported backend is \"dolt\" (default)", backendFlag)
		}
		for _, legacyFlag := range removedBackendInitFlags {
			if cmd.Flags().Changed(legacyFlag.name) {
				return fmt.Errorf("--%s belonged to %s: %s; use --backend=dolt (the default)", legacyFlag.name, legacyFlag.origin, legacyFlag.rationale)
			}
		}
		// Validate --database format early, before any side effects.
		if database != "" {
			if err := dolt.ValidateDatabaseName(database); err != nil {
				return fmt.Errorf("invalid database name %q: %v", database, err)
			}
		}

		// Resolve non-interactive mode: flag > env var > terminal detection.
		// This must be computed before any interactive prompts.
		nonInteractive := isNonInteractiveInit(nonInteractiveFlag)

		// Validate --role flag value
		if roleFlag != "" {
			switch roleFlag {
			case "maintainer", "contributor":
				// valid
			default:
				return fmt.Errorf("invalid --role %q: must be \"maintainer\" or \"contributor\"", roleFlag)
			}
		}

		// Fail-fast: contributor/team wizards require interaction
		if nonInteractive && contributor {
			return fmt.Errorf("--contributor requires interactive prompts and cannot be used with --non-interactive")
		}
		if nonInteractive && team {
			return fmt.Errorf("--team requires interactive prompts and cannot be used with --non-interactive")
		}

		// Dolt is the only supported backend.
		backend := configfile.BackendDolt

		// Also treat BEADS_DOLT_SERVER_MODE=1 env var as --server.
		if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
			initServerMode = true
		}

		// Shared server mode still uses a Dolt sql-server, so it must select
		// the server-backed store path during init. Without this, init can
		// persist shared-server intent in YAML while still creating an embedded
		// store and recording dolt_mode=embedded in metadata.json (GH#2946).
		if sharedServer || strings.EqualFold(os.Getenv("BEADS_DOLT_SHARED_SERVER"), "true") || os.Getenv("BEADS_DOLT_SHARED_SERVER") == "1" {
			initServerMode = true
		}

		// Set serverMode so !usesSQLServer() returns the correct value.
		// Both the global and cmdCtx must be set because PersistentPreRun
		// creates a fresh cmdCtx (with ServerMode=false) before Run executes.
		serverMode = initServerMode
		proxiedServerMode = initProxiedServer
		if cmdCtx != nil {
			cmdCtx.ServerMode = initServerMode
			cmdCtx.ProxiedServerMode = initProxiedServer
		}

		if initProxiedServer {
			if err := runInitProxiedServer(cmd, rootCtx, initProxiedServerInput{
				prefix:                 prefix,
				database:               database,
				roleFlag:               roleFlag,
				initRemote:             initRemote,
				initRemoteChanged:      initRemoteChanged,
				destroyToken:           destroyToken,
				serverConfigPath:       serverConfigPath,
				serverLogPath:          serverLogPath,
				serverRootPath:         serverRootPath,
				serverProxyPort:        serverProxyPort,
				serverProxyIdleTimeout: serverProxyIdleTimeout,
				externalConfig:         externalConfig,
				quiet:                  quiet,
				stealth:                stealth,
				skipHooks:              skipHooks,
				skipAgents:             skipAgents,
				reinitLocal:            reinitLocal,
				contributor:            contributor,
				team:                   team,
				fromJSONL:              fromJSONL,
				nonInteractive:         nonInteractive,
			}); err != nil {
				return err
			}
			return nil
		}

		// Propagate --shared-server flag to env so that IsSharedServerMode(),
		// ResolveDoltDir(), and DefaultConfig() all see shared mode immediately
		// (before config.yaml exists). Safe: init runs once and exits.
		if sharedServer {
			_ = os.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		}

		if debugMode {
			_ = os.Setenv("BEADS_DOLT_DEBUG", "1")
		}

		// Initialize config (PersistentPreRun doesn't run for init command).
		// This must happen before validation that depends on server/embedded
		// mode so dolt.mode from config.yaml is treated like --server.
		if err := config.Initialize(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
			// Non-fatal - continue with defaults
		}

		// config.yaml fallback for dolt.mode: if --server wasn't passed and
		// env var didn't set it, check config.yaml for dolt.mode: server.
		if !initServerMode {
			if modeVal := config.GetYamlConfig("dolt.mode"); strings.EqualFold(modeVal, "server") {
				initServerMode = true
				serverMode = initServerMode
				if cmdCtx != nil {
					cmdCtx.ServerMode = initServerMode
				}
			}
		}

		// Reject hyphens in --database for embedded mode. Must run AFTER
		// serverMode is set above — otherwise !usesSQLServer() always returns
		// true and incorrectly rejects server-mode names (GH#3231).
		if database != "" && strings.ContainsRune(database, '-') && !usesSQLServer() {
			return fmt.Errorf("database name %q contains hyphens which are invalid in embedded mode; use underscores instead (e.g. %q)",
				database, sanitizeDBName(database))
		}

		// Hard fail: if a remote dolt.host is configured, server mode MUST
		// be active — embedded mode has no host. dolt.port alone is ambient
		// plumbing (e.g. test harnesses) and is not treated as server intent.
		if !initServerMode {
			configHost := config.GetYamlConfig("dolt.host")
			envHost := os.Getenv("BEADS_DOLT_SERVER_HOST")
			configPort := config.GetYamlConfig("dolt.port")
			envPort := os.Getenv("BEADS_DOLT_SERVER_PORT")

			effectiveHost := configHost
			hostSource := "config.yaml"
			if envHost != "" {
				effectiveHost = envHost
				hostSource = "environment"
			}

			if effectiveHost != "" && !isLocalHost(effectiveHost) {
				detail := fmt.Sprintf("dolt.host (%s) is", effectiveHost)
				if configPort != "" || envPort != "" {
					detail = fmt.Sprintf("dolt.host (%s) and dolt.port are", effectiveHost)
				}
				return fmt.Errorf("%s set via %s but server mode is not enabled.\n"+
					"  Embedded mode has no host/port — these settings require server mode.\n"+
					"  Set dolt.mode: server in %s or pass --server to bd init.",
					detail, hostSource, config.UserConfigYamlPath())
			}
		}

		// Safety guard: check for existing beads data.
		// This prevents accidental re-initialization. --force and
		// --reinit-local both bypass this local-only guard; they do NOT
		// authorize cross-boundary operations on remote history (see
		// CheckRemoteSafety at cmd/bd/init_safety.go and
		// engdocs/adr/0002-init-safety-invariants.md).
		if !reinitLocal {
			if err := checkExistingBeadsData(prefix); err != nil {
				// --init-if-missing makes init idempotent, but ONLY for the
				// benign "workspace already initialized" case. An operational
				// error from the check (e.g. an unreadable .beads/embeddeddolt
				// directory) must still abort rather than be masked as a
				// successful skip.
				if initIfMissing && errors.Is(err, errWorkspaceAlreadyInitialized) {
					// Guard against masking a genuine mismatch: an explicit
					// --prefix or --database that does not match the existing
					// workspace must abort, since silently skipping would ignore
					// the request and reuse a different database. --database is
					// the authoritative selector, so it is checked even when
					// --prefix is also set.
					existing := existingWorkspaceDBName()
					if cmd.Flags().Changed("database") {
						if initIfMissingDatabaseMismatch(existing, database) {
							return fmt.Errorf("workspace already initialized as database %q, but --database %q was requested.\nRemove --database (or pass a matching value) to reuse the existing workspace", existing, database)
						}
					} else if cmd.Flags().Changed("prefix") {
						if initIfMissingPrefixMismatch(existing, prefix) {
							return fmt.Errorf("workspace already initialized as database %q, but --prefix %q was requested.\nRemove --prefix (or pass a matching value) to reuse the existing workspace", existing, prefix)
						}
					}
					if !quiet {
						fmt.Fprintln(os.Stderr, "Skipping init: workspace already initialized.")
					}
					return nil
				}
				return fmt.Errorf("%v", err)
			}
		}

		// Even with --reinit-local, warn about existing data and require
		// confirmation. Non-interactive mode accepts --destroy-token for
		// explicit opt-in; interactive mode prompts for typed confirmation.
		if reinitLocal {
			if count, err := countExistingIssues(prefix); err == nil && count > 0 {
				fmt.Fprintf(os.Stderr, "\n%s Re-initializing will destroy the existing database.\n\n", ui.RenderWarn("WARNING:"))
				fmt.Fprintf(os.Stderr, "  Existing issues: %d\n\n", count)
				fmt.Fprintf(os.Stderr, "  This action CANNOT be undone. All issues, dependencies, and\n")
				fmt.Fprintf(os.Stderr, "  Dolt commit history will be permanently lost.\n\n")
				fmt.Fprintf(os.Stderr, "  Before proceeding, consider:\n")
				fmt.Fprintf(os.Stderr, "    bd export > issue-export.jsonl    # Export issue records, not full DB state\n")
				fmt.Fprintf(os.Stderr, "    bd dolt status              # Check if this is a server config issue\n\n")
				if term.IsTerminal(int(os.Stdin.Fd())) {
					fmt.Fprintf(os.Stderr, "Type 'destroy %d issues' to confirm: ", count)
					scanner := bufio.NewScanner(os.Stdin)
					scanner.Scan()
					expected := fmt.Sprintf("destroy %d issues", count)
					if strings.TrimSpace(scanner.Text()) != expected {
						fmt.Fprintf(os.Stderr, "\nAborted. Database was NOT modified.\n")
						return &exitError{Code: ExitLocalExistsRefused}
					}
				} else {
					// Non-interactive (piped input, AI agent, etc.)
					//
					// ADR invariant (engdocs/adr/0002-init-safety-invariants.md):
					// runtime error text must not echo a complete destructive
					// invocation. See 'bd help init-safety' for the token
					// format. This closes the 58f5989bf failure class where
					// an agent copy-pasted the suggested command.
					expectedToken := FormatDestroyToken(prefix)
					if destroyToken == expectedToken {
						fmt.Fprintf(os.Stderr, "Destroy token accepted. Proceeding with re-initialization.\n")
					} else {
						fmt.Fprintf(os.Stderr, "Refusing to destroy %d issues in non-interactive mode.\n", count)
						fmt.Fprintf(os.Stderr, "  See 'bd help init-safety' for the required --destroy-token format.\n")
						fmt.Fprintf(os.Stderr, "  Or export issue records first: bd export > issue-export.jsonl\n")
						return &exitError{Code: ExitDestroyTokenMissing}
					}
				}
			}
		}

		// Handle stealth mode setup
		if stealth {
			if err := setupStealthMode(!quiet); err != nil {
				return fmt.Errorf("setting up stealth mode: %v", err)
			}

			// In stealth mode, skip git hooks installation
			// since we handle it globally
			skipHooks = true
		}

		// Check BEADS_DB environment variable if --db flag not set
		// (PersistentPreRun doesn't run for init command)
		if dbPath == "" {
			if envDB := os.Getenv("BEADS_DB"); envDB != "" {
				dbPath = envDB
			}
		}

		// Determine prefix with precedence: flag > config > auto-detect from git > auto-detect from directory name
		if prefix == "" {
			// Try to get from config file
			prefix = config.GetString("issue-prefix")
		}

		// auto-detect prefix from directory name
		if prefix == "" {
			// Auto-detect from directory name
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current directory: %v", err)
			}
			prefix = filepath.Base(cwd)
		}

		// Normalize prefix before storing it in config or deriving the Dolt
		// database name. Dots are not valid in issue prefixes and must match the
		// underscore form used for DoltDatabase/metadata.json. Leading dots
		// produce invalid names (e.g. ".claude" -> "claude"), the trailing
		// hyphen is added automatically during ID generation, and a leading
		// non-letter is prefixed with "bd_" so the derived MySQL identifier is
		// valid (directory names like "001" are common in temp dirs).
		prefix = normalizeIssuePrefix(prefix)
		remoteDivergenceConfirmed := false

		// Cross-boundary safety (bd-q83 / ADR 0002): check remote state
		// BEFORE any filesystem side-effects so a refusal exits cleanly.
		// We only refuse here; bootstrap decisions happen later once
		// beadsDir is computed. See CheckRemoteSafety in init_safety.go.
		{
			var earlySyncURL string
			earlyRemoteSource := initSyncRemoteNone
			earlyRemoteHasDoltData := false
			earlySyncURL, earlyRemoteSource = resolveInitConfiguredSyncRemote(initRemote, initRemoteChanged, resolveSyncRemote)
			if earlyRemoteSource == initSyncRemoteExplicit {
				// An explicit --remote is intent to bootstrap or wire that URL,
				// but it is not proof that the remote already contains Dolt
				// history. --from-jsonl is the exception: it selects a local
				// source and skips cloning, so we must probe now rather than
				// silently wiring a populated remote to orphan local history.
				if fromJSONL {
					earlyRemoteHasDoltData = gitRemoteHasDoltDataRef(earlySyncURL)
				}
			} else if earlyRemoteSource == initSyncRemoteConfigured {
				earlyRemoteHasDoltData = true // sync.remote configured = user intends bootstrap
			} else if earlyRemoteSource == initSyncRemoteNone && !stealth && isGitRepo() && !isBareGitRepo() {
				if originURL, err := gitOriginGetURL(); err == nil && originURL != "" {
					earlySyncURL = normalizeRemoteURL(originURL)
					earlyRemoteHasDoltData = gitOriginHasDoltDataRef()
				}
			}
			if earlySyncURL != "" {
				earlyDecision := CheckRemoteSafety(RemoteSafetyInput{
					Force:             force,
					ReinitLocal:       reinitLocal,
					FromJSONL:         fromJSONL,
					DiscardRemote:     discardRemote,
					DestroyToken:      destroyToken,
					ExpectedToken:     FormatDestroyToken(prefix),
					RemoteHasDoltData: earlyRemoteHasDoltData,
					IsInteractive:     term.IsTerminal(int(os.Stdin.Fd())),
				})
				if _, err := handleRemoteSafetyDecision(earlyDecision, prefix, earlySyncURL, destroyToken, func() bool {
					switch earlyRemoteSource {
					case initSyncRemoteExplicit:
						return gitRemoteHasDoltDataRef(earlySyncURL)
					case initSyncRemoteConfigured:
						return earlyRemoteHasDoltData
					default:
						return gitOriginHasDoltDataRef()
					}
				}, earlyRemoteHasDoltData, &remoteDivergenceConfirmed); err != nil {
					// The early guard refuses and confirms only; bootstrap
					// selection happens later after beadsDir/dbName are known.
					return err
				}
			}
		}

		// Determine beadsDir first (used for all storage path calculations).
		// BEADS_DIR takes precedence, otherwise use CWD/.beads (with redirect support).
		// This must be computed BEFORE initDBPath to ensure consistent path resolution
		// (avoiding macOS /var -> /private/var symlink issues when directory creation
		// happens between path computations).
		var beadsDirForInit string
		if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
			beadsDirForInit = utils.CanonicalizePath(envBeadsDir)
		} else {
			beadsDirForInit = beads.GetWorktreeFallbackBeadsDir()
			if beadsDirForInit == "" {
				localBeadsDir := filepath.Join(".", ".beads")
				beadsDirForInit = beads.FollowRedirect(localBeadsDir)
			}
		}

		// Determine storage path.
		//
		// Precedence: --db > BEADS_DIR > default (.beads/dolt)
		// If there's a redirect file, use the redirect target (GH#bd-0qel)
		initDBPath := dbPath
		if initDBPath == "" {
			// Dolt backend: respect dolt_data_dir config / BEADS_DOLT_DATA_DIR env
			initDBPath = doltserver.ResolveDoltDir(beadsDirForInit)
		}

		// Determine if we should create .beads/ directory in CWD or main repo root
		// For worktrees, .beads should always be in the main repository root
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %v", err)
		}

		hasExplicitBeadsDir := os.Getenv("BEADS_DIR") != ""

		// Use the beadsDir computed earlier (before any directory creation)
		// to ensure consistent path representation.
		beadsDir := beadsDirForInit

		// Prevent nested .beads directories
		// Check if current working directory is inside a .beads directory
		if strings.Contains(filepath.Clean(cwd), string(filepath.Separator)+".beads"+string(filepath.Separator)) ||
			strings.HasSuffix(filepath.Clean(cwd), string(filepath.Separator)+".beads") {
			fmt.Fprintf(os.Stderr, "Error: cannot initialize bd inside a .beads directory\n")
			fmt.Fprintf(os.Stderr, "Current directory: %s\n", cwd)
			fmt.Fprintf(os.Stderr, "Please run 'bd init' from outside the .beads directory.\n")
			return &exitError{Code: 1}
		}

		initDBDir := filepath.Dir(initDBPath)

		// Convert both to absolute paths for comparison
		beadsDirAbs, err := filepath.Abs(beadsDir)
		if err != nil {
			beadsDirAbs = filepath.Clean(beadsDir)
		}
		initDBDirAbs, err := filepath.Abs(initDBDir)
		if err != nil {
			initDBDirAbs = filepath.Clean(initDBDir)
		}

		// Always create local .beads/ when using default location (CWD/.beads).
		// The local directory is needed for metadata.json, config.yaml,
		// .gitignore, and hooks — regardless of where dolt data lives.
		// Only skip when BEADS_DIR explicitly points outside the project.
		//
		// Previous logic only created .beads/ when the dolt data dir was a
		// subdirectory of .beads/, which broke server mode with external
		// BEADS_DOLT_DATA_DIR or BEADS_DOLT_* env vars (GH#2519).
		useLocalBeads := !hasExplicitBeadsDir || filepath.Clean(initDBDirAbs) == filepath.Clean(beadsDirAbs)

		if useLocalBeads {
			// Create .beads directory with owner-only permissions (0700).
			if err := os.MkdirAll(beadsDir, config.BeadsDirPerm); err != nil {
				if os.IsPermission(err) {
					if runtime.GOOS == "windows" {
						return fmt.Errorf("failed to create .beads directory: %v\n\n"+
							"Windows Controlled Folder Access may be blocking bd.exe.\n"+
							"To fix: Open Windows Security > Virus & threat protection >\n"+
							"Ransomware protection > Allow an app through Controlled folder access\n"+
							"and add bd.exe (typically %%USERPROFILE%%\\go\\bin\\bd.exe).", err)
					} else {
						return fmt.Errorf("failed to create .beads directory: %v\n\n"+
							"Permission denied. Check directory ownership and permissions:\n"+
							"  ls -la %s\n"+
							"  chmod 755 %s", err, filepath.Dir(beadsDir), filepath.Dir(beadsDir))
					}
				}
				return fmt.Errorf("failed to create .beads directory: %v", err)
			}

			// Fix permissions on pre-existing .beads/ directories that may
			// have been created with a permissive umask (GH#3391).
			if fixed, err := config.FixBeadsDirPermissions(beadsDir); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: could not fix .beads permissions: %v\n", err)
				}
			} else if fixed && !quiet {
				fmt.Fprintf(os.Stderr, "Fixed .beads permissions to %04o\n", config.BeadsDirPerm)
			}

			// On Linux btrfs, disable transparent compression on .beads/ so that
			// dolt's hot append-only write path (under .beads/dolt/ or
			// .beads/embeddeddolt/) does not trigger kworker thrashing from
			// read-modify-write-recompress cycles. New files created inside this
			// directory inherit FS_NOCOW_FL automatically, so setting it here —
			// before dolt writes anything — covers both server and embedded modes.
			// No-op on non-Linux and non-btrfs filesystems (GH nocow-beads-dolt-init).
			if err := applyNoCOW(beadsDir); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to set FS_NOCOW_FL on %s: %v\n", beadsDir, err)
			}

			// Create/update .gitignore in .beads directory (only if missing or outdated)
			gitignorePath := filepath.Join(beadsDir, ".gitignore")
			check := doctor.CheckGitignore(cwd)
			if check.Status != "ok" {
				if err := os.WriteFile(gitignorePath, []byte(doctor.GitignoreTemplate), 0600); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", err)
					// Non-fatal - continue anyway
				}
			}

			// Add .dolt/ and *.db to project-root .gitignore (GH#2034)
			// Prevents users from accidentally committing Dolt database files.
			// Skip when BEADS_DIR points outside the current directory — the CWD
			// may not be a repo we should mutate (e.g. running from a worktree
			// with an external BEADS_DIR). When BEADS_DIR points to the same
			// repo's .beads/, the gitignore update is still appropriate.
			cwdAbs, _ := filepath.Abs(cwd)
			beadsDirIsLocal := strings.HasPrefix(beadsDirAbs, filepath.Clean(cwdAbs)+string(filepath.Separator))
			if beadsDirIsLocal {
				if stealth {
					// Stealth mode: route Dolt-file ignore patterns into .git/info/exclude instead
					// of a tracked .gitignore so collaborators never see beads-related changes.
					if err := addProjectPatternsToGitExclude(cwd, doctor.ProjectGitignorePatterns, !quiet); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to update git exclude: %v\n", err)
						// Non-fatal - continue anyway
					}
					// bd init --stealth is safe to re-run: clean up any beads section an earlier
					// (non-stealth or pre-fix) run leaked into the tracked .gitignore.
					if removed, err := removeBeadsProjectGitignoreSection(cwd); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to clean project .gitignore: %v\n", err)
						// Non-fatal - continue anyway
					} else if removed && !quiet {
						fmt.Printf("  %s Removed leaked beads section from tracked .gitignore\n", ui.RenderPass("✓"))
					}
				} else if err := doctor.EnsureProjectGitignore(cwd); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update project .gitignore: %v\n", err)
					// Non-fatal - continue anyway
				}
			}

		}

		// Ensure git is initialized — bd requires git for role config, sync branches,
		// hooks, worktrees, and fingerprint computation. git init is idempotent so
		// safe to call even if already in a git repo.
		// Skip when BEADS_DIR is explicitly set — the caller may be creating a
		// standalone .beads/ directory outside any git repo.
		if !isGitRepo() && !hasExplicitBeadsDir {
			gitInitCmd := exec.Command("git", "init")
			if output, err := gitInitCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to initialize git repository: %v\n%s", err, output)
			}
			// Clear cached git context so subsequent operations (e.g. hook
			// installation) see the newly-created repository (GH#2899).
			git.ResetCaches()
			if !quiet {
				fmt.Printf("  %s Initialized git repository\n", ui.RenderPass("✓"))
			}
		}

		// Ensure storage directory exists (.beads/dolt).
		// In server mode, dolt.New() connects via TCP and doesn't create local directories,
		// so we create the marker directory explicitly.
		// In embedded mode the engine creates its own directories under .beads/embeddeddolt/,
		// so skip this to avoid leaving an empty .beads/dolt/ artifact (GH#2903).
		if initServerMode {
			if err := os.MkdirAll(initDBPath, config.BeadsDirPerm); err != nil {
				return fmt.Errorf("failed to create storage directory %s: %v", initDBPath, err)
			}
			// Linux btrfs: disable compression on the dolt data dir to avoid
			// kworker thrashing on the append-only write path. Best-effort; a
			// non-btrfs filesystem returns nil from applyNoCOW.
			if err := applyNoCOW(initDBPath); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to set FS_NOCOW_FL on %s: %v\n", initDBPath, err)
			}
		}

		ctx := rootCtx

		// Create Dolt storage backend
		storagePath := doltserver.ResolveDoltDir(beadsDir)
		// Respect existing config's database name to avoid creating phantom catalog
		// entries when a user has renamed their database (GH#2051).
		dbName := ""
		if existingCfg, _ := configfile.Load(beadsDir); existingCfg != nil && existingCfg.DoltDatabase != "" {
			dbName = existingCfg.DoltDatabase
		} else if prefix != "" {
			// Sanitize hyphens to underscores for SQL-idiomatic database names.
			// Dots were already normalized in prefix above so issue_prefix and
			// DoltDatabase stay in lockstep.
			// Must match the sanitization applied to metadata.json DoltDatabase
			// field (line below), otherwise init creates a database with one name
			// but metadata.json records a different name, causing reopens to fail.
			dbName = dbNameFromPrefix(prefix)
		} else {
			dbName = "beads"
		}
		// --database flag overrides all prefix-based naming. This allows callers
		// (e.g. an orchestrator) to specify a pre-existing database name, preventing orphan
		// database creation when the database was already created externally.
		if database != "" {
			dbName = database
		}

		// Validate the auto-derived database name early so we can surface a clear,
		// actionable error instead of a cryptic failure from the storage layer.
		// The --database flag is already validated above; this catches cases where
		// the directory name produces an invalid identifier after sanitization
		// (e.g. spaces, '@', '!' that survive the hyphen/dot replacement).
		// Skip when dbName came from an existing config — it was valid when stored.
		if database == "" {
			if existingCfg, _ := configfile.Load(beadsDir); existingCfg == nil || existingCfg.DoltDatabase == "" {
				if err := dolt.ValidateDatabaseName(dbName); err != nil {
					dirName := filepath.Base(cwd)
					fmt.Fprintf(os.Stderr, "Error: directory name %q produces an invalid database name %q.\n", dirName, dbName)
					fmt.Fprintf(os.Stderr, "Re-run with a valid prefix: bd init --prefix <name>\n")
					fmt.Fprintf(os.Stderr, "(Database names must start with a letter or underscore and contain only letters, digits, underscores, or hyphens.)\n")
					return &exitError{Code: 1}
				}
			}
		}

		// Auto-bootstrap from remote if sync.remote (or deprecated
		// sync.git-remote) is configured, or git origin has Dolt data
		// (refs/dolt/data). This makes bd init and bd bootstrap
		// interchangeable — both clone from the remote when one exists.
		//
		// Cross-boundary safety (bd-q83 / ADR 0002): when the caller
		// passes --force or --reinit-local and the remote already has
		// refs/dolt/data, we must refuse — the new local identity would
		// orphan-push over the team's history on first write. The
		// CheckRemoteSafety chokepoint encodes this invariant; any future
		// flag that can interact with remote history must route through
		// it rather than adding another `&& !someFlag` here.
		syncResolutionURL, syncRemoteSource := resolveInitConfiguredSyncRemote(initRemote, initRemoteChanged, resolveSyncRemote)
		syncURL := syncResolutionURL
		syncURLFromConfig := syncURL != "" && syncRemoteSource != initSyncRemoteNone // true when URL came from explicit user config
		bootstrappedFromRemote := false
		syncFromRemote := false
		syncURLFromGitOrigin := false
		remoteHasDoltData := false

		if syncURL != "" {
			// sync.remote was explicitly configured. Treat it as bootstrap-
			// from-remote intent unless --from-jsonl selected a local JSONL
			// source. CheckRemoteSafety still enforces that local-source
			// flags can't silently fight a configured remote.
			// Trust the URL format as-is: normalizeRemoteURL would convert
			// http:// to git+http:// and break Dolt remotesapi endpoints
			// configured explicitly by the user (GH#3339).
			if syncRemoteSource == initSyncRemoteExplicit && !fromJSONL {
				syncFromRemote = true
			} else if syncRemoteSource == initSyncRemoteConfigured {
				remoteHasDoltData = true
			} else if syncRemoteSource == initSyncRemoteExplicit {
				remoteHasDoltData = gitRemoteHasDoltDataRef(syncURL)
			}
			if !syncFromRemote {
				decision := CheckRemoteSafety(RemoteSafetyInput{
					Force:             force,
					ReinitLocal:       reinitLocal,
					FromJSONL:         fromJSONL,
					DiscardRemote:     discardRemote,
					DestroyToken:      destroyToken,
					ExpectedToken:     FormatDestroyToken(prefix),
					RemoteHasDoltData: remoteHasDoltData,
					IsInteractive:     term.IsTerminal(int(os.Stdin.Fd())),
				})
				bootstrap, err := handleRemoteSafetyDecision(decision, prefix, syncURL, destroyToken, func() bool {
					if syncRemoteSource == initSyncRemoteExplicit {
						return gitRemoteHasDoltDataRef(syncURL)
					}
					return remoteHasDoltData
				}, remoteHasDoltData, &remoteDivergenceConfirmed)
				if err != nil {
					return err
				}
				syncFromRemote = bootstrap
			}
		} else if syncRemoteSource == initSyncRemoteNone && !stealth && isGitRepo() && !isBareGitRepo() {
			if originURL, err := gitOriginGetURL(); err == nil && originURL != "" {
				syncURL = normalizeRemoteURL(originURL)
				syncURLFromGitOrigin = true
				remoteHasDoltData = gitOriginHasDoltDataRef()

				decision := CheckRemoteSafety(RemoteSafetyInput{
					Force:             force,
					ReinitLocal:       reinitLocal,
					FromJSONL:         fromJSONL,
					DiscardRemote:     discardRemote,
					DestroyToken:      destroyToken,
					ExpectedToken:     FormatDestroyToken(prefix),
					RemoteHasDoltData: remoteHasDoltData,
					IsInteractive:     term.IsTerminal(int(os.Stdin.Fd())),
				})

				bootstrap, err := handleRemoteSafetyDecision(decision, prefix, syncURL, destroyToken, gitOriginHasDoltDataRef, remoteHasDoltData, &remoteDivergenceConfirmed)
				if err != nil {
					return err
				}
				if bootstrap {
					syncFromRemote = true
				}
			}
		}
		if syncFromRemote {
			var err error
			cloneCfg := initTimeCloneConfig(initServerMode, serverHost, serverPort, serverSocket, serverUser, dbName)
			err = cloneFromRemoteWithMode(ctx, beadsDir, syncURL, dbName, cloneCfg, initRemoteCloneMode(initServerMode, externalServer))
			if err != nil {
				if isEmptyRemoteCloneError(err) {
					if !quiet {
						fmt.Printf("  %s Remote has no Dolt data yet; initialized a fresh local database\n", ui.RenderWarn("!"))
					}
					syncFromRemote = false
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to clone remote %q: %v\n", syncURL, err)
					fmt.Fprintf(os.Stderr, "Hint: verify the URL is reachable and any credentials are valid, or omit --remote to initialize a fresh local database.\n")
					return &exitError{Code: 1}
				}
			} else {
				bootstrappedFromRemote = true
				if !quiet {
					fmt.Printf("  %s Bootstrapped from remote: %s\n", ui.RenderPass("✓"), syncURL)
				}
			}
		}

		// Build config. Beads always uses dolt sql-server.
		// AutoStart is always enabled during init — we need a server to initialize the database.
		//
		// Port resolution for init: use ONLY project-local sources (env var, port file)
		// to prevent cross-project data leakage (GH#2336). DefaultConfig falls through
		// to config.yaml / global config, which may resolve to another project's server
		// because metadata.json doesn't exist yet during init. For fresh inits, port 0
		// forces auto-start to allocate an ephemeral port for THIS project.
		initPort := 0
		if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
			if port, err := strconv.Atoi(p); err == nil && port > 0 {
				initPort = port
			}
		}
		if initPort == 0 {
			initPort = doltserver.ReadPortFile(beadsDir)
		}
		// Shared server mode intentionally uses a common port for all projects.
		if initPort == 0 && doltserver.IsSharedServerMode() {
			initPort = doltserver.DefaultSharedServerPort
		}
		doltCfg := &dolt.Config{
			Path:            storagePath,
			BeadsDir:        beadsDir,
			Database:        dbName,
			ServerPort:      initPort,
			ServerMode:      initServerMode,
			ProxiedServer:   initProxiedServer,
			CreateIfMissing: true, // bd init is the only path that should create databases
			AutoStart:       initServerMode && os.Getenv("BEADS_DOLT_AUTO_START") != "0",
			ServerTLS:       initDoltServerTLSFromEnv(),
		}
		if serverHost != "" {
			doltCfg.ServerHost = serverHost
		}
		if serverPort != 0 {
			doltCfg.ServerPort = serverPort
		}
		if serverSocket != "" {
			doltCfg.ServerSocket = serverSocket
		}
		if serverUser != "" {
			doltCfg.ServerUser = serverUser
		}

		// Route through the gateway credential machinery: when a credential
		// command is configured, its token becomes the connection username and
		// gateway semantics (no schema init, no CREATE DATABASE, no auto-start)
		// apply. No-op — and byte-identical to legacy init — when unconfigured.
		if err := applyInitGatewayCredential(ctx, beadsDir, doltCfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: resolving dolt credential command: %v\n", err)
			return &exitError{Code: 1}
		}

		initLock, err := acquireEmbeddedLock(beadsDir, initServerMode || initProxiedServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return &exitError{Code: 1}
		}
		defer initLock.Unlock()

		// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
		// directory — including noms/LOCK files. These are Dolt-internal files.
		// Removing them WILL cause unrecoverable data corruption and data loss.
		// Dolt manages these files itself; external interference is never safe.

		// In shared server mode, ensure the shared server is running before
		// opening the store. EnsureRunning won't auto-start because
		// ResolveServerMode returns ServerModeExternal, so start it
		// explicitly via the shared server directory (GH#2946).
		//
		// --external skips this: the server is managed outside bd (e.g.
		// Docker, systemd, testcontainers). The caller is responsible for
		// ensuring the server is reachable and the port file exists.
		//
		// Gateway mode also skips this: it connects to a remote authenticating
		// server and must not start a local shared server or create/write
		// beads_global (see shouldInitSharedGlobalDB).
		if !externalServer && shouldInitSharedGlobalDB(sharedServer, doltserver.IsSharedServerMode(), doltCfg.Gateway) {
			if sharedDir, err := doltserver.SharedServerDir(); err == nil {
				state, _ := doltserver.IsRunning(sharedDir)
				if state == nil || !state.Running {
					if _, startErr := doltserver.Start(sharedDir); startErr != nil {
						fmt.Fprintf(os.Stderr, "Error: failed to start shared Dolt server: %v\n", startErr)
						return &exitError{Code: 1}
					}
					if !quiet {
						fmt.Printf("  %s Shared Dolt server started\n", ui.RenderPass("✓"))
					}
				} else if debugMode {
					fmt.Fprintf(os.Stderr, "Warning: shared Dolt server (PID %d, port %d) is already running without debug flags.\n", state.PID, state.Port)
					fmt.Fprintf(os.Stderr, "  Restart to pick up debug mode:\n")
					fmt.Fprintf(os.Stderr, "    bd dolt stop && bd dolt start\n")
				}
			}

			// Ensure the global beads_global database exists on the shared server.
			// This is idempotent — safe to run on every init.
			globalHost := configfile.DefaultDoltServerHost
			if serverHost != "" {
				globalHost = serverHost
			}
			globalPort := initPort
			if globalPort == 0 {
				globalPort = doltserver.DefaultSharedServerPort
			}
			globalUser := configfile.DefaultDoltServerUser
			if serverUser != "" {
				globalUser = serverUser
			}
			if err := doltserver.EnsureGlobalDatabase(globalHost, globalPort, globalUser, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create global database: %v\n", err)
				// Non-fatal — project init should succeed even if global DB creation fails
			} else if !quiet {
				fmt.Printf("  %s Global database %s available\n", ui.RenderPass("✓"), doltserver.GlobalDatabaseName)
			}
		}

		store, err := newDoltStore(ctx, doltCfg)
		if err != nil {
			// #4259: the remote-migrate gate refused to auto-apply pending
			// migrations. When init just bootstrapped the clone, the REMOTE is
			// what is behind this binary — the gate's generic "adopt: bd
			// bootstrap" remedy is circular, and init has not yet written
			// metadata.json/config.yaml, so without finalization the workspace
			// is stranded where not even the gate's own unlock commands can
			// open it (bd-4mpy7).
			var gateErr *schema.RemoteMigrateGateError
			if errors.As(err, &gateErr) {
				if bootstrappedFromRemote {
					// Leave the workspace in the same finalized state the
					// bd bootstrap sync path produces (GH#3201) so
					// `bd migrate --force` (or BD_ALLOW_REMOTE_MIGRATE=1) and
					// `bd dolt push` can open the cloned database.
					fcfg := initTimeCloneConfig(initServerMode, serverHost, serverPort, serverSocket, serverUser, dbName)
					if ferr := finalizeSyncedBootstrap(beadsDir, syncURL, fcfg, dbName); ferr != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to finalize bootstrapped workspace: %v\n", ferr)
					}
				}
				if jsonOutput {
					handleRemoteMigrateGateJSON(gateErr)
				} else if bootstrappedFromRemote {
					printBootstrapRemoteBehindGuidance(os.Stderr, gateErr, syncURL, "bd init")
				} else {
					fmt.Fprint(os.Stderr, gateErr.UserMessage())
				}
				return &exitError{Code: 1}
			}
			fmt.Fprintf(os.Stderr, "Error: failed to open Dolt store: %v\n", err)
			return &exitError{Code: 1}
		}

		// Initialize global database schema and config in shared-server mode.
		// Opens a separate store connection to beads_global with CreateIfMissing
		// to trigger schema migration, then seeds the issue prefix and project ID.
		// Skipped in gateway mode: initGlobalDatabaseConfig rebuilds its config
		// without the Gateway flag, so it would run create/schema/write operations
		// against the authenticating gateway (see shouldInitSharedGlobalDB).
		if shouldInitSharedGlobalDB(sharedServer, doltserver.IsSharedServerMode(), doltCfg.Gateway) {
			initGlobalDatabaseConfig(ctx, doltCfg, quiet)
		}

		// Configure the remote in the Dolt store so bd dolt push/pull work
		// immediately after init/bootstrap. A git origin is a valid Dolt
		// remote even before refs/dolt/data exists; the first bd dolt push
		// creates that ref. This keeps the default path durable without
		// falling back to JSONL-as-sync. Gateway mode skips it: the hosted
		// server owns the database, so DOLT_REMOTE('add', ...) must not run
		// against it (see shouldWriteInitDoltRemote).
		if shouldWriteInitDoltRemote(doltCfg.Gateway, syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin, isDoltLocalOnly()) {
			configureInitDoltRemote(ctx, store, syncURL, quiet)
		}

		// === CONFIGURATION METADATA (Pattern A: Fatal) ===
		// Configuration metadata is essential for core functionality and must succeed.
		// These settings define fundamental behavior (issue IDs, sync workflow).
		// Failure here indicates a serious problem that prevents normal operation.

		// Set the issue prefix in config (only if not already configured —
		// avoid clobbering when multiple rigs share the same Dolt database)
		existing, existingErr := store.GetConfig(ctx, "issue_prefix")
		// Sanitize dots to underscores so issue IDs (e.g. "GPUPolynomials_jl-1")
		// remain valid identifiers. Must match DoltDatabase sanitization above.
		// In gateway mode the prefix is server-provisioned: adopt an existing one,
		// refuse to invent a missing one. A read error is surfaced as a transient
		// failure rather than misread as an unprovisioned (contract-violating) db.
		issuePrefix, writePrefix, err := resolveInitIssuePrefix(doltCfg.Gateway, existing, dbName, prefix, existingErr)
		if err != nil {
			_ = store.Close()
			return err
		}
		if writePrefix {
			if err := store.SetConfig(ctx, "issue_prefix", issuePrefix); err != nil {
				_ = store.Close()
				return fmt.Errorf("failed to set issue prefix: %v", err)
			}
		}

		// === TRACKING METADATA (Pattern B: Warn and Continue) ===
		// Tracking metadata enhances functionality (diagnostics, version checks, collision detection)
		// but the system works without it. Failures here degrade gracefully - we warn but continue.
		// Belt-and-suspenders: write then verify read-back for each field.

		// Tracking metadata (bd_version, repo_id, clone_id) is clone-local state.
		// In gateway mode it must not be written into the shared, server-owned
		// database: repo_id/clone_id are per-clone fingerprints and a last-init-wins
		// overwrite feeds false cross-project mismatch diagnostics, while a read-only
		// credential would only warn per field.
		if shouldWriteInitStateToDB(doltCfg.Gateway) {
			// Store bd version in clone-local metadata (dolt-ignored, no merge conflicts)
			if err := store.SetLocalMetadata(ctx, "bd_version", Version); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to write bd_version local metadata: %v\n", err)
			}

			// Compute and store repository fingerprint (FR-015)
			repoID, err := beads.ComputeRepoID()
			if err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: could not compute repository ID: %v\n", err)
				}
			} else {
				if verifyMetadata(ctx, store, "repo_id", repoID) && !quiet {
					fmt.Printf("  Repository ID: %s\n", repoID[:8])
				}
			}

			// Compute and store clone-specific ID (FR-016: skip on failure)
			cloneID, err := beads.GetCloneID()
			if err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: could not compute clone ID: %v\n", err)
				}
			} else {
				if verifyMetadata(ctx, store, "clone_id", cloneID) && !quiet {
					fmt.Printf("  Clone ID: %s\n", cloneID)
				}
			}
		}

		// Create or preserve metadata.json for database metadata (bd-zai fix)
		if useLocalBeads {
			// First, check if metadata.json already exists
			existingCfg, err := configfile.Load(beadsDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to load existing metadata.json: %v\n", err)
			}

			var cfg *configfile.Config
			if existingCfg != nil {
				// Preserve existing config
				cfg = existingCfg
			} else {
				cfg = configfile.DefaultConfig()
			}

			// Resolve the project identity UUID (GH#2372). It is stored in both
			// metadata.json and the database and verified on every connection to
			// detect cross-project leakage.
			//
			// Consult the database _project_id and adopt it when:
			//   - --database is set and the database already exists on a
			//     shared remote Dolt server (GH#2922), or
			//   - we just bootstrapped from a remote whose Dolt history
			//     already carried a _project_id, or
			//   - we are connected to an authenticating gateway server, whose
			//     database identity is provisioned server-side.
			// In all cases another rig — or the hosted server — has already
			// chosen an identity; minting a new one and writing it back would
			// overwrite the source identity and cause cross-project verification
			// to fail on subsequent pulls. In gateway mode bd refuses to mint one
			// at all (a missing identity is a provisioning-contract violation),
			// and because the hosted server is authoritative it reconciles the
			// identity on every init — even a re-init or preseeded workspace whose
			// metadata.json already carries a stale project_id. Otherwise init
			// (which opens with CreateIfMissing, skipping the storage identity
			// verifier) would save the stale id as success and every later normal
			// open would hard-fail with PROJECT IDENTITY MISMATCH.
			adoptedFromDB := ""
			var adoptReadErr error
			if store != nil && shouldConsultInitProjectID(doltCfg.Gateway, cfg.ProjectID, database, bootstrappedFromRemote) {
				existingID, err := store.GetMetadata(ctx, "_project_id")
				if err != nil {
					adoptReadErr = err
				} else if existingID != "" {
					adoptedFromDB = existingID
				}
			}
			localID := cfg.ProjectID
			resolvedID, identityChanged, err := resolveInitProjectID(doltCfg.Gateway, localID, adoptedFromDB, dbName, adoptReadErr)
			if err != nil {
				_ = store.Close()
				return err
			}
			if identityChanged && adoptedFromDB != "" && !quiet {
				if localID == "" {
					fmt.Printf("  %s Adopted project identity from existing database\n", ui.RenderPass("✓"))
				} else {
					fmt.Printf("  %s Reconciled local project identity with hosted database\n", ui.RenderPass("✓"))
				}
			}
			cfg.ProjectID = resolvedID

			// Always store backend explicitly in metadata.json
			cfg.Backend = backend
			// Metadata.json.database should point to the Dolt directory (not beads.db).
			// Backward-compat: older dolt setups left this as "beads.db", which is misleading.
			if backend == configfile.BackendDolt {
				if cfg.Database == "" || cfg.Database == beads.CanonicalDatabaseName {
					cfg.Database = "dolt"
				}

				// Set SQL database name. --database flag takes precedence over prefix-based
				// naming to avoid cross-rig contamination (bd-u8rda). Only set prefix-based
				// name if not already configured — overwriting a user-renamed database
				// creates phantom catalog entries that crash information_schema (GH#2051).
				if database != "" {
					cfg.DoltDatabase = database
				} else if cfg.DoltDatabase == "" && prefix != "" {
					// Sanitize hyphens to underscores for SQL-idiomatic names (GH#2142).
					// Dots were already normalized in prefix above.
					// Must match the sanitization applied to dbName above,
					// otherwise init creates a database with one name but metadata.json
					// records a different name, causing reopens to fail.
					cfg.DoltDatabase = strings.ReplaceAll(prefix, "-", "_")
				}

				if sharedServer || doltserver.IsSharedServerMode() {
					cfg.GlobalDoltDatabase = doltserver.GlobalDatabaseName
					cfg.GlobalProjectID = doltserver.GlobalProjectID
				}

				// Persist the connection mode matching this build.
				switch {
				case usesProxiedServer():
					cfg.DoltMode = configfile.DoltModeProxiedServer
				case usesSQLServer():
					cfg.DoltMode = configfile.DoltModeServer
				default:
					cfg.DoltMode = configfile.DoltModeEmbedded
				}

				if !usesProxiedServer() {
					if serverHost != "" {
						cfg.DoltServerHost = serverHost
					}
					if serverPort != 0 {
						cfg.DoltServerPort = serverPort
					}
					if serverSocket != "" {
						cfg.DoltServerSocket = serverSocket
					}
					if serverUser != "" {
						cfg.DoltServerUser = serverUser
					}
				}

			}

			if err := cfg.Save(beadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create metadata.json: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Write project identity to database for cross-project verification (GH#2372).
			// Skip in gateway mode: the identity is server-authoritative and the
			// credential may be read-only, so bd must not write it back.
			if store != nil && shouldWriteProjectIDLocally(doltCfg.Gateway, cfg.ProjectID) {
				if err := store.SetMetadata(ctx, "_project_id", cfg.ProjectID); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to write project ID to database: %v\n", err)
				}
			}

			// Create config.yaml template (prefix is stored in DB, not config.yaml)
			if err := createConfigYaml(beadsDir, false, ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create config.yaml: %v\n", err)
				// Non-fatal - continue anyway
			}

			// Persist sync.remote to config.yaml so fresh clones can
			// bootstrap from it (the Dolt database is gitignored).
			// Must run AFTER createConfigYaml which creates the file.
			// Persist the effective remote so future bootstrap and hooks know
			// Dolt, not JSONL, is the cross-machine sync path. Plain git
			// origins are valid here: the first push creates refs/dolt/data.
			if err := persistInitSyncRemote(beadsDir, initRemote, syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin); err != nil {
				return fmt.Errorf("failed to persist sync.remote to config.yaml: %v", err)
			}

			// Enable shared server mode if requested via flag OR env var (GH#2377).
			// Persist to config.yaml so the project continues working without the env var.
			if sharedServer || doltserver.IsSharedServerMode() {
				if err := config.SetYamlConfig("dolt.shared-server", "true"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to enable shared server mode: %v\n", err)
				} else if !quiet {
					fmt.Printf("  %s Shared server mode enabled\n", ui.RenderPass("✓"))
				}
			}

			if debugMode {
				if err := config.SetYamlConfig("dolt.debug", "true"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to persist dolt.debug: %v\n", err)
				} else if !quiet {
					serverDir := doltserver.ResolveServerDir(beadsDir)
					fmt.Printf("  %s Debug mode enabled\n", ui.RenderPass("✓"))
					fmt.Printf("      Server log:  %s\n", doltserver.LogPath(serverDir))
					fmt.Printf("      Profile dir: %s\n", doltserver.DebugProfileDir(beadsDir))
					fmt.Printf("      Note: cpu.pprof is written when the server exits cleanly (bd dolt stop).\n")
				}
			}

			// In stealth mode, persist no-git-ops: true so bd prime
			// automatically uses stealth session-close protocol (GH#2159)
			if stealth {
				if err := config.SaveConfigValue("no-git-ops", true, beadsDir); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to set no-git-ops in config: %v\n", err)
				}
			}

			// Create README.md
			if err := createReadme(beadsDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to create README.md: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Initialize last_import_time metadata to mark the database as synced.
		// This prevents bd doctor from reporting "No last_import_time recorded in database"
		// after init completes. Sets the metadata to current time in RFC3339 format.
		// (mybd-9gw: sync divergence fix). Skipped in gateway mode: this is client-local
		// sync state that must not be written into the shared, server-owned database.
		if shouldWriteInitStateToDB(doltCfg.Gateway) {
			if err := store.SetMetadata(ctx, "last_import_time", time.Now().Format(time.RFC3339)); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to initialize last_import_time: %v\n", err)
				// Non-fatal - continue anyway
			}
		}

		// Import from local JSONL if requested (GH#2023).
		// This must run after the store is created and prefix is set.
		if fromJSONL {
			localJSONLPath := configuredImportJSONLPath(beadsDir)
			if _, statErr := os.Stat(localJSONLPath); os.IsNotExist(statErr) {
				_ = store.Close()
				return fmt.Errorf("--from-jsonl specified but %s does not exist", localJSONLPath)
			}
			issueCount, importErr := importFromLocalJSONL(ctx, store, localJSONLPath)
			if importErr != nil {
				_ = store.Close()
				return fmt.Errorf("failed to import from JSONL: %v", importErr)
			}
			if !quiet {
				fmt.Printf("  Imported %d issues from %s\n", issueCount, localJSONLPath)
			}
		}

		// Prompt for contributor mode if:
		// - In a git repo (needed to set beads.role config)
		// - Interactive terminal (stdin is TTY) and not --non-interactive
		// - No explicit --contributor or --team flag provided
		// - No explicit --role flag provided
		if isGitRepo() && !contributor && !team && roleFlag == "" && !nonInteractive && shouldPromptForRole() {
			promptedContributor, err := promptContributorMode()
			if err != nil {
				if isCanceled(err) {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
					_ = store.Close()
					return errCanceled()
				}
				// Non-fatal: warn but continue with default behavior
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to prompt for role: %v\n", err)
				}
			} else if promptedContributor {
				contributor = true // Triggers contributor wizard below
			}
		} else if isGitRepo() && !contributor && !team {
			// If prompt was skipped (non-interactive or CI environment),
			// ensure beads.role is set to avoid "not configured" warning
			// during diagnostics. Use --role flag if provided, otherwise default.
			role := roleFlag
			if role == "" {
				role = "maintainer"
			}
			if _, hasRole := getBeadsRole(); !hasRole {
				if err := setBeadsRole(role); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set default beads.role: %v\n", err)
				}
			} else if roleFlag != "" {
				// Explicit --role flag overrides existing role
				if err := setBeadsRole(role); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set beads.role: %v\n", err)
				}
			}
		}

		// Run contributor wizard if --contributor flag is set or user chose contributor
		if contributor {
			if err := runContributorWizard(ctx, store); err != nil {
				canceled := isCanceled(err)
				if canceled {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
				}
				_ = store.Close()
				if canceled {
					return errCanceled()
				}
				return fmt.Errorf("running contributor wizard: %v", err)
			}

			// Contributor setup must also pin role detection to contributor.
			// Without this, SSH remotes can be inferred as maintainer and bypass routing.
			if isGitRepo() {
				if err := setBeadsRole("contributor"); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set beads.role=contributor: %v\n", err)
				}
			}
		}

		// Run team wizard if --team flag is set
		if team {
			if err := runTeamWizard(ctx, store); err != nil {
				canceled := isCanceled(err)
				if canceled {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
				}
				_ = store.Close()
				if canceled {
					return errCanceled()
				}
				return fmt.Errorf("running team wizard: %v", err)
			}
		}

		// Safety net: ensure beads.role is always set when in a git repo (GH#2950).
		// Earlier code paths may skip role-setting when BEADS_DIR is set,
		// promptContributorMode fails, or edge-case flag combinations are used.
		// This guarantees every init leaves a usable role-configured state.
		if isGitRepo() {
			if _, hasRole := getBeadsRole(); !hasRole {
				fallbackRole := "maintainer"
				if roleFlag != "" {
					fallbackRole = roleFlag
				}
				if err := setBeadsRole(fallbackRole); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to set beads.role=%s: %v\n", fallbackRole, err)
				}
			}
		}

		// Auto-configure contributor routing for fork repos (bd-umbf Child 1).
		// Non-interactive, idempotent; only fires when upstream remote detected
		// and routing.contributor is not already set.
		if !contributor && isGitRepo() {
			if err := autoConfigureForkContributor(ctx, store, quiet || nonInteractive, roleFlag); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to auto-configure fork contributor routing: %v\n", err)
			}
		}

		// Auto-commit Dolt state so bd doctor doesn't warn about uncommitted
		// changes and users don't need a separate "bd vc commit" step. Skipped in
		// gateway mode: the shared server owns its own history and the credential
		// may be read-only, so bd must not issue DOLT_ADD/DOLT_COMMIT against it.
		if shouldWriteInitStateToDB(doltCfg.Gateway) {
			if err := store.Commit(ctx, "bd init"); err != nil {
				// Non-fatal: some setups (e.g. no tables yet) may have nothing to commit
				if !strings.Contains(err.Error(), "nothing to commit") {
					fmt.Fprintf(os.Stderr, "Warning: failed to commit initial state: %v\n", err)
				}
			}
		}

		if err := store.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close database: %v\n", err)
		}

		if initServerMode {
			if err := doltserver.MarkDoltDirCompatible(storagePath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to write Dolt compatibility marker at %s: %v\n", storagePath, err)
			}
		}

		// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
		// directory — including noms/LOCK files. These are Dolt-internal files.
		// Removing them WILL cause unrecoverable data corruption and data loss.
		// Dolt manages these files itself; external interference is never safe.

		// Fork detection: offer to configure .git/info/exclude (GH#742)
		setupExclude, _ := cmd.Flags().GetBool("setup-exclude")
		if setupExclude {
			// Manual flag - always configure
			if err := setupForkExclude(!quiet); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
			}
		} else if !stealth && isGitRepo() {
			// Auto-detect fork and prompt (skip if stealth - it handles exclude already)
			if isFork, upstreamURL := detectForkSetup(); isFork {
				if nonInteractive {
					// In non-interactive mode, auto-configure fork exclude
					if err := setupForkExclude(!quiet); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
					}
				} else {
					shouldExclude, err := promptForkExclude(upstreamURL, quiet)
					if err != nil {
						if isCanceled(err) {
							fmt.Fprintln(os.Stderr, "Setup canceled.")
							return errCanceled()
						}
					}
					if shouldExclude {
						if err := setupForkExclude(!quiet); err != nil {
							fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
						}
					}
				}
			}
		}

		// Auto-export prompt: disabled by default, let users opt in
		// interactively for viewers and other JSONL integrations (GH#4062).
		// In non-interactive mode the default (disabled) is kept.
		if !nonInteractive && !quiet {
			wantExport, err := promptAutoExport()
			if err != nil && isCanceled(err) {
				fmt.Fprintln(os.Stderr, "Setup canceled.")
				return errCanceled()
			}
			if wantExport {
				if err := config.SetYamlConfig("export.auto", "true"); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to enable auto-export: %v\n", err)
				} else {
					fmt.Printf("  %s Auto-export enabled -> .beads/issues.jsonl\n", ui.RenderPass("✓"))
				}
			} else if !quiet {
				fmt.Printf("  %s Auto-export disabled (enable later with: bd config set export.auto true)\n", ui.RenderPass("✓"))
			}
		}

		// Check if we're in a git repo and hooks aren't installed
		// Install by default unless --skip-hooks is passed
		// Hooks are installed to .beads/hooks/ (uses git config core.hooksPath)
		// For jujutsu colocated repos, use simplified hooks (no staging needed)
		hooksExist := hooksInstalled()
		if !skipHooks && (!hooksExist || hooksNeedUpdate()) {
			if hooksExist && !quiet {
				fmt.Printf("  Updating hooks to version %s...\n", Version)
			}
			isJJ := git.IsJujutsuRepo()
			isColocated := git.IsColocatedJJGit()

			if isJJ && !isColocated {
				// Pure jujutsu repo (no git) - print alias instructions
				if !quiet {
					printJJAliasInstructions()
				}
			} else if isColocated {
				// Colocated jj+git repo - use simplified hooks
				if err := installJJHooks(); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to install jj hooks: %v\n", ui.RenderWarn("⚠"), err)
					fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", ui.RenderAccent("bd doctor --fix"))
				} else if !quiet {
					fmt.Printf("  Hooks installed (jujutsu mode - no staging)\n")
				}
			} else if isGitRepo() {
				// Regular git repo - install hooks to .beads/hooks/
				if err := installHooksWithOptions(managedHookNames, false, false, false, true); err != nil && !quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to install git hooks to .beads/hooks/: %v\n", ui.RenderWarn("⚠"), err)
					fmt.Fprintf(os.Stderr, "You can try again with: %s\n\n", ui.RenderAccent("bd hooks install --beads"))
				} else if !quiet {
					fmt.Printf("  Hooks installed to: .beads/hooks/\n")
				}
			}
		}

		// Initialize version tracking: create .local_version file during bd init
		// instead of deferring it to the first bd command.
		// This ensures no "Version Tracking" warning from bd doctor after init.
		if useLocalBeads {
			localVersionPath := filepath.Join(beadsDir, ".local_version")
			if err := writeLocalVersion(localVersionPath, Version); err != nil && !quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to initialize version tracking: %v\n", err)
				// Non-fatal - initialization still succeeded
			}
		}

		// Add agent instructions to AGENTS.md (or custom filename via --agents-file)
		// Skip in stealth mode (user wants invisible setup) or when explicitly skipped
		if !stealth && !skipAgents {
			agentsTemplate, _ := cmd.Flags().GetString("agents-template")
			agentsProfileStr, _ := cmd.Flags().GetString("agents-profile")
			agentsProfile := agents.Profile(agentsProfileStr)
			agentsFile, _ := cmd.Flags().GetString("agents-file")

			// Validate and persist custom agents filename
			if agentsFile != "" {
				if err := config.ValidateAgentsFile(agentsFile); err != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid --agents-file: %v\n", err)
					return
				}
				if err := config.SetYamlConfig("agents.file", agentsFile); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to persist agents.file to config: %v\n", err)
				}
			}

			// Use flag value directly if provided (honoring user intent even if
			// config persistence failed), otherwise read from config/default.
			resolvedAgentsFile := agentsFile
			if resolvedAgentsFile == "" {
				resolvedAgentsFile = config.SafeAgentsFile()
			}
			if isBareGitRepo() {
				if !quiet {
					fmt.Printf("  Skipping %s generation in bare repository\n", resolvedAgentsFile)
				}
			} else {
				renderOpts := agents.RenderOpts{HasRemote: shouldConfigureInitDoltRemote(syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin, isDoltLocalOnly()), NoPush: config.GetBool("no-push")}
				addAgentsInstructions(resolvedAgentsFile, !quiet, agentsTemplate, agentsProfile, renderOpts)
			}
		}

		// Auto-setup Claude hooks, Codex, and Cursor project integration. Skip in
		// stealth mode or when agents are skipped.
		if !stealth && !skipAgents && !isBareGitRepo() {
			if err := setup.InstallClaudeProject(stealth); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to setup Claude hooks: %v\n", err)
				}
				// Non-fatal - continue with init
			}
			if err := setup.InstallCodexProject(); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to setup Codex integration: %v\n", err)
				}
				// Non-fatal - continue with init
			}
			if err := setup.InstallCursorProject(); err != nil {
				if !quiet {
					fmt.Fprintf(os.Stderr, "Warning: failed to setup Cursor integration: %v\n", err)
				}
				// Non-fatal - continue with init
			}
		}

		// Auto-stage and commit beads files so bd doctor doesn't warn about
		// untracked files or dirty working tree in a clean room setup.
		// Only runs when not stealth, in a git repo, and using local storage.
		if !stealth && isGitRepo() && useLocalBeads {
			gitAddCmd := exec.Command("git", "add", ".beads/")
			if _, addErr := gitAddCmd.CombinedOutput(); addErr == nil {
				// Also stage the agents file if it exists
				agentsFileToStage := config.SafeAgentsFile()
				if _, statErr := os.Stat(agentsFileToStage); statErr == nil {
					agentsCmd := exec.Command("git", "add", agentsFileToStage)
					_ = agentsCmd.Run()
				}
				// Also stage Claude settings if created by init
				claudeSettingsPath := filepath.Join(".claude", "settings.json")
				if _, statErr := os.Stat(claudeSettingsPath); statErr == nil {
					claudeCmd := exec.Command("git", "add", claudeSettingsPath)
					_ = claudeCmd.Run()
				}
				// Also stage CLAUDE.md if created by setup
				if _, statErr := os.Stat("CLAUDE.md"); statErr == nil {
					claudeMdCmd := exec.Command("git", "add", "CLAUDE.md")
					_ = claudeMdCmd.Run()
				}
				// Also stage Codex and Cursor project integration files if created
				// by setup. Cursor project hooks/rules are meant to be committed
				// (a no-op git add if .cursor/ is gitignored in this repo).
				for _, path := range []string{".agents", ".codex", ".cursor"} {
					if _, statErr := os.Stat(path); statErr == nil {
						codexCmd := exec.Command("git", "add", path)
						_ = codexCmd.Run()
					}
				}
				// Also stage .gitignore if modified by EnsureProjectGitignore
				if _, statErr := os.Stat(".gitignore"); statErr == nil {
					giCmd := exec.Command("git", "add", ".gitignore")
					_ = giCmd.Run()
				}
				// Hooks installed by this init can call back into bd. Skip all
				// of them for the bootstrap commit to avoid self-deadlocking
				// while init still owns the embedded Dolt lock. --no-verify
				// alone does not skip prepare-commit-msg.
				commitArgs := []string{"-c", "core.hooksPath=", "commit", "--no-verify", "-m", "bd init: initialize beads issue tracking"}
				commitCmd := exec.Command("git", commitArgs...)
				if commitOut, commitErr := commitCmd.CombinedOutput(); commitErr != nil {
					if !quiet && !strings.Contains(string(commitOut), "nothing to commit") {
						fmt.Fprintf(os.Stderr, "Warning: failed to commit beads files: %v\n", commitErr)
					}
				} else if !quiet {
					fmt.Printf("  %s Committed beads files to git\n", ui.RenderPass("✓"))
				}
				// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
				// directory — including noms/LOCK files. These are Dolt-internal files.
				// Removing them WILL cause unrecoverable data corruption and data loss.
				// Dolt manages these files itself; external interference is never safe.
			}
		}

		// Check for missing git upstream and warn if not configured.
		// Only warn when remotes exist (has origin but no upstream).
		// Skip for brand-new repos with no remotes — the warning is noise there.
		if isGitRepo() && !quiet {
			if gitHasAnyRemotes() && !gitHasUpstream() {
				fmt.Fprintf(os.Stderr, "\n%s Git upstream not configured\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  For sync workflows, set your upstream with:\n")
				fmt.Fprintf(os.Stderr, "  %s\n\n", ui.RenderAccent("git remote add upstream <repo-url>"))
			}
			if !stealth && !initRemoteChanged && !shouldWireInitRemote(syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin) {
				printInitNoDoltRemoteWarning(true)
			}
		}

		// Skip output if quiet mode
		if quiet {
			return
		}

		if bootstrappedFromRemote {
			fmt.Printf("\n%s bd initialized from git remote!\n\n", ui.RenderPass("✓"))
		} else {
			fmt.Printf("\n%s bd initialized successfully!\n\n", ui.RenderPass("✓"))
		}
		fmt.Printf("  Backend: %s\n", ui.RenderAccent(backend))
		if !usesSQLServer() {
			fmt.Printf("  Mode: %s\n", ui.RenderAccent("embedded"))
		} else {
			host := serverHost
			if host == "" {
				host = configfile.DefaultDoltServerHost
			}
			port := serverPort
			if port == 0 {
				port = doltserver.DefaultConfig(beadsDir).Port
			}
			user := serverUser
			if user == "" {
				user = configfile.DefaultDoltServerUser
			}
			fmt.Printf("  Mode: %s\n", ui.RenderAccent("server"))
			fmt.Printf("  Server: %s\n", ui.RenderAccent(fmt.Sprintf("%s@%s:%d", user, host, port)))
			// Warn when using the default localhost — this is the #1 misconfiguration
			// for setups where Dolt runs on a remote machine (e.g., over Tailscale).
			if serverHost == "" && os.Getenv("BEADS_DOLT_SERVER_HOST") == "" {
				fmt.Fprintf(os.Stderr, "\n  %s Server host defaulted to %s.\n", ui.RenderWarn("⚠"), configfile.DefaultDoltServerHost)
				fmt.Fprintf(os.Stderr, "    If your Dolt server is remote, set BEADS_DOLT_SERVER_HOST or pass --server-host.\n")
			}
		}
		// Advertise the prefix that issue IDs will actually use. When the database
		// already carries a provisioned issue_prefix — gateway adoption of a
		// server-provisioned value, or a shared database another rig already
		// configured — that value, not the caller-derived local prefix, is what
		// appears in IDs, so the summary must show it instead of a prefix that
		// will never be minted.
		effectivePrefix := prefix
		if existing != "" {
			effectivePrefix = existing
		}
		fmt.Printf("  Database: %s\n", ui.RenderAccent(dbName))
		fmt.Printf("  Issue prefix: %s\n", ui.RenderAccent(effectivePrefix))
		fmt.Printf("  Issues will be named: %s\n\n", ui.RenderAccent(effectivePrefix+"-<hash> (e.g., "+effectivePrefix+"-a3f2dd)"))
		fmt.Printf("Run %s to get started.\n\n", ui.RenderAccent("bd quickstart"))

		// Detect backup files from a previous session (GH#2327).
		// This catches the branch-switch scenario: user ran bd init on a new
		// branch and the database was created fresh, but backup JSONL files
		// exist from a prior backup on this or another branch.
		if !bootstrappedFromRemote && dolt.HasBackupFiles(beadsDir) {
			fmt.Printf("  %s Backup files detected in .beads/backup/\n", ui.RenderWarn("!"))
			fmt.Printf("    To restore issues from a previous backup, run:\n")
			fmt.Printf("      %s\n\n", ui.RenderAccent("bd backup restore"))
		}

		// Run limited diagnostics to verify init succeeded.
		// Skipped in embedded mode: diagnostics use dolt.NewFromConfigWithOptions
		// which auto-starts a dolt sql-server. Embedded init already validates
		// the database via initSchema.
		if usesSQLServer() {
			doctorResult := runInitDiagnostics(cwd)
			hasIssues := false
			for _, check := range doctorResult.Checks {
				if check.Status != statusOK {
					hasIssues = true
					break
				}
			}
			if hasIssues {
				fmt.Printf("%s Setup incomplete. Some issues were detected:\n", ui.RenderWarn("⚠"))
				for _, check := range doctorResult.Checks {
					if check.Status != statusOK {
						fmt.Printf("  • %s: %s\n", check.Name, check.Message)
					}
				}
				fmt.Printf("\nRun %s to see details and fix these issues.\n\n", ui.RenderAccent("bd doctor --fix"))
			}
		}
		return nil
	},
}

func init() {
	initCmd.Flags().StringP("prefix", "p", "", "Issue prefix (default: current directory name)")
	initCmd.Flags().BoolP("quiet", "q", false, "Suppress output (quiet mode)")
	initCmd.Flags().Bool("contributor", false, "Run OSS contributor setup wizard")
	initCmd.Flags().Bool("team", false, "Run team workflow setup wizard")
	initCmd.Flags().Bool("stealth", false, "Enable stealth mode: global gitattributes and gitignore, no local repo tracking")
	initCmd.Flags().Bool("setup-exclude", false, "Configure .git/info/exclude to keep beads files local (for forks)")
	initCmd.Flags().Bool("skip-hooks", false, "Skip git hooks installation")
	initCmd.Flags().Bool("skip-agents", false, "Skip AGENTS.md and Claude/Codex/Cursor setup generation")
	initCmd.Flags().Bool("force", false, "Deprecated alias for --reinit-local. Bypasses only the LOCAL data-safety guard; does NOT authorize remote divergence (see 'bd help init-safety').")
	initCmd.Flags().Bool("reinit-local", false, "Re-initialize local .beads/ over existing local data. Does NOT authorize remote divergence; see --discard-remote.")
	initCmd.Flags().Bool("discard-remote", false, "Authorize discarding the configured remote's Dolt history when re-initializing. Requires --destroy-token in non-interactive mode; see 'bd help init-safety'.")
	initCmd.Flags().Bool("from-jsonl", false, "Import issues from configured import.path; refuses remote history unless --discard-remote authorizes replacement")
	initCmd.Flags().Bool("init-if-missing", false, "If the workspace is already initialized, skip init and exit 0 instead of failing (idempotent init for scaffolds)")
	initCmd.Flags().String("destroy-token", "", "Explicit confirmation token for destructive re-init in non-interactive mode (format: 'DESTROY-<prefix>')")
	initCmd.Flags().String("agents-template", "", "Path to custom AGENTS.md template (overrides embedded default)")
	initCmd.Flags().String("agents-profile", "", "AGENTS.md profile: 'minimal' (default, pointer to bd prime) or 'full' (complete command reference)")
	initCmd.Flags().String("agents-file", "", "Custom filename for agent instructions (default: AGENTS.md)")
	initCmd.Flags().String("remote", "", "Dolt remote URL to clone from and persist as sync.remote")

	// Non-interactive mode for CI/cloud agents
	initCmd.Flags().Bool("non-interactive", false, "Skip all interactive prompts (auto-detected in CI or non-TTY environments)")
	initCmd.Flags().String("role", "", "Set beads role without prompting: \"maintainer\" or \"contributor\"")

	// Backend selection: Dolt is the default and only supported backend.
	initCmd.Flags().String("backend", "", "Storage backend: dolt (default). Removed backends (postgres, mysql, sqlite) print migration guidance.")
	// Keep the short-lived removed-backend flags as hidden parser tombstones. A
	// legacy invocation can then reach the backend rollback guidance instead of
	// failing early with an opaque "unknown flag" error. The current backend rejects
	// these flags explicitly above; they are never consumed as connection data.
	for _, legacyFlag := range removedBackendInitFlags {
		initCmd.Flags().String(legacyFlag.name, "", legacyFlag.usage)
		_ = initCmd.Flags().MarkHidden(legacyFlag.name)
	}

	// Dolt server connection flags
	initCmd.Flags().Bool("server", false, "Use external dolt sql-server instead of embedded engine")
	initCmd.Flags().String("server-host", "", "Dolt server host (default: 127.0.0.1)")
	initCmd.Flags().Int("server-port", 0, "Dolt server port (default: 3307)")
	initCmd.Flags().String("server-socket", "", "Unix domain socket path (overrides host/port)")
	initCmd.Flags().String("server-user", "", "Dolt server MySQL user (default: root)")
	initCmd.Flags().String("database", "", "Use existing server database name (overrides prefix-based naming)")
	initCmd.Flags().Bool("shared-server", false, "Enable shared Dolt server mode (all projects share one server at ~/.beads/shared-server/)")
	initCmd.Flags().Bool("external", false, "Server is externally managed (skip server startup); use with --shared-server or --server")
	initCmd.Flags().Bool("debug", false, "Run the managed Dolt sql-server with --loglevel=debug and CPU profiling (--prof cpu). Persisted to config.yaml as dolt.debug. No effect on externally-managed servers.")
	initCmd.Flags().Bool("proxied-server", false, "[EXPERIMENTAL] Use a per-workspace proxied dolt sql-server (proxy + child dolt) rooted at .beads/dolt")
	initCmd.Flags().String("proxied-server-config-path", "", "[EXPERIMENTAL] Absolute path to an existing dolt sql-server YAML config (proxied-server mode only). When set, bd uses this file instead of auto-generating one. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-log-path", "", "[EXPERIMENTAL] Absolute path to the proxied dolt sql-server log file (proxied-server mode only). Default: <beadsDir>/dolt/server.log. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-root-path", "", "[EXPERIMENTAL] Absolute directory holding the proxied dolt sql-server's lockfiles, pidfiles, and child .dolt repository (proxied-server mode only). Default: <beadsDir>/dolt. May not exist yet — bd will create it. Relative paths are rejected.")
	initCmd.Flags().Int("proxied-server-port", 0, "[EXPERIMENTAL] Fixed TCP port for the proxy's loopback listener (proxied-server mode only). Default 0 = an OS-assigned free port. Startup fails if the port is already in use.")
	initCmd.Flags().Duration("proxied-server-idle-timeout", 0, "[EXPERIMENTAL] Idle duration after which the proxy shuts down its loopback listener and backend (proxied-server mode only). Omit for the built-in default (30s); 0 keeps the proxy and backend alive indefinitely; a positive value sets the window.")
	initCmd.Flags().String("proxied-server-external-host", "", "[EXPERIMENTAL] Hostname or IP of an externally-managed dolt sql-server the proxy should front (proxied-server mode only). Mutually exclusive with --proxied-server-external-socket-path.")
	initCmd.Flags().Int("proxied-server-external-port", 0, "[EXPERIMENTAL] TCP port of the externally-managed dolt sql-server (proxied-server mode only). Required when --proxied-server-external-host is set.")
	initCmd.Flags().String("proxied-server-external-socket-path", "", "[EXPERIMENTAL] Absolute unix socket path of the externally-managed dolt sql-server (proxied-server mode only). Mutually exclusive with --proxied-server-external-host. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-external-user", "", "[EXPERIMENTAL] MySQL user for the externally-managed dolt sql-server (proxied-server mode only). Defaults to \"root\" when empty. Password is read at runtime from $BEADS_PROXIED_SERVER_EXTERNAL_PASSWORD and is never persisted to disk.")
	initCmd.Flags().Bool("proxied-server-external-tls", false, "[EXPERIMENTAL] Require TLS when connecting to the externally-managed dolt sql-server (proxied-server mode only).")
	initCmd.Flags().String("proxied-server-external-tls-ca-cert-path", "", "[EXPERIMENTAL] Absolute path to a CA certificate (PEM) used to verify the externally-managed dolt sql-server. Empty uses the system trust store. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-external-tls-cert-path", "", "[EXPERIMENTAL] Absolute path to a client TLS certificate (for mTLS to the externally-managed dolt sql-server). Must be paired with --proxied-server-external-tls-key-path. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-external-tls-key-path", "", "[EXPERIMENTAL] Absolute path to the client TLS private key (for mTLS to the externally-managed dolt sql-server). Must be paired with --proxied-server-external-tls-cert-path. Relative paths are rejected.")
	initCmd.Flags().String("proxied-server-external-tls-server-name", "", "[EXPERIMENTAL] Server name to verify in the external dolt sql-server's TLS certificate. Defaults to the external host. Required with a unix socket unless --proxied-server-external-tls-skip-verify is set.")
	initCmd.Flags().Bool("proxied-server-external-tls-skip-verify", false, "[EXPERIMENTAL] Skip TLS certificate verification for the external dolt sql-server. Insecure; testing only.")
	initCmd.Flags().Duration("proxied-server-external-keep-alive", 0, "[EXPERIMENTAL] TCP keepalive period for the proxy→external connection. Zero uses the package default (30s).")

	rootCmd.AddCommand(initCmd)
}

var removedBackendInitFlags = []struct {
	name      string
	usage     string
	origin    string
	rationale string
}{
	{name: "pg-url", usage: "Legacy PostgreSQL connection URL (removed backend compatibility only)", origin: "the removed PostgreSQL/MySQL initialization paths", rationale: configfile.RemovedBackendRationale},
	{name: "pg-schema", usage: "Legacy PostgreSQL schema (removed backend compatibility only)", origin: "the removed PostgreSQL/MySQL initialization paths", rationale: configfile.RemovedBackendRationale},
	{name: "mysql-url", usage: "Legacy MySQL connection URL (removed backend compatibility only)", origin: "the removed PostgreSQL/MySQL initialization paths", rationale: configfile.RemovedBackendRationale},
	{name: "mysql-database", usage: "Legacy MySQL database (removed backend compatibility only)", origin: "the removed PostgreSQL/MySQL initialization paths", rationale: configfile.RemovedBackendRationale},
	{name: "sqlite-path", usage: "Legacy SQLite database file (removed backend compatibility only)", origin: "the removed SQLite initialization path", rationale: configfile.RemovedSQLiteRationale},
}

// migrateOldDatabases detects and migrates old database files to beads.db
func migrateOldDatabases(targetPath string, quiet bool) error {
	targetDir := filepath.Dir(targetPath)
	targetName := filepath.Base(targetPath)

	// If target already exists, no migration needed
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	}

	// Create .beads directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return fmt.Errorf("failed to create .beads directory: %w", err)
	}

	// Look for existing .db files in the .beads directory
	pattern := filepath.Join(targetDir, "*.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to search for existing databases: %w", err)
	}

	// Filter out the target file name and any backup files
	var oldDBs []string
	for _, match := range matches {
		baseName := filepath.Base(match)
		if baseName != targetName && !strings.HasSuffix(baseName, ".backup.db") {
			oldDBs = append(oldDBs, match)
		}
	}

	if len(oldDBs) == 0 {
		// No old databases to migrate
		return nil
	}

	if len(oldDBs) > 1 {
		// Multiple databases found - ambiguous, require manual intervention
		return fmt.Errorf("multiple database files found in %s: %v\nPlease manually rename the correct database to %s and remove others",
			targetDir, oldDBs, targetName)
	}

	// Migrate the single old database
	oldDB := oldDBs[0]
	if !quiet {
		fmt.Fprintf(os.Stderr, "→ Migrating database: %s → %s\n", filepath.Base(oldDB), targetName)
	}

	// Rename the old database to the new canonical name
	if err := os.Rename(oldDB, targetPath); err != nil {
		return fmt.Errorf("failed to migrate database %s to %s: %w", oldDB, targetPath, err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "✓ Database migration complete\n\n")
	}

	return nil
}

// errWorkspaceAlreadyInitialized marks the benign "this workspace already has a
// database" outcome from checkExistingBeadsData. --init-if-missing treats only
// this case as an idempotent skip; any other error from the check is operational
// (e.g. an unreadable .beads/embeddeddolt directory) and must still abort rather
// than be silently masked as success.
var errWorkspaceAlreadyInitialized = errors.New("workspace already initialized")

// workspaceExistsError carries a user-facing "already initialized" message while
// still matching errWorkspaceAlreadyInitialized via errors.Is, so callers can
// distinguish the benign case without the sentinel text leaking into the message.
type workspaceExistsError struct{ msg string }

func (e *workspaceExistsError) Error() string { return e.msg }
func (e *workspaceExistsError) Is(target error) bool {
	return target == errWorkspaceAlreadyInitialized
}

// alreadyInitialized builds a workspaceExistsError from a formatted message.
func alreadyInitialized(format string, args ...any) error {
	return &workspaceExistsError{msg: fmt.Sprintf(format, args...)}
}

// checkExistingBeadsDataAt checks for existing database at a specific beadsDir path.
// This is extracted to support both BEADS_DIR and CWD-based resolution.
//
// A returned error that matches errWorkspaceAlreadyInitialized means a database
// already exists (the benign, idempotent-skip case); any other error is
// operational and must not be treated as success.

func checkExistingBeadsDataAt(beadsDir string, prefix string) error {
	// Check if .beads directory exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil // No .beads directory, safe to init
	}

	// metadata.json is authoritative for the configured backend, so resolve it once
	// and dispatch. Removed-backend tombstones are marked by metadata
	// alone — there is no local Dolt directory to inspect — so a plain
	// `bd init` (which defaults to Dolt) must not silently repoint a live SQL
	// workspace to a fresh embedded Dolt DB and orphan its issues. --reinit-local
	// /--force bypass this (handled by the caller). Invalid metadata must fail closed:
	// without an explicit reinitialization request, init may not overwrite the only
	// marker for an external or otherwise nonlocal database.
	cfg, cfgErr := configfile.Load(beadsDir)
	if cfgErr != nil {
		return fmt.Errorf("failed to load %s: %w; refusing to reinitialize automatically (restore the metadata or use --reinit-local after safeguarding existing data)", configfile.ConfigPath(beadsDir), cfgErr)
	}
	if cfg != nil {
		if guardErr := validateConfiguredBackend(cfg); guardErr != nil {
			return guardErr
		}
	}

	if cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		if cfg.IsDoltProxiedServerMode() {
			proxiedRoot, rootErr := resolveProxiedServerRootPath(beadsDir)
			if rootErr != nil {
				return fmt.Errorf("resolve proxied server root: %w", rootErr)
			}
			if info, statErr := os.Stat(proxiedRoot); statErr == nil && info.IsDir() {
				return alreadyInitialized(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

Aborting.`, ui.RenderWarn("⚠"), proxiedRoot, ui.RenderAccent("bd list"))
			}
			return nil
		}

		// Embedded mode stores databases under `.beads/embeddeddolt/<db>/`.
		// Use the target workspace metadata rather than ambient process state so
		// init guards remain deterministic even when another test or earlier
		// command has rebound global server-mode state.
		if !cfg.IsDoltServerMode() {
			embeddedRoot := filepath.Join(beadsDir, "embeddeddolt")
			entries, err := os.ReadDir(embeddedRoot)
			if err != nil {
				if os.IsNotExist(err) {
					return nil // No embedded root -> fresh clone, safe to init
				}
				return fmt.Errorf("failed to read embedded dolt directory %s: %w", embeddedRoot, err)
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				if info, statErr := os.Stat(filepath.Join(embeddedRoot, entry.Name(), ".dolt")); statErr == nil && info.IsDir() {
					location := filepath.Join(embeddedRoot, entry.Name())
					return alreadyInitialized(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

If the database is genuinely corrupt and unrecoverable:
  bd export > issue-export.jsonl        # Export issue records first
  bd init --reinit-local --prefix %s    # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), location, ui.RenderAccent("bd list"), prefix)
				}
			}
			return nil
		}

		// Check both the local directory AND server mode config.
		// In server mode the local dolt/ directory may be empty — the database
		// lives on the Dolt sql-server. Checking only the directory would miss
		// server-mode installations.
		doltPath := doltserver.ResolveDoltDir(beadsDir)
		doltDirExists := false
		if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
			doltDirExists = true
		}
		if doltDirExists || cfg.IsDoltServerMode() {
			// For server mode, distinguish "DB exists" from "DB missing" (FR-010).
			if cfg.IsDoltServerMode() && !doltDirExists {
				host := cfg.GetDoltServerHost()
				port := doltserver.DefaultConfig(beadsDir).Port
				dbName := cfg.GetDoltDatabase()
				password := cfg.GetDoltServerPassword()
				user := cfg.GetDoltServerUser()

				result := checkDatabaseOnServer(host, port, user, password, dbName, cfg.GetDoltServerTLS())
				if result.Reachable && !result.Exists && result.Err == nil {
					// Server is up but DB doesn't exist. Since we also know
					// doltDirExists==false, this is a fresh clone — there's no
					// local database to protect. Allow init to proceed so the
					// user can bootstrap (e.g. via --from-jsonl). (GH#2433)
					return nil
				}
				if result.Reachable && result.Exists {
					// Server up and DB exists — fall through to "already initialized" error.
				} else {
					// Server unreachable or error during check: this is a fresh clone
					// with committed metadata.json but no local dolt/ directory.
					// Allow init to proceed so the user can bootstrap the database
					// (e.g. via --from-jsonl). (GH#2433)
					return nil
				}
			}

			location := doltPath
			if cfg.IsDoltServerMode() {
				host := cfg.GetDoltServerHost()
				port := doltserver.DefaultConfig(beadsDir).Port
				location = fmt.Sprintf("dolt server at %s:%d", host, port)
			}
			return alreadyInitialized(`
%s Found existing Dolt database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

If the database is genuinely corrupt and unrecoverable:
  bd export > issue-export.jsonl        # Export issue records first
  bd init --reinit-local --prefix %s    # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), location, ui.RenderAccent("bd list"), prefix)
		}
		// Backend is Dolt but no dolt directory exists yet — this is a fresh
		// clone. Any beads.db file is a legacy SQLite artifact, not the active
		// database. Skip the SQLite checks below and allow init to proceed.
		return nil
	}

	// Check for redirect file - if present, check the redirect target
	redirectTarget := beads.FollowRedirect(beadsDir)
	if redirectTarget != beadsDir {
		targetDBPath := filepath.Join(redirectTarget, beads.CanonicalDatabaseName)
		if _, err := os.Stat(targetDBPath); err == nil {
			return alreadyInitialized(`
%s Cannot init: redirect target already has database

Local .beads redirects to: %s
That location already has: %s

The redirect target is already initialized. Running init here would overwrite it.

To use the existing database:
  Just run bd commands normally (e.g., %s)
  The redirect will route to the canonical database.

If the database is genuinely corrupt and unrecoverable:
  bd export > issue-export.jsonl        # Export issue records first
  bd init --reinit-local --prefix %s    # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), redirectTarget, targetDBPath, ui.RenderAccent("bd list"), prefix)
		}
		return nil // Redirect target has no database - safe to init
	}

	// Check for existing database file (no redirect case)
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); err == nil {
		return alreadyInitialized(`
%s Found existing database: %s

This workspace is already initialized.

To use the existing database:
  Just run bd commands normally (e.g., %s)

If the database is genuinely corrupt and unrecoverable:
  bd export > issue-export.jsonl        # Export issue records first
  bd init --reinit-local --prefix %s    # Then reinitialize

Aborting.`, ui.RenderWarn("⚠"), dbPath, ui.RenderAccent("bd list"), prefix)
	}

	return nil // No database found, safe to init
}

// countExistingIssues attempts to connect to the existing database and count
// issues. Returns 0 if the database is unreachable or empty. Used by --force
// safeguard to show users what they're about to destroy.
func countExistingIssues(_ string) (int, error) {
	var beadsDir string
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		beadsDir = utils.CanonicalizePath(envBeadsDir)
	} else {
		beadsDir = beads.FindBeadsDir()
		if beadsDir == "" {
			return 0, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := newDoltStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = store.Close() }()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return 0, err
	}
	if stats == nil {
		return 0, nil
	}
	return stats.TotalIssues, nil
}

// checkExistingBeadsData checks for existing database files
// and returns an error if found (safety guard for bd-emg)
//
// Note: This only blocks when a database already exists (workspace is initialized).
// Fresh clones without a database are allowed — init will create the database.
//
// For worktrees, checks the main repository root instead of current directory
// since worktrees should share the database with the main repository.
//
// For redirects, checks the redirect target and errors if it already has a database.
// This prevents accidentally overwriting an existing canonical database (GH#bd-0qel).
// normalizeIssuePrefix applies the prefix normalization rules shared by the
// init path and the --init-if-missing mismatch guard: strip leading dots, drop
// the trailing hyphen, convert dots to underscores, and prepend "bd_" when the
// result would not start with a SQL-identifier-safe character. Keeping this in
// one place ensures the mismatch check derives exactly the same name init does.
func normalizeIssuePrefix(prefix string) string {
	prefix = strings.TrimLeft(prefix, ".")
	prefix = strings.TrimRight(prefix, "-")
	prefix = strings.ReplaceAll(prefix, ".", "_")
	if len(prefix) > 0 && !((prefix[0] >= 'a' && prefix[0] <= 'z') || (prefix[0] >= 'A' && prefix[0] <= 'Z') || prefix[0] == '_') {
		prefix = "bd_" + prefix
	}
	return prefix
}

// dbNameFromPrefix derives the Dolt database name from a normalized issue
// prefix (hyphens become underscores), matching how init records DoltDatabase.
func dbNameFromPrefix(prefix string) string {
	return strings.ReplaceAll(prefix, "-", "_")
}

// initIfMissingPrefixMismatch reports whether an explicit --prefix request
// conflicts with the existing workspace, in which case --init-if-missing must
// abort rather than silently skip (otherwise the requested prefix is ignored).
// existingDBName is the existing workspace's recorded Dolt database name;
// requestedPrefix is the raw, un-normalized --prefix value. Returns false when
// the existing name is unknown so an undeterminable state falls through to the
// benign skip.
func initIfMissingPrefixMismatch(existingDBName, requestedPrefix string) bool {
	if existingDBName == "" {
		return false
	}
	requested := dbNameFromPrefix(normalizeIssuePrefix(requestedPrefix))
	return requested != "" && !strings.EqualFold(existingDBName, requested)
}

// initIfMissingDatabaseMismatch reports whether an explicit --database request
// conflicts with the existing workspace. --database is the authoritative database
// selector (it overrides prefix-based naming later in init), so an explicit
// mismatch must abort --init-if-missing rather than silently reuse a different
// database. existingDBName is the existing workspace's recorded Dolt database
// name; requestedDatabase is the raw --database value. Returns false when either
// is unknown so an undeterminable state falls through to the benign skip.
func initIfMissingDatabaseMismatch(existingDBName, requestedDatabase string) bool {
	if existingDBName == "" || requestedDatabase == "" {
		return false
	}
	return !strings.EqualFold(existingDBName, requestedDatabase)
}

// resolveInitBeadsDir resolves the .beads directory that init would target,
// using the same precedence as checkExistingBeadsData (BEADS_DIR > worktree
// fallback > CWD). Returns "" when it cannot be determined.
func resolveInitBeadsDir() string {
	if envBeadsDir := os.Getenv("BEADS_DIR"); envBeadsDir != "" {
		return utils.CanonicalizePath(envBeadsDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if isGitRepo() && git.IsWorktree() {
		return beads.GetWorktreeFallbackBeadsDir()
	}
	return filepath.Join(cwd, ".beads")
}

// existingWorkspaceDBName returns the Dolt database name explicitly recorded
// for the already-initialized workspace at the init target, or "" if it cannot
// be determined. It reads the raw DoltDatabase field rather than
// GetDoltDatabase() on purpose: the getter falls back to a default name (and an
// env override), which would manufacture a phantom value and trigger false
// mismatches. An unset value safely falls through to the benign skip.
func existingWorkspaceDBName() string {
	beadsDir := resolveInitBeadsDir()
	if beadsDir == "" {
		return ""
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.DoltDatabase
}

func checkExistingBeadsData(prefix string) error {
	beadsDir := resolveInitBeadsDir()
	if beadsDir == "" {
		return nil // Can't determine target, allow init to proceed
	}
	return checkExistingBeadsDataAt(beadsDir, prefix)
}

// isNonInteractiveInit returns true if init should run without interactive prompts.
// Precedence: explicit flag > BD_NON_INTERACTIVE env > CI env > terminal detection.
// Setting BD_NON_INTERACTIVE=0 or BD_NON_INTERACTIVE=false explicitly forces
// interactive mode, overriding CI detection and terminal checks.
func isNonInteractiveInit(flagValue bool) bool {
	if flagValue {
		return true
	}
	if v := os.Getenv("BD_NON_INTERACTIVE"); v != "" {
		if v == "1" || v == "true" {
			return true
		}
		// Explicit BD_NON_INTERACTIVE=0/false forces interactive mode,
		// overriding CI and terminal detection.
		return false
	}
	if v := os.Getenv("CI"); v == "true" || v == "1" {
		return true
	}
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// shouldPromptForRole returns true if we should prompt the user for their role.
// Skips prompt in non-interactive contexts (CI, scripts, piped input).
func shouldPromptForRole() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// getBeadsRole reads the beads.role git config value.
// Returns the role and true if configured, or empty string and false if not set.
func getBeadsRole() (string, bool) {
	cmd := exec.Command("git", "config", "--get", "beads.role")
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	role := strings.TrimSpace(string(output))
	if role == "" {
		return "", false
	}
	return role, true
}

// setBeadsRole writes the beads.role git config value.
func setBeadsRole(role string) error {
	cmd := exec.Command("git", "config", "beads.role", role)
	return cmd.Run()
}

// promptContributorMode prompts the user to determine if they are a contributor.
// Returns true if the user indicates they are a contributor, false otherwise.
//
// Behavior:
// - If beads.role is already set: shows current role, offers to change
// - If not set: prompts "Contributing to someone else's repo? [y/N]"
// - Sets git config beads.role based on answer
func promptContributorMode() (isContributor bool, err error) {
	ctx := getRootContext()
	reader := bufio.NewReader(os.Stdin)

	// Check if role is already configured
	existingRole, hasRole := getBeadsRole()
	if hasRole {
		fmt.Printf("\n%s Already configured as: %s\n", ui.RenderAccent("▶"), ui.RenderBold(existingRole))
		fmt.Print("Change role? [y/N]: ")

		response, err := readLineWithContext(ctx, reader, os.Stdin)
		if err != nil {
			return false, fmt.Errorf("failed to read input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))

		if response != "y" && response != "yes" {
			// Keep existing role
			return existingRole == "contributor", nil
		}
		// Fall through to re-prompt
		fmt.Println()
	}

	// Prompt for role
	fmt.Print("Contributing to someone else's repo? [y/N]: ")

	response, err := readLineWithContext(ctx, reader, os.Stdin)
	if err != nil {
		return false, fmt.Errorf("failed to read input: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))

	isContributor = response == "y" || response == "yes"

	// Set the role in git config
	role := "maintainer"
	if isContributor {
		role = "contributor"
	}

	if err := setBeadsRole(role); err != nil {
		return isContributor, fmt.Errorf("failed to set beads.role config: %w", err)
	}

	return isContributor, nil
}

// promptAutoExport asks the user whether to enable optional auto-export.
// Returns true to enable it, false to leave it disabled.
func promptAutoExport() (bool, error) {
	fmt.Printf("\n%s Auto-export can keep .beads/issues.jsonl up to date after write commands.\n", ui.RenderAccent("▶"))
	fmt.Println("  This optional JSONL export is useful for viewers (bv), interchange, and issue-level migration.")
	fmt.Println("  Dolt remotes/backups handle cross-machine sync and backup.")
	fmt.Print("\nEnable auto-export? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	response, err := readLineWithContext(getRootContext(), reader, os.Stdin)
	if err != nil {
		if isCanceled(err) {
			return true, err
		}
		response = ""
	}
	response = strings.TrimSpace(strings.ToLower(response))

	// Default to no. Users and integrations can enable it explicitly.
	return response == "y" || response == "yes", nil
}

func shouldWireInitRemote(syncURL string, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin bool) bool {
	// A git origin is a valid Dolt remote even before refs/dolt/data exists:
	// the first `bd dolt push` creates the Dolt ref on that git remote. We
	// still keep explicit empty --remote as an opt-out because that leaves
	// syncURL empty and syncURLFromGitOrigin false.
	return syncURL != "" && (syncFromRemote || syncURLFromConfig || syncURLFromGitOrigin)
}

func shouldConfigureInitDoltRemote(syncURL string, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin, localOnly bool) bool {
	return !localOnly && shouldWireInitRemote(syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin)
}

// shouldWriteInitDoltRemote reports whether init should actually configure (write)
// the Dolt "origin" remote on the store. It layers gateway suppression on top of
// shouldConfigureInitDoltRemote: a gateway is a passive client of a server-owned
// database, so AddRemote's CALL DOLT_REMOTE('add', ...) would mutate shared remote
// state on a writable credential (or merely warn per-init on a read-only one) —
// exactly the server-owned write the other gateway gates (shouldInitSharedGlobalDB,
// shouldWriteInitStateToDB, shouldWriteProjectIDLocally) already suppress. The
// rendered agent-instruction HasRemote flag intentionally keeps using the raw
// shouldConfigureInitDoltRemote decision, since the git remote still exists for
// documentation even when gateway sync does not use dolt push.
func shouldWriteInitDoltRemote(gateway bool, syncURL string, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin, localOnly bool) bool {
	return !gateway && shouldConfigureInitDoltRemote(syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin, localOnly)
}

// handleRemoteSafetyDecision applies a CheckRemoteSafety decision at an init
// remote-divergence checkpoint. It returns (bootstrap, err): bootstrap is true
// when the caller should clone/bootstrap from the remote, and err is a non-nil
// *exitError when init must abort with a specific exit code.
//
// It must NOT call os.Exit: this runs inside the init RunE body, whose deferred
// metrics CloseEventAndAdd only fires on a normal return. Every refusal path
// returns an *exitError so the caller can propagate it up through RunE and keep
// the usage-metrics close intact (see errors.go).
func handleRemoteSafetyDecision(decision RemoteSafetyDecision, prefix, syncURL, destroyToken string, remoteHasDoltData func() bool, observedRemoteHasDoltData bool, confirmed *bool) (bool, error) {
	switch decision.Action {
	case ActionRefuseDivergence, ActionRequireDestroyToken:
		fmt.Fprintf(os.Stderr, "\n%s\n\n", decision.UserMessage)
		return false, &exitError{Code: decision.ExitCode}
	case ActionBootstrap:
		return true, nil
	case ActionProceedWithDivergence:
		if decision.Reason == "authorized-divergence" && term.IsTerminal(int(os.Stdin.Fd())) && destroyToken == "" && !*confirmed {
			expected := FormatDestroyToken(prefix)
			fmt.Fprintf(os.Stderr, "\n%s You are about to discard the remote's Dolt history.\n\n", ui.RenderWarn("WARNING:"))
			fmt.Fprintf(os.Stderr, "  Remote: %s\n", syncURL)
			fmt.Fprintf(os.Stderr, "  Type %q to confirm: ", expected)
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Scan()
			if strings.TrimSpace(scanner.Text()) != expected {
				fmt.Fprintf(os.Stderr, "\nAborted. See 'bd help init-safety' for details.\n")
				return false, &exitError{Code: ExitDestroyTokenMissing}
			}
			*confirmed = true
		}
		if remoteHasDoltData != nil && remoteHasDoltData() != observedRemoteHasDoltData {
			fmt.Fprintf(os.Stderr, "\nAborted: remote state changed during confirmation. Re-run to re-verify intent.\n")
			return false, &exitError{Code: ExitRemoteDivergenceRefused}
		}
	}
	return false, nil
}

func configureInitDoltRemote(ctx context.Context, store storage.DoltStorage, syncURL string, quiet bool) {
	hasRemote, _ := store.HasRemote(ctx, "origin")
	if hasRemote {
		return
	}
	if err := store.AddRemote(ctx, "origin", syncURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to add remote 'origin': %v\n", err)
		return
	}
	if !quiet {
		fmt.Printf("  %s Configured Dolt remote: origin → %s\n", ui.RenderPass("✓"), syncURL)
	}
}

func printInitNoDoltRemoteWarning(withExportNote bool) {
	fmt.Fprintf(os.Stderr, "\n%s No Dolt remote configured\n", ui.RenderWarn("⚠"))
	if withExportNote {
		fmt.Fprintln(os.Stderr, "  Issues are stored in local Dolt. .beads/issues.jsonl is an export,")
		fmt.Fprintln(os.Stderr, "  not cross-machine sync or the source of truth.")
	}
	if originURL, err := gitOriginGetURL(); err == nil && originURL != "" {
		fmt.Fprintln(os.Stderr, "  To use your git origin for durable sync, run:")
		fmt.Fprintf(os.Stderr, "    %s\n", ui.RenderAccent("bd dolt remote add origin "+normalizeRemoteURL(originURL)))
		fmt.Fprintf(os.Stderr, "    %s\n\n", ui.RenderAccent("bd dolt push"))
		return
	}
	fmt.Fprintln(os.Stderr, "  To enable durable sync, add a git origin and then run:")
	fmt.Fprintf(os.Stderr, "    %s\n", ui.RenderAccent("bd dolt push"))
}

type initSyncRemoteSource int

const (
	initSyncRemoteNone initSyncRemoteSource = iota
	initSyncRemoteExplicit
	initSyncRemoteConfigured
)

func resolveInitConfiguredSyncRemote(initRemote string, initRemoteChanged bool, resolveConfiguredRemote func() string) (string, initSyncRemoteSource) {
	if initRemoteChanged {
		return initRemote, initSyncRemoteExplicit
	}
	if syncURL := resolveConfiguredRemote(); syncURL != "" {
		return syncURL, initSyncRemoteConfigured
	}
	return "", initSyncRemoteNone
}

func initRemoteCloneMode(initServerMode, externalServer bool) remoteCloneMode {
	if !initServerMode {
		return remoteCloneEmbedded
	}
	if externalServer {
		return remoteCloneExternalServer
	}
	return remoteCloneCLI
}

func initDoltServerTLSFromEnv() bool {
	return (&configfile.Config{}).GetDoltServerTLS()
}

func initTimeCloneConfig(serverMode bool, serverHost string, serverPort int, serverSocket, serverUser, dbName string) *configfile.Config {
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = dbName
	if serverMode {
		cfg.DoltMode = configfile.DoltModeServer
		cfg.DoltServerHost = configfile.DefaultDoltServerHost
		cfg.DoltServerUser = configfile.DefaultDoltServerUser
	} else {
		cfg.DoltMode = configfile.DoltModeEmbedded
	}
	if serverHost != "" {
		cfg.DoltServerHost = serverHost
	}
	if serverPort != 0 {
		cfg.DoltServerPort = serverPort
	}
	if serverSocket != "" {
		cfg.DoltServerSocket = serverSocket
	}
	if serverUser != "" {
		cfg.DoltServerUser = serverUser
	}
	return cfg
}

func persistInitSyncRemote(beadsDir, initRemote, syncURL string, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin bool) error {
	if initRemote != "" {
		return config.SetYamlConfigInDir(beadsDir, "sync.remote", initRemote)
	}
	if !shouldWireInitRemote(syncURL, syncFromRemote, syncURLFromConfig, syncURLFromGitOrigin) {
		return nil
	}
	if existing := config.GetStringFromDir(beadsDir, "sync.remote"); existing != "" {
		return nil
	}
	return config.SetYamlConfigInDir(beadsDir, "sync.remote", syncURL)
}

func isEmptyRemoteCloneError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "contains no dolt data")
}

// verifyMetadata writes a metadata field and verifies the write succeeded.
// Returns true if write+verify succeeded, false with warning if either failed.
func verifyMetadata(ctx context.Context, store storage.DoltStorage, key, value string) bool {
	if err := store.SetMetadata(ctx, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write %s metadata: %v\n", key, err)
		if usesSQLServer() {
			fmt.Fprintf(os.Stderr, "  Run 'bd doctor --fix' to repair.\n")
		}
		return false
	}
	// Verify read-back
	readBack, err := store.GetMetadata(ctx, key)
	if err != nil || readBack != value {
		fmt.Fprintf(os.Stderr, "Warning: %s metadata write did not persist (wrote %q, read %q)\n", key, value, readBack)
		if usesSQLServer() {
			fmt.Fprintf(os.Stderr, "  Run 'bd doctor --fix' to repair.\n")
		}
		return false
	}
	return true
}

// initGlobalDatabaseConfig opens a store connection to the beads_global database
// and seeds its configuration (issue prefix, project ID). The database must already
// exist (created by EnsureGlobalDatabase). This function is idempotent — it only
// sets config values that are not already present.
func initGlobalDatabaseConfig(ctx context.Context, projectCfg *dolt.Config, quiet bool) {
	globalCfg := &dolt.Config{
		Path:            projectCfg.Path,
		BeadsDir:        projectCfg.BeadsDir,
		Database:        doltserver.GlobalDatabaseName,
		ServerHost:      projectCfg.ServerHost,
		ServerPort:      projectCfg.ServerPort,
		ServerUser:      projectCfg.ServerUser,
		ServerPassword:  projectCfg.ServerPassword,
		ServerMode:      true,
		CreateIfMissing: true,
		AutoStart:       false, // server is already running
	}

	globalStore, err := newDoltStore(ctx, globalCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to open global database: %v\n", err)
		return
	}
	defer func() { _ = globalStore.Close() }()

	// Set issue prefix (only if not already configured)
	existing, _ := globalStore.GetConfig(ctx, "issue_prefix")
	if existing == "" {
		if err := globalStore.SetConfig(ctx, "issue_prefix", doltserver.GlobalIssuePrefix); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set global issue prefix: %v\n", err)
		}
	}

	// Set well-known project ID for the global database
	existingID, _ := globalStore.GetMetadata(ctx, "_project_id")
	if existingID == "" {
		if err := globalStore.SetMetadata(ctx, "_project_id", doltserver.GlobalProjectID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to set global project ID: %v\n", err)
		}
	}

	if !quiet {
		fmt.Printf("  %s Global database schema initialized\n", ui.RenderPass("✓"))
	}
}

func resolveInitDoltMode(proxiedFlag, sharedFlag, serverFlag bool) string {
	if proxiedFlag || os.Getenv("BEADS_DOLT_PROXIED_SERVER") == "1" {
		return "proxied-server"
	}
	shared := os.Getenv("BEADS_DOLT_SHARED_SERVER")
	if sharedFlag || shared == "1" || strings.EqualFold(shared, "true") {
		return "shared-server"
	}
	if serverFlag || os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
		return "server"
	}
	return "embedded"
}
