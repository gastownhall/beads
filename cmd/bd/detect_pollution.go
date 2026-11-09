package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// Test-pollution helpers for doctor_pollution.go, create.go and export.go.
// The scorer itself lives in cmd/bd/doctor/pollution.go so the doctor
// validation gate shares it (GH#5025). The deprecated 'bd detect-pollution'
// command has been removed; use 'bd doctor --check=pollution' instead.

// isTestIssue checks if an issue title looks like a test issue based on common
// test prefixes. Used for warnings during creation and for export filtering;
// pollution scoring itself goes through doctor.DetectTestPollution.
func isTestIssue(title string) bool {
	return doctor.HasTestTitlePrefix(title)
}

// pollutionResult mirrors doctor.PollutionResult on the historical package-main
// seam, so the behavioral regression tests for the scorer (detect_pollution_test.go)
// keep compiling while production code uses the exported doctor type.
type pollutionResult struct {
	issue   *types.Issue
	score   float64
	reasons []string
}

// detectTestPollution adapts doctor.DetectTestPollution to that seam.
func detectTestPollution(issues []*types.Issue) []pollutionResult {
	results := make([]pollutionResult, 0, len(issues))
	for _, p := range doctor.DetectTestPollution(issues) {
		results = append(results, pollutionResult{issue: p.Issue, score: p.Score, reasons: p.Reasons})
	}
	return results
}

func backupPollutedIssues(polluted []doctor.PollutionResult, path string) error {
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
