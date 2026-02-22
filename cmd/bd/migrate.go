package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var migrateCmd = &cobra.Command{
	Use:     "migrate",
	GroupID: "maint",
	Short:   "Database migration commands",
	Long: `Database migration and data transformation commands.

Without subcommand, checks and updates database metadata to current version.

Backend migration flags:
  --to-dolt     Migrate from SQLite to Dolt backend

Subcommands:
  issues      Move issues between repositories
  sync        Set up sync.branch workflow for multi-clone setups
`,
	Run: func(cmd *cobra.Command, _ []string) {
		autoYes, _ := cmd.Flags().GetBool("yes")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		updateRepoID, _ := cmd.Flags().GetBool("update-repo-id")
		inspect, _ := cmd.Flags().GetBool("inspect")

		// Block writes in readonly mode (migration modifies data, --inspect is read-only)
		if !dryRun && !inspect {
			CheckReadonly("migrate")
		}

		// Handle --update-repo-id first
		if updateRepoID {
			handleUpdateRepoID(dryRun, autoYes)
			return
		}

		// Handle --inspect flag (show migration plan for AI agents)
		if inspect {
			handleInspect()
			return
		}

		// Handle --to-dolt flag (SQLite to Dolt migration)
		toDolt, _ := cmd.Flags().GetBool("to-dolt")
		if toDolt {
			handleToDoltMigration(dryRun, autoYes)
			return
		}

		// Handle --to-sqlite flag (no longer supported)
		toSQLite, _ := cmd.Flags().GetBool("to-sqlite")
		if toSQLite {
			handleToSQLiteMigration(dryRun, autoYes)
			return
		}

		// Find .beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "no_beads_directory",
					"message": "No .beads directory found. Run 'bd init' first.",
				})
				os.Exit(1)
			} else {
				FatalErrorWithHint("no .beads directory found", "run 'bd init' to initialize bd")
			}
		}

		// Load config
		cfg, err := loadOrCreateConfig(beadsDir)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "config_load_failed",
					"message": err.Error(),
				})
				os.Exit(1)
			}
			FatalError("failed to load config: %v", err)
		}

		// Handle Dolt metadata update
		handleDoltMetadataUpdate(cfg, beadsDir, dryRun)
	},
}

// handleDoltMetadataUpdate handles version metadata updates for Dolt backends.
func handleDoltMetadataUpdate(cfg *configfile.Config, beadsDir string, dryRun bool) {
	doltPath := cfg.DatabasePath(beadsDir)

	// Check if Dolt database directory exists
	info, err := os.Stat(doltPath)
	if err != nil || !info.IsDir() {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "no_databases",
				"message": "No Dolt database found in .beads/",
			})
		} else {
			fmt.Fprintf(os.Stderr, "No Dolt database found at %s\n", doltPath)
			fmt.Fprintf(os.Stderr, "Run 'bd init' to create a new database.\n")
		}
		return
	}

	// Open database
	ctx := rootCtx
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "open_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to open Dolt database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Check current state of all metadata fields
	currentVersion, _ := store.GetMetadata(ctx, "bd_version")
	currentRepoID, _ := store.GetMetadata(ctx, "repo_id")
	currentCloneID, _ := store.GetMetadata(ctx, "clone_id")

	needsVersionUpdate := currentVersion != Version
	needsRepoID := currentRepoID == ""
	needsCloneID := currentCloneID == ""

	// If everything is already current, return early
	if !needsVersionUpdate && !needsRepoID && !needsCloneID {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "current",
				"message": fmt.Sprintf("Dolt database already at version %s", Version),
			})
		} else {
			fmt.Printf("Dolt database version: %s\n", currentVersion)
			fmt.Printf("%s\n", ui.RenderPass("✓ Version matches"))
			fmt.Printf("%s\n", ui.RenderPass("✓ All metadata fields present"))
		}
		return
	}

	if dryRun {
		dryRunResult := map[string]interface{}{
			"dry_run":              true,
			"needs_version_update": needsVersionUpdate,
			"needs_repo_id":        needsRepoID,
			"needs_clone_id":       needsCloneID,
		}
		if needsVersionUpdate {
			dryRunResult["current_version"] = currentVersion
			dryRunResult["target_version"] = Version
		}
		if jsonOutput {
			outputJSON(dryRunResult)
		} else {
			fmt.Println("Dry run mode - no changes will be made")
			if needsVersionUpdate {
				fmt.Printf("Would update Dolt version: %s → %s\n", currentVersion, Version)
			}
			if needsRepoID {
				fmt.Println("Would set repo_id")
			}
			if needsCloneID {
				fmt.Println("Would set clone_id")
			}
		}
		return
	}

	versionUpdated := false
	repoIDSet := false
	cloneIDSet := false

	// Update bd_version if needed
	if needsVersionUpdate {
		if !jsonOutput {
			fmt.Printf("Updating Dolt schema version: %s → %s\n", currentVersion, Version)
		}

		// Detect and set issue_prefix if missing
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix == "" {
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err == nil && len(issues) > 0 {
				detectedPrefix := utils.ExtractIssuePrefix(issues[0].ID)
				if detectedPrefix != "" {
					if err := store.SetConfig(ctx, "issue_prefix", detectedPrefix); err != nil {
						if !jsonOutput {
							fmt.Fprintf(os.Stderr, "Warning: failed to set issue prefix: %v\n", err)
						}
					} else if !jsonOutput {
						fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Detected and set issue prefix: %s", detectedPrefix)))
					}
				}
			}
		}

		// Update version metadata (fatal on failure — version is critical)
		if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "version_update_failed",
					"message": err.Error(),
				})
				os.Exit(1)
			}
			FatalError("failed to update version: %v", err)
		}
		versionUpdated = true

		if !jsonOutput {
			fmt.Printf("%s\n", ui.RenderPass("✓ Version updated"))
		}
	}

	// Set repo_id if missing (non-fatal — may fail in non-git environments)
	if needsRepoID {
		computed, err := beads.ComputeRepoID()
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: could not compute repo_id: %v\n", err)
			}
		} else {
			if err := store.SetMetadata(ctx, "repo_id", computed); err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to set repo_id: %v\n", err)
				}
			} else {
				repoIDSet = true
				if !jsonOutput {
					fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Set repo_id: %s", truncateID(computed, 8))))
				}
			}
		}
	}

	// Set clone_id if missing (non-fatal — may fail in non-git environments)
	if needsCloneID {
		computed, err := beads.GetCloneID()
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: could not compute clone_id: %v\n", err)
			}
		} else {
			if err := store.SetMetadata(ctx, "clone_id", computed); err != nil {
				if !jsonOutput {
					fmt.Fprintf(os.Stderr, "Warning: failed to set clone_id: %v\n", err)
				}
			} else {
				cloneIDSet = true
				if !jsonOutput {
					fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Set clone_id: %s", truncateID(computed, 8))))
				}
			}
		}
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":           "success",
			"current_database": cfg.Database,
			"backend":          "dolt",
			"version":          Version,
			"version_updated":  versionUpdated,
			"repo_id_set":      repoIDSet,
			"clone_id_set":     cloneIDSet,
		})
	} else {
		fmt.Printf("\nDolt database: %s (version %s)\n", cfg.Database, Version)
	}
}

// truncateID safely truncates an ID string to maxLen characters.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen]
}

// loadOrCreateConfig loads metadata.json or creates default if not found
func loadOrCreateConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, err
	}

	// Create default if no config exists
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	return cfg, nil
}

func handleUpdateRepoID(dryRun bool, autoYes bool) {
	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_database",
				"message": "No beads database found. Run 'bd init' first.",
			})
			os.Exit(1)
		}
		FatalErrorWithHint("no beads database found", "run 'bd init' to initialize bd")
	}

	// Compute new repo ID
	newRepoID, err := beads.ComputeRepoID()
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "compute_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to compute repository ID: %v", err)
	}

	// Open database
	store, err := dolt.NewFromConfig(rootCtx, beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "open_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to open database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Get old repo ID
	ctx := rootCtx
	oldRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil && err.Error() != "metadata key not found: repo_id" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "read_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to read repo_id: %v", err)
	}

	oldDisplay := "none"
	if len(oldRepoID) >= 8 {
		oldDisplay = oldRepoID[:8]
	}

	if dryRun {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"dry_run":     true,
				"old_repo_id": oldDisplay,
				"new_repo_id": truncateID(newRepoID, 8),
			})
		} else {
			fmt.Println("Dry run mode - no changes will be made")
			fmt.Printf("Would update repository ID:\n")
			fmt.Printf("  Old: %s\n", oldDisplay)
			fmt.Printf("  New: %s\n", truncateID(newRepoID, 8))
		}
		return
	}

	// Prompt for confirmation if repo_id exists and differs
	if oldRepoID != "" && oldRepoID != newRepoID && !autoYes && !jsonOutput {
		fmt.Printf("WARNING: Changing repository ID can break sync if other clones exist.\n\n")
		fmt.Printf("Current repo ID: %s\n", oldDisplay)
		fmt.Printf("New repo ID:     %s\n\n", truncateID(newRepoID, 8))
		fmt.Printf("Continue? [y/N] ")
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Canceled")
			return
		}
	}

	// Update repo ID
	if err := store.SetMetadata(ctx, "repo_id", newRepoID); err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "update_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to update repo_id: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":      "success",
			"old_repo_id": oldDisplay,
			"new_repo_id": truncateID(newRepoID, 8),
		})
	} else {
		fmt.Printf("%s\n\n", ui.RenderPass("✓ Repository ID updated"))
		fmt.Printf("  Old: %s\n", oldDisplay)
		fmt.Printf("  New: %s\n", truncateID(newRepoID, 8))
	}
}

// handleInspect shows migration plan and database state for AI agent analysis
func handleInspect() {
	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": "No .beads directory found. Run 'bd init' first.",
			})
			os.Exit(1)
		}
		FatalErrorWithHint("no .beads directory found", "run 'bd init' to initialize bd")
	}

	// Load config
	cfg, err := loadOrCreateConfig(beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_load_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to load config: %v", err)
	}

	// Check if database exists (don't create it)
	targetPath := cfg.DatabasePath(beadsDir)
	dbExists := false
	if _, err := os.Stat(targetPath); err == nil {
		dbExists = true
	} else if !os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_stat_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to check database: %v", err)
	}

	// If database doesn't exist, return inspection with defaults
	if !dbExists {
		result := map[string]interface{}{
			"registered_migrations": listMigrations(),
			"current_state": map[string]interface{}{
				"schema_version": "missing",
				"issue_count":    0,
				"config":         map[string]string{},
				"missing_config": []string{},
				"db_exists":      false,
			},
			"warnings":            []string{"Database does not exist - run 'bd init' first"},
			"invariants_to_check": []string{},
		}

		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Println("\nMigration Inspection")
			fmt.Println("====================")
			fmt.Println("Database: missing")
			fmt.Println("\n⚠ Database does not exist - run 'bd init' first")
		}
		return
	}

	// Open database in read-only mode for inspection
	store, err := dolt.NewFromConfigWithOptions(rootCtx, beadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_open_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to open database: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := rootCtx

	// Get current schema version
	schemaVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		schemaVersion = "unknown"
	}

	// Get issue count
	issueCount := 0
	if stats, err := store.GetStatistics(ctx); err == nil {
		issueCount = stats.TotalIssues
	}

	// Get config
	configMap := make(map[string]string)
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix != "" {
		configMap["issue_prefix"] = prefix
	}

	// Detect missing config
	missingConfig := []string{}
	if issueCount > 0 && prefix == "" {
		missingConfig = append(missingConfig, "issue_prefix")
	}

	// Get registered migrations
	registeredMigrations := listMigrations()

	// Generate warnings
	warnings := []string{}
	if issueCount > 0 && prefix == "" {
		detectedPrefix := ""
		if issues, err := store.SearchIssues(ctx, "", types.IssueFilter{}); err == nil && len(issues) > 0 {
			detectedPrefix = utils.ExtractIssuePrefix(issues[0].ID)
		}
		warnings = append(warnings, fmt.Sprintf("issue_prefix config not set - may break commands after migration (detected: %s)", detectedPrefix))
	}
	if schemaVersion != Version {
		warnings = append(warnings, fmt.Sprintf("schema version mismatch (current: %s, expected: %s)", schemaVersion, Version))
	}

	// Output result
	result := map[string]interface{}{
		"registered_migrations": registeredMigrations,
		"current_state": map[string]interface{}{
			"schema_version": schemaVersion,
			"issue_count":    issueCount,
			"config":         configMap,
			"missing_config": missingConfig,
			"db_exists":      true,
		},
		"warnings":            warnings,
		"invariants_to_check": []string{},
	}

	if jsonOutput {
		outputJSON(result)
	} else {
		fmt.Println("\nMigration Inspection")
		fmt.Println("====================")
		fmt.Printf("Schema Version: %s\n", schemaVersion)
		fmt.Printf("Issue Count: %d\n", issueCount)
		fmt.Printf("Registered Migrations: %d\n", len(registeredMigrations))

		if len(warnings) > 0 {
			fmt.Println("\nWarnings:")
			for _, w := range warnings {
				fmt.Printf("  ⚠ %s\n", w)
			}
		}

		if len(missingConfig) > 0 {
			fmt.Println("\nMissing Config:")
			for _, k := range missingConfig {
				fmt.Printf("  - %s\n", k)
			}
		}
		fmt.Println()
	}
}

// handleToSeparateBranch configures separate branch workflow for existing repos
func handleToSeparateBranch(branch string, dryRun bool) {
	// Validate branch name
	b := strings.TrimSpace(branch)
	if b == "" || strings.ContainsAny(b, " \t\n") {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "invalid_branch",
				"message": "Branch name cannot be empty or contain whitespace",
			})
			os.Exit(1)
		}
		FatalErrorWithHint(fmt.Sprintf("invalid branch name '%s'", branch), "branch name cannot be empty or contain whitespace")
	}

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": "No .beads directory found. Run 'bd init' first.",
			})
			os.Exit(1)
		}
		FatalErrorWithHint("no .beads directory found", "run 'bd init' to initialize bd")
	}

	// Load config
	cfg, err := loadOrCreateConfig(beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_load_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to load config: %v", err)
	}

	// Check database exists
	targetPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_missing",
				"message": "Database not found. Run 'bd init' first.",
			})
			os.Exit(1)
		}
		FatalErrorWithHint(fmt.Sprintf("database not found: %s", targetPath), "run 'bd init' to initialize bd")
	}

	// Open database
	store, err := dolt.NewFromConfig(rootCtx, beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_open_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to open database: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Get current sync.branch config
	ctx := rootCtx
	current, _ := store.GetConfig(ctx, "sync.branch")

	// Dry-run mode
	if dryRun {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"dry_run":  true,
				"previous": current,
				"branch":   b,
				"changed":  current != b,
			})
		} else {
			fmt.Println("Dry run mode - no changes will be made")
			if current == b {
				fmt.Printf("sync.branch already set to '%s'\n", b)
			} else {
				fmt.Printf("Would set sync.branch: '%s' → '%s'\n", current, b)
			}
		}
		return
	}

	// Check if already set
	if current == b {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":  "noop",
				"branch":  b,
				"message": "sync.branch already set to this value",
			})
		} else {
			fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ sync.branch already set to '%s'", b)))
			fmt.Println("No changes needed")
		}
		return
	}

	// Update sync.branch config
	if err := store.SetConfig(ctx, "sync.branch", b); err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_update_failed",
				"message": err.Error(),
			})
			os.Exit(1)
		}
		FatalError("failed to set sync.branch: %v", err)
	}

	// Success output
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":   "success",
			"previous": current,
			"branch":   b,
			"message":  "Enabled separate branch workflow",
		})
	} else {
		fmt.Printf("%s\n\n", ui.RenderPass("✓ Enabled separate branch workflow"))
		fmt.Printf("Set sync.branch to '%s'\n\n", b)
		fmt.Println("Next steps:")
		fmt.Println("  1. No restart required. sync.branch is active immediately.")
		fmt.Printf("     bd dolt push\n\n")
		fmt.Println("  2. Your existing data is preserved - no changes to git history")
		fmt.Println("  3. Future issue updates are stored in Dolt directly")
	}
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	// nolint:gosec // G304: src is validated migration backup path
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// nolint:gosec // G306: backup file needs to be readable by other tools
	return os.WriteFile(dst, data, 0644)
}

// migrateSyncCmd is the "bd migrate sync <branch>" subcommand that
// configures the separate-branch workflow for multi-clone setups.
// Previously this was documented but never wired as an actual subcommand,
// so bd doctor's recommendation to run "bd migrate sync beads-sync" would fail.
var migrateSyncCmd = &cobra.Command{
	Use:   "sync <branch>",
	Short: "Set up sync.branch workflow for multi-clone setups",
	Long: `Configure separate branch workflow for multi-clone setups.

This sets the sync.branch config value so that issue data is committed
to a dedicated branch, keeping your main branch clean.

Example:
  bd migrate sync beads-sync`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if !dryRun {
			CheckReadonly("migrate sync")
		}
		handleToSeparateBranch(args[0], dryRun)
	},
}

func init() {
	migrateCmd.Flags().Bool("yes", false, "Auto-confirm prompts")
	migrateCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateCmd.Flags().Bool("to-dolt", false, "Migrate from SQLite to Dolt backend")
	migrateCmd.Flags().Bool("to-sqlite", false, "Migrate from Dolt to SQLite (no longer supported)")
	migrateCmd.Flags().Bool("update-repo-id", false, "Update repository ID (use after changing git remote)")
	migrateCmd.Flags().Bool("inspect", false, "Show migration plan and database state for AI agent analysis")
	migrateCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output migration statistics in JSON format")

	migrateSyncCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateSyncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	migrateCmd.AddCommand(migrateSyncCmd)

	rootCmd.AddCommand(migrateCmd)
}
