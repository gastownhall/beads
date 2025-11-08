package main
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)
var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Run comprehensive database health checks",
	Long: `Run all validation checks to ensure database integrity:
- Orphaned dependencies (references to deleted issues)
- Duplicate issues (identical content)
- Test pollution (leaked test issues)
- Git merge conflicts in JSONL
Example:
  bd validate                           # Run all checks
  bd validate --fix-all                 # Auto-fix all issues
  bd validate --checks=orphans,dupes    # Run specific checks
  bd validate --checks=conflicts        # Check for git conflicts
  bd validate --json                    # Output in JSON format`,
	Run: func(cmd *cobra.Command, _ []string) {
		// Check daemon mode - not supported yet (uses direct storage access)
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: validate command not yet supported in daemon mode\n")
			fmt.Fprintf(os.Stderr, "Use: bd --no-daemon validate\n")
			os.Exit(1)
		}
		fixAll, _ := cmd.Flags().GetBool("fix-all")
		checksFlag, _ := cmd.Flags().GetString("checks")
		jsonOut, _ := cmd.Flags().GetBool("json")
		ctx := context.Background()
		// Parse and normalize checks
		checks, err := parseChecks(checksFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintf(os.Stderr, "Valid checks: orphans, duplicates, pollution, conflicts\n")
			os.Exit(2)
		}
		// Fetch all issues once for checks that need them
		var allIssues []*types.Issue
		needsIssues := false
		for _, check := range checks {
			if check == "orphans" || check == "duplicates" || check == "pollution" {
				needsIssues = true
				break
			}
		}
		if needsIssues {
			allIssues, err = store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
				os.Exit(1)
			}
		}
		results := validationResults{
			checks:      make(map[string]checkResult),
			checkOrder:  checks,
		}
		// Run each check
		for _, check := range checks {
			switch check {
			case "orphans":
				results.checks["orphans"] = validateOrphanedDeps(ctx, allIssues, fixAll)
			case "duplicates":
				results.checks["duplicates"] = validateDuplicates(ctx, allIssues, fixAll)
			case "pollution":
				results.checks["pollution"] = validatePollution(ctx, allIssues, fixAll)
			case "conflicts":
				results.checks["conflicts"] = validateGitConflicts(ctx, fixAll)
			}
		}
		// Output results
		if jsonOut {
			outputJSON(results.toJSON())
		} else {
			results.print(fixAll)
		}
		// Exit with error code if issues found or errors occurred
		if results.hasFailures() {
			os.Exit(1)
		}
	},
}
// parseChecks normalizes and validates check names
func parseChecks(checksFlag string) ([]string, error) {
	defaultChecks := []string{"orphans", "duplicates", "pollution", "conflicts"}
	if checksFlag == "" {
		return defaultChecks, nil
	}
	// Map of synonyms to canonical names
	synonyms := map[string]string{
		"dupes":         "duplicates",
		"git-conflicts": "conflicts",
	}
	var result []string
	seen := make(map[string]bool)
	parts := strings.Split(checksFlag, ",")
	for _, part := range parts {
		check := strings.ToLower(strings.TrimSpace(part))
		if check == "" {
			continue
		}
		// Map synonyms
		if canonical, ok := synonyms[check]; ok {
			check = canonical
		}
		// Validate
		valid := false
		for _, validCheck := range defaultChecks {
			if check == validCheck {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown check: %s", part)
		}
		// Deduplicate
		if !seen[check] {
			seen[check] = true
			result = append(result, check)
		}
	}
	return result, nil
}
type checkResult struct {
	name        string
	issueCount  int
	fixedCount  int
	err         error
	suggestions []string
}
type validationResults struct {
	checks     map[string]checkResult
	checkOrder []string
}
func (r *validationResults) hasFailures() bool {
	for _, result := range r.checks {
		if result.err != nil {
			return true
		}
		if result.issueCount > 0 && result.fixedCount < result.issueCount {
			return true
		}
	}
	return false
}
func (r *validationResults) toJSON() map[string]interface{} {
	output := map[string]interface{}{
		"checks": map[string]interface{}{},
	}
	totalIssues := 0
	totalFixed := 0
	hasErrors := false
	for name, result := range r.checks {
		var errorStr interface{}
		if result.err != nil {
			errorStr = result.err.Error()
			hasErrors = true
		}
		output["checks"].(map[string]interface{})[name] = map[string]interface{}{
			"issue_count":  result.issueCount,
			"fixed_count":  result.fixedCount,
			"error":        errorStr,
			"failed":       result.err != nil,
			"suggestions":  result.suggestions,
		}
		totalIssues += result.issueCount
		totalFixed += result.fixedCount
	}
	output["total_issues"] = totalIssues
	output["total_fixed"] = totalFixed
	output["healthy"] = !hasErrors && (totalIssues == 0 || totalIssues == totalFixed)
	return output
}
func (r *validationResults) print(_ bool) {
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()
	fmt.Println("\nValidation Results:")
	fmt.Println("===================")
	totalIssues := 0
	totalFixed := 0
	// Print in deterministic order
	for _, name := range r.checkOrder {
		result := r.checks[name]
		prefix := "✓"
		colorFunc := green
		if result.err != nil {
			prefix = "✗"
			colorFunc = red
			fmt.Printf("%s %s: ERROR - %v\n", colorFunc(prefix), result.name, result.err)
		} else if result.issueCount > 0 {
			prefix = "⚠"
			colorFunc = yellow
			if result.fixedCount > 0 {
				fmt.Printf("%s %s: %d found, %d fixed\n", colorFunc(prefix), result.name, result.issueCount, result.fixedCount)
			} else {
				fmt.Printf("%s %s: %d found\n", colorFunc(prefix), result.name, result.issueCount)
			}
		} else {
			fmt.Printf("%s %s: OK\n", colorFunc(prefix), result.name)
		}
		totalIssues += result.issueCount
		totalFixed += result.fixedCount
	}
	fmt.Println()
	if totalIssues == 0 {
		fmt.Printf("%s Database is healthy!\n", green("✓"))
	} else if totalFixed == totalIssues {
		fmt.Printf("%s Fixed all %d issues\n", green("✓"), totalFixed)
	} else {
		remaining := totalIssues - totalFixed
		fmt.Printf("%s Found %d issues", yellow("⚠"), totalIssues)
		if totalFixed > 0 {
			fmt.Printf(" (fixed %d, %d remaining)", totalFixed, remaining)
		}
		fmt.Println()
		// Print suggestions
		fmt.Println("\nRecommendations:")
		for _, result := range r.checks {
			for _, suggestion := range result.suggestions {
				fmt.Printf("  - %s\n", suggestion)
			}
		}
	}
}
func validateOrphanedDeps(ctx context.Context, allIssues []*types.Issue, fix bool) checkResult {
	result := checkResult{name: "orphaned dependencies"}
	// Build ID existence map
	existingIDs := make(map[string]bool)
	for _, issue := range allIssues {
		existingIDs[issue.ID] = true
	}
	// Find orphaned dependencies
	type orphanedDep struct {
		issueID    string
		orphanedID string
	}
	var orphaned []orphanedDep
	for _, issue := range allIssues {
		for _, dep := range issue.Dependencies {
			if !existingIDs[dep.DependsOnID] {
				orphaned = append(orphaned, orphanedDep{
					issueID:    issue.ID,
					orphanedID: dep.DependsOnID,
				})
			}
		}
	}
	result.issueCount = len(orphaned)
	if fix && len(orphaned) > 0 {
		// Group by issue
		orphansByIssue := make(map[string][]string)
		for _, o := range orphaned {
			orphansByIssue[o.issueID] = append(orphansByIssue[o.issueID], o.orphanedID)
		}
		// Fix each issue
		for issueID, orphanedIDs := range orphansByIssue {
			for _, orphanedID := range orphanedIDs {
				if err := store.RemoveDependency(ctx, issueID, orphanedID, actor); err == nil {
					result.fixedCount++
				}
			}
		}
		if result.fixedCount > 0 {
			markDirtyAndScheduleFlush()
		}
	}
	if result.issueCount > result.fixedCount {
		result.suggestions = append(result.suggestions, "Run 'bd repair-deps --fix' to remove orphaned dependencies")
	}
	return result
}
func validateDuplicates(_ context.Context, allIssues []*types.Issue, fix bool) checkResult {
	result := checkResult{name: "duplicates"}
	// Find duplicates
	duplicateGroups := findDuplicateGroups(allIssues)
	// Count total duplicate issues (excluding one canonical per group)
	for _, group := range duplicateGroups {
		result.issueCount += len(group) - 1
	}
	if fix && len(duplicateGroups) > 0 {
		// Note: Auto-merge is complex and requires user review
		// We don't auto-fix duplicates, just report them
		result.suggestions = append(result.suggestions, 
			fmt.Sprintf("Run 'bd duplicates --auto-merge' to merge %d duplicate groups", len(duplicateGroups)))
	} else if result.issueCount > 0 {
		result.suggestions = append(result.suggestions,
			fmt.Sprintf("Run 'bd duplicates' to review %d duplicate groups", len(duplicateGroups)))
	}
	return result
}
func validatePollution(_ context.Context, allIssues []*types.Issue, fix bool) checkResult {
	result := checkResult{name: "test pollution"}
	// Detect pollution
	polluted := detectTestPollution(allIssues)
	result.issueCount = len(polluted)
	if fix && len(polluted) > 0 {
		// Note: Deleting issues is destructive, we just suggest it
		result.suggestions = append(result.suggestions,
			fmt.Sprintf("Run 'bd detect-pollution --clean' to delete %d test issues", len(polluted)))
	} else if result.issueCount > 0 {
		result.suggestions = append(result.suggestions,
			fmt.Sprintf("Run 'bd detect-pollution' to review %d potential test issues", len(polluted)))
	}
	return result
}
func validateGitConflicts(_ context.Context, fix bool) checkResult {
	result := checkResult{name: "git conflicts"}
	// Check JSONL file for conflict markers
	jsonlPath := findJSONLPath()
	// nolint:gosec // G304: jsonlPath is validated JSONL file from findJSONLPath
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No JSONL file = no conflicts
			return result
		}
		result.err = fmt.Errorf("failed to read JSONL: %w", err)
		return result
	}
	// Look for git conflict markers in raw bytes (before JSON decoding)
	// This prevents false positives when issue content contains these strings
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
	if len(conflictLines) > 0 {
		result.issueCount = 1 // One conflict situation
		result.suggestions = append(result.suggestions,
			fmt.Sprintf("Git conflict markers found in %s at lines: %v", jsonlPath, conflictLines))
		result.suggestions = append(result.suggestions,
			"To resolve, choose one version:")
		result.suggestions = append(result.suggestions,
			"  git checkout --ours .beads/issues.jsonl && bd import -i .beads/issues.jsonl")
		result.suggestions = append(result.suggestions,
			"  git checkout --theirs .beads/issues.jsonl && bd import -i .beads/issues.jsonl")
		result.suggestions = append(result.suggestions,
			"For advanced field-level merging: https://github.com/neongreen/mono/tree/main/beads-merge")
	}
	// Can't auto-fix git conflicts
	if fix && result.issueCount > 0 {
		result.suggestions = append(result.suggestions,
			"Note: Git conflicts cannot be auto-fixed with --fix-all")
	}
	return result
}
func init() {
	validateCmd.Flags().Bool("fix-all", false, "Auto-fix all fixable issues")
	validateCmd.Flags().String("checks", "", "Comma-separated list of checks (orphans,duplicates,pollution,conflicts)")
	rootCmd.AddCommand(validateCmd)
}
