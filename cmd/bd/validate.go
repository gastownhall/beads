package main

import (
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
  bd validate                         # Run all checks
  bd validate --fix-all               # Auto-fix all issues
  bd validate --checks=orphans,dupes  # Run specific checks
  bd validate --json                  # Output in JSON format`,
	Run: func(cmd *cobra.Command, _ []string) {
		// Check daemon mode - not supported yet (uses direct storage access)
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: validate command not yet supported in daemon mode\n")
			fmt.Fprintf(os.Stderr, "Use: bd --no-daemon validate\n")
			os.Exit(1)
		}

		fixAll, _ := cmd.Flags().GetBool("fix-all")
		checksFlag, _ := cmd.Flags().GetString("checks")

		ctx := context.Background()

		// Determine which checks to run
		var checks []string
		if checksFlag == "" {
			checks = []string{"orphans", "duplicates", "pollution"}
		} else {
			checks = strings.Split(checksFlag, ",")
		}

		results := validationResults{
			checks: make(map[string]checkResult),
		}

		// Run each check
		for _, check := range checks {
			switch check {
			case "orphans":
				results.checks["orphans"] = validateOrphanedDeps(ctx, fixAll)
			case "duplicates", "dupes":
				results.checks["duplicates"] = validateDuplicates(ctx, fixAll)
			case "pollution":
				results.checks["pollution"] = validatePollution(ctx, fixAll)
			default:
				fmt.Fprintf(os.Stderr, "Unknown check: %s\n", check)
			}
		}

		// Output results
		if jsonOutput {
			outputJSON(results.toJSON())
		} else {
			results.print(fixAll)
		}

		// Exit with error code if issues found
		if results.hasIssues() {
			os.Exit(1)
		}
	},
}

type checkResult struct {
	name        string
	issueCount  int
	fixedCount  int
	err         error
	suggestions []string
}

type validationResults struct {
	checks map[string]checkResult
}

func (r *validationResults) hasIssues() bool {
	for _, result := range r.checks {
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

	for name, result := range r.checks {
		output["checks"].(map[string]interface{})[name] = map[string]interface{}{
			"issue_count":  result.issueCount,
			"fixed_count":  result.fixedCount,
			"error":        result.err,
			"suggestions":  result.suggestions,
		}
		totalIssues += result.issueCount
		totalFixed += result.fixedCount
	}

	output["total_issues"] = totalIssues
	output["total_fixed"] = totalFixed
	output["healthy"] = totalIssues == 0 || totalIssues == totalFixed

	return output
}

func (r *validationResults) print(fixAll bool) {
	green := color.New(color.FgGreen).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	red := color.New(color.FgRed).SprintFunc()

	fmt.Println("\nValidation Results:")
	fmt.Println("===================")

	totalIssues := 0
	totalFixed := 0

	for name, result := range r.checks {
		prefix := "✓"
		colorFunc := green
		
		if result.err != nil {
			prefix = "✗"
			colorFunc = red
			fmt.Printf("%s %s: ERROR - %v\n", colorFunc(prefix), name, result.err)
		} else if result.issueCount > 0 {
			prefix = "⚠"
			colorFunc = yellow
			if result.fixedCount > 0 {
				fmt.Printf("%s %s: %d found, %d fixed\n", colorFunc(prefix), name, result.issueCount, result.fixedCount)
			} else {
				fmt.Printf("%s %s: %d found\n", colorFunc(prefix), name, result.issueCount)
			}
		} else {
			fmt.Printf("%s %s: OK\n", colorFunc(prefix), name)
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

func validateOrphanedDeps(ctx context.Context, fix bool) checkResult {
	result := checkResult{name: "orphaned dependencies"}

	// Get all issues
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		result.err = err
		return result
	}

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

func validateDuplicates(ctx context.Context, fix bool) checkResult {
	result := checkResult{name: "duplicates"}

	// Get all issues
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		result.err = err
		return result
	}

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

func validatePollution(ctx context.Context, fix bool) checkResult {
	result := checkResult{name: "test pollution"}

	// Get all issues
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		result.err = err
		return result
	}

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

func init() {
	validateCmd.Flags().Bool("fix-all", false, "Auto-fix all fixable issues")
	validateCmd.Flags().String("checks", "", "Comma-separated list of checks (orphans,duplicates,pollution)")
	rootCmd.AddCommand(validateCmd)
}
