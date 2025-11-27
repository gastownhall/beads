package fix

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/deletions"
)

// HydrateDeletionsManifest populates deletions.jsonl from git history.
// It finds all issue IDs that were ever in the JSONL but are no longer present,
// and adds them to the deletions manifest.
func HydrateDeletionsManifest(path string) error {
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	// bd-6xd: issues.jsonl is the canonical filename
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	// Also check for legacy beads.jsonl
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		legacyPath := filepath.Join(beadsDir, "beads.jsonl")
		if _, err := os.Stat(legacyPath); err == nil {
			jsonlPath = legacyPath
		} else {
			return fmt.Errorf("no JSONL file found in .beads/")
		}
	}

	// Load existing deletions manifest to avoid duplicates
	deletionsPath := deletions.DefaultPath(beadsDir)
	existingDeletions, err := deletions.LoadDeletions(deletionsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing deletions: %w", err)
	}

	// Get current IDs from JSONL
	currentIDs, err := getCurrentJSONLIDs(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read current JSONL: %w", err)
	}

	// Get historical IDs from git
	historicalIDs, err := getHistoricalJSONLIDs(path, jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to get historical IDs from git: %w", err)
	}

	// Find deleted IDs (in history but not in current, and not already in manifest)
	var deletedIDs []string
	for id := range historicalIDs {
		if !currentIDs[id] {
			// Skip if already in deletions manifest
			if _, exists := existingDeletions.Records[id]; exists {
				continue
			}
			deletedIDs = append(deletedIDs, id)
		}
	}

	if len(deletedIDs) == 0 {
		// Create empty deletions manifest to signal hydration is complete
		// This prevents the check from re-warning after --fix runs
		if err := deletions.WriteDeletions(deletionsPath, nil); err != nil {
			return fmt.Errorf("failed to create empty deletions manifest: %w", err)
		}
		fmt.Println("  No deleted issues found in git history (created empty manifest)")
		return nil
	}

	// Add to deletions manifest
	now := time.Now()

	for _, id := range deletedIDs {
		record := deletions.DeletionRecord{
			ID:        id,
			Timestamp: now,
			Actor:     "bd-doctor-hydrate",
			Reason:    "Hydrated from git history",
		}
		if err := deletions.AppendDeletion(deletionsPath, record); err != nil {
			return fmt.Errorf("failed to append deletion record for %s: %w", id, err)
		}
	}

	fmt.Printf("  Added %d deletion records to manifest\n", len(deletedIDs))
	return nil
}

// getCurrentJSONLIDs reads the current JSONL file and returns a set of IDs.
func getCurrentJSONLIDs(jsonlPath string) (map[string]bool, error) {
	ids := make(map[string]bool)

	file, err := os.Open(jsonlPath) // #nosec G304 - path validated by caller
	if err != nil {
		if os.IsNotExist(err) {
			return ids, nil
		}
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &issue); err != nil {
			continue
		}
		if issue.ID != "" {
			ids[issue.ID] = true
		}
	}

	return ids, scanner.Err()
}

// getHistoricalJSONLIDs uses git log to find all IDs that were ever in the JSONL.
func getHistoricalJSONLIDs(repoPath, jsonlPath string) (map[string]bool, error) {
	// Get the relative path for the JSONL file
	relPath, err := filepath.Rel(repoPath, jsonlPath)
	if err != nil {
		relPath = jsonlPath
	}

	// Use the commit-by-commit approach which is more memory efficient
	// and allows us to properly parse JSON rather than regex matching
	return getHistoricalIDsViaDiff(repoPath, relPath)
}

// looksLikeIssueID validates that a string looks like a beads issue ID.
// Issue IDs have the format: prefix-hash or prefix-number (e.g., bd-abc123, myproject-42)
func looksLikeIssueID(id string) bool {
	if id == "" {
		return false
	}
	// Must contain at least one dash
	dashIdx := strings.Index(id, "-")
	if dashIdx <= 0 || dashIdx >= len(id)-1 {
		return false
	}
	// Prefix should be alphanumeric (letters/numbers/underscores)
	prefix := id[:dashIdx]
	for _, c := range prefix {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	// Suffix should be alphanumeric (base36 hash or number), may contain dots for children
	suffix := id[dashIdx+1:]
	for _, c := range suffix {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.') {
			return false
		}
	}
	return true
}

// getHistoricalIDsViaDiff walks through git history commit-by-commit to find all IDs.
// This is more memory efficient than git log -p and allows proper JSON parsing.
func getHistoricalIDsViaDiff(repoPath, relPath string) (map[string]bool, error) {
	ids := make(map[string]bool)

	// Get list of all commits that touched the file
	cmd := exec.Command("git", "log", "--all", "--format=%H", "--", relPath)
	cmd.Dir = repoPath

	output, err := cmd.Output()
	if err != nil {
		return ids, fmt.Errorf("git log failed: %w", err)
	}

	commits := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(commits) == 0 || (len(commits) == 1 && commits[0] == "") {
		return ids, nil
	}

	// For each commit, get the file content and extract IDs
	for _, commit := range commits {
		if commit == "" {
			continue
		}

		// Get file content at this commit
		showCmd := exec.Command("git", "show", commit+":"+relPath) // #nosec G204 - args are from git log output
		showCmd.Dir = repoPath

		content, err := showCmd.Output()
		if err != nil {
			// File might not exist at this commit
			continue
		}

		// Parse each line for IDs
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, `"id"`) {
				var issue struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(line), &issue); err == nil && issue.ID != "" {
					// Validate the ID looks like an issue ID to avoid false positives
					if looksLikeIssueID(issue.ID) {
						ids[issue.ID] = true
					}
				}
			}
		}
	}

	return ids, nil
}
