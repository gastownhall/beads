package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/subosito/gotenv"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/molecules"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var (
	changeDir   string
	dbPath      string
	actor       string
	store       storage.DoltStorage
	uowProvider uow.UnitOfWorkProvider
	jsonOutput  bool

	// Signal-aware context for graceful cancellation
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// Hook runner for extensibility
	hookRunner *hooks.Runner

	// Store concurrency protection
	storeMutex  sync.Mutex // Protects store access from background goroutine
	storeActive = false    // Tracks if store is available

	// Version upgrade tracking
	versionUpgradeDetected = false // Set to true if bd version changed since last run
	previousVersion        = ""    // The last bd version user had (empty = first run or unknown)
	upgradeAcknowledged    = false // Set to true after showing upgrade notification once per session
)

type envSnapshotValue struct {
	value string
	ok    bool
}

var changeDirEnvSnapshot map[string]envSnapshotValue

var (
	noColorFlag       bool
	sandboxMode       bool
	globalFlag        bool
	serverMode        bool
	proxiedServerMode bool
	readonlyMode      bool               // Read-only mode: block write operations (for worker sandboxes)
	storeIsReadOnly   bool               // Track if store was opened read-only (for staleness checks)
	ignoreSchemaSkew  bool               // Proceed despite forward schema drift
	lockTimeout       = 30 * time.Second // Dolt open timeout (fixed default)
	profileEnabled    bool
	profileFile       *os.File
	traceFile         *os.File
	memProfilePath    string
	verboseFlag       bool // Enable verbose/debug output
	quietFlag         bool // Suppress non-essential output

	// Dolt auto-commit policy (flag/config). Values: off | on
	doltAutoCommit string

	// commandDidWrite is set when a command performs a write that should trigger
	// auto-flush. Used to decide whether to auto-commit Dolt after the command completes.
	// Thread-safe via atomic.Bool to avoid data races in concurrent flush operations.
	commandDidWrite atomic.Bool

	// commandMayEmptyJSONLExport is set by destructive maintenance commands
	// after they actually delete rows, allowing post-run auto-export to record
	// an intentional empty JSONL artifact instead of treating it as ambiguous.
	commandMayEmptyJSONLExport atomic.Bool

	// commandDidExplicitDoltCommit is set when a command already created a Dolt commit
	// explicitly (e.g., bd sync in dolt-native mode, hook flows, bd vc commit).
	// This prevents a redundant auto-commit attempt in PersistentPostRun.
	commandDidExplicitDoltCommit bool

	// commandDidWriteTipMetadata is set when a command records a tip as "shown" by writing
	// metadata (tip_*_last_shown). This will be used to create a separate Dolt commit for
	// tip writes, even when the main command is read-only.
	commandDidWriteTipMetadata bool

	// commandTipIDsShown tracks which tip IDs were shown in this command (deduped).
	// This is used for tip-commit message formatting.
	commandTipIDsShown map[string]struct{}

	// commandSpan is the root OTel span for the current command execution.
	// All storage and AI spans are nested as children of this span.
	commandSpan oteltrace.Span
)

// readOnlyCommands lists commands that only read from the database.
// These commands open the store in read-only mode. See GH#804.
var readOnlyCommands = map[string]bool{
	"list":       true,
	"ready":      true,
	"show":       true,
	"stats":      true,
	"blocked":    true,
	"count":      true,
	"search":     true,
	"query":      true,
	"graph":      true,
	"duplicates": true,
	"comments":   true, // list comments (not add)
	"current":    true, // bd sync mode current
	"ping":       true,
	"backup":     true, // reads from Dolt, writes only to .beads/backup/
	"export":     true, // reads from Dolt, writes JSONL to file/stdout
}

// isReadOnlyCommand returns true if the command only reads from the database.
// This is used to open the store in read-only mode, preventing file modifications
// that would trigger file watchers. See GH#804.
func isReadOnlyCommand(cmdName string) bool {
	return readOnlyCommands[cmdName]
}

// isWorkingSetReconcileCommand reports whether cmd's whole purpose is to
// reconcile the Dolt working set: "bd dolt commit" or "bd vc commit". These
// commands are the documented recovery from a pending-migration dirty-table
// refusal, but they also open the store, and an open runs the migration -
// hitting that same refusal before the commit that would clear the dirty
// state ever runs. Opening leniently (embeddeddolt.OpenForWorkingSetReconcile)
// breaks that deadlock by skipping the migration instead of failing the open
// (gastownhall/beads#4566).
func isWorkingSetReconcileCommand(cmd *cobra.Command) bool {
	if cmd.Name() != "commit" {
		return false
	}
	parent := cmd.Parent()
	if parent == nil {
		return false
	}
	return parent.Name() == "dolt" || parent.Name() == "vc"
}

// isForcedMigrate reports whether cmd is `bd migrate` or `bd migrate schema`
// invoked with --force: the operator confirming they are the single designated
// migrator, so the remote-migrate gate (#4259) must not block this run's store
// opens. Consulted in the root PersistentPreRunE because the gate fires during
// store open (and during autoMigrateOnVersionBump), long before the migrate
// command's own RunE.
func isForcedMigrate(cmd *cobra.Command) bool {
	if cmd != migrateCmd && cmd != migrateSchemaCmd {
		return false
	}
	force, _ := cmd.Flags().GetBool("force")
	return force
}

// forcedMigratePreviewFlag returns the name of a preview flag (--dry-run,
// --inspect) that conflicts with --force on a forced migrate invocation, or ""
// when there is no conflict. The combination must be rejected BEFORE the store
// opens: with the gate override set, the open itself applies pending schema
// migrations, so the preview flag would be honored only after the destructive
// work it exists to prevent had already happened.
func forcedMigratePreviewFlag(cmd *cobra.Command) string {
	for _, name := range []string{"dry-run", "inspect"} {
		if v, err := cmd.Flags().GetBool(name); err == nil && v {
			return name
		}
	}
	return ""
}

// applyNoColorFlag disables colorized output when --no-color is set.
// Complements the NO_COLOR / CLICOLOR=0 env detection in package ui,
// giving callers a per-invocation override.
func applyNoColorFlag() {
	if noColorFlag {
		ui.DisableColors()
	}
}

// loadBeadsEnvFile loads .beads/.env into process environment for per-project
// Dolt credentials (GH#2520). Uses gotenv.Load which is non-overriding —
// existing shell env vars always take precedence.
// Safe to call with an empty beadsDir (no-op).
func loadBeadsEnvFile(beadsDir string) {
	if beadsDir == "" {
		return
	}
	envFile := filepath.Join(beadsDir, ".env")
	if _, err := os.Stat(envFile); err != nil {
		return
	}
	_ = gotenv.Load(envFile)
}

func logConfigDiscovery(beadsDir, reason string) {
	metadataPath := filepath.Join(beadsDir, configfile.ConfigFileName)
	configYAMLPath := filepath.Join(beadsDir, "config.yaml")
	_, metadataErr := os.Stat(metadataPath)
	_, yamlErr := os.Stat(configYAMLPath)
	debug.Logf("Debug: %s at %s -> metadata=%v (%v), config.yaml=%v (%v)\n",
		reason, beadsDir, metadataErr == nil, metadataErr, yamlErr == nil, yamlErr)
}

func shouldLogDefaultDoltDatabase(cfg *configfile.Config) bool {
	return cfg != nil && cfg.DoltDatabase == "" && os.Getenv("BEADS_DOLT_SERVER_DATABASE") == ""
}

// loadBeadsSelectionEnvFile loads only the selector keys needed for early
// workspace/database discovery. Unlike loadBeadsEnvFile, this intentionally
// limits itself to BEADS_DIR / BEADS_DB / BD_DB so caller credentials and
// runtime knobs do not leak into explicit-target commands before rebinding.
func loadBeadsSelectionEnvFile(beadsDir string) {
	if beadsDir == "" {
		return
	}
	envFile := filepath.Join(beadsDir, ".env")
	pairs, err := gotenv.Read(envFile)
	if err != nil {
		return
	}
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		if os.Getenv(key) != "" {
			continue
		}
		if value, ok := pairs[key]; ok && strings.TrimSpace(value) != "" {
			_ = os.Setenv(key, value)
		}
	}
}

// loadSelectionEnvironment loads only the selector keys required to discover
// the target workspace/database before the store-init path runs. This preserves
// historical support for .beads/.env files that route commands via BEADS_DB or
// BEADS_DIR without importing the caller workspace's broader runtime settings.
func loadSelectionEnvironment() {
	if os.Getenv("BEADS_DIR") != "" || os.Getenv("BEADS_DB") != "" || os.Getenv("BD_DB") != "" {
		return
	}
	if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
		loadBeadsSelectionEnvFile(beadsDir)
	}
}

// loadEnvironment runs the lightweight, always-needed environment setup that
// must happen before the noDbCommands early return. This ensures commands like
// "bd doctor --server" pick up per-project Dolt credentials from .beads/.env.
//
// This function intentionally does NOT do any store initialization, auto-migrate,
// or telemetry setup — those belong in the store-init phase that runs after the
// noDbCommands check.
func loadEnvironment() {
	// FindBeadsDir is lightweight (filesystem walk, no git subprocesses)
	// and resolves BEADS_DIR, redirects, and worktree paths.
	if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
		loadBeadsEnvFile(beadsDir)
		// Non-fatal warning if .beads/ directory has overly permissive access.
		config.CheckBeadsDirPermissions(beadsDir)
	}
}

var sharedServerEmbeddedMismatchWarned bool

// warnSharedServerEmbeddedMismatch detects the case where shared-server mode
// is active but metadata.json explicitly pins dolt_mode=embedded. The
// shared-server setting wins for this invocation (GH#2946/2949: stale embedded
// metadata must not hide server-backed issue state), but bd never rewrites the
// committed metadata.json — per-machine environment must not leak into shared
// config (bd-6dnrw.5). Print guidance so the user resolves the conflict
// explicitly.
func warnSharedServerEmbeddedMismatch(cfg *configfile.Config) {
	if cfg == nil || sharedServerEmbeddedMismatchWarned {
		return
	}
	if strings.ToLower(strings.TrimSpace(cfg.DoltMode)) != configfile.DoltModeEmbedded {
		return
	}
	if !doltserver.IsSharedServerMode() {
		return
	}
	sharedServerEmbeddedMismatchWarned = true
	fmt.Fprintln(os.Stderr, "Notice: shared-server mode is enabled (BEADS_DOLT_SHARED_SERVER or dolt.shared-server in config.yaml) but .beads/metadata.json pins dolt_mode=\"embedded\". Using the shared server for this run.")
	fmt.Fprintln(os.Stderr, "  To persist server mode: set dolt_mode to \"server\" in .beads/metadata.json and commit it.")
	fmt.Fprintln(os.Stderr, "  To stay embedded: unset BEADS_DOLT_SHARED_SERVER (or remove dolt.shared-server from config.yaml).")
}

// loadServerModeFromBeadsDir loads the storage mode (embedded vs server vs
// proxied-server) from the given beads directory's metadata.json so that
// usesSQLServer() and usesProxiedServer() return the correct values.
//
// A metadata.json that exists but cannot be loaded is a hard error: treating
// it like an absent file silently flips server-mode deployments onto the
// embedded store, where every query answers from an empty relic with exit 0
// (false-empty). Absent metadata.json (cfg == nil) keeps the fresh-repo
// embedded default.
func loadServerModeFromBeadsDir(beadsDir string) error {
	if beadsDir == "" {
		return nil
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("load %s: %w (storage mode unknown; data commands will refuse to run rather than fall back to the embedded store)", configfile.ConfigPath(beadsDir), err)
	}
	if cfg == nil {
		return nil
	}
	warnSharedServerEmbeddedMismatch(cfg)
	psm := cfg.IsDoltProxiedServerMode()
	sm := cfg.IsDoltServerMode()
	// GH#2946: shared-server override for stale metadata.json (no-db commands)
	if !sm && !psm && doltserver.IsSharedServerMode() {
		sm = true
	}
	serverMode = sm
	proxiedServerMode = psm
	if cmdCtx != nil {
		cmdCtx.ServerMode = sm
		cmdCtx.ProxiedServerMode = psm
	}
	return nil
}

// loadServerModeFromConfig loads the storage mode (embedded vs server vs
// proxied-server) from metadata.json so that usesSQLServer() and
// usesProxiedServer() return the correct values. Called for commands that
// skip full DB init but still need to know the mode.
func loadServerModeFromConfig() error {
	return loadServerModeFromBeadsDir(beads.FindBeadsDir())
}

func preserveRedirectSourceDatabase(beadsDir string) {
	if beadsDir == "" || os.Getenv("BEADS_DOLT_SERVER_DATABASE") != "" {
		return
	}

	rInfo := beads.ResolveRedirect(beadsDir)
	if rInfo.WasRedirected && rInfo.SourceDatabase != "" {
		_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", rInfo.SourceDatabase)
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] Preserved source dolt_database %q across redirect\n", rInfo.SourceDatabase)
		}
	}
}

func selectedNoDBBeadsDir(cmd *cobra.Command) string {
	if cmd != nil && cmd.Root() != nil && cmd.Root().PersistentFlags().Changed("db") && dbPath != "" {
		if selectedBeadsDir := resolveCommandBeadsDir(dbPath); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	} else if cmd != nil && cmd.PersistentFlags().Changed("db") && dbPath != "" {
		if selectedBeadsDir := resolveCommandBeadsDir(dbPath); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	} else if envDB := os.Getenv("BEADS_DB"); envDB != "" {
		if selectedBeadsDir := resolveCommandBeadsDir(envDB); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	} else if envDB := os.Getenv("BD_DB"); envDB != "" {
		if selectedBeadsDir := resolveCommandBeadsDir(envDB); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	}
	if os.Getenv("BEADS_DIR") != "" {
		if selectedBeadsDir := beads.FindBeadsDir(); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	}
	if dbPath != "" {
		if selectedBeadsDir := resolveCommandBeadsDir(dbPath); selectedBeadsDir != "" {
			return selectedBeadsDir
		}
	}
	return beads.FindBeadsDir()
}

func isSelectedNoDBCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Name() == "context" || cmd.Name() == "where" {
		return true
	}
	if cmd.Parent() == nil || cmd.Parent().Name() != "dolt" {
		return false
	}
	switch cmd.Name() {
	case "push", "pull", "commit":
		return false
	default:
		return true
	}
}

// configCommandCanRunWithoutStore returns true for config subcommands whose Run
// path can execute without an opened Dolt store. This lets no-workspace calls
// fail or degrade in the command itself instead of tripping low-level DB init.
func configCommandCanRunWithoutStore(cmd *cobra.Command, args []string) bool {
	if cmd == nil || cmd.Parent() == nil || cmd.Parent().Name() != "config" {
		return false
	}

	switch cmd.Name() {
	case "show", "validate", "drift", "apply":
		return true
	case "set", "get", "unset":
		if len(args) == 0 {
			return true
		}
		key := args[0]
		return config.IsYamlOnlyKey(key) || key == "beads.role"
	case "set-many":
		if len(args) == 0 {
			return true
		}
		for _, arg := range args {
			key, _, ok := strings.Cut(arg, "=")
			if !ok || key == "" {
				return true
			}
			if !config.IsYamlOnlyKey(key) && key != "beads.role" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func prepareSelectedCommandContext(beadsDir string, loadEnv bool) {
	if beadsDir == "" {
		return
	}
	_ = os.Setenv("BEADS_DIR", beadsDir)
	if loadEnv {
		loadBeadsEnvFile(beadsDir)
	}
	preserveRedirectSourceDatabase(beadsDir)
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to reinitialize config for selected beads dir: %v\n", err)
	}
	config.CheckBeadsDirPermissions(beadsDir)
	if err := loadServerModeFromBeadsDir(beadsDir); err != nil {
		// Warn, don't fatal: this context also serves no-DB commands —
		// doctor, init, bootstrap, config — which are exactly the repair
		// paths for a corrupt metadata.json. Data commands stay protected
		// by the hard error at store init and in the store factories.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
}

func prepareSelectedNoDBContext(beadsDir string) {
	prepareSelectedCommandContext(beadsDir, true)
}

// refreshBoundCommandConfig reapplies config-backed defaults after the command
// context has been rebound to a resolved target beads directory. This keeps
// explicit flags authoritative while letting rerouted/explicit-db commands use
// the target repo's config rather than the caller's config.
func refreshBoundCommandConfig(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	root := cmd.Root()
	if root == nil {
		root = cmd
	}
	if !root.PersistentFlags().Changed("json") && !root.PersistentFlags().Changed("format") {
		jsonOutput = config.GetBool("json")
	}
	if !root.PersistentFlags().Changed("readonly") {
		readonlyMode = config.GetBool("readonly")
	}
	if !root.PersistentFlags().Changed("actor") {
		actor = config.GetString("actor")
	}
	if !root.PersistentFlags().Changed("dolt-auto-commit") {
		doltAutoCommit = config.GetString("dolt.auto-commit")
	}
}

// resolveCommandBeadsDir maps a discovered Dolt data path back to the owning
// .beads directory. filepath.Dir(dbPath) only works when the Dolt data lives
// under .beads/dolt; custom dolt_data_dir values can place it elsewhere.
func resolveCommandBeadsDir(dbPath string) string {
	if dbPath == "" {
		return ""
	}

	// Use the same validated candidate logic as the helper/reopen path
	// (GH#2627). This checks filepath.Dir, canonicalized paths, AND
	// FindBeadsDir — but only returns a candidate whose metadata.json
	// actually points to dbPath, preventing CWD discovery from overriding
	// an explicit --db flag.
	if beadsDir := resolveBeadsDirForDBPath(dbPath); beadsDir != "" {
		return beadsDir
	}

	for dir := filepath.Dir(dbPath); dir != "" && dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, ".beads")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}

	// No candidate matched — fall back to parent directory of the db path.
	// This handles bootstrap/init where no metadata.json exists yet.
	return filepath.Dir(dbPath)
}

// getActorWithGit returns the actor for audit trails with git config fallback.
// Priority: --actor flag > BEADS_ACTOR env > BD_ACTOR env (deprecated) > git config user.name > $USER > "unknown"
// This provides a sensible default for developers: their git identity is used unless
// explicitly overridden
func getActorWithGit() string {
	// If actor is already set (from --actor flag), use it
	if actor != "" {
		return actor
	}

	// Check BEADS_ACTOR env var (primary env override)
	if beadsActor := os.Getenv("BEADS_ACTOR"); beadsActor != "" {
		return beadsActor
	}

	// Check BD_ACTOR env var (deprecated alias, kept for backwards compatibility)
	if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
		return bdActor
	}

	// Try git config user.name - the natural default for a git-native tool
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if gitUser := strings.TrimSpace(string(out)); gitUser != "" {
			return gitUser
		}
	}

	// Fall back to system username
	if user := os.Getenv("USER"); user != "" {
		return user
	}

	return "unknown"
}

// getOwner returns the human owner for CV attribution.
// Priority: GIT_AUTHOR_EMAIL env > git config user.email > "" (empty)
// This is the foundation for HOP CV (curriculum vitae) chains per Decision 008.
// Unlike actor (which tracks who executed), owner tracks the human responsible.
func getOwner() string {
	// Check GIT_AUTHOR_EMAIL first - this is set during git commit operations
	if authorEmail := os.Getenv("GIT_AUTHOR_EMAIL"); authorEmail != "" {
		return authorEmail
	}

	// Fall back to git config user.email - the natural default
	if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
		if gitEmail := strings.TrimSpace(string(out)); gitEmail != "" {
			return gitEmail
		}
	}

	// Return empty if no email found (owner is optional)
	return ""
}

func init() {
	// Initialize viper configuration
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
	}

	// Register persistent flags
	rootCmd.PersistentFlags().StringVarP(&changeDir, "directory", "C", "", "Change to this directory before running the command (like git -C)")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Database path (default: auto-discover .beads/*.db)")
	rootCmd.PersistentFlags().StringVar(&actor, "actor", "", "Actor name for audit trail (default: $BEADS_ACTOR, git user.name, $USER)")
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.PersistentFlags().String("format", "", "Output format (json). Alias for --json")
	_ = rootCmd.PersistentFlags().MarkHidden("format") // Hidden alias for CLI ergonomics
	rootCmd.PersistentFlags().BoolVar(&sandboxMode, "sandbox", false, "Sandbox mode: disables Dolt auto-push")
	rootCmd.PersistentFlags().BoolVar(&readonlyMode, "readonly", false, "Read-only mode: block write operations (for worker sandboxes)")
	rootCmd.PersistentFlags().BoolVar(&globalFlag, "global", false, "Use the global shared-server database (beads_global)")
	rootCmd.PersistentFlags().StringVar(&doltAutoCommit, "dolt-auto-commit", "", "Dolt auto-commit policy (off|on|batch). 'on': commit after each write. 'batch': defer commits to bd dolt commit; uncommitted changes persist in the working set until then. SIGTERM/SIGHUP flush pending batch commits. Default: off. Override via config key dolt.auto-commit")
	rootCmd.PersistentFlags().BoolVar(&profileEnabled, "profile", false, "Generate CPU profile for performance analysis")
	rootCmd.PersistentFlags().StringVar(&memProfilePath, "mem-profile", "", "Write heap profile to FILE on exit (also respects BEADS_MEM_PROFILE)")
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Enable verbose/debug output")
	rootCmd.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false, "Suppress non-essential output (errors only)")
	rootCmd.PersistentFlags().BoolVar(&ignoreSchemaSkew, "ignore-schema-skew", false, "Proceed despite forward schema drift (some queries may fail)")
	rootCmd.PersistentFlags().BoolVar(&noColorFlag, "no-color", false, "Disable color output (also: NO_COLOR=1 or CLICOLOR=0)")

	// Add --version flag to root command (same behavior as version subcommand)
	rootCmd.Flags().BoolP("version", "V", false, "Print version information")

	// Command groups for organized help output (Tufte-inspired)
	rootCmd.AddGroup(&cobra.Group{ID: "issues", Title: "Working With Issues:"})
	rootCmd.AddGroup(&cobra.Group{ID: "views", Title: "Views & Reports:"})
	rootCmd.AddGroup(&cobra.Group{ID: "deps", Title: "Dependencies & Structure:"})
	rootCmd.AddGroup(&cobra.Group{ID: "sync", Title: "Sync & Data:"})
	rootCmd.AddGroup(&cobra.Group{ID: "setup", Title: "Setup & Configuration:"})
	// NOTE: Many maintenance commands (clean, cleanup, compact, validate, repair-deps)
	// should eventually be consolidated into 'bd doctor' and 'bd doctor --fix' to simplify
	// the user experience. The doctor command can detect issues and offer fixes interactively.
	rootCmd.AddGroup(&cobra.Group{ID: "maint", Title: "Maintenance:"})
	rootCmd.AddGroup(&cobra.Group{ID: "advanced", Title: "Integrations & Advanced:"})

	// Custom help function with semantic coloring (Tufte-inspired)
	// Note: Usage output (shown on errors) is not styled to avoid recursion issues
	rootCmd.SetHelpFunc(colorizedHelpFunc)
}

func resolveChangeDirBeadsDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve -C directory %q: %w", path, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot use -C directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cannot use -C directory %q: not a directory", path)
	}
	beadsDir := beads.FindBeadsDirFrom(absPath)
	if beadsDir == "" {
		return "", fmt.Errorf("cannot use -C directory %q: no beads project found", path)
	}
	return beadsDir, nil
}

func applyChangeDirSelection() error {
	if strings.TrimSpace(changeDir) == "" {
		return nil
	}
	beadsDir, err := resolveChangeDirBeadsDir(changeDir)
	if err != nil {
		return HandleError("%v", err)
	}
	changeDirEnvSnapshot = make(map[string]envSnapshotValue, 3)
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB"} {
		value, ok := os.LookupEnv(key)
		changeDirEnvSnapshot[key] = envSnapshotValue{value: value, ok: ok}
	}
	_ = os.Setenv("BEADS_DIR", beadsDir)
	return nil
}

func restoreChangeDirSelection() {
	if changeDirEnvSnapshot == nil {
		return
	}
	for key, snapshot := range changeDirEnvSnapshot {
		if snapshot.ok {
			_ = os.Setenv(key, snapshot.value)
		} else {
			_ = os.Unsetenv(key)
		}
	}
	changeDirEnvSnapshot = nil
}

var rootCmd = &cobra.Command{
	Use:   "bd",
	Short: "bd - Dependency-aware issue tracker",
	Long:  `Issues chained together like beads. A lightweight issue tracker with first-class dependency support.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Handle --version flag on root command
		if v, _ := cmd.Flags().GetBool("version"); v {
			fmt.Printf("bd version %s (%s)\n", Version, Build)
			return
		}
		// No subcommand - show help
		_ = cmd.Help() // Help() always returns nil for cobra commands
	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		applyNoColorFlag()

		// Initialize CommandContext to hold runtime state (replaces scattered globals)
		initCommandContext()

		// Reset per-command write tracking (used by Dolt auto-commit).
		commandDidWrite.Store(false)
		commandMayEmptyJSONLExport.Store(false)
		commandDidExplicitDoltCommit = false
		commandDidWriteTipMetadata = false
		commandTipIDsShown = make(map[string]struct{})

		// Set up signal-aware context with batch commit flush on shutdown.
		// Unlike signal.NotifyContext, this also handles SIGHUP and flushes
		// pending batch commits before canceling the context.
		rootCtx, rootCancel = setupGracefulShutdown()

		// Initialize OTel (no-op unless BD_OTEL_METRICS_URL or BD_OTEL_STDOUT=true).
		// Must run before any DB access so SQL spans nest under command spans.
		if err := telemetry.Init(rootCtx, "bd", Version); err != nil {
			debug.Logf("warning: telemetry init failed: %v", err)
		}

		// Materialize the user-level metrics config only when metrics are
		// actually enabled. When metrics are disabled (BD_DISABLE_METRICS or a
		// user-global metrics.disabled), there is nothing to bootstrap. The
		// send-metrics flusher is exempt so it never recurses into bootstrap.
		// This mirrors the resolveMetricsEnabled() gate on the first-run notice
		// below. (~/.config/bd/ lives outside the repo, so this write is not a
		// stealth/per-repository trace; stealth init is handled by suppressing
		// the first-run notice, not by skipping this user-global bootstrap.)
		if cmd.Name() != metrics.SendMetricsSubcommand && resolveMetricsEnabled() {
			if err := metrics.EnsureUserConfigDefaults(); err != nil {
				debug.Logf("warning: ensure user config defaults failed: %v", err)
			}
		}

		if _, err := metrics.Init(Version, resolveMetricsEnabled(), resolveMetricsEndpoint()); err != nil {
			debug.Logf("warning: metrics init failed: %v", err)
		}

		if cmd.Name() == metrics.SendMetricsSubcommand {
			return nil
		}

		// Start root span for this command. rootCtx now carries the span, so
		// all downstream DB and AI calls become child spans automatically.
		rootCtx, commandSpan = telemetry.Tracer("bd").Start(rootCtx, "bd.command."+cmd.Name(),
			oteltrace.WithAttributes(
				attribute.String("bd.command", cmd.Name()),
				attribute.String("bd.version", Version),
				attribute.String("bd.args", scrubArgsForTelemetry(os.Args[1:], secretFlagTokens(cmd))),
			),
		)

		// Apply verbosity flags early (before any output)
		debug.SetVerbose(verboseFlag)
		debug.SetQuiet(quietFlag)

		if err := applyChangeDirSelection(); err != nil {
			return err
		}

		// Block dangerous env var overrides that could cause data fragmentation (bd-hevyw).
		if err := checkBlockedEnvVars(); err != nil {
			return HandleError("%v", err)
		}

		loadSelectionEnvironment()

		// Apply viper configuration if flags weren't explicitly set
		// Priority: flags > viper (config file + env vars) > defaults
		// Do this BEFORE early-return so init/version/help respect config

		// Track flag overrides for notification (only in verbose mode)
		flagOverrides := make(map[string]struct {
			Value  interface{}
			WasSet bool
		})

		// Handle --format json alias (desire-path from GH#2612)
		if cmd.Root().PersistentFlags().Changed("format") {
			format, _ := cmd.Root().PersistentFlags().GetString("format")
			if strings.EqualFold(format, "json") {
				jsonOutput = true
			}
		}
		// If flag wasn't explicitly set, use viper value
		if !cmd.Root().PersistentFlags().Changed("json") && !cmd.Root().PersistentFlags().Changed("format") {
			jsonOutput = config.GetBool("json")
		} else {
			flagOverrides["json"] = struct {
				Value  interface{}
				WasSet bool
			}{jsonOutput, true}
		}
		if !cmd.Root().PersistentFlags().Changed("readonly") {
			readonlyMode = config.GetBool("readonly")
		} else {
			flagOverrides["readonly"] = struct {
				Value  interface{}
				WasSet bool
			}{readonlyMode, true}
		}
		if !cmd.Root().PersistentFlags().Changed("db") && dbPath == "" &&
			os.Getenv("BEADS_DB") == "" && os.Getenv("BD_DB") == "" && os.Getenv("BEADS_DIR") == "" {
			dbPath = config.GetString("db")
		} else if cmd.Root().PersistentFlags().Changed("db") {
			flagOverrides["db"] = struct {
				Value  interface{}
				WasSet bool
			}{dbPath, true}
		}
		if !cmd.Root().PersistentFlags().Changed("actor") && actor == "" {
			actor = config.GetString("actor")
		} else if cmd.Root().PersistentFlags().Changed("actor") {
			flagOverrides["actor"] = struct {
				Value  interface{}
				WasSet bool
			}{actor, true}
		}
		if !cmd.Root().PersistentFlags().Changed("dolt-auto-commit") && strings.TrimSpace(doltAutoCommit) == "" {
			doltAutoCommit = config.GetString("dolt.auto-commit")
		} else if cmd.Root().PersistentFlags().Changed("dolt-auto-commit") {
			flagOverrides["dolt-auto-commit"] = struct {
				Value  interface{}
				WasSet bool
			}{doltAutoCommit, true}
		}

		// --ignore-schema-skew sets BD_IGNORE_SCHEMA_SKEW so the env-var escape
		// hatch works uniformly for all store open paths (dolt, embedded).
		if ignoreSchemaSkew {
			_ = os.Setenv("BD_IGNORE_SCHEMA_SKEW", "1")
		}

		// Check for and log configuration overrides (only in verbose mode)
		if verboseFlag {
			overrides := config.CheckOverrides(flagOverrides)
			for _, override := range overrides {
				config.LogOverride(override)
			}
		}

		// GH#1093: Check noDbCommands BEFORE expensive operations
		// to avoid spawning git subprocesses for simple commands
		// like "bd version" that don't need database access.
		noDbCommands := []string{
			"__complete",       // Cobra's internal completion command (shell completions work without db)
			"__completeNoDesc", // Cobra's completion without descriptions (used by fish)
			"bash",
			"bootstrap",
			"completion",
			"context", // reads config files directly, does not need DB open
			"codex-hook",
			"cursor-hook", // shells out to `bd prime`; never opens the store itself
			"doctor",
			"dolt", // bare "bd dolt" shows help only; subcommands handled below
			"fish",
			"formula", // parser-only subcommands; add a store-needed guard before adding DB-backed formula subcommands
			"help",
			"hook", // manages its own store lifecycle (#1719)
			"hooks",
			"human",
			"init",
			"merge",
			"metrics", // config-only: status/on/off/example never touch the DB
			"onboard",
			"powershell",
			"prime",
			"quickstart",
			metrics.SendMetricsSubcommand,
			"setup",
			"version",
			"where",
			"zsh",
		}

		// GH#2042: Dolt subcommands that need the store for version-control operations.
		// All other dolt subcommands (show, set, test, start, stop, status) are
		// config/diagnostic commands that skip DB init via the "dolt" parent entry above.
		needsStoreDoltSubcommands := []string{"push", "pull", "commit"}

		// GH#2224: Dolt grandchild subcommands (e.g. "bd dolt remote add") whose
		// Cobra parent is "remote", not "dolt". These need the store but would be
		// silently skipped if "remote" were ever added to noDbCommands.
		needsStoreDoltGrandchildren := []string{"remote"}

		skipStoreMigrateSubcommands := []string{"from-server-to-proxied-server", "from-proxied-server-to-server", "from-shared-server-to-proxied-server", "from-proxied-server-to-shared-server"}

		// Check both the command name and parent command name for subcommands
		cmdName := cmd.Name()
		isSubcommand := cmd.Parent() != nil && cmd.Parent().Name() != "bd"
		skipsStoreInit := false
		if cmd.Parent() != nil {
			parentName := cmd.Parent().Name()
			if parentName == "dolt" && slices.Contains(needsStoreDoltSubcommands, cmdName) {
				// GH#2042: dolt push/pull/commit need the store — fall through to init
			} else if slices.Contains(needsStoreDoltGrandchildren, parentName) {
				// GH#2224: dolt remote add/list/remove need the store — fall through to init
			} else if parentName == "migrate" && slices.Contains(skipStoreMigrateSubcommands, cmdName) {
				skipsStoreInit = true
			} else if slices.Contains(noDbCommands, parentName) {
				skipsStoreInit = true
			}
		}
		// Only skip for top-level commands in noDbCommands, not subcommands
		// that happen to share names (e.g., "bd backup init" vs "bd init").
		if slices.Contains(noDbCommands, cmdName) && !isSubcommand {
			skipsStoreInit = true
		}

		// Skip for root command with no subcommand (just shows help)
		if cmd.Parent() == nil && cmdName == cmd.Use {
			skipsStoreInit = true
		}

		// Also skip for --version flag on root command (cmdName would be "bd")
		if v, _ := cmd.Flags().GetBool("version"); v {
			skipsStoreInit = true
		}

		// One-time friendly heads-up about anonymous usage metrics. Placed after
		// the config-derived json/quiet rebind and command classification above so
		// it can read the real output mode and command identity — that is how it
		// stays suppressed in JSON/hook/protocol/quiet/stealth contexts and never
		// corrupts machine-readable output. No-op after the first run.
		maybeShowMetricsFirstRunNotice(cmd)

		// Commands that skip store initialization still need early config/env
		// setup before they inspect server mode or per-project Dolt settings.
		// Rebind them to the selected workspace so explicit --db / BEADS_DB
		// targets behave consistently across doctor/bootstrap/context/dolt.
		if skipsStoreInit {
			prepareSelectedNoDBContext(selectedNoDBBeadsDir(cmd))
			refreshBoundCommandConfig(cmd)
			if beadsDir := os.Getenv("BEADS_DIR"); beadsDir == "" {
				loadEnvironment()
				if err := loadServerModeFromConfig(); err != nil {
					// Warn, don't fatal: skipsStoreInit commands (doctor,
					// init, bootstrap, version, ...) never select a store,
					// and several of them are the repair path for the very
					// corruption being reported.
					fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				}
			}
			if _, err := getDoltAutoCommitMode(); err != nil {
				return HandleError("%v", err)
			}
		}

		if skipsStoreInit {
			return nil
		}

		// Performance profiling setup
		if profileEnabled {
			timestamp := time.Now().Format("20060102-150405")
			if f, _ := os.Create(fmt.Sprintf("bd-profile-%s-%s.prof", cmd.Name(), timestamp)); f != nil {
				profileFile = f
				_ = pprof.StartCPUProfile(f) // Best effort: profiling is a debug tool, failure is non-fatal
			}
			if f, _ := os.Create(fmt.Sprintf("bd-trace-%s-%s.out", cmd.Name(), timestamp)); f != nil {
				traceFile = f
				_ = trace.Start(f) // Best effort: profiling is a debug tool, failure is non-fatal
			}
		}

		// Auto-detect sandboxed environment (Phase 2 for GH #353)
		if !cmd.Root().PersistentFlags().Changed("sandbox") {
			if isSandboxed() {
				sandboxMode = true
				fmt.Fprintf(os.Stderr, "ℹ️  Sandbox detected, using direct mode\n")
			}
		}

		// Capture redirect info BEFORE FindDatabasePath() follows the redirect.
		// When .beads/redirect points to a shared directory with a different
		// dolt_database, the source's database name would be lost. Capture it
		// early and set BEADS_DOLT_SERVER_DATABASE so all store opens use it.
		if dbPath == "" {
			preserveRedirectSourceDatabase(beads.GetRedirectInfo().LocalDir)
		}

		if dbPath == "" {
			if bd := beads.FindBeadsDir(); bd != "" {
				cfg, cfgErr := configfile.Load(bd)
				if cfgErr != nil || cfg != nil && (cfg.IsDoltProxiedServerMode() || !configfile.IsSupportedBackend(cfg.Backend)) {
					// Proxied-server and removed-backend workspaces may have no
					// local Dolt database file. Invalid or unknown metadata likewise must
					// reach config validation instead of becoming a generic "no database"
					// result. metadata.json identifies the workspace so store selection can
					// route or reject it explicitly.
					dbPath = bd
				}
			}
		}

		// Initialize database path
		if dbPath == "" {
			// Use public API to find database (same logic as extensions)
			if foundDB := beads.FindDatabasePath(); foundDB != "" {
				dbPath = foundDB
			} else {
				// No database found — allow some commands to run without a database
				// - import: auto-initializes database if missing
				// - setup: creates editor integration files (no DB needed)
				// - config subcommands that operate on config.yaml, git config,
				//   or best-effort diagnostics only (GH#536, bd-934, bd-omc, bd-3rw)
				if configCommandCanRunWithoutStore(cmd, args) {
					// When --db is provided, resolve BEADS_DIR so yaml-only
					// config writes target the correct directory (GH#3348).
					if dbPath != "" {
						if beadsDir := resolveCommandBeadsDir(dbPath); beadsDir != "" {
							prepareSelectedCommandContext(beadsDir, false)
						}
					}
					return nil
				}

				// GH#3686: `bd create --repo=<path-or-URL>` targets a different
				// repo's workspace. Without this, PreRun exits with "no beads
				// database found" before create.go's --repo handling runs, even
				// when the target has a valid workspace of its own. Resolve
				// local targets here so store initialization points at them.
				// Remote --repo URLs need no local database at all: create.go
				// opens the remote store itself via the remote cache and never
				// touches the local `store` global on that path (a gap left by
				// #4615, which only handled local paths), so skip local
				// discovery entirely instead of falling through to the "no
				// beads database found" exit below.
				if cmd.Name() == "create" && cmd.Flags().Changed("repo") {
					if repoVal, _ := cmd.Flags().GetString("repo"); repoVal != "" {
						if remotecache.IsRemoteURL(repoVal) {
							return nil
						}
						targetBeadsDir := filepath.Join(routing.ExpandPath(repoVal), ".beads")
						dbPath = utils.CanonicalizePath(filepath.Join(targetBeadsDir, beads.CanonicalDatabaseName))
					}
				}

				if dbPath == "" && cmd.Name() != "import" && cmd.Name() != "setup" {
					// No database found - provide context-aware error message
					fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
					fmt.Fprintf(os.Stderr, "Hint: %s\n", diagHint())
					fmt.Fprintf(os.Stderr, "      or set BEADS_DIR to point to your .beads directory\n")
					return SilentExit()
				}

				if dbPath == "" {
					// For import/setup commands, set default database path
					// Invariant: dbPath must always be absolute. Use CanonicalizePath for OS-agnostic
					// handling (symlinks, case normalization on macOS).
					//
					// IMPORTANT: Use FindBeadsDir() to get the correct .beads directory,
					// which follows redirect files. Without this, a redirected .beads
					// would create a local database instead of using the redirect target.
					// (GH#bd-0qel)
					targetBeadsDir := beads.FindBeadsDir()
					if targetBeadsDir == "" {
						targetBeadsDir = ".beads"
					}
					dbPath = utils.CanonicalizePath(filepath.Join(targetBeadsDir, beads.CanonicalDatabaseName))
				}
			}
		}

		beadsDir := resolveCommandBeadsDir(dbPath)
		prepareSelectedCommandContext(beadsDir, true)
		refreshBoundCommandConfig(cmd)
		if _, err := getDoltAutoCommitMode(); err != nil {
			return HandleError("%v", err)
		}

		// Resolve the backend before version tracking, migration, server startup, or
		// any store construction. PostgreSQL/MySQL values are retained as metadata
		// tombstones so an existing workspace fails closed instead of falling through
		// to a new, empty Dolt database.
		cfg, cfgErr := configfile.Load(beadsDir)
		if cfgErr != nil {
			return HandleError("failed to load beads config from %s: %v (refusing to fall back to the embedded store; fix or restore metadata.json and retry)", beadsDir, cfgErr)
		}
		if backendErr := validateConfiguredBackend(cfg); backendErr != nil {
			return HandleError("%v", backendErr)
		}

		// Set actor for audit trail
		actor = getActorWithGit()
		// Attach actor to the command span now that we have it.
		if commandSpan != nil {
			commandSpan.SetAttributes(attribute.String("bd.actor", actor))
		}

		// Track bd version changes
		// Best-effort tracking - failures are silent
		trackBdVersion()

		// Check if this is a read-only command (GH#804)
		// Read-only commands open the store in read-only mode to avoid modifying
		// the database (which breaks file watchers).
		useReadOnly := isReadOnlyCommand(cmd.Name())

		// If the operator passed --force on `bd migrate` or `bd migrate schema`,
		// set the programmatic gate override before both autoMigrateOnVersionBump
		// and the main store open — both open their own store connections and the
		// gate fires on each.
		forcedMigrate := isForcedMigrate(cmd)
		if forcedMigrate {
			if name := forcedMigratePreviewFlag(cmd); name != "" {
				return HandleError("--force cannot be combined with --%s: opening the store with the gate overridden applies pending migrations before the preview runs", name)
			}
		}
		// Unconditional set-or-clear keeps the override self-clearing should the
		// root command ever be re-run in-process (tests, a future server mode).
		schema.SetForceAllowRemoteMigrate(forcedMigrate)

		// Auto-migrate database on version bump (bd-jgxi).
		// Runs for ALL commands (including read-only ones) because the migration
		// opens its own store connection, writes the version metadata, commits it,
		// and closes BEFORE the main store is opened. This ensures bd doctor and
		// read-only commands see the correct version after a CLI upgrade.

		autoMigrateOnVersionBump(beadsDir)

		// Initialize direct storage access
		var err error

		// Create Dolt storage config — resolve dolt data dir which may be
		// on a different filesystem (e.g., ext4 for performance on WSL).
		doltPath := doltserver.ResolveDoltDir(beadsDir)
		doltCfg := &dolt.Config{
			ReadOnly:    useReadOnly,
			BeadsDir:    beadsDir,
			LenientOpen: isWorkingSetReconcileCommand(cmd),
		}

		// Load config to get database name and server connection settings.
		// A present-but-unloadable metadata.json must stop the command here:
		// continuing with the zero-value config silently selects the embedded
		// store with the default database name, and on server-mode
		// deployments that empty relic answers every query with an empty
		// result set and exit 0 (false-empty), which readers misinterpret as
		// "no work". Absent metadata.json (cfg == nil, cfgErr == nil) keeps
		// the fresh-repo embedded default below.
		if cfg != nil {
			warnSharedServerEmbeddedMismatch(cfg)
			doltCfg.ProxiedServer = cfg.IsDoltProxiedServerMode()
			proxiedServerMode = doltCfg.ProxiedServer
			if cmdCtx != nil {
				cmdCtx.ProxiedServerMode = doltCfg.ProxiedServer
			}

			doltCfg.ServerMode = cfg.IsDoltServerMode()
			// Shared server mode (dolt.shared-server in config.yaml) is a
			// form of server mode. Override metadata.json if it still says
			// embedded — handles installs created before GH#2946 fix. Skip
			// this for proxied-server: it's its own backend, not server.
			if !doltCfg.ServerMode && !doltCfg.ProxiedServer && doltserver.IsSharedServerMode() {
				doltCfg.ServerMode = true
			}
			serverMode = doltCfg.ServerMode
			if cmdCtx != nil {
				cmdCtx.ServerMode = doltCfg.ServerMode
			}

			// Always set database name (needed for bootstrap to find
			// prefix-based databases like "beads_hq"; see #1669)
			doltCfg.Database = cfg.GetDoltDatabase()
			if shouldLogDefaultDoltDatabase(cfg) {
				logConfigDiscovery(beadsDir, fmt.Sprintf("metadata loaded without dolt_database; using default database name %q", configfile.DefaultDoltDatabase))
			}

			doltCfg.ServerHost = cfg.GetDoltServerHost()
			// Use doltserver.DefaultConfig for port resolution (env > port file >
			// config.yaml). Port 0 is fine here — auto-start will resolve it.
			doltCfg.ServerPort = doltserver.DefaultConfig(beadsDir).Port
			doltCfg.ServerSocket = cfg.GetDoltServerSocket()
			// A configured credential command targets an authenticating gateway server:
			// run it for a short-lived token used as the connection username. Fail closed
			// — never fall back to the static/root user when a command was configured but
			// failed. Mirrors applyResolvedConfig, which this hand-built doltCfg path
			// bypasses. Server mode only: embedded stores never present a username, so the
			// command must not run (or fail) embedded opens even when the env var is set.
			// Dolt-only: the gateway credential command mints a Dolt server
			// username. IsSharedServerMode() forces ServerMode true with no backend
			// guard, so non-Dolt metadata must not try to resolve a server username.
			if doltCfg.ServerMode && cfg.GetBackend() == configfile.BackendDolt {
				if _, credErr := dolt.ApplyGatewayCredential(rootCtx, cfg, doltCfg); credErr != nil {
					return HandleError("resolving dolt credential command: %v", credErr)
				}
			}
			if doltCfg.ServerUser == "" {
				doltCfg.ServerUser = cfg.GetDoltServerUser()
			}
			// Use the resolved port for credential lookup — metadata.json port
			// and runtime port can diverge (e.g., tunnel on 3308 vs local on 3307).
			doltCfg.ServerPassword = cfg.GetDoltServerPasswordForPort(doltCfg.ServerPort)
			doltCfg.ServerTLS = cfg.GetDoltServerTLS()
		} else if cfgErr == nil {
			logConfigDiscovery(beadsDir, "config discovery")
			// Load returned (nil, nil) — no config file found.
			// Fall back to the canonical default database name; matches the
			// behavior of newDoltStoreFromConfig / newReadOnlyStoreFromConfig
			// (see store_factory.go). Without this, embeddeddolt.New rejects
			// the empty database name with "database name must not be empty
			// (caller should default to \"beads\")".
			fmt.Fprintf(os.Stderr, "warning: no beads configuration found in %s; using default database name %q\n", beadsDir, configfile.DefaultDoltDatabase)
			doltCfg.Database = configfile.DefaultDoltDatabase
		}
		// Honor shared-server mode even when no project config was found
		// (cfg == nil) or the parse failed. The override inside the
		// cfg != nil branch above is skipped in those cases, so without this
		// an exported BEADS_DOLT_SHARED_SERVER is silently ignored and bd
		// falls through to embeddeddolt.Open, creating a phantom embedded DB
		// that subsequent writes fragment into (GH#3817). This is idempotent:
		// when the override above already ran, ServerMode is already true.
		if !doltCfg.ServerMode && !doltCfg.ProxiedServer && doltserver.IsSharedServerMode() {
			doltCfg.ServerMode = true
			serverMode = doltCfg.ServerMode
			if cmdCtx != nil {
				cmdCtx.ServerMode = doltCfg.ServerMode
			}
		}
		// Defensive: embeddeddolt.New rejects an empty database name, so
		// default it even on paths that never set one.
		if doltCfg.Database == "" {
			doltCfg.Database = configfile.DefaultDoltDatabase
		}
		doltCfg.SyncRemote = resolveSyncRemote()

		// --global flag: switch to the global shared-server database.
		// Must be in shared-server mode; errors otherwise.
		if globalFlag {
			if !doltserver.IsSharedServerMode() {
				return HandleError("--global requires shared-server mode (set BEADS_DOLT_SHARED_SERVER=1 or dolt.shared-server: true in config.yaml)")
			}
			doltCfg.Database = doltserver.GlobalDatabaseName
		}

		// Keep standalone CLI auto-start behavior centralized so doctor and
		// other helper paths stay in lockstep with the main command path.
		dolt.ApplyCLIAutoStart(beadsDir, doltCfg)

		// In proxied mode the CLI short-circuits to the uowProvider path and
		// dispatches through the *_proxied_server.go duals.
		if proxiedServerMode {
			p, err := newProxiedServerUOWProvider(rootCtx, beadsDir)
			if err != nil {
				return HandleError("failed to open uow provider: %v", err)
			}
			uowProvider = p

			reconcileVersionProxiedServer(rootCtx)

			syncCommandContext()
			return nil
		}

		// Default auto-commit based on mode when the user hasn't set a value:
		// - Server mode: OFF — the server handles commits via its own transaction
		//   lifecycle; firing DOLT_COMMIT after every write under concurrent load
		//   causes 'database is read only' errors.
		// - Embedded mode: ON — each command writes to the working set and needs
		//   a Dolt commit in PersistentPostRun to persist changes to history.
		if strings.TrimSpace(doltAutoCommit) == "" {
			if !usesSQLServer() {
				doltAutoCommit = string(doltAutoCommitOn)
			} else {
				doltAutoCommit = string(doltAutoCommitOff)
			}
		}

		doltCfg.Path = doltPath

		// WARNING: DO NOT remove, delete, or modify files inside Dolt's .dolt/
		// directory — including noms/LOCK files. These are Dolt-internal files.
		// Removing them WILL cause unrecoverable data corruption and data loss.
		// Dolt manages these files itself; external interference is never safe.

		store, err = newDoltStore(rootCtx, doltCfg)

		// Track final read-only state for staleness checks (GH#1089)
		storeIsReadOnly = doltCfg.ReadOnly

		if err != nil {
			// Check for fresh clone scenario
			if handleFreshCloneError(err) {
				return SilentExit()
			}
			// Schema skew gets dedicated UX with actionable rebuild instructions.
			var skewErr *schema.SchemaSkewError
			if errors.As(err, &skewErr) {
				if jsonOutput {
					handleSchemaSkewJSON(skewErr)
				} else {
					fmt.Fprint(os.Stderr, skewErr.UserMessage())
				}
				return SilentExit()
			}
			// #4259: the remote-migrate gate blocks silent in-place migration of a
			// remote-backed database and tells the operator to migrate-or-adopt.
			var gateErr *schema.RemoteMigrateGateError
			if errors.As(err, &gateErr) {
				if jsonOutput {
					handleRemoteMigrateGateJSON(gateErr)
				} else {
					fmt.Fprint(os.Stderr, gateErr.UserMessage())
				}
				return SilentExit()
			}
			return HandleError("failed to open database: %v", err)
		}

		// Mark store as active for flush goroutine safety
		storeMutex.Lock()
		storeActive = true
		storeMutex.Unlock()

		// Auto-import from issues.jsonl when embedded database is empty (GH#2994).
		// This handles the upgrade path from pre-0.56 (dolt/) to 1.0+ (embeddeddolt/)
		// where the new embedded database starts empty but the git-tracked JSONL
		// still has all the user's data.
		// Skip auto-import when the user is explicitly running "bd import" —
		// the import command handles JSONL files itself and auto-importing
		// first would interfere (double-import / upsert confusion).
		if shouldRunAutoImportJSONL(cmd, store, useReadOnly, globalFlag, doltCfg.ServerMode) &&
			!isDisablingImportAutoViaConfigCommand(cmd, args) {
			maybeAutoImportJSONL(rootCtx, store, beadsDir)
		}

		// Validate workspace identity for write commands (GH#2438, GH#2372)
		// Skip for read-only commands since they can't corrupt data.
		// Skip for --global: the global database uses a sentinel project ID
		// that won't match any project's metadata.json.
		if !useReadOnly && !globalFlag && os.Getenv("BEADS_SKIP_IDENTITY_CHECK") != "1" {
			if err := validateWorkspaceIdentity(rootCtx, beadsDir); err != nil {
				return err
			}
		}

		// Initialize hook runner
		// dbPath is .beads/something.db, so workspace root is parent of .beads
		if dbPath != "" {
			beadsDir := filepath.Dir(dbPath)
			hookRunner = hooks.NewRunner(filepath.Join(beadsDir, "hooks"))
		}

		// Compose the storage decorator chain: OTel instrumentation (no-op
		// when telemetry is off) wrapped by hook firing (skipped when
		// BD_NO_HOOKS=1, which is useful for bulk imports, migrations, or
		// environments where on_create/on_update/on_close hooks should not
		// run). Order matters — see wireStorageDecorators in storage_chain.go.
		store = wireStorageDecorators(store, hookRunner, config.GetBool("no-hooks"))

		// Warn if multiple databases detected in directory hierarchy
		warnMultipleDatabases(dbPath)

		// Load molecule templates from hierarchical catalog locations
		// Templates are loaded after auto-import to ensure the database is up-to-date.
		// Skip for import command to avoid conflicts during import operations.
		if cmd.Name() != "import" && store != nil {
			beadsDir := filepath.Dir(dbPath)
			loader := molecules.NewLoader(store)
			if result, err := loader.LoadAll(rootCtx, beadsDir); err != nil {
				debug.Logf("warning: failed to load molecules: %v", err)
			} else if result.Loaded > 0 {
				debug.Logf("loaded %d molecules from %v", result.Loaded, result.Sources)
			}
		}

		// Sync all state to CommandContext for unified access.
		syncCommandContext()

		// Tips (including sync conflict proactive checks) are shown via maybeShowTip()
		// after successful command execution, not in PreRun
		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		defer restoreChangeDirSelection()

		if proxiedServerMode {
			if uowProvider != nil {
				_ = uowProvider.Close(rootCtx)
				uowProvider = nil
			}
		} else {
			// Dolt auto-commit: after a successful write command (and after final flush),
			// create a Dolt commit so changes don't remain only in the working set.
			if commandDidWrite.Load() && !commandDidExplicitDoltCommit {
				if err := maybeAutoCommit(rootCtx, doltAutoCommitParams{Command: cmd.Name()}); err != nil {
					return HandleError("dolt auto-commit failed: %v", err)
				}
			}

			// Tip metadata auto-commit: if a tip was shown, create a separate Dolt commit for the
			// tip_*_last_shown metadata updates. This may happen even for otherwise read-only commands.
			if commandDidWriteTipMetadata && len(commandTipIDsShown) > 0 {
				// Only applies when dolt auto-commit is enabled and backend is versioned (Dolt).
				if mode, err := getDoltAutoCommitMode(); err != nil {
					return HandleError("dolt tip auto-commit failed: %v", err)
				} else if mode == doltAutoCommitOn {
					// Apply tip metadata writes now (deferred in recordTipShown for Dolt).
					for tipID := range commandTipIDsShown {
						key := fmt.Sprintf("tip_%s_last_shown", tipID)
						value := time.Now().Format(time.RFC3339)
						if err := store.SetLocalMetadata(rootCtx, key, value); err != nil {
							return HandleError("dolt tip auto-commit failed: %v", err)
						}
					}

					ids := make([]string, 0, len(commandTipIDsShown))
					for tipID := range commandTipIDsShown {
						ids = append(ids, tipID)
					}
					msg := formatDoltAutoCommitMessage("tip", getActor(), ids)
					if err := maybeAutoCommit(rootCtx, doltAutoCommitParams{Command: "tip", MessageOverride: msg}); err != nil {
						return HandleError("dolt tip auto-commit failed: %v", err)
					}
				}
			}

			// Auto-backup: sync a Dolt-native backup if enabled and due
			maybeAutoBackup(rootCtx)

			// Auto-export: write git-tracked JSONL for portability if enabled and due.
			// Read-only commands must not perform post-run maintenance writes or emit
			// sync guidance after machine-readable output.
			if shouldRunPostCommandAutoExport(cmd) {
				if err := maybeAutoExport(rootCtx, commandAllowsEmptyAutoExport(cmd)); err != nil {
					return HandleError("%v", err)
				}
			}

			// Auto-push: push to Dolt remote if enabled and due.
			// Skip for read-only commands to avoid unnecessary network operations
			// and metadata writes on commands like bd list/show/ready (GH#2191).
			if !isReadOnlyCommand(cmd.Name()) {
				maybeAutoPush(rootCtx)
			}

			// Signal that store is closing (prevents background flush from accessing closed store)
			storeMutex.Lock()
			storeActive = false
			storeMutex.Unlock()

			if store != nil {
				_ = store.Close() // Best effort cleanup
			}
		}

		// End the command span and flush OTel data before process exit.
		if commandSpan != nil {
			commandSpan.End()
			commandSpan = nil
		}
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		telemetry.Shutdown(shutdownCtx)
		shutdownCancel()

		if profileFile != nil {
			pprof.StopCPUProfile()
			_ = profileFile.Close() // Best effort cleanup
		}
		if traceFile != nil {
			trace.Stop()
			_ = traceFile.Close() // Best effort cleanup
		}

		// Heap profiling: --mem-profile flag or BEADS_MEM_PROFILE env var.
		// Runs a GC first by default; BEADS_MEM_PROFILE_NOGC=1 skips it to capture peak.
		heapDest := memProfilePath
		if heapDest == "" {
			heapDest = os.Getenv("BEADS_MEM_PROFILE")
		}
		if heapDest != "" {
			if os.Getenv("BEADS_MEM_PROFILE_NOGC") == "" {
				runtime.GC()
			}
			if f, err := os.Create(heapDest); err == nil { // #nosec G304 -- user-supplied profiling path
				_ = pprof.WriteHeapProfile(f)
				_ = f.Close()
			}
		}
		// Optional one-line MemStats summary: BEADS_MEM_STATS=/path/to/stats.txt
		if statsDest := os.Getenv("BEADS_MEM_STATS"); statsDest != "" {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if f, err := os.Create(statsDest); err == nil { // #nosec G304 -- user-supplied profiling path
				fmt.Fprintf(f, "HeapAlloc=%d HeapSys=%d HeapInuse=%d HeapObjects=%d\n",
					ms.HeapAlloc, ms.HeapSys, ms.HeapInuse, ms.HeapObjects)
				_ = f.Close()
			}
		}

		// Cancel the signal context to clean up resources
		if rootCancel != nil {
			rootCancel()
		}
		return nil
	},
}

func shouldRunPostCommandAutoExport(cmd *cobra.Command) bool {
	if cmd == nil {
		return true
	}
	return !isReadOnlyCommand(cmd.Name())
}

func shouldRunAutoImportJSONL(cmd *cobra.Command, s storage.DoltStorage, useReadOnly, globalFlag, serverMode bool) bool {
	if cmd == nil || s == nil || useReadOnly || globalFlag || serverMode {
		return false
	}
	// import.auto=false (or BD_IMPORT_AUTO=false) must disable ALL auto-import
	// behavior, not just the git-hook sync path (importJSONLForSync). Without
	// this check, a fresh/empty database would silently auto-import stale
	// issues.jsonl on every write command regardless of the config setting
	// (GH#4304).
	if !config.GetBool("import.auto") {
		return false
	}
	return cmd.Name() != "import"
}

// isDisablingImportAutoViaConfigCommand reports whether the command about to
// run is "bd config set import.auto false" (or an equivalent
// "bd config set-many ... import.auto=false" pair). shouldRunAutoImportJSONL
// runs in PersistentPreRun before configSetCmd/configSetManyCmd write the new
// value to config.yaml, so without this exemption the master switch would
// trigger the very auto-import it is meant to disable on its own invocation
// when a stale .beads/issues.jsonl sits next to an empty database (GH#4304).
func isDisablingImportAutoViaConfigCommand(cmd *cobra.Command, args []string) bool {
	if cmd == nil || cmd.Parent() == nil || cmd.Parent().Name() != "config" {
		return false
	}
	switch cmd.Name() {
	case "set":
		return len(args) >= 2 && args[0] == "import.auto" && isFalsyConfigValue(args[1])
	case "set-many":
		for _, arg := range args {
			key, value, ok := strings.Cut(arg, "=")
			if ok && key == "import.auto" && isFalsyConfigValue(value) {
				return true
			}
		}
	}
	return false
}

// isFalsyConfigValue reports whether a config value string parses as a
// boolean false (e.g. "false", "0", "f").
func isFalsyConfigValue(value string) bool {
	parsed, err := strconv.ParseBool(value)
	return err == nil && !parsed
}

func commandAllowsEmptyAutoExport(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	switch cmd.Name() {
	case "prune", "purge":
		return commandMayEmptyJSONLExport.Load()
	default:
		return false
	}
}

// blockedEnvVars lists environment variables that must not be set because they
// could silently override the storage backend via viper's AutomaticEnv, causing
// data fragmentation (bd-hevyw).
var blockedEnvVars = []string{"BD_BACKEND", "BD_DATABASE_BACKEND"}

// checkBlockedEnvVars returns an error if any blocked env vars are set.
func checkBlockedEnvVars() error {
	for _, name := range blockedEnvVars {
		if os.Getenv(name) != "" {
			return fmt.Errorf("%s env var is not supported and has been removed to prevent data fragmentation.\n"+
				"Unset %s; storage selection comes from .beads/metadata.json. To choose a different supported backend, follow 'bd help init-safety'; do not edit metadata.json by hand", name, name)
		}
	}
	return nil
}

// setupGracefulShutdown creates a context that cancels on SIGINT/SIGTERM/SIGHUP.
// Before cancellation, it flushes pending batch commits so that accumulated
// changes in the Dolt working set are not lost on graceful shutdown.
func setupGracefulShutdown() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is returned and called by caller

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		select {
		case <-sigCh:
			flushBatchCommitOnShutdown()
			cancel()
			// On second signal, force exit
			<-sigCh
			os.Exit(1)
		case <-ctx.Done():
			signal.Stop(sigCh)
		}
	}()

	return ctx, cancel
}

// flushBatchCommitOnShutdown commits any pending batch changes before process exit.
// This prevents data loss when SIGTERM/SIGHUP kills a process with uncommitted
// batch writes sitting in the Dolt working set.
func flushBatchCommitOnShutdown() {
	mode, err := getDoltAutoCommitMode()
	if err != nil || mode != doltAutoCommitBatch {
		return
	}

	storeMutex.Lock()
	active := storeActive
	st := store
	storeMutex.Unlock()

	if !active || st == nil {
		return
	}

	// Use a fresh context with timeout — rootCtx is about to be canceled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := st.Commit(ctx, "bd: flush pending changes on shutdown"); err != nil {
		if !isDoltNothingToCommit(err) {
			fmt.Fprintf(os.Stderr, "\nWarning: failed to flush batch commit on shutdown: %v\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "\nFlushed pending batch commit on shutdown\n")
	}
}

// validateWorkspaceIdentity checks that the project identity from metadata.json
// matches the database's stored project_id. A mismatch indicates configuration
// drift — the CLI may be pointing at the wrong database (GH#2438, GH#2372).
//
// This check only runs for write commands because:
// 1. Read commands are safe even against wrong databases (no data mutation)
// 2. The check requires an open store connection
// 3. New databases won't have _project_id yet (bootstrap case)
func validateWorkspaceIdentity(ctx context.Context, beadsDir string) error {
	if store == nil {
		return nil // No store connection, nothing to validate
	}

	// Load project_id from metadata.json
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil // No config, skip validation (fresh init)
	}
	configProjectID := cfg.ProjectID
	if configProjectID == "" {
		return nil // No project_id in config (pre-identity era)
	}

	// Get project_id from database
	dbProjectID, err := store.GetMetadata(ctx, "_project_id")
	if err != nil || dbProjectID == "" {
		return nil // No project_id in DB (new or pre-identity database)
	}

	// Compare: mismatch means drift
	if configProjectID != dbProjectID {
		fmt.Fprintf(os.Stderr, "Error: workspace identity mismatch detected\n\n")
		fmt.Fprintf(os.Stderr, "  metadata.json project_id: %s\n", configProjectID)
		fmt.Fprintf(os.Stderr, "  database _project_id:     %s\n\n", dbProjectID)
		fmt.Fprintf(os.Stderr, "This means the CLI config and database belong to different projects.\n")
		fmt.Fprintf(os.Stderr, "Possible causes:\n")
		fmt.Fprintf(os.Stderr, "  • BEADS_DIR points to a different project's .beads/\n")
		fmt.Fprintf(os.Stderr, "  • Dolt server endpoint changed and now serves a different database\n")
		fmt.Fprintf(os.Stderr, "  • metadata.json was copied from another project\n\n")
		fmt.Fprintf(os.Stderr, "Recovery: run 'bd doctor --fix' or 'bd bootstrap' to reconcile workspace metadata with the authoritative database when shared-server metadata drifted.\n")
		fmt.Fprintf(os.Stderr, "To diagnose: bd context --json\n")
		fmt.Fprintf(os.Stderr, "To override: set BEADS_SKIP_IDENTITY_CHECK=1\n")
		return SilentExit()
	}
	return nil
}

func main() {
	// BD_NAME overrides the binary name in help text (e.g. BD_NAME=ops makes
	// "ops --help" show "ops" instead of "bd"). Useful for multi-instance
	// setups where wrapper scripts set BEADS_DIR for routing.
	if name := os.Getenv("BD_NAME"); name != "" {
		rootCmd.Use = name
	}

	// Register --all flag on Cobra's auto-generated help command.
	// Must be called after init() so all subcommands are registered and
	// Cobra has created its default help command.
	rootCmd.InitDefaultHelpCmd()
	registerHelpAllFlag()

	executedCmd, err := rootCmd.ExecuteC()

	// Finalize queued metrics and detach the uploader. Shared with the os.Exit
	// guards (CheckReadonly and the pre-run gates) so every exit path flushes the
	// same way instead of only the clean RunE/ExecuteC return.
	metrics.CloseAndFlush()

	if err != nil {
		if code, ok := exitCodeFromError(err); ok {
			os.Exit(code)
		}
		if executedCmd != nil && executedCmd.SilenceErrors {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
		}
		os.Exit(1)
	}
}

func resolveMetricsEnabled() bool {
	if v, ok := os.LookupEnv(metrics.EnvDisableMetrics); ok {
		return !envTruthyValue(v)
	}
	// DO_NOT_TRACK is a disable-only alias: a truthy value opts out, but a
	// falsey or empty value (DO_NOT_TRACK=0/false/"") must fall through to the
	// user's saved preference instead of forcing metrics back on over a saved
	// `bd metrics off`. Only BD_DISABLE_METRICS (checked first) is a
	// bidirectional override.
	if v, ok := os.LookupEnv(metrics.EnvDoNotTrack); ok && envTruthyValue(v) {
		return false
	}
	// Consent is the user's own global choice: resolve it from the user-global
	// config only, never merged project/BEADS_DIR config. Otherwise a
	// repository's .beads/config.yaml (highest viper precedence) could re-enable
	// metrics for a user who ran `bd metrics off`.
	return !config.MetricsDisabledByUserConfig()
}

func resolveMetricsEndpoint() string {
	if v := os.Getenv(metrics.EnvEndpoint); v != "" {
		return v
	}
	// Like enablement, the endpoint is resolved from env + user-global config
	// only so a repository can never redirect where a user's metrics are sent.
	if ep := config.UserMetricsEndpoint(); ep != "" {
		return ep
	}
	return metrics.DefaultEndpoint
}

func envTruthyValue(v string) bool {
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false":
		return false
	}
	return true
}

// secretFlagNames are long flag names whose entire value is an opaque credential
// that must never reach the bd.args telemetry span. The flag's value is redacted
// wholesale. Only federation add-peer's --password currently qualifies. Its shorthand (-p) is
// resolved per command via secretFlagTokens so the same letter bound to
// --priority/--prefix/--parallel on other commands is never redacted.
var secretFlagNames = map[string]bool{"password": true}

// secretFlagTokens returns the concrete --long and -short flag tokens that carry a
// secret value for cmd. Resolving against the running command is what makes the
// redaction "by flag identity": -p is treated as secret only on the command that
// actually binds it to a secret flag (federation add-peer), not on the many
// commands that bind -p to a non-secret option.
func secretFlagTokens(cmd *cobra.Command) map[string]bool {
	tokens := make(map[string]bool)
	if cmd == nil {
		return tokens
	}
	for name := range secretFlagNames {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		tokens["--"+f.Name] = true
		if f.Shorthand != "" {
			tokens["-"+f.Shorthand] = true
		}
	}
	return tokens
}

// scrubArgsForTelemetry joins argv for the bd.args span attribute with any
// credential-bearing values redacted. A secretFlags token's value is redacted
// wholesale across the `--password <v>`,
// `--password=<v>`, `-p <v>`, `-p=<v>`, and `-p<v>` spellings pflag accepts. Every
// other arg gets a conservative DSN/userinfo scrub as defense in depth so a
// positional connection string cannot leak a password.
func scrubArgsForTelemetry(argv []string, secretFlags map[string]bool) string {
	parts := make([]string, len(argv))
	redactNext := false
	for i, a := range argv {
		if redactNext {
			parts[i] = "xxxxx"
			redactNext = false
			continue
		}
		if name, value, ok := strings.Cut(a, "="); ok {
			if secretFlags[name] {
				// --password=<secret> / -p=<secret> — redact the whole value.
				parts[i] = name + "=xxxxx"
				continue
			}
			if strings.HasPrefix(name, "-") {
				scrubbed := scrubUserinfoPassword(scrubPotentialDSNPasswords(value))
				if scrubbed != value {
					// Preserve an arbitrary flag name while parsing its equals-value as
					// a possible DSN. Passing the whole token to url.Parse would treat
					// the flag prefix as the URL scheme and miss query credentials.
					parts[i] = name + "=" + scrubbed
					continue
				}
			}
		}
		if i > 0 {
			if secretFlags[argv[i-1]] {
				// <secret> following a bare --password / -p token.
				parts[i] = "xxxxx"
				continue
			}
		}
		if short, ok := secretShorthandPrefix(a, secretFlags); ok {
			// -p<secret> — pflag's concatenated shorthand spelling.
			parts[i] = short + "xxxxx"
			continue
		}
		if secretShorthandTakesSeparateValue(a, secretFlags) {
			// -qp <secret> — a boolean shorthand cluster ending in the
			// value-taking secret shorthand, with its value in the next token.
			parts[i] = a
			redactNext = true
			continue
		}
		parts[i] = scrubUserinfoPassword(scrubPotentialDSNPasswords(a))
	}
	return strings.Join(parts, " ")
}

// secretShorthandPrefix reports whether a is pflag's concatenated secret-shorthand
// spelling, returning the "-x...-p" prefix to preserve. Long flags cannot concatenate
// a value, so only -X<value> shorthands are matched.
//
// pflag also accepts a CLUSTER of boolean shorthands ending in a value-taking
// shorthand: given boolean flags -q/-v and value flag -p, "-qpSECRET" parses as -q
// followed by -p SECRET, and "-vpSECRET" parses as -v followed by -p SECRET — but the
// raw token still reaches telemetry as one string. Walk the leading run of letters in
// a; the first letter whose "-x" token is a registered secret shorthand ends the
// cluster, and everything after it is that flag's value, regardless of how many
// boolean shorthands preceded it. This mirrors pflag's own grammar (a cluster is zero
// or more boolean shorthands followed by one value-taking shorthand) without needing
// the running command's flag set here: it is conservative in the safe direction,
// since treating a longer prefix as consumed by the secret shorthand only ever
// over-redacts, never under-redacts.
func secretShorthandPrefix(a string, secretFlags map[string]bool) (string, bool) {
	if len(a) < 3 || a[0] != '-' || a[1] == '-' {
		return "", false
	}
	for i := 1; i < len(a); i++ {
		c := a[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return "", false
		}
		if secretFlags["-"+string(c)] {
			if i+1 >= len(a) {
				return "", false // no value follows; not the concatenated spelling
			}
			return a[:i+1], true
		}
	}
	return "", false
}

// secretShorthandTakesSeparateValue recognizes a boolean-shorthand cluster that
// ends in a registered secret shorthand with no attached value. For example,
// pflag parses "-qp secret" as -q followed by -p=secret.
func secretShorthandTakesSeparateValue(a string, secretFlags map[string]bool) bool {
	if len(a) < 3 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		c := a[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
		if secretFlags["-"+string(c)] {
			return i == len(a)-1
		}
	}
	return false
}

// scrubUserinfoPassword redacts the password in a URL/DSN userinfo section
// (postgres://user:PASS@host or user:PASS@tcp(...)); args without a user:pass@
// userinfo pass through unchanged, so ordinary text is never mangled.
func scrubUserinfoPassword(a string) string {
	at := strings.LastIndexByte(a, '@')
	if at < 0 {
		return a
	}
	head := a[:at]
	start := 0
	if s := strings.LastIndex(head, "//"); s >= 0 {
		start = s + 2 // userinfo begins after the scheme's "//"
	}
	colon := strings.IndexByte(head[start:], ':')
	if colon < 0 {
		return a // no "user:pass" userinfo, nothing to redact
	}
	return head[:start+colon+1] + "xxxxx" + a[at:]
}
