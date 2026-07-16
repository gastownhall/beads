package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage/flatfile"
	"github.com/steveyegge/beads/internal/types"
)

// RunFlatfileChecks runs all health checks for a flat-file backend workspace.
func RunFlatfileChecks(beadsDir string) []DoctorCheck {
	var checks []DoctorCheck

	checks = append(checks, checkFlatfileDirs(beadsDir))
	checks = append(checks, checkFlatfileIssuesParse(beadsDir))
	checks = append(checks, checkFlatfileIDFilenameMatch(beadsDir))
	checks = append(checks, checkFlatfileOrphans(beadsDir))
	checks = append(checks, checkFlatfileDanglingDeps(beadsDir))
	checks = append(checks, checkFlatfileCounter(beadsDir))
	checks = append(checks, checkFlatfileGitTracking(beadsDir))

	return checks
}

// IsFlatfileBackend returns true if beadsDir is a flatfile workspace.
func IsFlatfileBackend(beadsDir string) bool {
	return flatfile.IsFlatFileBackend(beadsDir)
}

func checkFlatfileDirs(beadsDir string) DoctorCheck {
	required := []string{"issues", "comments", "events", "memories"}
	var missing []string
	for _, d := range required {
		if _, err := os.Stat(filepath.Join(beadsDir, d)); os.IsNotExist(err) {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		// Not an error: git does not track empty directories, so a fresh
		// clone of a flatfile repo is always missing its empty subdirs
		// (typically comments/, memories/). NewFlatFileStore recreates
		// every missing dir on the next store open, so the workspace is
		// healthy as-is — reporting StatusError here broke CI doctor
		// gates on every clone and steered users toward a needless
		// --force reinit.
		return DoctorCheck{
			Name:     "Flatfile Directories",
			Status:   StatusOK,
			Message:  fmt.Sprintf("Missing directories (auto-created on next store open): %s", strings.Join(missing, ", ")),
			Category: CategoryCore,
		}
	}
	return DoctorCheck{
		Name:     "Flatfile Directories",
		Status:   StatusOK,
		Message:  "All required directories present",
		Category: CategoryCore,
	}
}

func checkFlatfileIssuesParse(beadsDir string) DoctorCheck {
	issuesDir := filepath.Join(beadsDir, "issues")
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Issue File Integrity",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Cannot read issues directory: %v", err),
			Category: CategoryData,
		}
	}

	total := 0
	var broken []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		total++
		data, err := os.ReadFile(filepath.Join(issuesDir, e.Name()))
		if err != nil {
			broken = append(broken, fmt.Sprintf("%s (read error)", e.Name()))
			continue
		}
		var issue struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(data, &issue); err != nil {
			broken = append(broken, fmt.Sprintf("%s (invalid JSON)", e.Name()))
			continue
		}
		if issue.ID == "" {
			broken = append(broken, fmt.Sprintf("%s (missing id)", e.Name()))
		}
		if issue.Title == "" {
			broken = append(broken, fmt.Sprintf("%s (missing title)", e.Name()))
		}
	}

	if len(broken) > 0 {
		return DoctorCheck{
			Name:     "Issue File Integrity",
			Status:   StatusError,
			Message:  fmt.Sprintf("%d/%d issues have problems", len(broken), total),
			Detail:   strings.Join(broken, "\n"),
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     "Issue File Integrity",
		Status:   StatusOK,
		Message:  fmt.Sprintf("%d issues, all valid JSON with required fields", total),
		Category: CategoryData,
	}
}

func checkFlatfileIDFilenameMatch(beadsDir string) DoctorCheck {
	issuesDir := filepath.Join(beadsDir, "issues")
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Issue ID/Filename Match",
			Status:   StatusWarning,
			Message:  "Cannot read issues directory",
			Category: CategoryData,
		}
	}

	var mismatched []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		expectedID := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(issuesDir, e.Name()))
		if err != nil {
			continue
		}
		var issue struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &issue) != nil {
			continue
		}
		if issue.ID != expectedID {
			mismatched = append(mismatched, fmt.Sprintf("%s contains id=%q", e.Name(), issue.ID))
		}
	}

	if len(mismatched) > 0 {
		return DoctorCheck{
			Name:     "Issue ID/Filename Match",
			Status:   StatusError,
			Message:  fmt.Sprintf("%d issues have ID/filename mismatch", len(mismatched)),
			Detail:   strings.Join(mismatched, "\n"),
			Fix:      "Rename the file to match the ID inside, or update the ID field",
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     "Issue ID/Filename Match",
		Status:   StatusOK,
		Message:  "All issue IDs match their filenames",
		Category: CategoryData,
	}
}

func checkFlatfileOrphans(beadsDir string) DoctorCheck {
	issueIDs, err := loadIssueIDs(filepath.Join(beadsDir, "issues"))
	if err != nil {
		// Computing orphans from an empty ID set would flag every comment
		// and event as orphaned and steer the user toward deleting all
		// history over what is only a read failure (TASKS-awth).
		return DoctorCheck{
			Name:     "Orphan Data",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Cannot read issues directory: %v", err),
			Category: CategoryData,
		}
	}

	var orphans []string

	// Orphan comment dirs
	commentEntries, _ := os.ReadDir(filepath.Join(beadsDir, "comments"))
	for _, e := range commentEntries {
		if e.IsDir() && !issueIDs[e.Name()] {
			orphans = append(orphans, fmt.Sprintf("comments/%s/ (no matching issue)", e.Name()))
		}
	}

	// Orphan event files
	eventEntries, _ := os.ReadDir(filepath.Join(beadsDir, "events"))
	for _, e := range eventEntries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			id := strings.TrimSuffix(e.Name(), ".jsonl")
			if !issueIDs[id] {
				orphans = append(orphans, fmt.Sprintf("events/%s (no matching issue)", e.Name()))
			}
		}
	}

	if len(orphans) > 0 {
		return DoctorCheck{
			Name:     "Orphan Data",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d orphaned comment/event entries", len(orphans)),
			Detail:   strings.Join(orphans, "\n"),
			Fix:      "Remove orphaned directories/files, or recreate the missing issues",
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     "Orphan Data",
		Status:   StatusOK,
		Message:  "No orphaned comments or events",
		Category: CategoryData,
	}
}

func checkFlatfileDanglingDeps(beadsDir string) DoctorCheck {
	issuesDir := filepath.Join(beadsDir, "issues")
	issueIDs, err := loadIssueIDs(issuesDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dependency References",
			Status:   StatusWarning,
			Message:  "Cannot read issues directory",
			Category: CategoryData,
		}
	}

	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		return DoctorCheck{
			Name:     "Dependency References",
			Status:   StatusWarning,
			Message:  "Cannot read issues directory",
			Category: CategoryData,
		}
	}

	var dangling []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(issuesDir, e.Name()))
		var issue struct {
			ID           string              `json:"id"`
			Dependencies []*types.Dependency `json:"dependencies"`
		}
		if json.Unmarshal(data, &issue) != nil {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep.DependsOnID != "" && !issueIDs[dep.DependsOnID] {
				dangling = append(dangling, fmt.Sprintf("%s depends on %s (missing)", issue.ID, dep.DependsOnID))
			}
		}
	}

	if len(dangling) > 0 {
		return DoctorCheck{
			Name:     "Dependency References",
			Status:   StatusWarning,
			Message:  fmt.Sprintf("%d dangling dependency references", len(dangling)),
			Detail:   strings.Join(dangling, "\n"),
			Fix:      "Remove broken dependencies with 'bd dep remove'",
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     "Dependency References",
		Status:   StatusOK,
		Message:  "All dependency targets exist",
		Category: CategoryData,
	}
}

func checkFlatfileCounter(beadsDir string) DoctorCheck {
	counterPath := filepath.Join(beadsDir, "counter.json")
	data, err := os.ReadFile(counterPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DoctorCheck{
				Name:     "ID Counter",
				Status:   StatusOK,
				Message:  "No counter file (hash-based IDs only)",
				Category: CategoryData,
			}
		}
		return DoctorCheck{
			Name:     "ID Counter",
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot read counter.json: %v", err),
			Category: CategoryData,
		}
	}

	var counter struct {
		Prefixes map[string]int `json:"prefixes"`
	}
	if err := json.Unmarshal(data, &counter); err != nil {
		return DoctorCheck{
			Name:     "ID Counter",
			Status:   StatusError,
			Message:  fmt.Sprintf("Invalid counter.json: %v", err),
			Fix:      "Delete counter.json and recreate with 'bd init --force'",
			Category: CategoryData,
		}
	}

	return DoctorCheck{
		Name:     "ID Counter",
		Status:   StatusOK,
		Message:  fmt.Sprintf("Valid, %d prefix(es) tracked", len(counter.Prefixes)),
		Category: CategoryData,
	}
}

func checkFlatfileGitTracking(beadsDir string) DoctorCheck {
	// CheckGitignored returns "" both for "not ignored" and "no git repo";
	// establish the repo exists first, otherwise a workspace with no repo at
	// all would get a false "tracked by git" all-clear (TASKS-4gnf).
	if !flatfile.InGitRepo(beadsDir) {
		return DoctorCheck{
			Name:     "Git Tracking",
			Status:   StatusWarning,
			Message:  "not inside a git repository — flat-file sync (push/pull) has no git repo to work through; issues exist only on this machine",
			Fix:      "Run 'git init' (or move the workspace into a git repo) so .beads/issues/ can be tracked",
			Category: CategoryGit,
		}
	}
	warning := flatfile.CheckGitignored(beadsDir)
	if warning != "" {
		return DoctorCheck{
			Name:     "Git Tracking",
			Status:   StatusError,
			Message:  ".beads/issues/ is gitignored — flat-file sync will not work",
			Detail:   warning,
			Fix:      "Remove or narrow the gitignore pattern so .beads/issues/ is tracked",
			Category: CategoryGit,
		}
	}
	return DoctorCheck{
		Name:     "Git Tracking",
		Status:   StatusOK,
		Message:  ".beads/issues/ is tracked by git",
		Category: CategoryGit,
	}
}

// loadIssueIDs returns a set of issue IDs from the issues directory. A read
// error is returned rather than swallowed: an empty set caused by EACCES or
// I/O failure is indistinguishable from "no issues" and made callers report
// every comment/event as orphaned (TASKS-awth).
func loadIssueIDs(issuesDir string) (map[string]bool, error) {
	ids := make(map[string]bool)
	entries, err := os.ReadDir(issuesDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".tmp") {
			ids[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	return ids, nil
}
