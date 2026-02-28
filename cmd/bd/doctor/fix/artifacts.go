package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClassicArtifacts removes beads classic artifacts found by scanning the path.
// Only removes artifacts that are safe to delete:
// - JSONL export artifacts (not issues.jsonl itself)
// - SQLite WAL/SHM files and backup databases
// - Extra files in redirect-only .beads directories
func ClassicArtifacts(path string) error {
	var removed, skipped, errCount int

	// Walk the directory tree looking for .beads/ directories
	err := filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip directories we can't read
		}

		// Skip heavy directories
		base := filepath.Base(walkPath)
		if info.IsDir() && (base == "node_modules" || base == "vendor" || base == "__pycache__") {
			return filepath.SkipDir
		}

		// We only care about directories named ".beads"
		if !info.IsDir() || base != ".beads" {
			return nil
		}

		r, s, e := cleanBeadsDirArtifacts(walkPath)
		removed += r
		skipped += s
		errCount += e

		return filepath.SkipDir
	})
	if err != nil {
		return fmt.Errorf("failed to walk directory tree: %w", err)
	}

	// Report summary
	fmt.Printf("  Artifact cleanup: %d removed, %d skipped, %d errors\n", removed, skipped, errCount)

	if skipped > 0 {
		fmt.Println("  Skipped items may need manual review (e.g., issues.jsonl in dolt dirs, beads.db files)")
	}

	if errCount > 0 {
		return fmt.Errorf("%d artifact(s) could not be removed", errCount)
	}

	return nil
}

// cleanBeadsDirArtifacts cleans artifacts from a single .beads directory.
// Returns counts of removed, skipped, and errored items.
func cleanBeadsDirArtifacts(beadsDir string) (removed, skipped, errCount int) {
	hasDolt := hasDoltDir(beadsDir)
	isRedirectExpected := isRedirectExpectedLocation(beadsDir)
	hasRedirect := hasRedirectFilePresent(beadsDir)

	// 1. Clean JSONL artifacts in dolt-native directories
	if hasDolt {
		r, s, e := cleanJSONLArtifacts(beadsDir)
		removed += r
		skipped += s
		errCount += e
	}

	// 2. Clean SQLite artifacts
	r, s, e := cleanSQLiteArtifacts(beadsDir)
	removed += r
	skipped += s
	errCount += e

	// 3. Clean cruft .beads directories (only if redirect is expected AND present)
	if isRedirectExpected && hasRedirect {
		r, e := cleanCruftBeadsDirFiles(beadsDir)
		removed += r
		errCount += e
	}

	return
}

// hasDoltDir returns true if the .beads directory contains a dolt/ subdirectory.
func hasDoltDir(beadsDir string) bool {
	info, err := os.Stat(getDatabasePath(beadsDir))
	return err == nil && info.IsDir()
}

// isRedirectExpectedLocation returns true if this .beads directory should contain
// only a redirect file.
func isRedirectExpectedLocation(beadsDir string) bool {
	parent := filepath.Dir(beadsDir)
	grandparent := filepath.Dir(parent)
	grandparentName := filepath.Base(grandparent)
	parentName := filepath.Base(parent)

	// Pattern: */polecats/*/.beads/
	if grandparentName == "polecats" {
		return true
	}
	// Pattern: */crew/*/.beads/
	if grandparentName == "crew" {
		return true
	}
	// Pattern: */refinery/rig/.beads/
	if parentName == "rig" && grandparentName == "refinery" {
		return true
	}
	// Pattern: .git/beads-worktrees/*/.beads/
	if grandparentName == "beads-worktrees" {
		return true
	}
	// Rig-root .beads/ with a mayor/rig/.beads/ canonical location
	canonicalDir := filepath.Join(parent, "mayor", "rig", ".beads")
	if _, err := os.Stat(canonicalDir); err == nil {
		// Also check it has polecats/ or mayor/ siblings
		for _, sibling := range []string{"mayor", "polecats"} {
			if info, err := os.Stat(filepath.Join(parent, sibling)); err == nil && info.IsDir() {
				return true
			}
		}
	}

	return false
}

// hasRedirectFilePresent returns true if the .beads directory has a redirect file.
func hasRedirectFilePresent(beadsDir string) bool {
	_, err := os.Stat(filepath.Join(beadsDir, "redirect"))
	return err == nil
}

// cleanJSONLArtifacts removes stale JSONL files from a dolt-native .beads directory.
func cleanJSONLArtifacts(beadsDir string) (removed, skipped, errCount int) {
	// Safe to delete (not the primary data source)
	safeFiles := []string{
		"issues.jsonl.new",
		"beads.left.jsonl",
	}

	for _, name := range safeFiles {
		path := filepath.Join(beadsDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Remove(path); err != nil {
			fmt.Printf("  Error removing %s: %v\n", path, err)
			errCount++
			continue
		}
		fmt.Printf("  Removed: %s (JSONL artifact)\n", path)
		removed++
	}

	// interactions.jsonl - only remove if empty
	interPath := filepath.Join(beadsDir, "interactions.jsonl")
	if info, err := os.Stat(interPath); err == nil {
		if info.Size() == 0 {
			if err := os.Remove(interPath); err != nil {
				fmt.Printf("  Error removing %s: %v\n", interPath, err)
				errCount++
			} else {
				fmt.Printf("  Removed: %s (empty interactions log)\n", interPath)
				removed++
			}
		} else {
			fmt.Printf("  Skip (not empty): %s\n", interPath)
			skipped++
		}
	}

	// issues.jsonl in dolt-native directory - skip (needs manual review)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(issuesPath); err == nil {
		fmt.Printf("  Skip (needs review): %s (issues.jsonl in dolt-native dir)\n", issuesPath)
		skipped++
	}

	return
}

// cleanSQLiteArtifacts removes leftover SQLite database files.
func cleanSQLiteArtifacts(beadsDir string) (removed, skipped, errCount int) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		// WAL and SHM files are always safe to delete
		if name == "beads.db-shm" || name == "beads.db-wal" {
			path := filepath.Join(beadsDir, name)
			if err := os.Remove(path); err != nil {
				fmt.Printf("  Error removing %s: %v\n", path, err)
				errCount++
				continue
			}
			fmt.Printf("  Removed: %s (SQLite WAL/SHM)\n", path)
			removed++
			continue
		}

		// beads.db - skip (needs manual review, could be active)
		if name == "beads.db" {
			path := filepath.Join(beadsDir, name)
			fmt.Printf("  Skip (needs review): %s\n", path)
			skipped++
			continue
		}

		// Backup databases are safe to delete
		if strings.HasPrefix(name, "beads.backup-") && strings.HasSuffix(name, ".db") {
			path := filepath.Join(beadsDir, name)
			if err := os.Remove(path); err != nil {
				fmt.Printf("  Error removing %s: %v\n", path, err)
				errCount++
				continue
			}
			fmt.Printf("  Removed: %s (pre-migration backup)\n", path)
			removed++
		}
	}

	return
}

// cleanCruftBeadsDirFiles removes everything from a .beads directory except
// the redirect file and .gitkeep.
func cleanCruftBeadsDirFiles(beadsDir string) (removed, errCount int) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return 0, 1
	}

	for _, entry := range entries {
		name := entry.Name()
		// Keep redirect and .gitkeep
		if name == "redirect" || name == ".gitkeep" {
			continue
		}

		entryPath := filepath.Join(beadsDir, name)

		// Validate path doesn't escape
		rel, err := filepath.Rel(beadsDir, entryPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}

		if entry.IsDir() {
			if err := os.RemoveAll(entryPath); err != nil {
				fmt.Printf("  Error removing %s: %v\n", entryPath, err)
				errCount++
				continue
			}
		} else {
			if err := os.Remove(entryPath); err != nil {
				fmt.Printf("  Error removing %s: %v\n", entryPath, err)
				errCount++
				continue
			}
		}
		fmt.Printf("  Removed: %s (cruft in redirect-only dir)\n", entryPath)
		removed++
	}

	return
}
