package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// Pollution detection utilities used by doctor_pollution.go, create.go, and export.go.
// The deprecated 'bd detect-pollution' command has been removed;
// use 'bd doctor --check=pollution' instead.

type pollutionResult struct {
	issue   *types.Issue
	score   float64
	reasons []string
}

// highConfidence reports whether the hit scored in the high-confidence band.
func (p pollutionResult) highConfidence() bool {
	return doctor.PollutionResult{Score: p.score}.HighConfidence()
}

// isTestIssue checks if an issue title looks like a test issue based on common test prefixes.
// It is the title check alone, used for warnings during creation and export;
// pollution detection needs corroborating signals (doctor.DetectTestPollution).
func isTestIssue(title string) bool {
	return doctor.HasTestTitlePrefix(title)
}

// detectTestPollution runs the scorer shared with the validate gate
// (doctor.DetectTestPollution).
func detectTestPollution(issues []*types.Issue) []pollutionResult {
	var results []pollutionResult
	for _, p := range doctor.DetectTestPollution(issues) {
		results = append(results, pollutionResult{issue: p.Issue, score: p.Score, reasons: p.Reasons})
	}
	return results
}

func backupPollutedIssues(polluted []pollutionResult, path string) error {
	// Create backup file
	// nolint:gosec // G304: path is provided by user as explicit backup location
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create backup file: %w", err)
	}
	defer file.Close()

	// Write each issue as JSONL
	for _, p := range polluted {
		data, err := json.Marshal(p.issue)
		if err != nil {
			return fmt.Errorf("failed to marshal issue %s: %w", p.issue.ID, err)
		}

		if _, err := file.WriteString(string(data) + "\n"); err != nil {
			return fmt.Errorf("failed to write issue %s: %w", p.issue.ID, err)
		}
	}

	return nil
}
