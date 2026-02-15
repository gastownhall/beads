package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	storagefactory "github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/syncbranch"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var migrateCmd = &cobra.Command{
	Use:     "migrate",
	GroupID: "maint",
	Short:   "Database migration commands",
	Long: `Database migration and data transformation commands.

Without subcommand, detects and migrates database schema to current version:
- Finds all .db files in .beads/
- Checks schema versions
- Migrates old databases to beads.db
- Updates schema version metadata
- Removes stale databases (with confirmation)

Backend migration flags:
  --to-dolt     Migrate from SQLite to Dolt backend
  --to-sqlite   Migrate from Dolt to SQLite backend (escape hatch)

Subcommands:
  hash-ids    Migrate sequential IDs to hash-based IDs (legacy)
  issues      Move issues between repositories
  sync        Set up sync.branch workflow for multi-clone setups
`,
	Run: func(cmd *cobra.Command, _ []string) {
		autoYes, _ := cmd.Flags().GetBool("yes")
		cleanup, _ := cmd.Flags().GetBool("cleanup")
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

		// Handle --to-sqlite flag (Dolt to SQLite migration, escape hatch)
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

		// Load config to get target database name (respects user's config.json)
		cfg, err := loadOrCreateConfig(beadsDir)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "config_load_failed",
					"message": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
			}
			os.Exit(1)
		}

		// For Dolt backend, handle metadata update directly via storage factory
		// (detectDatabases only finds .db files, which don't exist for Dolt)
		if cfg.GetBackend() == configfile.BackendDolt {
			handleDoltMetadataUpdate(cfg, beadsDir, dryRun)
			return
		}

		// Detect all database files (SQLite backend only)
		databases, err := detectDatabases(beadsDir)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "detection_failed",
					"message": err.Error(),
				})
				os.Exit(1)
			} else {
				FatalError("%v", err)
			}
		}

		if len(databases) == 0 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"status":  "no_databases",
					"message": "No database files found in .beads/",
				})
			} else {
				fmt.Fprintf(os.Stderr, "No database files found in %s\n", beadsDir)
				fmt.Fprintf(os.Stderr, "Run 'bd init' to create a new database.\n")
			}
			return
		}

		// Check if target database exists and is current (use metadata.json name)
		targetPath := cfg.DatabasePath(beadsDir)
		var currentDB *dbInfo
		var oldDBs []*dbInfo

		for _, db := range databases {
			if db.path == targetPath {
				currentDB = db
			} else {
				oldDBs = append(oldDBs, db)
			}
		}

		// Print status
		if !jsonOutput {
			fmt.Printf("Database migration status:\n\n")
			if currentDB != nil {
				fmt.Printf("  Current database: %s\n", filepath.Base(currentDB.path))
				fmt.Printf("  Schema version: %s\n", currentDB.version)
				if currentDB.version != Version {
					fmt.Printf("  ⚠ %s\n", ui.RenderWarn(fmt.Sprintf("Version mismatch (current: %s, expected: %s)", currentDB.version, Version)))
				} else {
					fmt.Printf("  %s\n", ui.RenderPass("✓ Version matches"))
				}
			} else {
				fmt.Printf("  %s\n", ui.RenderWarn(fmt.Sprintf("No %s found", cfg.Database)))
			}

			if len(oldDBs) > 0 {
				fmt.Printf("\n  Old databases found:\n")
				for _, db := range oldDBs {
					fmt.Printf("    - %s (version: %s)\n", filepath.Base(db.path), db.version)
				}
			}
			fmt.Println()
		}

		// Determine migration actions
		needsMigration := false
		needsVersionUpdate := false

		if currentDB == nil && len(oldDBs) == 1 {
			// Migrate single old database to beads.db
			needsMigration = true
		} else if currentDB == nil && len(oldDBs) > 1 {
			// Multiple old databases - ambiguous
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":     "ambiguous_migration",
					"message":   "Multiple old database files found",
					"databases": formatDBList(oldDBs),
				})
				os.Exit(1)
			} else {
				fmt.Fprintf(os.Stderr, "Error: multiple old database files found:\n")
				for _, db := range oldDBs {
					fmt.Fprintf(os.Stderr, "  - %s (version: %s)\n", filepath.Base(db.path), db.version)
				}
				fmt.Fprintf(os.Stderr, "\nPlease manually rename the correct database to %s and remove others.\n", cfg.Database)
				os.Exit(1)
			}
		} else if currentDB != nil && currentDB.version != Version {
			// Update version metadata
			needsVersionUpdate = true
		}

		// Perform migrations
		if dryRun {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"dry_run":              true,
					"needs_migration":      needsMigration,
					"needs_version_update": needsVersionUpdate,
					"old_databases":        formatDBList(oldDBs),
				})
			} else {
				fmt.Println("Dry run mode - no changes will be made")
				if needsMigration && len(oldDBs) > 0 {
					fmt.Printf("Would migrate: %s → %s\n", filepath.Base(oldDBs[0].path), cfg.Database)
				}
				if needsVersionUpdate {
					fmt.Printf("Would update version: %s → %s\n", currentDB.version, Version)
				}
				if cleanup && len(oldDBs) > 0 {
					fmt.Printf("Would remove %d old database(s)\n", len(oldDBs))
				}
			}
			return
		}

		// Migrate old database to target name (from config.json)
		if needsMigration {
			oldDB := oldDBs[0]
			if !jsonOutput {
				fmt.Printf("Migrating database: %s → %s\n", filepath.Base(oldDB.path), cfg.Database)
			}

			// Create backup before migration
			if !dryRun {
				backupPath := strings.TrimSuffix(oldDB.path, ".db") + ".backup-pre-migrate-" + time.Now().Format("20060102-150405") + ".db"
				if err := copyFile(oldDB.path, backupPath); err != nil {
					if jsonOutput {
						outputJSON(map[string]interface{}{
							"error":   "backup_failed",
							"message": err.Error(),
						})
					} else {
						fmt.Fprintf(os.Stderr, "Error: failed to create backup: %v\n", err)
					}
					os.Exit(1)
				}
				if !jsonOutput {
					fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Created backup: %s", filepath.Base(backupPath))))
				}
			}

			if err := os.Rename(oldDB.path, targetPath); err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "migration_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to migrate database: %v\n", err)
				}
				os.Exit(1)
			}

			// Clean up orphaned WAL files from old database
			cleanupWALFiles(oldDB.path)

			// Update current DB reference
			currentDB = oldDB
			currentDB.path = targetPath
			needsVersionUpdate = true

			if !jsonOutput {
				fmt.Printf("%s\n\n", ui.RenderPass("✓ Migration complete"))
			}
		}

		// Update schema version if needed
		if needsVersionUpdate && currentDB != nil {
			if !jsonOutput {
				fmt.Printf("Updating schema version: %s → %s\n", currentDB.version, Version)
			}

			// Clean up WAL files before opening to avoid "disk I/O error"
			cleanupWALFiles(currentDB.path)

			store, err := storagefactory.NewFromConfigWithOptions(rootCtx, beadsDir, storagefactory.Options{})
			if err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "version_update_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				}
				os.Exit(1)
			}

			ctx := rootCtx

			// Detect and set issue_prefix if missing (fixes GH #201)
			prefix, err := store.GetConfig(ctx, "issue_prefix")
			if err != nil || prefix == "" {
				// Get first issue to detect prefix
				issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
				if err == nil && len(issues) > 0 {
					detectedPrefix := utils.ExtractIssuePrefix(issues[0].ID)
					if detectedPrefix != "" {
						if err := store.SetConfig(ctx, "issue_prefix", detectedPrefix); err != nil {
							_ = store.Close()
							if jsonOutput {
								outputJSON(map[string]interface{}{
									"error":   "prefix_detection_failed",
									"message": err.Error(),
								})
							} else {
								fmt.Fprintf(os.Stderr, "Error: failed to set issue prefix: %v\n", err)
							}
							os.Exit(1)
						}
						if !jsonOutput {
							fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Detected and set issue prefix: %s", detectedPrefix)))
						}
					}
				}
			}

			if err := store.SetMetadata(ctx, "bd_version", Version); err != nil {
				_ = store.Close()
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "version_update_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to update version: %v\n", err)
				}
				os.Exit(1)
			}

			// Close and checkpoint to finalize the WAL
			if err := store.Close(); err != nil {
				if !jsonOutput {
					fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: error closing database: %v", err)))
				}
			}

			if !jsonOutput {
				fmt.Printf("%s\n\n", ui.RenderPass("✓ Version updated"))
			}
		}

		// Clean up old databases
		if cleanup && len(oldDBs) > 0 {
			// If we migrated one database, remove it from the cleanup list
			if needsMigration {
				oldDBs = oldDBs[1:]
			}

			if len(oldDBs) > 0 {
				if !autoYes && !jsonOutput {
					fmt.Printf("Found %d old database file(s):\n", len(oldDBs))
					for _, db := range oldDBs {
						fmt.Printf("  - %s (version: %s)\n", filepath.Base(db.path), db.version)
					}
					fmt.Print("\nRemove these files? [y/N] ")
					var response string
					_, _ = fmt.Scanln(&response)
					if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
						fmt.Println("Cleanup canceled")
						return
					}
				}

				for _, db := range oldDBs {
					if err := os.Remove(db.path); err != nil {
						if !jsonOutput {
							fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: failed to remove %s: %v", filepath.Base(db.path), err)))
						}
					} else if !jsonOutput {
						fmt.Printf("Removed %s\n", filepath.Base(db.path))
					}
				}

				if !jsonOutput {
					fmt.Printf("\n%s\n", ui.RenderPass("✓ Cleanup complete"))
				}
			}
		}

		// Save updated config
		if !dryRun {
			if err := cfg.Save(beadsDir); err != nil {
				if !jsonOutput {
					fmt.Printf("%s\n", ui.RenderWarn(fmt.Sprintf("Warning: failed to save metadata.json: %v", err)))
				}
				// Don't fail migration if config save fails
			}
		}

		// Final status
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":           "success",
				"current_database": cfg.Database,
				"version":          Version,
				"migrated":         needsMigration,
				"version_updated":  needsVersionUpdate,
				"cleaned_up":       cleanup && len(oldDBs) > 0,
			})
		} else {
			fmt.Println("\nMigration complete!")
			fmt.Printf("Current database: %s (version %s)\n", cfg.Database, Version)
		}
	},
}

type dbInfo struct {
	path    string
	version string
}

// handleDoltMetadataUpdate handles version metadata updates for Dolt backends.
// The standard migration flow (detectDatabases, rename .db files) is SQLite-only.
// For Dolt, we ensure version metadata and identity fields are set correctly.
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
			fmt.Fprintf(os.Stderr, "Run 'bd init' to create a new database, or 'bd migrate --to-dolt' to migrate from SQLite.\n")
		}
		return
	}

	// Open database using storage factory
	ctx := rootCtx
	store, err := storagefactory.NewFromConfigWithOptions(ctx, beadsDir, storagefactory.Options{})
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "open_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to open Dolt database: %v\n", err)
		}
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	// Check current state of all metadata fields
	// Best effort: empty metadata means first-time setup
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
			} else {
				fmt.Fprintf(os.Stderr, "Error: failed to update version: %v\n", err)
			}
			os.Exit(1)
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

func detectDatabases(beadsDir string) ([]*dbInfo, error) {
	pattern := filepath.Join(beadsDir, "*.db")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to search for databases: %w", err)
	}

	var databases []*dbInfo
	for _, match := range matches {
		// Skip backup files
		if strings.HasSuffix(match, ".backup.db") {
			continue
		}

		// Check if file exists and is readable
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}

		// Get version from database
		version := getDBVersion(match)
		databases = append(databases, &dbInfo{
			path:    match,
			version: version,
		})
	}

	return databases, nil
}

func getDBVersion(dbPath string) string {
	// Open database read-only using file URI (same as production code)
	connStr := "file:" + dbPath + "?mode=ro&_time_format=sqlite"
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return "unknown"
	}
	defer db.Close()

	// Ping to ensure connection is actually established
	if err := db.Ping(); err != nil {
		return "unknown"
	}

	// Try to read version from metadata table
	var version string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&version)
	if err == nil {
		return version
	}

	// If the row doesn't exist but table does, this is still a database with metadata
	// Check if metadata table exists
	var tableName string
	err = db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='metadata'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		return "pre-0.17.5"
	}

	// Table exists but version query failed (probably no bd_version key)
	if err == nil {
		return "unknown"
	}

	return "unknown"
}

func formatDBList(dbs []*dbInfo) []map[string]string {
	result := make([]map[string]string, len(dbs))
	for i, db := range dbs {
		result[i] = map[string]string{
			"path":    db.path,
			"name":    filepath.Base(db.path),
			"version": db.version,
		}
	}
	return result
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
		} else {
			fmt.Fprintf(os.Stderr, "Error: no beads database found\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to initialize bd\n")
		}
		os.Exit(1)
	}

	// Compute new repo ID
	newRepoID, err := beads.ComputeRepoID()
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "compute_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to compute repository ID: %v\n", err)
		}
		os.Exit(1)
	}

	// Open database using storage factory (supports both SQLite and Dolt backends)
	store, err := storagefactory.NewFromConfigWithOptions(rootCtx, beadsDir, storagefactory.Options{})
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "open_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
		}
		os.Exit(1)
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
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to read repo_id: %v\n", err)
		}
		os.Exit(1)
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
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to update repo_id: %v\n", err)
		}
		os.Exit(1)
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

// truncateID safely truncates an ID string to maxLen characters.
// Returns the full string if it's shorter than maxLen.
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

// cleanupWALFiles removes orphaned WAL and SHM files for a given database path
func cleanupWALFiles(dbPath string) {
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"

	// Best effort cleanup of SQLite WAL/SHM files
	_ = os.Remove(walPath)
	_ = os.Remove(shmPath)
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
		} else {
			fmt.Fprintf(os.Stderr, "Error: no .beads directory found\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to initialize bd\n")
		}
		os.Exit(1)
	}

	// Load config
	cfg, err := loadOrCreateConfig(beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_load_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		}
		os.Exit(1)
	}

	// Check if database exists (don't create it)
	targetPath := cfg.DatabasePath(beadsDir)
	dbExists := false
	if _, err := os.Stat(targetPath); err == nil {
		dbExists = true
	} else if !os.IsNotExist(err) {
		// Stat error (not just "doesn't exist")
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_stat_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to check database: %v\n", err)
		}
		os.Exit(1)
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

	// Open database in read-only mode for inspection (supports both SQLite and Dolt)
	store, err := storagefactory.NewFromConfigWithOptions(rootCtx, beadsDir, storagefactory.Options{ReadOnly: true})
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_open_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
		}
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	ctx := rootCtx

	// Get current schema version
	schemaVersion, err := store.GetMetadata(ctx, "bd_version")
	if err != nil {
		schemaVersion = "unknown"
	}

	// Get issue count (use efficient COUNT query)
	issueCount := 0
	if stats, err := store.GetStatistics(ctx); err == nil {
		issueCount = stats.TotalIssues
	}

	// Get config
	configMap := make(map[string]string)
	prefix, _ := store.GetConfig(ctx, "issue_prefix") // Best effort: empty prefix is valid
	if prefix != "" {
		configMap["issue_prefix"] = prefix
	}

	// Detect missing config
	missingConfig := []string{}
	if issueCount > 0 && prefix == "" {
		missingConfig = append(missingConfig, "issue_prefix")
	}

	// Get registered migrations (all migrations are idempotent and run on every open)
	registeredMigrations := listMigrations()

	// Build invariants list
	invariantNames := []string{}

	// Generate warnings
	warnings := []string{}
	if issueCount > 0 && prefix == "" {
		// Detect prefix from first issue (efficient query for just 1 issue)
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
		"invariants_to_check": invariantNames,
	}

	if jsonOutput {
		outputJSON(result)
	} else {
		// Human-readable output
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

		fmt.Printf("\nInvariants to Check: %d\n", len(invariantNames))
		for _, inv := range invariantNames {
			fmt.Printf("  ✓ %s\n", inv)
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
		} else {
			fmt.Fprintf(os.Stderr, "Error: invalid branch name '%s'\n", branch)
			fmt.Fprintf(os.Stderr, "Branch name cannot be empty or contain whitespace\n")
		}
		os.Exit(1)
	}

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": "No .beads directory found. Run 'bd init' first.",
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: no .beads directory found\n")
			fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to initialize bd\n")
		}
		os.Exit(1)
	}

	// Load config
	cfg, err := loadOrCreateConfig(beadsDir)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_load_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		}
		os.Exit(1)
	}

	// Check database exists
	targetPath := cfg.DatabasePath(beadsDir)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_missing",
				"message": "Database not found. Run 'bd init' first.",
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: database not found: %s\n", targetPath)
			fmt.Fprintf(os.Stderr, "Hint: run 'bd init' to initialize bd\n")
		}
		os.Exit(1)
	}

	// Open database (supports both SQLite and Dolt backends)
	store, err := storagefactory.NewFromConfigWithOptions(rootCtx, beadsDir, storagefactory.Options{})
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "database_open_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
		}
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()

	// Get current sync.branch config
	ctx := rootCtx
	current, _ := store.GetConfig(ctx, "sync.branch") // Best effort: empty means no sync branch configured

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

	// Update sync.branch config (GH#923: use syncbranch.Set for validation)
	if err := syncbranch.Set(ctx, store, b); err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "config_update_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: failed to set sync.branch: %v\n", err)
		}
		os.Exit(1)
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
		fmt.Println("  1. Restart the daemon to create worktree and start committing to the branch:")
		fmt.Printf("     bd daemon restart\n")
		fmt.Printf("     bd daemon start --auto-commit\n\n")
		fmt.Println("  2. Your existing data is preserved - no changes to git history")
		fmt.Println("  3. Future issue updates will be committed to the separate branch")
		fmt.Println("\nSee docs/PROTECTED_BRANCHES.md for complete workflow guide")
	}
}

func init() {
	migrateCmd.Flags().Bool("yes", false, "Auto-confirm cleanup prompts")
	migrateCmd.Flags().Bool("cleanup", false, "Remove old database files after migration")
	migrateCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateCmd.Flags().Bool("update-repo-id", false, "Update repository ID (use after changing git remote)")
	migrateCmd.Flags().Bool("inspect", false, "Show migration plan and database state for AI agent analysis")
	migrateCmd.Flags().Bool("to-dolt", false, "Migrate from SQLite to Dolt backend")
	migrateCmd.Flags().Bool("to-sqlite", false, "Migrate from Dolt to SQLite backend (escape hatch)")
	migrateCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output migration statistics in JSON format")
	rootCmd.AddCommand(migrateCmd)
}
