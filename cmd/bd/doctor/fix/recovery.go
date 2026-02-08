package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

// databaseCorruptionRecovery recovers a corrupted database from JSONL backup.
// It backs up the corrupted database, deletes it, and re-imports from JSONL.
func databaseCorruptionRecovery(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Dolt backend: use Dolt-specific recovery
	if cfg, _ := configfile.Load(beadsDir); cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		return doltCorruptionRecovery(path, beadsDir)
	}

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

// doltCorruptionRecovery recovers a corrupted Dolt database from JSONL backup.
// The recovery procedure:
//  1. Verify JSONL backup exists and has issues
//  2. Back up the corrupted dolt directory
//  3. Remove the corrupted dolt directory
//  4. Re-initialize via bd init --backend dolt --force --from-jsonl
//
// The Dolt bootstrap in factory_dolt.go will automatically import from JSONL
// when it finds no existing dolt database, so removing the corrupted directory
// and reinitializing is sufficient.
func doltCorruptionRecovery(path, beadsDir string) error {
	doltPath := filepath.Join(beadsDir, "dolt")

	// Check if dolt directory exists
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return fmt.Errorf("no Dolt database to recover at %s", doltPath)
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	if jsonlPath == "" {
		return fmt.Errorf("no JSONL backup found - cannot recover Dolt database (try restoring from git history)")
	}

	// Count issues in JSONL
	issueCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if issueCount == 0 {
		return fmt.Errorf("JSONL is empty - cannot recover Dolt database (try restoring from git history)")
	}

	// Back up corrupted dolt directory
	ts := time.Now().UTC().Format("20060102T150405Z")
	backupPath := doltPath + "." + ts + ".corrupt.backup"
	fmt.Printf("  Backing up corrupted Dolt database to %s\n", filepath.Base(backupPath))
	if err := os.Rename(doltPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup corrupted Dolt database: %w", err)
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		// Restore corrupted database on failure
		_ = os.Rename(backupPath, doltPath)
		return err
	}

	// Reinitialize: bd init --backend dolt --force --from-jsonl -q
	// This creates a fresh dolt database and the Dolt bootstrap will import from JSONL.
	fmt.Printf("  Recovering %d issues from %s into fresh Dolt database\n", issueCount, filepath.Base(jsonlPath))
	initCmd := newBdCmd(bdBinary, "init", "--backend", "dolt", "--force", "-q", "--skip-hooks", "--skip-merge-driver")
	initCmd.Dir = path
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr

	if err := initCmd.Run(); err != nil {
		// Restore backup on failure
		fmt.Printf("  Warning: recovery failed, restoring corrupted Dolt database from %s\n", filepath.Base(backupPath))
		_ = os.RemoveAll(doltPath) // Remove any partial init
		_ = os.Rename(backupPath, doltPath)
		return fmt.Errorf("failed to reinitialize Dolt database: %w", err)
	}

	fmt.Printf("  ✓ Recovered %d issues from JSONL backup into fresh Dolt database\n", issueCount)
	fmt.Printf("  Corrupted database preserved at: %s\n", filepath.Base(backupPath))
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

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Dolt backend: use Dolt-specific recovery
	if cfg, _ := configfile.Load(beadsDir); cfg != nil && cfg.GetBackend() == configfile.BackendDolt {
		return doltCorruptionRecoveryWithOptions(path, beadsDir, force, source)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")

	// Check if database exists
	dbExists := false
	if _, err := os.Stat(dbPath); err == nil {
		dbExists = true
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	jsonlExists := jsonlPath != ""

	// Check for contradictory flags early
	if force && source == "db" {
		return fmt.Errorf("--force and --source=db are contradictory: --force implies the database is broken and recovery should use JSONL. Use --source=jsonl or --source=auto with --force")
	}

	// Determine source of truth based on --source flag and availability
	var useJSONL bool
	switch source {
	case "jsonl":
		// Explicit JSONL preference
		if !jsonlExists {
			return fmt.Errorf("--source=jsonl specified but no JSONL file found")
		}
		useJSONL = true
		if force {
			fmt.Println("  Using JSONL as source of truth (--force --source=jsonl)")
		} else {
			fmt.Println("  Using JSONL as source of truth (--source=jsonl)")
		}
	case "db":
		// Explicit database preference (already checked for force+db contradiction above)
		if !dbExists {
			return fmt.Errorf("--source=db specified but no database found")
		}
		useJSONL = false
		fmt.Println("  Using database as source of truth (--source=db)")
	case "auto":
		// Auto-detect: prefer JSONL if database is corrupted or force is set
		if force {
			// Force mode implies database is broken - use JSONL
			if !jsonlExists {
				return fmt.Errorf("--force requires JSONL for recovery but no JSONL file found")
			}
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (--force mode)")
		} else if !dbExists && jsonlExists {
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

// doltCorruptionRecoveryWithOptions recovers a corrupted Dolt database with force and source flags.
func doltCorruptionRecoveryWithOptions(path, beadsDir string, force bool, source string) error {
	doltPath := filepath.Join(beadsDir, "dolt")

	// Check if dolt directory exists
	doltExists := false
	if info, err := os.Stat(doltPath); err == nil && info.IsDir() {
		doltExists = true
	}

	// Find JSONL file
	jsonlPath := findJSONLPath(beadsDir)
	jsonlExists := jsonlPath != ""

	// Check for contradictory flags
	if force && source == "db" {
		return fmt.Errorf("--force and --source=db are contradictory: --force implies the database is broken and recovery should use JSONL. Use --source=jsonl or --source=auto with --force")
	}

	// Determine source of truth
	var useJSONL bool
	switch source {
	case "jsonl":
		if !jsonlExists {
			return fmt.Errorf("--source=jsonl specified but no JSONL file found")
		}
		useJSONL = true
		if force {
			fmt.Println("  Using JSONL as source of truth (--force --source=jsonl)")
		} else {
			fmt.Println("  Using JSONL as source of truth (--source=jsonl)")
		}
	case "db":
		if !doltExists {
			return fmt.Errorf("--source=db specified but no Dolt database found")
		}
		useJSONL = false
		fmt.Println("  Using Dolt database as source of truth (--source=db)")
	case "auto":
		if force {
			if !jsonlExists {
				return fmt.Errorf("--force requires JSONL for recovery but no JSONL file found")
			}
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (--force mode)")
		} else if !doltExists && jsonlExists {
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (Dolt database missing)")
		} else if doltExists && !jsonlExists {
			useJSONL = false
			fmt.Println("  Using Dolt database as source of truth (JSONL missing)")
		} else if !doltExists && !jsonlExists {
			return fmt.Errorf("neither Dolt database nor JSONL found - cannot recover")
		} else {
			useJSONL = true
			fmt.Println("  Using JSONL as source of truth (auto-detected, Dolt database appears corrupted)")
		}
	default:
		return fmt.Errorf("invalid --source value: %s (valid values: auto, jsonl, db)", source)
	}

	if !useJSONL {
		fmt.Println("  Dolt database is the source of truth - skipping recovery")
		return nil
	}

	// Count issues in JSONL
	issueCount, err := countJSONLIssues(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if issueCount == 0 {
		return fmt.Errorf("JSONL is empty - cannot recover Dolt database (try restoring from git history)")
	}

	// Backup corrupted dolt directory if it exists
	if doltExists {
		ts := time.Now().UTC().Format("20060102T150405Z")
		backupPath := doltPath + "." + ts + ".corrupt.backup"
		fmt.Printf("  Backing up Dolt database to %s\n", filepath.Base(backupPath))
		if err := os.Rename(doltPath, backupPath); err != nil {
			return fmt.Errorf("failed to backup Dolt database: %w", err)
		}
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Reinitialize: bd init --backend dolt --force -q
	fmt.Printf("  Recovering %d issues from %s into fresh Dolt database\n", issueCount, filepath.Base(jsonlPath))
	initCmd := newBdCmd(bdBinary, "init", "--backend", "dolt", "--force", "-q", "--skip-hooks", "--skip-merge-driver")
	initCmd.Dir = path
	initCmd.Stdout = os.Stdout
	initCmd.Stderr = os.Stderr

	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("failed to reinitialize Dolt database: %w", err)
	}

	fmt.Printf("  ✓ Recovered %d issues from JSONL backup into fresh Dolt database\n", issueCount)
	return nil
}
