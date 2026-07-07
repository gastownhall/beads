package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
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

Subcommands:
  hooks       Plan git hook migration to marker-managed format
  issues      Move issues between repositories
  schema      Apply pending schema migrations (idempotent)
  sync        Set up sync.branch workflow for multi-clone setups
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("migrate")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		autoYes, _ := cmd.Flags().GetBool("yes")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		updateRepoID, _ := cmd.Flags().GetBool("update-repo-id")
		inspect, _ := cmd.Flags().GetBool("inspect")

		if !dryRun && !inspect {
			CheckReadonly("migrate")
		}

		if updateRepoID {
			return handleUpdateRepoID(dryRun, autoYes)
		}

		if inspect {
			return handleInspect()
		}

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			if jsonOutput {
				if jerr := outputJSON(map[string]interface{}{
					"error":   "no_beads_directory",
					"message": activeWorkspaceNotFoundMessage() + " " + diagHint() + ".",
				}); jerr != nil {
					return jerr
				}
				return SilentExit()
			}
			return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
		}

		cfg, err := loadOrCreateConfig(beadsDir)
		if err != nil {
			if jsonOutput {
				if jerr := outputJSON(map[string]interface{}{
					"error":   "config_load_failed",
					"message": err.Error(),
				}); jerr != nil {
					return jerr
				}
				return SilentExit()
			}
			return HandleError("failed to load config: %v", err)
		}

		return handleDoltMetadataUpdate(cfg, dryRun)
	},
}

func handleDoltMetadataUpdate(cfg *configfile.Config, dryRun bool) error {
	ctx := rootCtx
	store := getStore()
	if store == nil {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":  "no_databases",
				"message": "No Dolt database found in .beads/",
			})
		}
		fmt.Fprintf(os.Stderr, "No Dolt database found. Run 'bd init' to create a new database.\n")
		return nil
	}

	currentVersion, _ := store.GetLocalMetadata(ctx, "bd_version")
	currentRepoID, _ := store.GetMetadata(ctx, "repo_id")
	currentCloneID, _ := store.GetMetadata(ctx, "clone_id")

	needsVersionUpdate := currentVersion != Version
	needsRepoID := currentRepoID == ""
	needsCloneID := currentCloneID == ""

	if !needsVersionUpdate && !needsRepoID && !needsCloneID {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":  "current",
				"message": fmt.Sprintf("Dolt database already at version %s", Version),
			})
		}
		fmt.Printf("Dolt database version: %s\n", currentVersion)
		fmt.Printf("%s\n", ui.RenderPass("✓ Version matches"))
		fmt.Printf("%s\n", ui.RenderPass("✓ All metadata fields present"))
		return nil
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
			return outputJSON(dryRunResult)
		}
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
		return nil
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

		if err := store.SetLocalMetadata(ctx, "bd_version", Version); err != nil {
			if jsonOutput {
				if jerr := outputJSON(map[string]interface{}{
					"error":   "version_update_failed",
					"message": err.Error(),
				}); jerr != nil {
					return jerr
				}
				return SilentExit()
			}
			return HandleError("failed to update version: %v", err)
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

	if versionUpdated || repoIDSet || cloneIDSet {
		commandDidWrite.Store(true)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":           "success",
			"current_database": cfg.Database,
			"backend":          "dolt",
			"version":          Version,
			"version_updated":  versionUpdated,
			"repo_id_set":      repoIDSet,
			"clone_id_set":     cloneIDSet,
		})
	}
	fmt.Printf("\nDolt database: %s (version %s)\n", cfg.Database, Version)
	return nil
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

func handleUpdateRepoID(dryRun bool, autoYes bool) error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "no_database",
				"message": "No beads database found. " + diagHint() + ".",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint("no beads database found", diagHint())
	}

	newRepoID, err := beads.ComputeRepoID()
	if err != nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "compute_failed",
				"message": err.Error(),
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("failed to compute repository ID: %v", err)
	}

	store := getStore()
	if store == nil {
		return HandleError("no database — run 'bd init' first")
	}

	ctx := rootCtx
	oldRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil && err.Error() != "metadata key not found: repo_id" {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "read_failed",
				"message": err.Error(),
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("failed to read repo_id: %v", err)
	}

	oldDisplay := "none"
	if len(oldRepoID) >= 8 {
		oldDisplay = oldRepoID[:8]
	}

	if dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"dry_run":     true,
				"old_repo_id": oldDisplay,
				"new_repo_id": truncateID(newRepoID, 8),
			})
		}
		fmt.Println("Dry run mode - no changes will be made")
		fmt.Printf("Would update repository ID:\n")
		fmt.Printf("  Old: %s\n", oldDisplay)
		fmt.Printf("  New: %s\n", truncateID(newRepoID, 8))
		return nil
	}

	if oldRepoID != "" && oldRepoID != newRepoID && !autoYes && !jsonOutput {
		fmt.Printf("WARNING: Changing repository ID can break sync if other clones exist.\n\n")
		fmt.Printf("Current repo ID: %s\n", oldDisplay)
		fmt.Printf("New repo ID:     %s\n\n", truncateID(newRepoID, 8))
		fmt.Printf("Continue? [y/N] ")
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Canceled")
			return nil
		}
	}

	if err := store.SetMetadata(ctx, "repo_id", newRepoID); err != nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "update_failed",
				"message": err.Error(),
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("failed to update repo_id: %v", err)
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":      "success",
			"old_repo_id": oldDisplay,
			"new_repo_id": truncateID(newRepoID, 8),
		})
	}
	fmt.Printf("%s\n\n", ui.RenderPass("✓ Repository ID updated"))
	fmt.Printf("  Old: %s\n", oldDisplay)
	fmt.Printf("  New: %s\n", truncateID(newRepoID, 8))
	return nil
}

func handleInspect() error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": activeWorkspaceNotFoundMessage() + " " + diagHint() + ".",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	dbExists := getStore() != nil

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
			"warnings":            []string{"Database does not exist - " + diagHint()},
			"invariants_to_check": []string{},
		}

		if jsonOutput {
			return outputJSON(result)
		}
		fmt.Println("\nMigration Inspection")
		fmt.Println("====================")
		fmt.Println("Database: missing")
		fmt.Println("\n⚠ Database does not exist - " + diagHint())
		return nil
	}

	store := getStore()
	if store == nil {
		return HandleError("no database — run 'bd init' first")
	}

	ctx := rootCtx

	// Get current schema version
	schemaVersion, err := store.GetLocalMetadata(ctx, "bd_version")
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
		return outputJSON(result)
	}
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
	return nil
}

func handleSchemaMigrate() error {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": activeWorkspaceNotFoundMessage() + " " + diagHint() + ".",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	store := getStore()
	if store == nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "no_database",
				"message": "No database found. Run 'bd init' to create a new database.",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint("no database", "Run 'bd init' to create a new database")
	}

	migrator, ok := storage.UnwrapStore(store).(storage.SchemaMigrator)
	if !ok {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "unsupported_backend",
				"message": "current storage backend does not support schema migration",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("current storage backend does not support schema migration")
	}

	applied, err := migrator.ApplySchemaMigrations(rootCtx)
	if err != nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "schema_migration_failed",
				"message": err.Error(),
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("schema migration failed: %v", err)
	}

	latest := schema.LatestVersion()
	status := "current"
	if applied > 0 {
		status = "applied"
		commandDidWrite.Store(true)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":         status,
			"applied":        applied,
			"latest_version": latest,
		})
	}

	if applied == 0 {
		fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Schema already at v%d", latest)))
		return nil
	}
	fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Applied %d schema migration(s); schema now at v%d", applied, latest)))
	return nil
}

func handleToSeparateBranch(branch string, dryRun bool) error {
	b := strings.TrimSpace(branch)
	if b == "" || strings.ContainsAny(b, " \t\n") {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "invalid_branch",
				"message": "Branch name cannot be empty or contain whitespace",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint(fmt.Sprintf("invalid branch name '%s'", branch), "branch name cannot be empty or contain whitespace")
	}

	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": activeWorkspaceNotFoundMessage() + " " + diagHint() + ".",
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleErrorWithHint(activeWorkspaceNotFoundError(), diagHint())
	}

	store := getStore()
	if store == nil {
		return HandleError("no database — run 'bd init' first")
	}

	ctx := rootCtx
	current, _ := store.GetConfig(ctx, "sync.branch")

	if dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"dry_run":  true,
				"previous": current,
				"branch":   b,
				"changed":  current != b,
			})
		}
		fmt.Println("Dry run mode - no changes will be made")
		if current == b {
			fmt.Printf("sync.branch already set to '%s'\n", b)
		} else {
			fmt.Printf("Would set sync.branch: '%s' → '%s'\n", current, b)
		}
		return nil
	}

	if current == b {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status":  "noop",
				"branch":  b,
				"message": "sync.branch already set to this value",
			})
		}
		fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ sync.branch already set to '%s'", b)))
		fmt.Println("No changes needed")
		return nil
	}

	if err := store.SetConfig(ctx, "sync.branch", b); err != nil {
		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"error":   "config_update_failed",
				"message": err.Error(),
			}); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("failed to set sync.branch: %v", err)
	}

	commandDidWrite.Store(true)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"status":   "success",
			"previous": current,
			"branch":   b,
			"message":  "Enabled separate branch workflow",
		})
	}
	fmt.Printf("%s\n\n", ui.RenderPass("✓ Enabled separate branch workflow"))
	fmt.Printf("Set sync.branch to '%s'\n\n", b)
	fmt.Println("Next steps:")
	fmt.Println("  1. No restart required. sync.branch is active immediately.")
	fmt.Printf("     bd dolt push\n\n")
	fmt.Println("  2. Your existing data is preserved - no changes to git history")
	fmt.Println("  3. Future issue updates are stored in Dolt directly")
	return nil
}

// listMigrations returns registered Dolt schema migrations. The compat runner
// was retired once all historical migrations had SQL equivalents; this is
// kept as a stable hook for `bd migrate --inspect` output.
func listMigrations() []string {
	return nil
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("migrate-sync")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if !dryRun {
			CheckReadonly("migrate sync")
		}
		return handleToSeparateBranch(args[0], dryRun)
	},
}

var migrateSchemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Apply pending schema migrations (idempotent)",
	Long: `Apply pending schema migrations idempotently.

Schema migrations also run automatically on store open, so this subcommand
is typically a no-op. It exists to make migration explicit and observable
in CI, release gates, and recovery scenarios.

Example:
  bd migrate schema
  bd migrate schema --json`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		CheckReadonly("migrate schema")

		evt := metrics.NewCommandEvent("migrate-schema")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return handleSchemaMigrate()
	},
}

func init() {
	migrateCmd.Flags().Bool("yes", false, "Auto-confirm prompts")
	migrateCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateCmd.Flags().Bool("update-repo-id", false, "Update repository ID (use after changing git remote)")
	migrateCmd.Flags().Bool("inspect", false, "Show migration plan and database state for AI agent analysis")
	migrateCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output migration statistics in JSON format")

	migrateSyncCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateSyncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	migrateCmd.AddCommand(migrateSyncCmd)

	migrateHooksCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateHooksCmd.Flags().Bool("apply", false, "Apply planned hook migration changes")
	migrateHooksCmd.Flags().Bool("yes", false, "Skip confirmation prompt for --apply")
	migrateHooksCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	migrateCmd.AddCommand(migrateHooksCmd)

	migrateSchemaCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	migrateCmd.AddCommand(migrateSchemaCmd)

	rootCmd.AddCommand(migrateCmd)
}
