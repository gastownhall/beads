package doctor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// CheckMergeArtifacts detects temporary git merge files in .beads directory.
// These are created during git merges and should be cleaned up.
func CheckMergeArtifacts(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

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
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}

	// Open database read-only
	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return DoctorCheck{
			Name:    "Orphaned Dependencies",
			Status:  "ok",
			Message: "N/A (unable to open database)",
		}
	}
	defer db.Close()

	// Query for orphaned dependencies
	query := `
		SELECT d.issue_id, d.depends_on_id, d.type
		FROM dependencies d
		LEFT JOIN issues i ON d.depends_on_id = i.id
		WHERE i.id IS NULL
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
func CheckDuplicateIssues(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}

	// Open store to use existing duplicate detection
	ctx := context.Background()
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "N/A (unable to open database)",
		}
	}
	defer func() { _ = store.Close() }()

	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "N/A (unable to query issues)",
		}
	}

	// Find duplicates by title+description hash
	seen := make(map[string][]string) // hash -> list of IDs
	for _, issue := range issues {
		if issue.Status == types.StatusTombstone {
			continue
		}
		key := issue.Title + "|" + issue.Description
		seen[key] = append(seen[key], issue.ID)
	}

	var duplicateGroups int
	var totalDuplicates int
	for _, ids := range seen {
		if len(ids) > 1 {
			duplicateGroups++
			totalDuplicates += len(ids) - 1 // exclude the canonical one
		}
	}

	if duplicateGroups == 0 {
		return DoctorCheck{
			Name:    "Duplicate Issues",
			Status:  "ok",
			Message: "No duplicate issues",
		}
	}

	return DoctorCheck{
		Name:    "Duplicate Issues",
		Status:  "warning",
		Message: fmt.Sprintf("%d duplicate issue(s) in %d group(s)", totalDuplicates, duplicateGroups),
		Detail:  "Duplicates cannot be auto-fixed",
		Fix:     "Run 'bd duplicates' to review and merge duplicates",
	}
}

// CheckTestPollution detects test issues that may have leaked into the database.
func CheckTestPollution(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "N/A (unable to open database)",
		}
	}
	defer db.Close()

	// Look for common test patterns in titles
	query := `
		SELECT COUNT(*) FROM issues
		WHERE status != 'tombstone'
		AND (
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
		Fix:     "Run 'bd detect-pollution' to review and clean test issues",
	}
}

// CheckChildParentDependencies detects the child→parent dependency anti-pattern.
// This creates a deadlock: child can't start (parent open), parent can't close (children not done).
func CheckChildParentDependencies(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  "ok",
			Message: "N/A (no database)",
		}
	}

	db, err := openDBReadOnly(dbPath)
	if err != nil {
		return DoctorCheck{
			Name:    "Child-Parent Dependencies",
			Status:  "ok",
			Message: "N/A (unable to open database)",
		}
	}
	defer db.Close()

	// Query for child→parent dependencies where issue_id starts with depends_on_id + "."
	// This uses SQLite's LIKE pattern matching
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE d.issue_id LIKE d.depends_on_id || '.%'
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
		Message:  fmt.Sprintf("%d child→parent dependency anti-pattern(s) detected", len(badDeps)),
		Detail:   detail,
		Fix:      "Run 'bd doctor --fix' to remove child→parent dependencies",
		Category: CategoryMetadata,
	}
}

// CheckGitConflicts detects git conflict markers in JSONL file.
func CheckGitConflicts(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")
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
