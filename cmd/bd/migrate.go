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
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	_ "modernc.org/sqlite"
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

		// Handle --update-repo-id first
		if updateRepoID {
			handleUpdateRepoID(dryRun, autoYes)
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

		// Check if beads.db exists and is current
		targetPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
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
				color.Yellow("  No beads.db found\n")
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
				fmt.Fprintf(os.Stderr, "\nPlease manually rename the correct database to beads.db and remove others.\n")
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
					fmt.Printf("Would migrate: %s → beads.db\n", filepath.Base(oldDBs[0].path))
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

		// Migrate old database to beads.db
		if needsMigration {
			oldDB := oldDBs[0]
			if !jsonOutput {
				fmt.Printf("Migrating database: %s → beads.db\n", filepath.Base(oldDB.path))
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
			_ = store.Close()

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
					fmt.Scanln(&response)
					if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
						fmt.Println("Cleanup cancelled")
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
				store.Close()
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
						store.Close()
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
				store.Close()
				
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
				
				// Set id_mode=hash after successful migration (not in dry-run)
				if !dryRun {
					store, err := sqlite.New(targetPath)
					if err == nil {
						ctx := context.Background()
						if err := store.SetConfig(ctx, "id_mode", "hash"); err != nil {
							if !jsonOutput {
								fmt.Fprintf(os.Stderr, "Warning: failed to set id_mode=hash: %v\n", err)
							}
						} else if !jsonOutput {
							color.Green("✓ Switched database to hash ID mode\n")
						}
						store.Close()
					}
				}
			} else {
				store.Close()
				if !jsonOutput {
					fmt.Println("Database already uses hash-based IDs")
				}
			}
		}
		
		// Final status
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":           "success",
				"current_database": beads.CanonicalDatabaseName,
				"version":          Version,
				"migrated":         needsMigration,
				"version_updated":  needsVersionUpdate,
				"cleaned_up":       cleanup && len(oldDBs) > 0,
			})
		} else {
			fmt.Println("\nMigration complete!")
			fmt.Printf("Current database: beads.db (version %s)\n", Version)
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
	defer store.Close()

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
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Cancelled")
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

func init() {
	migrateCmd.Flags().Bool("yes", false, "Auto-confirm cleanup prompts")
	migrateCmd.Flags().Bool("cleanup", false, "Remove old database files after migration")
	migrateCmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	migrateCmd.Flags().Bool("update-repo-id", false, "Update repository ID (use after changing git remote)")
	migrateCmd.Flags().Bool("to-hash-ids", false, "Migrate sequential IDs to hash-based IDs")
	rootCmd.AddCommand(migrateCmd)
}
