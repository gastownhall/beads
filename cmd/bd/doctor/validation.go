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
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
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

	// Find duplicates by content hash (matching bd duplicates algorithm)
	// Only check open issues - closed issues are done, no point flagging duplicates
	seen := make(map[string][]string) // hash -> list of IDs
	for _, issue := range issues {
		if issue.Status == types.StatusTombstone || issue.Status == types.StatusClosed {
			continue
		}
		// Content key matches bd duplicates: title + description + design + acceptanceCriteria + status
		key := issue.Title + "|" + issue.Description + "|" + issue.Design + "|" + issue.AcceptanceCriteria + "|" + string(issue.Status)
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
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
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
		Fix:     "Run 'bd doctor --check=pollution' to review and clean test issues",
	}
}

// CheckChildParentDependencies detects child→parent blocking dependencies.
// These often indicate a modeling mistake (deadlock: child waits for parent, parent waits for children).
// However, they may be intentional in some workflows, so removal requires explicit opt-in.
func CheckChildParentDependencies(path string) DoctorCheck {
	// Follow redirect to resolve actual beads directory (bd-tvus fix)
	beadsDir := resolveBeadsDir(filepath.Join(path, ".beads"))
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

	// Query for child→parent BLOCKING dependencies where issue_id starts with depends_on_id + "."
	// Only matches blocking types (blocks, conditional-blocks, waits-for) that cause deadlock.
	// Excludes 'parent-child' type which is a legitimate structural hierarchy relationship.
	query := `
		SELECT d.issue_id, d.depends_on_id
		FROM dependencies d
		WHERE d.issue_id LIKE d.depends_on_id || '.%'
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

// CheckRedirectSyncBranchConflict detects when both redirect and sync-branch are configured.
// This is a configuration error: redirect means "my database is elsewhere (I'm a client)",
// while sync-branch means "I own my database and sync it myself". These are mutually exclusive.
// bd-wayc3: Added to detect incompatible configuration before sync fails.
func CheckRedirectSyncBranchConflict(path string) DoctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Check if redirect file exists
	redirectFile := filepath.Join(beadsDir, beads.RedirectFileName)
	if _, err := os.Stat(redirectFile); os.IsNotExist(err) {
		return DoctorCheck{
			Name:     "Redirect + Sync-Branch",
			Status:   StatusOK,
			Message:  "No redirect configured",
			Category: CategoryData,
		}
	}

	// Redirect exists - check if sync-branch is also configured
	// Read config.yaml directly since we need to check the local config, not the resolved one
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - path constructed safely
	if err != nil {
		// No config file - no conflict possible
		return DoctorCheck{
			Name:     "Redirect + Sync-Branch",
			Status:   StatusOK,
			Message:  "Redirect active (no local config)",
			Category: CategoryData,
		}
	}

	// Parse sync-branch from config.yaml (simple line-based parsing)
	// Handles: sync-branch: value, sync-branch: "value", sync-branch: 'value'
	// Also handles trailing comments: sync-branch: value # comment
	configStr := string(data)
	for _, line := range strings.Split(configStr, "\n") {
		line = strings.TrimSpace(line)
		// Skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "sync-branch:") {
			value := strings.TrimPrefix(line, "sync-branch:")
			// Remove trailing comment if present
			if idx := strings.Index(value, "#"); idx != -1 {
				value = value[:idx]
			}
			value = strings.TrimSpace(value)
			// Remove quotes if present
			value = strings.Trim(value, `"'`)
			if value != "" {
				// Found both redirect and sync-branch - conflict!
				return DoctorCheck{
					Name:     "Redirect + Sync-Branch",
					Status:   StatusWarning,
					Message:  fmt.Sprintf("Redirect active but sync-branch=%q configured", value),
					Detail:   "Redirect and sync-branch are mutually exclusive. Redirected clones should not have sync-branch.",
					Fix:      "Remove sync-branch from config.yaml (set to empty string or delete the line)",
					Category: CategoryData,
				}
			}
		}
	}

	return DoctorCheck{
		Name:     "Redirect + Sync-Branch",
		Status:   StatusOK,
		Message:  "Redirect active (no sync-branch conflict)",
		Category: CategoryData,
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
