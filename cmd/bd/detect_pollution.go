package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// Pollution utilities used by doctor_pollution.go, create.go, and export.go.
// The classifier itself is doctor.DetectTestPollution, shared with the full
// doctor run's Test Pollution check.
// The deprecated 'bd detect-pollution' command has been removed;
// use 'bd doctor --check=pollution' instead.

// isTestIssue checks if an issue title looks like a test issue based on common test prefixes.
// create uses it for its warning and description requirement and export for
// --scrub; the shared predicate lives in doctor.IsTestTitle.
func isTestIssue(title string) bool {
	return doctor.IsTestTitle(title)
}

func backupPollutedIssues(polluted []doctor.PollutionMatch, path string) error {
	// Create backup file
	// nolint:gosec // G304: path is provided by user as explicit backup location
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer file.Close()

	// Write each issue as JSONL
	for _, p := range polluted {
		data, err := json.Marshal(p.Issue)
		if err != nil {
			return fmt.Errorf("failed to marshal issue %s: %w", p.Issue.ID, err)
		}

		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			return fmt.Errorf("failed to write issue %s: %w", p.Issue.ID, err)
		}
	}

	return nil
}
