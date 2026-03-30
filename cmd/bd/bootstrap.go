package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"golang.org/x/term"
)

var bootstrapCmd = &cobra.Command{
	Use:     "bootstrap",
	GroupID: "setup",
	Short:   "Non-destructive database setup for fresh clones and recovery",
	Long: `Bootstrap sets up the beads database without destroying existing data.
Unlike 'bd init --force', bootstrap will never delete existing issues.

Bootstrap auto-detects the right action:
  • If sync.git-remote is configured: clones from the remote
  • If .beads/backup/*.jsonl exists: restores from backup
  • If .beads/issues.jsonl exists: imports from git-tracked JSONL
  • If no database exists: creates a fresh one
  • If database already exists: validates and reports status

This is the recommended command for:
  • Setting up beads on a fresh clone
  • Recovering after moving to a new machine
  • Repairing a broken database configuration

Examples:
  bd bootstrap              # Auto-detect and set up
  bd bootstrap --dry-run    # Show what would be done
  bd bootstrap --json       # Output plan as JSON
`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// Find beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"action":     "none",
					"reason":     "no .beads directory found",
					"suggestion": "Run 'bd init' to create a new project",
				})
			} else {
				fmt.Fprintf(os.Stderr, "No .beads directory found.\n")
				fmt.Fprintf(os.Stderr, "To create a new project, use: bd init\n")
				fmt.Fprintf(os.Stderr, "Bootstrap is for existing projects that need database setup.\n")
			}
			os.Exit(1)
		}

		// Load config
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
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
		if err := executeBootstrapPlan(plan, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
			os.Exit(1)
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

func detectBootstrapAction(beadsDir string, cfg *configfile.Config) BootstrapPlan {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	// Check for existing database (path differs between server and embedded mode)
	var dbPath string
	if isEmbeddedMode() {
		dbPath = filepath.Join(beadsDir, "embeddeddolt")
	} else {
		dbPath = doltserver.ResolveDoltDir(beadsDir)
	}
	if info, err := os.Stat(dbPath); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(dbPath)
		if len(entries) > 0 {
			plan.HasExisting = true
			plan.Action = "none"
			plan.Reason = "Database already exists at " + dbPath
			return plan
		}
	}

	// Check sync.git-remote
	syncRemote := config.GetString("sync.git-remote")
	if syncRemote != "" {
		plan.SyncRemote = syncRemote
		plan.Action = "sync"
		plan.Reason = "sync.git-remote configured — will clone from " + syncRemote
		return plan
	}

	// Auto-detect: probe origin for refs/dolt/data
	if isGitRepo() && !isBareGitRepo() {
		if originURL, err := gitRemoteGetURL("origin"); err == nil && originURL != "" {
			if gitLsRemoteHasRef("origin", "refs/dolt/data") {
				plan.SyncRemote = gitURLToDoltRemote(originURL)
				plan.Action = "sync"
				plan.Reason = "Found existing beads database on origin (refs/dolt/data) — will clone from " + originURL
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

// confirmPrompt asks the user to confirm an action. Returns true if the user
// confirms or if stdin is not a terminal (non-interactive/CI contexts).
func confirmPrompt(message string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [Y/n] ", message)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

func executeBootstrapPlan(plan BootstrapPlan, cfg *configfile.Config) error {
	if !confirmPrompt("Proceed?") {
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
	dbName := cfg.GetDoltDatabase()

	if isEmbeddedMode() {
		// Embedded mode: open a connection to the embedded engine and use
		// DOLT_CLONE to create the database from the remote URL.
		dataDir := filepath.Join(plan.BeadsDir, "embeddeddolt")
		if err := os.MkdirAll(dataDir, 0o750); err != nil {
			return fmt.Errorf("create embeddeddolt directory: %w", err)
		}

		// Open a connection without specifying a database (clone creates it).
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
		if err != nil {
			return fmt.Errorf("open embedded engine for clone: %w", err)
		}
		defer func() { _ = cleanup() }()

		if err := versioncontrolops.DoltClone(ctx, db, plan.SyncRemote, dbName); err != nil {
			return fmt.Errorf("clone from remote: %w", err)
		}

		// Clean stale noms LOCK files left by the clone process.
		if removed, _ := dolt.CleanStaleNomsLocks(dataDir); removed > 0 {
			fmt.Fprintf(os.Stderr, "Cleaned %d stale noms LOCK file(s)\n", removed)
		}

		fmt.Fprintf(os.Stderr, "Synced database from %s\n", plan.SyncRemote)
		return nil
	}

	doltDir := doltserver.ResolveDoltDir(plan.BeadsDir)
	synced, err := dolt.BootstrapFromGitRemoteWithDB(ctx, doltDir, plan.SyncRemote, dbName)
	if err != nil {
		return fmt.Errorf("sync from remote: %w", err)
	}
	if synced {
		// Clean stale noms LOCK files left by the dolt clone process.
		// These prevent dolt sql-server from starting (GH#bd-cmo).
		if removed, _ := dolt.CleanStaleNomsLocks(doltDir); removed > 0 {
			fmt.Fprintf(os.Stderr, "Cleaned %d stale noms LOCK file(s)\n", removed)
		}
		fmt.Fprintf(os.Stderr, "Synced database from %s\n", plan.SyncRemote)
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

func init() {
	bootstrapCmd.Flags().Bool("dry-run", false, "Show what would be done without doing it")
	rootCmd.AddCommand(bootstrapCmd)
}
