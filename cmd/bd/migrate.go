package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate database to current version",
	Long: `Detect and migrate database files to the current version.

This command:
- Finds all .db files in .beads/
- Checks schema versions
- Migrates old databases to beads.db
- Updates schema version metadata
- Migrates sequential IDs to hash-based IDs (with --to-hash-ids)
- Removes stale databases (with confirmation)`,
	Run: func(cmd *cobra.Command, _ []string) {
		autoYes, _ := cmd.Flags().GetBool("yes")
		cleanup, _ := cmd.Flags().GetBool("cleanup")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		updateRepoID, _ := cmd.Flags().GetBool("update-repo-id")
		toHashIDs, _ := cmd.Flags().GetBool("to-hash-ids")
		inspect, _ := cmd.Flags().GetBool("inspect")

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

		// Find .beads directory
		beadsDir := findBeadsDir()
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

	// Detect all database files
		databases, err := detectDatabases(beadsDir)
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":   "detection_failed",
					"message": err.Error(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(1)
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
					color.Yellow("  ⚠ Version mismatch (current: %s, expected: %s)\n", currentDB.version, Version)
				} else {
					color.Green("  ✓ Version matches\n")
				}
			} else {
				color.Yellow("  No %s found\n", cfg.Database)
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
			} else {
				fmt.Fprintf(os.Stderr, "Error: multiple old database files found:\n")
				for _, db := range oldDBs {
					fmt.Fprintf(os.Stderr, "  - %s (version: %s)\n", filepath.Base(db.path), db.version)
				}
				fmt.Fprintf(os.Stderr, "\nPlease manually rename the correct database to %s and remove others.\n", cfg.Database)
			}
			os.Exit(1)
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
				if needsMigration {
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
					color.Green("✓ Created backup: %s\n", filepath.Base(backupPath))
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
				color.Green("✓ Migration complete\n\n")
			}
		}

		// Update schema version if needed
		if needsVersionUpdate && currentDB != nil {
			if !jsonOutput {
				fmt.Printf("Updating schema version: %s → %s\n", currentDB.version, Version)
			}

			// Clean up WAL files before opening to avoid "disk I/O error"
			cleanupWALFiles(currentDB.path)

			store, err := sqlite.New(currentDB.path)
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

			ctx := context.Background()
			
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
							color.Green("✓ Detected and set issue prefix: %s\n", detectedPrefix)
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
					color.Yellow("Warning: error closing database: %v\n", err)
				}
			}

			if !jsonOutput {
				color.Green("✓ Version updated\n\n")
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
							color.Yellow("Warning: failed to remove %s: %v\n", filepath.Base(db.path), err)
						}
					} else if !jsonOutput {
						fmt.Printf("Removed %s\n", filepath.Base(db.path))
					}
				}

				if !jsonOutput {
					color.Green("\n✓ Cleanup complete\n")
				}
			}
		}


		// Migrate to hash IDs if requested
		if toHashIDs {
			if !jsonOutput {
				fmt.Println("\n→ Migrating to hash-based IDs...")
			}
			
			store, err := sqlite.New(targetPath)
			if err != nil {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "hash_migration_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				}
				os.Exit(1)
			}
			
			ctx := context.Background()
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
			_ = store.Close()
			if jsonOutput {
					outputJSON(map[string]interface{}{
						"error":   "hash_migration_failed",
						"message": err.Error(),
					})
				} else {
					fmt.Fprintf(os.Stderr, "Error: failed to list issues: %v\n", err)
				}
				os.Exit(1)
			}
			
			if len(issues) > 0 && !isHashID(issues[0].ID) {
				// Create backup
				if !dryRun {
					backupPath := strings.TrimSuffix(targetPath, ".db") + ".backup-pre-hash-" + time.Now().Format("20060102-150405") + ".db"
					if err := copyFile(targetPath, backupPath); err != nil {
						_ = store.Close()
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
						color.Green("✓ Created backup: %s\n", filepath.Base(backupPath))
					}
					}
					
					mapping, err := migrateToHashIDs(ctx, store, issues, dryRun)
					_ = store.Close()
				
				if err != nil {
					if jsonOutput {
						outputJSON(map[string]interface{}{
							"error":   "hash_migration_failed",
							"message": err.Error(),
						})
					} else {
						fmt.Fprintf(os.Stderr, "Error: hash ID migration failed: %v\n", err)
					}
					os.Exit(1)
				}
				
				if !jsonOutput {
					if dryRun {
						fmt.Printf("\nWould migrate %d issues to hash-based IDs\n", len(mapping))
					} else {
						color.Green("✓ Migrated %d issues to hash-based IDs\n", len(mapping))
						}
						}
						} else {
						_ = store.Close()
				if !jsonOutput {
					fmt.Println("Database already uses hash-based IDs")
				}
			}
		}
		
		// Save updated config with current version (fixes GH #193)
		if !dryRun {
			if err := cfg.Save(beadsDir); err != nil {
				if !jsonOutput {
					color.Yellow("Warning: failed to update metadata.json version: %v\n", err)
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
	// Open database read-only
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return "unknown"
	}
	defer db.Close()

	// Try to read version from metadata table
	var version string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&version)
	if err == nil {
		return version
	}

	// Check if metadata table exists
	var tableName string
	err = db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='metadata'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		return "pre-0.17.5"
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
	// Find database
	foundDB := beads.FindDatabasePath()
	if foundDB == "" {
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

	// Open database
	store, err := sqlite.New(foundDB)
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
	ctx := context.Background()
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
				"new_repo_id": newRepoID[:8],
			})
		} else {
			fmt.Println("Dry run mode - no changes will be made")
			fmt.Printf("Would update repository ID:\n")
			fmt.Printf("  Old: %s\n", oldDisplay)
			fmt.Printf("  New: %s\n", newRepoID[:8])
		}
		return
	}

	// Prompt for confirmation if repo_id exists and differs
	if oldRepoID != "" && oldRepoID != newRepoID && !autoYes && !jsonOutput {
		fmt.Printf("WARNING: Changing repository ID can break sync if other clones exist.\n\n")
		fmt.Printf("Current repo ID: %s\n", oldDisplay)
		fmt.Printf("New repo ID:     %s\n\n", newRepoID[:8])
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
			"new_repo_id": newRepoID[:8],
		})
	} else {
		color.Green("✓ Repository ID updated\n\n")
		fmt.Printf("  Old: %s\n", oldDisplay)
		fmt.Printf("  New: %s\n", newRepoID[:8])
	}
}

// loadOrCreateConfig loads metadata.json or creates default if not found
func loadOrCreateConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, err
	}
	
	// Create default if no config exists
	if cfg == nil {
		cfg = configfile.DefaultConfig(Version)
	} else {
		// Update version field in existing config (fixes GH #193)
		cfg.Version = Version
	}
	
	return cfg, nil
}

// cleanupWALFiles removes orphaned WAL and SHM files for a given database path
func cleanupWALFiles(dbPath string) {
	walPath := dbPath + "-wal"
	shmPath := dbPath + "-shm"
	
	// Best effort - don't fail if these don't exist
	_ = os.Remove(walPath)
	_ = os.Remove(shmPath)
}

// handleInspect shows migration plan and database state for AI agent analysis
func handleInspect() {
	// Find .beads directory
	beadsDir := findBeadsDir()
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
			"registered_migrations": sqlite.ListMigrations(),
			"current_state": map[string]interface{}{
				"schema_version": "missing",
				"issue_count":    0,
				"config":         map[string]string{},
				"missing_config": []string{},
				"db_exists":      false,
			},
			"warnings":            []string{"Database does not exist - run 'bd init' first"},
			"invariants_to_check": sqlite.GetInvariantNames(),
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
	store, err := sqlite.New(targetPath)
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

	ctx := context.Background()

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
	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix != "" {
		configMap["issue_prefix"] = prefix
	}

	// Detect missing config
	missingConfig := []string{}
	if issueCount > 0 && prefix == "" {
		missingConfig = append(missingConfig, "issue_prefix")
	}

	// Get registered migrations (all migrations are idempotent and run on every open)
	registeredMigrations := sqlite.ListMigrations()
	
	// Build invariants list
	invariantNames := sqlite.GetInvariantNames()

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

func init() {
	migrateCmd.Flags().Bool("yes", false, "Auto-confirm cleanup prompts")
	migrateCmd.Flags().Bool("cleanup", false, "Remove old database files after migration")
	migrateCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateCmd.Flags().Bool("update-repo-id", false, "Update repository ID (use after changing git remote)")
	migrateCmd.Flags().Bool("to-hash-ids", false, "Migrate sequential IDs to hash-based IDs")
	migrateCmd.Flags().Bool("inspect", false, "Show migration plan and database state for AI agent analysis")
	migrateCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output migration statistics in JSON format")
	rootCmd.AddCommand(migrateCmd)
}
