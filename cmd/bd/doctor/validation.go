//go:build cgo

package doctor

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openStoreDB opens the beads database and returns the underlying *sql.DB for
// raw queries. The caller must close the returned store when done.
func openStoreDB(beadsDir string) (*sql.DB, *dolt.DoltStore, error) {
	ctx := context.Background()
	doltPath := filepath.Join(beadsDir, "dolt")
	store, err := dolt.New(ctx, &dolt.Config{Path: doltPath, ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	db := store.UnderlyingDB()
	if db == nil {
		_ = store.Close() // Best effort cleanup
		return nil, nil, fmt.Errorf("storage backend has no underlying database")
	}
	return db, store, nil
}

// CheckMergeArtifacts detects temporary git merge files in .beads directory.
// These are created during git merges and should be cleaned up.
func CheckMergeArtifacts(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Merge Artifacts",
			Status:  "ok",
			Message: "N/A (no .beads directory)",
		}
	}

	// Read patterns from .beads/.gitignore (merge artifacts section)
	patterns, err := readMergeArtifactPatterns(beadsDir)
	if err != nil {
		// No .gitignore or can't read it - use default patterns
		patterns = []string{
			"*.base.jsonl",
			"*.left.jsonl",
			"*.right.jsonl",
			"*.meta.json",
		}
	}

	// Find matching files
	var artifacts []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(beadsDir, pattern))
		if err != nil {
			continue
		}
		artifacts = append(artifacts, matches...)
	}

	if len(artifacts) == 0 {
		return DoctorCheck{
			Name:    "Merge Artifacts",
			Status:  "ok",
			Message: "No merge artifacts found",
		}
	}

	// Build list of relative paths for display
	var relPaths []string
	for _, f := range artifacts {
		if rel, err := filepath.Rel(beadsDir, f); err == nil {
			relPaths = append(relPaths, rel)
		}
	}

	return DoctorCheck{
		Name:    "Merge Artifacts",
		Status:  "warning",
		Message: fmt.Sprintf("%d temporary merge file(s) found", len(artifacts)),
		Detail:  strings.Join(relPaths, ", "),
		Fix:     "Run 'bd doctor --fix' to remove merge artifacts",
	}
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

// CheckOrphanedDependencies detects dependencies pointing to non-existent issues.
func CheckOrphanedDependencies(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Query for orphaned dependencies.
	// Exclude external: refs — these are synthetic cross-rig tracking deps
	// injected by the JSONL exporter and intentionally reference issues not
	// present in the local database (#1593).
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM dependencies d
		LEFT JOIN issues i ON d.depends_on_id = i.id
		WHERE i.id IS NULL
		  AND d.depends_on_id NOT LIKE 'external:%'
	`
	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "N/A (query failed)",
		}
	}
	defer rows.Close()

	var orphans []string
	for rows.Next() {
		var issueID, dependsOnID, depType string
		if err := rows.Scan(&issueID, &dependsOnID, &depType); err == nil {
			orphans = append(orphans, fmt.Sprintf("%s→%s", issueID, dependsOnID))
		}
	}

	if len(orphans) == 0 {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "No orphaned dependencies",
		}
	}

	detail := strings.Join(orphans, ", ")
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}

	return DoctorCheck{
		Name:    "Orphaned Dependencies",
		Status:  "warning",
		Message: fmt.Sprintf("%d orphaned dependency reference(s)", len(orphans)),
		Detail:  detail,
		Fix:     "Run 'bd doctor --fix' to remove orphaned dependencies",
	}
}

// CheckDuplicateIssues detects issues with identical content.
// When gastownMode is true, the threshold parameter defines how many duplicates
// are acceptable before warning (default 1000 for gastown's ephemeral wisps).
func CheckDuplicateIssues(path string, gastownMode bool, gastownThreshold int) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	// Use SQL aggregation to find duplicates without loading all issues into memory.
	// The old approach loaded every issue via SearchIssues which was O(n) in both
	// time and memory — catastrophically slow on large databases (e.g., 23k+ issues
	// took 66 seconds over MySQL wire protocol).
	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  StatusOK,
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Count duplicate groups and total duplicates using SQL GROUP BY.
	// This matches the original algorithm: group by title|description|design|acceptance_criteria|status,
	// only for non-closed issues.
	query := `
		SELECT COUNT(*) as group_count, SUM(cnt - 1) as dup_count
		FROM (
			SELECT COUNT(*) as cnt
			FROM issues
			WHERE status != 'closed'
			GROUP BY title, description, design, acceptance_criteria, status
			HAVING COUNT(*) > 1
		) dups
	`
	var groupCount, dupCount sql.NullInt64
	if err := db.QueryRow(query).Scan(&groupCount, &dupCount); err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "N/A (unable to query issues)",
		}
	}

	duplicateGroups := int(groupCount.Int64)
	totalDuplicates := int(dupCount.Int64)

	// Apply threshold based on mode
	threshold := 0 // Default: any duplicates are warnings
	if gastownMode {
		threshold = gastownThreshold // Gastown: configurable threshold (default 1000)
	}

	if totalDuplicates == 0 {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "No duplicate issues",
		}
	}

	// Only warn if duplicate count exceeds threshold
	if totalDuplicates > threshold {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "warning",
			Message: fmt.Sprintf("%d duplicate issue(s) in %d group(s)", totalDuplicates, duplicateGroups),
			Detail:  "Duplicates cannot be auto-fixed",
			Fix:     "Run 'bd duplicates' to review and merge duplicates",
		}
	}

	// Under threshold - OK
	message := "No duplicate issues"
	if gastownMode && totalDuplicates > 0 {
		message = fmt.Sprintf("%d duplicate(s) detected (within gastown threshold of %d)", totalDuplicates, threshold)
	}
	return DoctorCheck{
		Name:    "Duplicate Issues",
		Status:  "ok",
		Message: message,
	}
}

// CheckTestPollution detects test issues that may have leaked into the database.
func CheckTestPollution(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Look for common test patterns in titles
	query := `
		SELECT COUNT(*) FROM issues
		WHERE (
			title LIKE 'test-%' OR
			title LIKE 'Test Issue%' OR
			title LIKE '%test issue%' OR
			id LIKE 'test-%'
		)
	`
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "N/A (query failed)",
		}
	}

	if count == 0 {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "No test pollution detected",
		}
	}

	return DoctorCheck{
		Name:    "Test Pollution",
		Status:  "warning",
		Message: fmt.Sprintf("%d potential test issue(s) detected", count),
		Detail:  "Test issues may have leaked into production database",
		Fix:     "Run 'bd doctor --check=pollution' to review and clean test issues",
	}
}

// CheckChildParentDependencies detects child→parent blocking dependencies.
// These often indicate a modeling mistake (deadlock: child waits for parent, parent waits for children).
// However, they may be intentional in some workflows, so removal requires explicit opt-in.
func CheckChildParentDependencies(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))

	db, store, err := openStoreDB(beadsDir)
	if err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}
	defer func() { _ = store.Close() }()

	// Query for child→parent BLOCKING dependencies where issue_id starts with depends_on_id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE d.issue_id LIKE CONCAT(d.depends_on_id, '.%')
		  AND d.type IN ('blocks', 'conditional-blocks', 'waits-for')
	`
	rows, err := db.Query(query)
	if err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  "ok",
			Message: "N/A (query failed)",
		}
	}
	defer rows.Close()

	var badDeps []string
	for rows.Next() {
		var issueID, dependsOnID string
		if err := rows.Scan(&issueID, &dependsOnID); err == nil {
			badDeps = append(badDeps, fmt.Sprintf("%s→%s", issueID, dependsOnID))
		}
	}

	if len(badDeps) == 0 {
		return DoctorCheck{
			Name:     "Child-Parent Dependencies",
			Status:   "ok",
			Message:  "No child→parent dependencies",
			Category: CategoryMetadata,
		}
	}

	detail := strings.Join(badDeps, ", ")
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}

	return DoctorCheck{
		Name:     "Child-Parent Dependencies",
		Status:   "warning",
		Message:  fmt.Sprintf("%d child→parent dependency detected (may cause deadlock)", len(badDeps)),
		Detail:   detail,
		Fix:      "Run 'bd doctor --fix --fix-child-parent' to remove (if unintentional)",
		Category: CategoryMetadata,
	}
}

// CheckGitConflicts detects git conflict markers in JSONL file.
func CheckGitConflicts(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  "ok",
			Message: "N/A (no JSONL file)",
		}
	}

	data, err := os.ReadFile(jsonlPath) // #nosec G304 - path constructed safely
	if err != nil {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  "ok",
			Message: "N/A (unable to read JSONL)",
		}
	}

	// Look for conflict markers at start of lines
	lines := bytes.Split(data, []byte("\n"))
	var conflictLines []int
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("<<<<<<< ")) ||
			bytes.Equal(trimmed, []byte("=======")) ||
			bytes.HasPrefix(trimmed, []byte(">>>>>>> ")) {
			conflictLines = append(conflictLines, i+1)
		}
	}

	if len(conflictLines) == 0 {
		return DoctorCheck{
			Name:    "Git Conflicts",
			Status:  "ok",
			Message: "No git conflicts in JSONL",
		}
	}

	return DoctorCheck{
		Name:    "Git Conflicts",
		Status:  "error",
		Message: fmt.Sprintf("Git conflict markers found at %d location(s)", len(conflictLines)),
		Detail:  fmt.Sprintf("Conflict markers at lines: %v", conflictLines),
		Fix:     "Resolve conflicts manually: git checkout --ours or --theirs .beads/issues.jsonl",
	}
}
