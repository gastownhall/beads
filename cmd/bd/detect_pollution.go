package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

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

// testPrefixPattern matches common test issue title prefixes.
// Compiled once at package level for use in isTestIssue and detectTestPollution.
var testPrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// isTestIssue checks if an issue title looks like a test issue based on common test prefixes.
// This function is used both for warnings during creation and for pollution detection.
func isTestIssue(title string) bool {
	return testPrefixPattern.MatchString(strings.ToLower(title))
}

// detectTestPollution scores candidates that look like leaked test fixtures (GH#5025);
// see the inline signal comments below for the corroboration policy.
func detectTestPollution(issues []*types.Issue) []pollutionResult {
	var results []pollutionResult
	sequentialPattern := regexp.MustCompile(`^[a-z]+-\d+$`)

	for _, issue := range issues {
		if issue == nil {
			continue
		}
		// Never flag closed issues or epics (destructive --clean must not touch real work).
		if issue.Status == types.StatusClosed {
			continue
		}
		if issue.IssueType == types.TypeEpic {
			continue
		}

		score := 0.0
		var reasons []string
		corroboration := false

		title := strings.ToLower(issue.Title)
		desc := strings.TrimSpace(issue.Description)

		// Title prefix is a weak signal alone (0.4) — real bugs/infra use "test-*" names.
		hasPrefix := testPrefixPattern.MatchString(title)
		if hasPrefix {
			score += 0.4
			reasons = append(reasons, "Title starts with test prefix")
		}

		// Sequential bare ID + minimal description (corroborates fixture-style issues).
		// NB: this pattern also matches ordinary "prefix-N" IDs from
		// issue_id_mode=counter (internal/storage/issueops/helpers.go
		// NextCounterIDTx) or an explicit `bd create --id bd-42`, so it must
		// never be enough on its own without other test-specific signals.
		if sequentialPattern.MatchString(issue.ID) && len(desc) < 20 {
			score += 0.4
			corroboration = true
			reasons = append(reasons, "Sequential ID with minimal description")
		}

		// Empty / near-empty description (corroborates throwaway fixtures).
		if len(desc) == 0 {
			score += 0.4
			corroboration = true
			reasons = append(reasons, "No description")
		} else if len(desc) < 20 {
			// Bump applies only with a test-prefixed title (GH#5137): counter-mode
			// "prefix-N" IDs alone must not push a real issue over threshold.
			// Deliberately clears 0.7 on prefix+thin alone; see the boundary test.
			weight := 0.2
			if hasPrefix {
				weight = 0.3
			}
			score += weight
			corroboration = true
			reasons = append(reasons, "Very short description")
		}

		// Explicit generic fixture titles (corroboration, not a standalone
		// clean signal). Weighted at 0.3 rather than 0.5 (GH#5137 review):
		// these are unanchored substring matches ("test issue" also appears
		// inside legitimate titles like "Fix test issue detection
		// regression"), so title-substring + empty-description alone
		// (0.3 + 0.4 = 0.7) must land in the review-only band and never
		// reach the 0.9 --clean cutoff without another corroborating signal
		// (e.g. a test-prefixed title or a sequential/hash fixture ID).
		if strings.Contains(title, "issue for testing") ||
			strings.Contains(title, "test issue") ||
			strings.Contains(title, "sample issue") {
			score += 0.3
			corroboration = true
			reasons = append(reasons, "Generic test title")
		}

		// Threshold AND corroboration: prefix-only titled real work stays unflagged.
		if score >= 0.7 && corroboration {
			results = append(results, pollutionResult{
				issue:   issue,
				score:   score,
				reasons: reasons,
			})
		}
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
