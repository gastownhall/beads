package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DatabaseCorruptionRecovery recovers a corrupted database from JSONL backup.
// It backs up the corrupted database, deletes it, and re-imports from JSONL.
func DatabaseCorruptionRecovery(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("no database to recover")
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	if jsonlPath == "" {
		return fmt.Errorf("no JSONL backup found - cannot recover (try restoring from git history)")
	}

	// Count issues in JSONL
	issueCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if issueCount == 0 {
		return fmt.Errorf("JSONL is empty - cannot recover (try restoring from git history)")
	}

	// Backup corrupted database
	backupPath := dbPath + ".corrupt"
	fmt.Printf("  Backing up corrupted database to %s\n", filepath.Base(backupPath))
	if err := os.Rename(dbPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup corrupted database: %w", err)
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		// Restore corrupted database on failure
		_ = os.Rename(backupPath, dbPath)
		return err
	}

	// Run bd import with --rename-on-import to handle prefix mismatches
	fmt.Printf("  Recovering %d issues from %s\n", issueCount, filepath.Base(jsonlPath))
	cmd := exec.Command(bdBinary, "import", "-i", jsonlPath, "--rename-on-import") // #nosec G204
	cmd.Dir = path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Keep backup on failure
		fmt.Printf("  Warning: recovery failed, corrupted database preserved at %s\n", filepath.Base(backupPath))
		return fmt.Errorf("failed to import from JSONL: %w", err)
	}

	// Run migrate to set version metadata
	migrateCmd := exec.Command(bdBinary, "migrate") // #nosec G204
	migrateCmd.Dir = path
	migrateCmd.Stdout = os.Stdout
	migrateCmd.Stderr = os.Stderr
	if err := migrateCmd.Run(); err != nil {
		// Non-fatal - import succeeded, version just won't be set
		fmt.Printf("  Warning: migration failed (non-fatal): %v\n", err)
	}

	fmt.Printf("  Recovered %d issues from JSONL backup\n", issueCount)
	return nil
}

// DatabaseCorruptionRecoveryWithOptions recovers a corrupted database with force and source selection support.
//
// Parameters:
//   - path: workspace path
//   - force: if true, bypasses validation and forces recovery even when database can't be opened
//   - source: source of truth selection ("auto", "jsonl", "db")
//
// Force mode is useful when the database has validation errors that prevent normal opening.
// Source selection allows choosing between JSONL and database when both exist but diverge.
func DatabaseCorruptionRecoveryWithOptions(path string, force bool, source string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Check if database exists
	dbExists := false
	if _, err := os.Stat(dbPath); err == nil {
		dbExists = true
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	jsonlExists := jsonlPath != ""

	// Determine source of truth based on --source flag and availability
	var useJSONL bool
	switch source {
	case "jsonl":
		// Explicit JSONL preference
		if !jsonlExists {
			return fmt.Errorf("--source=jsonl specified but no JSONL file found")
		}
		useJSONL = true
		fmt.Println("  Using JSONL as source of truth (--source=jsonl)")
	case "db":
		// Explicit database preference
		if !dbExists {
			return fmt.Errorf("--source=db specified but no database found")
		}
		useJSONL = false
		fmt.Println("  Using database as source of truth (--source=db)")
	case "auto":
		// Auto-detect: prefer JSONL if database is corrupted, otherwise prefer database
		if !dbExists && jsonlExists {
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (database missing)")
		} else if dbExists && !jsonlExists {
			useJSONL = false
			fmt.Println("  Using database as source of truth (JSONL missing)")
		} else if !dbExists && !jsonlExists {
			return fmt.Errorf("neither database nor JSONL found - cannot recover")
		} else {
			// Both exist - prefer JSONL for recovery since we're in corruption recovery
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (auto-detected, database appears corrupted)")
		}
	default:
		return fmt.Errorf("invalid --source value: %s (valid values: auto, jsonl, db)", source)
	}

	// If force mode is enabled, always use JSONL recovery (bypass database validation)
	if force {
		useJSONL = true
		fmt.Println("  Force mode enabled: bypassing database validation, using JSONL recovery")
	}

	// If using database as source, just run migration (no recovery needed)
	if !useJSONL {
		fmt.Println("  Database is the source of truth - skipping recovery")
		return nil
	}

	// JSONL recovery path
	if jsonlPath == "" {
		return fmt.Errorf("no JSONL backup found - cannot recover (try restoring from git history)")
	}

	// Count issues in JSONL
	issueCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if issueCount == 0 {
		return fmt.Errorf("JSONL is empty - cannot recover (try restoring from git history)")
	}

	// Backup existing database if it exists
	if dbExists {
		backupPath := dbPath + ".corrupt"
		fmt.Printf("  Backing up database to %s\n", filepath.Base(backupPath))
		if err := os.Rename(dbPath, backupPath); err != nil {
			return fmt.Errorf("failed to backup database: %w", err)
		}
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		// Restore database on failure if it existed
		if dbExists {
			backupPath := dbPath + ".corrupt"
			_ = os.Rename(backupPath, dbPath)
		}
		return err
	}

	// Run bd import with --rename-on-import to handle prefix mismatches
	fmt.Printf("  Recovering %d issues from %s\n", issueCount, filepath.Base(jsonlPath))
	importArgs := []string{"import", "-i", jsonlPath, "--rename-on-import"}
	if force {
		// Force mode: skip git history checks, import from working tree
		importArgs = append(importArgs, "--force", "--no-git-history")
	}

	cmd := exec.Command(bdBinary, importArgs...) // #nosec G204
	cmd.Dir = path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		// Keep backup on failure
		if dbExists {
			backupPath := dbPath + ".corrupt"
			fmt.Printf("  Warning: recovery failed, database preserved at %s\n", filepath.Base(backupPath))
		}
		return fmt.Errorf("failed to import from JSONL: %w", err)
	}

	// Run migrate to set version metadata
	migrateCmd := exec.Command(bdBinary, "migrate") // #nosec G204
	migrateCmd.Dir = path
	migrateCmd.Stdout = os.Stdout
	migrateCmd.Stderr = os.Stderr
	if err := migrateCmd.Run(); err != nil {
		// Non-fatal - import succeeded, version just won't be set
		fmt.Printf("  Warning: migration failed (non-fatal): %v\n", err)
	}

	fmt.Printf("  ✓ Recovered %d issues from JSONL backup\n", issueCount)
	return nil
}
