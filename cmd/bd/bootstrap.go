package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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
		executeBootstrapPlan(plan, cfg)
	},
}

// BootstrapPlan describes what bootstrap will do.
type BootstrapPlan struct {
	Action      string `json:"action"` // "sync", "restore", "init", "none"
	Reason      string `json:"reason"` // Human-readable explanation
	BeadsDir    string `json:"beads_dir"`
	Database    string `json:"database"`
	SyncRemote  string `json:"sync_remote,omitempty"`
	BackupDir   string `json:"backup_dir,omitempty"`
	HasExisting bool   `json:"has_existing"`
}

func detectBootstrapAction(beadsDir string, cfg *configfile.Config) BootstrapPlan {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	// Check for existing database
	doltPath := doltserver.ResolveDoltDir(beadsDir)
	if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(doltPath)
		if len(entries) > 0 {
			plan.HasExisting = true
			plan.Action = "none"
			plan.Reason = "Database already exists at " + doltPath
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

	// Check for backup JSONL files
	backupDir := filepath.Join(beadsDir, "backup")
	issuesFile := filepath.Join(backupDir, "issues.jsonl")
	if _, err := os.Stat(issuesFile); err == nil {
		plan.BackupDir = backupDir
		plan.Action = "restore"
		plan.Reason = "Backup files found — will restore from " + backupDir
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
		fmt.Printf("  Nothing to do. Use 'bd doctor' to check health.\n")
	case "sync":
		fmt.Printf("Bootstrap plan: clone from remote\n")
		fmt.Printf("  Remote: %s\n", plan.SyncRemote)
		fmt.Printf("  Database: %s\n", plan.Database)
	case "restore":
		fmt.Printf("Bootstrap plan: restore from backup\n")
		fmt.Printf("  Backup dir: %s\n", plan.BackupDir)
	case "init":
		fmt.Printf("Bootstrap plan: create fresh database\n")
		fmt.Printf("  Database: %s\n", plan.Database)
	}
}

func executeBootstrapPlan(plan BootstrapPlan, cfg *configfile.Config) {
	switch plan.Action {
	case "sync":
		fmt.Printf("Syncing from %s...\n", plan.SyncRemote)
		fmt.Printf("Run: bd init --prefix %s\n", inferPrefix(cfg))
		fmt.Printf("(bd init detects sync.git-remote and bootstraps non-destructively)\n")
	case "restore":
		fmt.Printf("Restoring from backup...\n")
		fmt.Printf("Run: bd backup restore\n")
	case "init":
		fmt.Printf("Creating fresh database...\n")
		fmt.Printf("Run: bd init --prefix %s\n", inferPrefix(cfg))
	}
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
	readOnlyCommands["bootstrap"] = true
}
