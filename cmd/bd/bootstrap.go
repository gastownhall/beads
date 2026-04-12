package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"golang.org/x/term"
)

type bootstrapServerProbeConfig struct {
	host     string
	port     int
	user     string
	pass     string
	database string
	tls      bool
}

type bootstrapServerDBCheck struct {
	Exists    bool
	Reachable bool
	Err       error
}

var checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
	host := probeCfg.host
	port := probeCfg.port
	dbName := probeCfg.database
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     probeCfg.user,
		Password: probeCfg.pass,
		TLS:      probeCfg.tls,
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return bootstrapServerDBCheck{Reachable: false, Err: err}
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return bootstrapServerDBCheck{Reachable: false, Err: err}
	}

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return bootstrapServerDBCheck{Reachable: true, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return bootstrapServerDBCheck{Reachable: true, Err: err}
		}
		if name == dbName {
			return bootstrapServerDBCheck{Exists: true, Reachable: true}
		}
	}
	if err := rows.Err(); err != nil {
		return bootstrapServerDBCheck{Reachable: true, Err: err}
	}

	return bootstrapServerDBCheck{Exists: false, Reachable: true}
}

var bootstrapCmd = &cobra.Command{
	Use:     "bootstrap",
	GroupID: "setup",
	Short:   "Non-destructive database setup for fresh clones and recovery",
	Long: `Bootstrap sets up the beads database without destroying existing data.
Unlike 'bd init --force', bootstrap will never delete existing issues.

Bootstrap auto-detects the right action:
  • If sync.remote is configured: clones from the remote
  • If git origin has Dolt data (refs/dolt/data): clones from git
  • If .beads/backup/*.jsonl exists: restores from backup
  • If .beads/issues.jsonl exists: imports from git-tracked JSONL
  • If no database exists: creates a fresh one
  • If database already exists: validates and reports status

This is the recommended command for:
  • Setting up beads on a fresh clone
  • Recovering after moving to a new machine
  • Repairing a broken database configuration

Non-interactive mode (--non-interactive, --yes/-y, or BD_NON_INTERACTIVE=1):
  Skips the confirmation prompt before executing the bootstrap plan.
  Also auto-detected when stdin is not a terminal or CI=true is set.

Examples:
  bd bootstrap              # Auto-detect and set up
  bd bootstrap --dry-run    # Show what would be done
  bd bootstrap --json       # Output plan as JSON
  bd bootstrap --yes        # Skip confirmation prompt
`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yesFlag, _ := cmd.Flags().GetBool("yes")
		nonInteractiveFlag, _ := cmd.Flags().GetBool("non-interactive")

		// Resolve non-interactive mode: flag > env var > CI env > terminal detection.
		nonInteractive := isNonInteractiveBootstrap(yesFlag || nonInteractiveFlag)

		// Find beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			// No .beads directory exists yet. Before giving up, probe the
			// git remote for Dolt data stored in git (refs/dolt/data). This
			// is the "fresh second clone" case: clone1 pushed Beads state
			// to a git remote, and clone2 needs to bootstrap from it.
			// Only applies to git remotes — Dolt-native remotes (DoltHub,
			// S3, etc.) should be configured via sync.remote. (GH#2792)
			//
			// If found, synthesize the theoretical .beads path and fall
			// through to the normal detectBootstrapAction + executeBootstrapPlan
			// flow. Actual directory creation is deferred to executeSyncAction
			// to preserve --dry-run semantics.
			if isGitRepo() && !isBareGitRepo() {
				if originURL, err := gitRemoteGetURL("origin"); err == nil && originURL != "" {
					if gitLsRemoteHasRef("origin", "refs/dolt/data") {
						if fallbackDir := beads.GetWorktreeFallbackBeadsDir(); fallbackDir != "" {
							beadsDir = fallbackDir
						} else {
							cwd, err := os.Getwd()
							if err != nil {
								FatalError("failed to get working directory: %v", err)
							}
							beadsDir = filepath.Join(cwd, ".beads")
						}
					}
				}
			}
		}

		if beadsDir == "" {
			// No .beads and no remote data — nothing to bootstrap from.
			if jsonOutput {
				outputJSON(noWorkspaceBootstrapPayload())
			} else {
				fmt.Fprintf(os.Stderr, "%s\n", activeWorkspaceNotFoundMessage())
				fmt.Fprintf(os.Stderr, "Hint: %s\n", diagHint())
				fmt.Fprintf(os.Stderr, "Bootstrap is for existing projects that need database setup.\n")
			}
			os.Exit(1)
		}

		// Load config from .beads/metadata.json. When the beadsDir was
		// synthesized (fresh clone or rig with no local .beads), the file
		// won't exist. In that case, walk up parent directories to find a
		// workspace-level metadata.json that contains the correct database
		// name (e.g. dolt_database). Without this, server-mode rigs get the
		// default name "beads" instead of their configured name. (GH#3029)
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			cfg = findParentConfig(beadsDir)
		}
		if cfg == nil {
			cfg = configfile.DefaultConfig()
		}

		// Determine action based on state
		plan := detectBootstrapAction(beadsDir, cfg)

		if jsonOutput {
			outputJSON(plan)
			if plan.Action == "none" || dryRun {
				return
			}
		} else {
			printBootstrapPlan(plan)
			if plan.Action == "none" || dryRun {
				return
			}
		}

		// Execute the plan
		if err := executeBootstrapPlan(plan, cfg, nonInteractive); err != nil {
			FatalError("Bootstrap failed: %v", err)
		}
	},
}

// BootstrapPlan describes what bootstrap will do.
type BootstrapPlan struct {
	Action      string `json:"action"` // "sync", "restore", "jsonl-import", "init", "none"
	Reason      string `json:"reason"` // Human-readable explanation
	BeadsDir    string `json:"beads_dir"`
	Database    string `json:"database"`
	SyncRemote  string `json:"sync_remote,omitempty"`
	BackupDir   string `json:"backup_dir,omitempty"`
	JSONLFile   string `json:"jsonl_file,omitempty"`
	HasExisting bool   `json:"has_existing"`
}

func noWorkspaceBootstrapPayload() map[string]interface{} {
	return map[string]interface{}{
		"action":     "none",
		"reason":     activeWorkspaceNotFoundError(),
		"suggestion": diagHint(),
	}
}

func detectBootstrapAction(beadsDir string, cfg *configfile.Config) BootstrapPlan {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	// Check for existing database (path differs between server and embedded mode).
	// Use cfg.IsDoltServerMode() (which checks metadata.json + env vars) plus
	// doltserver.IsSharedServerMode() (which also checks config.yaml) so that
	// shared-server mode configured via dolt.shared-server: true in config.yaml
	// correctly resolves the database path. (GH#30)
	isServer := cfg.IsDoltServerMode() || doltserver.IsSharedServerMode()
	var dbPath string
	if isServer {
		dbPath = doltserver.ResolveDoltDir(beadsDir)
	} else {
		dbPath = filepath.Join(beadsDir, "embeddeddolt")
	}
	if info, err := os.Stat(dbPath); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(dbPath)
		if len(entries) > 0 {
			if isServer {
				resolved := doltserver.DefaultConfig(beadsDir)
				probeCfg := bootstrapServerProbeConfig{
					host:     cfg.GetDoltServerHost(),
					port:     resolved.Port,
					user:     cfg.GetDoltServerUser(),
					pass:     cfg.GetDoltServerPassword(),
					database: cfg.GetDoltDatabase(),
					tls:      cfg.GetDoltServerTLS(),
				}
				result := checkBootstrapServerDB(probeCfg)
				if result.Err != nil {
					plan.Action = "none"
					plan.Reason = fmt.Sprintf("Could not verify existing server database %s: %v", cfg.GetDoltDatabase(), result.Err)
					return plan
				}
				if result.Exists {
					plan.HasExisting = true
					plan.Action = "none"
					plan.Reason = fmt.Sprintf("Database %s already exists on server at %s:%d", probeCfg.database, probeCfg.host, probeCfg.port)
					return plan
				}
			} else {
				plan.HasExisting = true
				plan.Action = "none"
				plan.Reason = "Database already exists at " + dbPath
				return plan
			}
		}
	}

	// Check sync.remote (primary) or sync.git-remote (deprecated fallback)
	syncRemote := resolveSyncRemote()
	if syncRemote != "" {
		plan.SyncRemote = normalizeRemoteURL(syncRemote)
		plan.Action = "sync"
		plan.Reason = "sync.remote configured — will clone from " + syncRemote
		return plan
	}

	// Auto-detect: probe git origin for Dolt data stored in git
	// (refs/dolt/data). This only applies to git remotes — Dolt-native
	// remotes (DoltHub, S3, etc.) must be configured via sync.remote.
	if isGitRepo() && !isBareGitRepo() {
		if originURL, err := gitRemoteGetURL("origin"); err == nil && originURL != "" {
			if gitLsRemoteHasRef("origin", "refs/dolt/data") {
				plan.SyncRemote = normalizeRemoteURL(originURL)
				plan.Action = "sync"
				plan.Reason = "Found Dolt data on git origin (refs/dolt/data) — will clone from " + originURL
				return plan
			}
		}
	}

	// Check for backup JSONL files (must be non-empty to be useful)
	backupDir := filepath.Join(beadsDir, "backup")
	issuesFile := filepath.Join(backupDir, "issues.jsonl")
	if info, err := os.Stat(issuesFile); err == nil && info.Size() > 0 {
		plan.BackupDir = backupDir
		plan.Action = "restore"
		plan.Reason = "Backup files found — will restore from " + backupDir
		return plan
	}

	// Check for git-tracked JSONL (the portable export format)
	gitJSONL := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(gitJSONL); err == nil {
		plan.JSONLFile = gitJSONL
		plan.Action = "jsonl-import"
		plan.Reason = "Git-tracked issues.jsonl found — will import from " + gitJSONL
		return plan
	}

	// Fresh setup
	plan.Action = "init"
	plan.Reason = "No existing database, remote, or backup — will create fresh database"
	return plan
}

func printBootstrapPlan(plan BootstrapPlan) {
	switch plan.Action {
	case "none":
		fmt.Printf("✓ Database already exists: %s\n", plan.BeadsDir)
		if isEmbeddedMode() {
			fmt.Printf("  Nothing to do.\n")
		} else {
			fmt.Printf("  Nothing to do. Use 'bd doctor' to check health.\n")
		}
	case "sync":
		fmt.Printf("Bootstrap plan: clone from remote\n")
		fmt.Printf("  Remote: %s\n", plan.SyncRemote)
		fmt.Printf("  Database: %s\n", plan.Database)
	case "restore":
		fmt.Printf("Bootstrap plan: restore from backup\n")
		fmt.Printf("  Backup dir: %s\n", plan.BackupDir)
	case "jsonl-import":
		fmt.Printf("Bootstrap plan: import from git-tracked JSONL\n")
		fmt.Printf("  JSONL file: %s\n", plan.JSONLFile)
		fmt.Printf("  Database: %s\n", plan.Database)
	case "init":
		fmt.Printf("Bootstrap plan: create fresh database\n")
		fmt.Printf("  Database: %s\n", plan.Database)
	}
}

// confirmPrompt asks the user to confirm an action. Returns true if
// nonInteractive is set, stdin is not a terminal, or the user confirms.
func confirmPrompt(message string, nonInteractive bool) bool {
	if nonInteractive {
		return true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [Y/n] ", message)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

func executeBootstrapPlan(plan BootstrapPlan, cfg *configfile.Config, nonInteractive bool) error {
	if !confirmPrompt("Proceed?", nonInteractive) {
		fmt.Fprintf(os.Stderr, "Aborted.\n")
		return nil
	}

	ctx := context.Background()

	switch plan.Action {
	case "sync":
		return executeSyncAction(ctx, plan, cfg)
	case "restore":
		return executeRestoreAction(ctx, plan, cfg)
	case "jsonl-import":
		return executeJSONLImportAction(ctx, plan, cfg)
	case "init":
		return executeInitAction(ctx, plan, cfg)
	}
	return nil
}

func executeInitAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.Commit(ctx, "bd bootstrap"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Created fresh database with prefix %q\n", prefix)
	return nil
}

func executeRestoreAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.Commit(ctx, "bd bootstrap: init"); err != nil {
		return fmt.Errorf("commit init: %w", err)
	}

	if err := runBackupRestore(ctx, s, plan.BackupDir, false); err != nil {
		return fmt.Errorf("restore from backup: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Restored from backup\n")
	return nil
}

func executeJSONLImportAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.Commit(ctx, "bd bootstrap: init"); err != nil {
		return fmt.Errorf("commit init: %w", err)
	}

	count, err := importFromLocalJSONL(ctx, s, plan.JSONLFile)
	if err != nil {
		return fmt.Errorf("import from JSONL: %w", err)
	}

	if err := s.Commit(ctx, "bd bootstrap: import from issues.jsonl"); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Imported %d issues from %s\n", count, plan.JSONLFile)
	return nil
}

func executeSyncAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	// Ensure .beads directory exists — it may not in the "fresh clone"
	// bootstrap path where we detected remote data before .beads was
	// created. Deferred here to preserve --dry-run semantics. (GH#2792)
	if err := os.MkdirAll(plan.BeadsDir, 0o750); err != nil {
		return fmt.Errorf("create beads directory: %w", err)
	}

	dbName := cfg.GetDoltDatabase()
	if err := cloneFromRemote(ctx, plan.BeadsDir, plan.SyncRemote, dbName); err != nil {
		return err
	}

	// Finalize the bootstrapped workspace so subsequent bd commands can open
	// the cloned database. Without metadata.json and config.yaml,
	// configfile.Load() returns nil, callers fall back to the default
	// dolt_database name, and bd loses track of the cloned database —
	// producing "no beads configuration found" and "Error 1105: no database
	// selected" on bd status / bd dolt push in fresh clones. Every other
	// bootstrap action (init, restore, jsonl-import) writes these files via
	// newDoltStore + createConfigYaml; the sync path historically did not.
	// (GH#3201)
	return finalizeSyncedBootstrap(plan.BeadsDir, plan.SyncRemote, cfg, dbName)
}

// finalizeSyncedBootstrap writes metadata.json and config.yaml after a
// successful sync clone, matching the on-disk layout that bd init produces.
// It is idempotent: re-running over an already-finalized workspace leaves
// existing files intact (createConfigYaml skips if config.yaml exists; the
// metadata.json write is a full rewrite that preserves caller fields).
func finalizeSyncedBootstrap(beadsDir, syncRemote string, cfg *configfile.Config, dbName string) error {
	// Preserve whatever upstream fields were already set in cfg (which may
	// be DefaultConfig when metadata.json was absent, or a parent workspace
	// config propagated by findParentConfig), then fill in the bits
	// required by configfile.Load consumers.
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = dbName
	if isEmbeddedMode() {
		cfg.DoltMode = configfile.DoltModeEmbedded
	} else {
		cfg.DoltMode = configfile.DoltModeServer
	}
	// Mirror init's convention: metadata.json database points at the Dolt
	// directory rather than the legacy "beads.db" placeholder.
	if cfg.Database == "" || cfg.Database == beads.CanonicalDatabaseName {
		cfg.Database = "dolt"
	}

	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}

	if err := createConfigYaml(beadsDir, false, ""); err != nil {
		return fmt.Errorf("create config.yaml: %w", err)
	}

	// Persist sync.remote so subsequent fresh clones (and bd bootstrap
	// retries) can rediscover the remote without re-probing origin refs.
	if syncRemote != "" {
		if err := config.SetYamlConfig("sync.remote", syncRemote); err != nil {
			return fmt.Errorf("persist sync.remote to config.yaml: %w", err)
		}
	}

	return nil
}

// cloneFromRemote clones a Dolt database from a remote URL.
// In embedded mode, uses the embedded engine's DOLT_CLONE procedure.
// In server mode, shells out to dolt clone via BootstrapFromRemoteWithDB.
// Shared by bd init and bd bootstrap to keep clone logic in one place.
func cloneFromRemote(ctx context.Context, beadsDir, remoteURL, dbName string) error {
	if isEmbeddedMode() {
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		if err := os.MkdirAll(dataDir, 0o750); err != nil {
			return fmt.Errorf("create embeddeddolt directory: %w", err)
		}
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
		if err != nil {
			return fmt.Errorf("open embedded engine for clone: %w", err)
		}
		defer func() { _ = cleanup() }()

		if err := versioncontrolops.DoltClone(ctx, db, remoteURL, dbName); err != nil {
			return fmt.Errorf("clone from remote: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Synced database from %s\n", remoteURL)
		return nil
	}

	doltDir := doltserver.ResolveDoltDir(beadsDir)
	synced, err := dolt.BootstrapFromRemoteWithDB(ctx, doltDir, remoteURL, dbName)
	if err != nil {
		return fmt.Errorf("sync from remote: %w", err)
	}
	if synced {
		fmt.Fprintf(os.Stderr, "Synced database from %s\n", remoteURL)
	}
	return nil
}

func inferPrefix(cfg *configfile.Config) string {
	db := cfg.GetDoltDatabase()
	if db != "" && db != "beads" {
		return db
	}
	cwd, _ := os.Getwd()
	return filepath.Base(cwd)
}

// isNonInteractiveBootstrap returns true if bootstrap should skip confirmation prompts.
// Precedence: explicit flag > BD_NON_INTERACTIVE env > CI env > terminal detection.
func isNonInteractiveBootstrap(flagValue bool) bool {
	if flagValue {
		return true
	}
	if v := os.Getenv("BD_NON_INTERACTIVE"); v == "1" || v == "true" {
		return true
	}
	if v := os.Getenv("CI"); v == "true" || v == "1" {
		return true
	}
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// findParentConfig walks up from beadsDir's parent looking for a
// .beads/metadata.json in ancestor directories. This handles the case where a
// rig subdirectory (its own git repo) doesn't have a local .beads but its
// parent workspace does. Returns nil if no parent config is found.
func findParentConfig(beadsDir string) *configfile.Config {
	// Start from the parent of beadsDir's enclosing directory.
	// beadsDir is typically "<project>/.beads", so we start from <project>'s parent.
	start := filepath.Dir(filepath.Dir(beadsDir))
	homeDir, _ := os.UserHomeDir()

	for dir := start; dir != "/" && dir != "."; {
		candidate := filepath.Join(dir, ".beads")
		if cfg, err := configfile.Load(candidate); err == nil && cfg != nil {
			return cfg
		}

		// Don't search above $HOME
		if homeDir != "" && dir == homeDir {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

func init() {
	bootstrapCmd.Flags().Bool("dry-run", false, "Show what would be done without doing it")
	bootstrapCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompts (for CI/automation)")
	bootstrapCmd.Flags().Bool("non-interactive", false, "Alias for --yes")
	rootCmd.AddCommand(bootstrapCmd)
}
