package fix

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// MergeArtifacts removes temporary git merge files from .beads directory.
func MergeArtifacts(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Read patterns from .gitignore or use defaults
	patterns, err := readMergeArtifactPatterns(beadsDir)
	if err != nil {
		patterns = []string{
			"*.base.jsonl",
			"*.left.jsonl",
			"*.right.jsonl",
			"*.meta.json",
		}
	}

	// Find and delete matching files
	var deleted int
	var errors []string

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
		if err != nil {
			continue
		}
		for _, file := range matches {
			if err := os.Remove(file); err != nil {
				if !os.IsNotExist(err) {
					errors = append(errors, fmt.Sprintf("%s: %v", filepath.Base(file), err))
				}
			} else {
				deleted++
				fmt.Printf("  Removed %s\n", filepath.Base(file))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("failed to remove some files: %s", strings.Join(errors, "; "))
	}

	if deleted == 0 {
		fmt.Println("  No merge artifacts to remove")
	} else {
		fmt.Printf("  Removed %d merge artifact(s)\n", deleted)
	}

	return nil
}

// readMergeArtifactPatterns reads patterns from .beads/.gitignore merge section
func readMergeArtifactPatterns(beadsDir string) ([]string, error) {
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	file, err := os.Open(gitignorePath) // #nosec G304 - path constructed from beadsDir
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var patterns []string
	inMergeSection := false
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.Contains(line, "Merge artifacts") {
			inMergeSection = true
			continue
		}

		if inMergeSection && strings.HasPrefix(line, "#") {
			break
		}

		if inMergeSection && line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "!") {
			patterns = append(patterns, line)
		}
	}

	return patterns, scanner.Err()
}

// OrphanedDependencies removes dependencies pointing to non-existent issues.
// If verbose is true, prints each removed dependency; otherwise shows only summary.
func OrphanedDependencies(path string, verbose bool) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Open database
	db, err := openDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Find orphaned dependencies
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		LEFT JOIN issues i ON d.depends_on_id = i.id
		WHERE i.id IS NULL
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query orphaned dependencies: %w", err)
	}
	defer rows.Close()

	type orphan struct {
		issueID     string
		dependsOnID string
	}
	var orphans []orphan

	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.issueID, &o.dependsOnID); err == nil {
			orphans = append(orphans, o)
		}
	}

	if len(orphans) == 0 {
		fmt.Println("  No orphaned dependencies to fix")
		return nil
	}

	// Delete orphaned dependencies
	// Show individual items if verbose or count is small (<20)
	showIndividual := verbose || len(orphans) < 20
	var removed int
	for _, o := range orphans {
		_, err := db.Exec("DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
			o.issueID, o.dependsOnID)
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", o.issueID, o.dependsOnID, err)
		} else {
			// Mark issue as dirty for export
			_, _ = db.Exec("INSERT OR IGNORE INTO dirty_issues (issue_id) VALUES (?)", o.issueID)
			removed++
			if showIndividual {
				fmt.Printf("  Removed orphaned dependency: %s→%s\n", o.issueID, o.dependsOnID)
			}
		}
	}

	fmt.Printf("  Fixed %d orphaned dependency reference(s)\n", removed)
	return nil
}

// ChildParentDependencies removes child→parent blocking dependencies.
// These often indicate a modeling mistake (deadlock: child waits for parent, parent waits for children).
// Requires explicit opt-in via --fix-child-parent flag since some workflows may use these intentionally.
// If verbose is true, prints each removed dependency; otherwise shows only summary.
func ChildParentDependencies(path string, verbose bool) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Open database
	db, err := openDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Find child→parent BLOCKING dependencies where issue_id starts with depends_on_id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM dependencies d
		WHERE d.issue_id LIKE d.depends_on_id || '.%'
		  AND d.type IN ('blocks', 'conditional-blocks', 'waits-for')
	`
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query child-parent dependencies: %w", err)
	}
	defer rows.Close()

	type badDep struct {
		issueID     string
		dependsOnID string
		depType     string
	}
	var badDeps []badDep

	for rows.Next() {
		var d badDep
		if err := rows.Scan(&d.issueID, &d.dependsOnID, &d.depType); err == nil {
			badDeps = append(badDeps, d)
		}
	}

	if len(badDeps) == 0 {
		fmt.Println("  No child→parent dependencies to fix")
		return nil
	}

	// Delete child→parent blocking dependencies (preserving parent-child type)
	// Show individual items if verbose or count is small (<20)
	showIndividual := verbose || len(badDeps) < 20
	var removed int
	for _, d := range badDeps {
		_, err := db.Exec("DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ? AND type = ?",
			d.issueID, d.dependsOnID, d.depType)
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", d.issueID, d.dependsOnID, err)
		} else {
			// Mark issue as dirty for export
			_, _ = db.Exec("INSERT OR IGNORE INTO dirty_issues (issue_id) VALUES (?)", d.issueID)
			removed++
			if showIndividual {
				fmt.Printf("  Removed child→parent dependency: %s→%s\n", d.issueID, d.dependsOnID)
			}
		}
	}

	fmt.Printf("  Fixed %d child→parent dependency anti-pattern(s)\n", removed)
	return nil
}

// openDB opens a SQLite database for read-write access
func openDB(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite3", sqliteConnString(dbPath, false))
}
