package fix

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
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

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Orphaned dependencies fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	// Find orphaned dependencies (exclude external: cross-rig tracking refs, #1593)
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		LEFT JOIN issues i ON d.depends_on_id = i.id
		WHERE i.id IS NULL
		  AND d.depends_on_id NOT LIKE 'external:%'
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
	// Uses explicit transaction so writes persist when @@autocommit is OFF
	// (e.g. Dolt server started with --no-auto-commit).
	showIndividual := verbose || len(orphans) < 20
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var removed int
	for _, o := range orphans {
		_, err := tx.Exec("DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
			o.issueID, o.dependsOnID)
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", o.issueID, o.dependsOnID, err)
		} else {
			removed++
			if showIndividual {
				fmt.Printf("  Removed orphaned dependency: %s→%s\n", o.issueID, o.dependsOnID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit orphaned dependency removals: %w", err)
	}

	// Commit changes in Dolt
	_, _ = db.Exec("CALL DOLT_COMMIT('-Am', 'doctor: remove orphaned dependencies')") // Best effort: commit advisory; schema fix already applied in-memory

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

	db, err := openDoltDB(beadsDir)
	if err != nil {
		fmt.Printf("  Child-parent dependencies fix skipped (%v)\n", err)
		return nil
	}
	defer db.Close()

	// Find child→parent BLOCKING dependencies where issue_id starts with depends_on_id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM dependencies d
		WHERE d.issue_id LIKE CONCAT(d.depends_on_id, '.%')
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
	// Uses explicit transaction so writes persist when @@autocommit is OFF
	// (e.g. Dolt server started with --no-auto-commit).
	showIndividual := verbose || len(badDeps) < 20
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	var removed int
	for _, d := range badDeps {
		_, err := tx.Exec("DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ? AND type = ?",
			d.issueID, d.dependsOnID, d.depType)
		if err != nil {
			fmt.Printf("  Warning: failed to remove %s→%s: %v\n", d.issueID, d.dependsOnID, err)
		} else {
			removed++
			if showIndividual {
				fmt.Printf("  Removed child→parent dependency: %s→%s\n", d.issueID, d.dependsOnID)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit dependency removals: %w", err)
	}

	// Commit changes in Dolt
	_, _ = db.Exec("CALL DOLT_COMMIT('-Am', 'doctor: remove child-parent dependency anti-patterns')") // Best effort: commit advisory; schema fix already applied in-memory

	fmt.Printf("  Fixed %d child→parent dependency anti-pattern(s)\n", removed)
	return nil
}

// openDoltDB opens a Dolt database connection via MySQL protocol.
func openDoltDB(beadsDir string) (*sql.DB, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("no database configuration found")
	}

	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()

	dsn := fmt.Sprintf("%s@tcp(%s:%d)/%s", user, host, port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("dolt server connection failed: %w", err)
	}

	// Verify the connection actually works
	if err := db.Ping(); err != nil {
		_ = db.Close() // Best effort cleanup
		return nil, fmt.Errorf("dolt server not reachable at %s:%d: %w", host, port, err)
	}

	return db, nil
}
