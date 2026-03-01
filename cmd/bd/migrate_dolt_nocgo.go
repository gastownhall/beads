//go:build !cgo

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
)

// handleToDoltMigration performs SQLite→Dolt migration for non-CGO builds
// using the system sqlite3 CLI for data extraction. This delegates to the
// same runMigrationPhases path used by auto-migration (shimMigrateSQLiteToDolt).
func handleToDoltMigration(dryRun bool, autoYes bool) {
	ctx := context.Background()

	// Find .beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_beads_directory",
				"message": "No .beads directory found. Run 'bd init' first.",
			})
		} else {
			FatalErrorWithHint("no .beads directory found", "run 'bd init' to initialize bd")
		}
		os.Exit(1)
	}

	// Find SQLite database
	sqlitePath := findSQLiteDB(beadsDir)
	if sqlitePath == "" {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "no_sqlite_database",
				"message": "No SQLite database found to migrate",
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: No SQLite database found to migrate\n")
			fmt.Fprintf(os.Stderr, "Hint: no .db files found in %s\n", beadsDir)
		}
		os.Exit(1)
	}

	// Verify sqlite3 CLI is available (required for non-CGO builds)
	sqlite3Path, err := exec.LookPath("sqlite3")
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "sqlite3_not_found",
				"message": "The sqlite3 CLI tool is required for migration in non-CGO builds.",
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: sqlite3 CLI tool not found\n")
			fmt.Fprintf(os.Stderr, "This binary was built without CGO, so the sqlite3 CLI is needed for data extraction.\n")
			fmt.Fprintf(os.Stderr, "Hint: install sqlite3, or rebuild with CGO_ENABLED=1\n")
		}
		os.Exit(1)
	}
	_ = sqlite3Path

	// Extract data from SQLite via CLI
	data, err := extractViaSQLiteCLI(ctx, sqlitePath)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "extraction_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		}
		os.Exit(1)
	}

	// Dolt path
	doltPath := filepath.Join(beadsDir, "dolt")

	// Show migration plan
	if !jsonOutput {
		fmt.Printf("SQLite to Dolt Migration\n")
		fmt.Printf("========================\n\n")
		fmt.Printf("Source: %s\n", sqlitePath)
		fmt.Printf("Target: %s\n", doltPath)
		fmt.Printf("Issues to migrate: %d\n", data.issueCount)
		eventCount, commentCount := countMigrationRecords(data)
		fmt.Printf("Events to migrate: %d\n", eventCount)
		fmt.Printf("Comments to migrate: %d\n", commentCount)
		fmt.Printf("Config keys: %d\n", len(data.config))
		if data.prefix != "" {
			fmt.Printf("Issue prefix: %s\n", data.prefix)
		}
		fmt.Printf("Note: using sqlite3 CLI for extraction (non-CGO build)\n")
		fmt.Println()
	}

	// Dry run mode
	if dryRun {
		eventCount, commentCount := countMigrationRecords(data)
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"dry_run":       true,
				"source":        sqlitePath,
				"target":        doltPath,
				"issue_count":   data.issueCount,
				"event_count":   eventCount,
				"comment_count": commentCount,
				"config_keys":   len(data.config),
				"prefix":        data.prefix,
				"would_backup":  true,
				"nocgo":         true,
			})
		} else {
			fmt.Println("Dry run mode - no changes will be made")
			fmt.Println("Would perform:")
			fmt.Printf("  1. Create backup of source database\n")
			fmt.Printf("  2. Create target database at %s\n", doltPath)
			fmt.Printf("  3. Import %d issues with labels and dependencies\n", data.issueCount)
			fmt.Printf("  4. Import %d events (issue history)\n", eventCount)
			fmt.Printf("  5. Import %d comments (legacy comments table)\n", commentCount)
			fmt.Printf("  6. Copy %d config values\n", len(data.config))
			fmt.Printf("  7. Update metadata.json\n")
		}
		return
	}

	// Prompt for confirmation
	if !autoYes && !jsonOutput {
		fmt.Printf("This will:\n")
		fmt.Printf("  1. Create a backup of your SQLite database\n")
		fmt.Printf("  2. Create a Dolt database and import all data\n")
		fmt.Printf("  3. Update metadata.json to use Dolt backend\n")
		fmt.Printf("  4. Keep your SQLite database (can be deleted after verification)\n\n")
		fmt.Printf("Continue? [y/N] ")
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Migration canceled")
			return
		}
	}

	// Backup SQLite database
	fmt.Fprintf(os.Stderr, "Backing up SQLite database...\n")
	backupPath, err := backupSQLite(sqlitePath)
	if err != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "backup_failed",
				"message": err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: backup failed: %s\n", err)
		}
		os.Exit(1)
	}
	if !jsonOutput {
		fmt.Printf("%s\n", ui.RenderPass("✓ Created backup: "+filepath.Base(backupPath)))
	}

	// Determine database name
	dbName := "beads"
	if existingCfg, _ := configfile.Load(beadsDir); existingCfg != nil && existingCfg.DoltDatabase != "" {
		dbName = existingCfg.DoltDatabase
	} else if data.prefix != "" {
		dbName = data.prefix
	}

	// Resolve server connection settings
	resolvedHost := "127.0.0.1"
	resolvedPort := 0
	resolvedUser := "root"
	resolvedPassword := ""
	resolvedTLS := false
	autoStart := os.Getenv("GT_ROOT") == "" && os.Getenv("BEADS_DOLT_AUTO_START") != "0"
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil {
		resolvedHost = cfg.GetDoltServerHost()
		resolvedPort = doltserver.DefaultConfig(beadsDir).Port
		resolvedUser = cfg.GetDoltServerUser()
		resolvedPassword = cfg.GetDoltServerPassword()
		resolvedTLS = cfg.GetDoltServerTLS()
	}

	// Run shared migration phases
	params := &migrationParams{
		beadsDir:       beadsDir,
		sqlitePath:     sqlitePath,
		backupPath:     backupPath,
		data:           data,
		dbName:         dbName,
		serverHost:     resolvedHost,
		serverPort:     resolvedPort,
		serverUser:     resolvedUser,
		serverPassword: resolvedPassword,
		doltCfg: &dolt.Config{
			Path:            doltPath,
			Database:        dbName,
			CreateIfMissing: true,
			ServerHost:      resolvedHost,
			ServerPort:      resolvedPort,
			ServerUser:      resolvedUser,
			ServerPassword:  resolvedPassword,
			ServerTLS:       resolvedTLS,
			AutoStart:       autoStart,
		},
	}

	imported, skipped, migErr := runMigrationPhases(ctx, params)
	if migErr != nil {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"error":   "migration_failed",
				"message": migErr.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: migration failed: %s\n", migErr)
		}
		os.Exit(1)
	}

	// Print results
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"status":          "success",
			"backend":         "dolt",
			"issues_imported": imported,
			"issues_skipped":  skipped,
			"backup_path":     backupPath,
			"dolt_path":       doltPath,
		})
	} else {
		if skipped > 0 {
			fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Imported %d issues (%d skipped)", imported, skipped)))
		} else {
			fmt.Printf("%s\n", ui.RenderPass(fmt.Sprintf("✓ Imported %d issues", imported)))
		}
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ Migration complete!"))
		fmt.Println()
		fmt.Printf("Your beads now use DOLT storage.\n")
		fmt.Printf("Backup: %s\n", backupPath)
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Println("  - Verify data: bd list")
		fmt.Println("  - After verification, you can delete the old database:")
		fmt.Printf("    rm %s\n", sqlitePath)
	}
}

// countMigrationRecords counts events and comments across all issues.
// This is the nocgo variant of migrationRecordCounts (defined in migrate_dolt.go for CGO builds).
func countMigrationRecords(data *migrationData) (eventCount int, commentCount int) {
	for _, events := range data.eventsMap {
		eventCount += len(events)
	}
	for _, comments := range data.commentsMap {
		commentCount += len(comments)
	}
	return eventCount, commentCount
}

// listMigrations returns an empty list (no Dolt migrations without CGO).
func listMigrations() []string {
	return nil
}
