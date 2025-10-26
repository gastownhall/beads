package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var duplicatesCmd = &cobra.Command{
	Use:   "duplicates",
	Short: "Find and optionally merge duplicate issues",
	Long: `Find issues with identical content (title, description, design, acceptance criteria).

Groups issues by content hash and reports duplicates with suggested merge targets.
The merge target is chosen by:
1. Reference count (most referenced issue wins)
2. Lexicographically smallest ID if reference counts are equal

Only groups issues with matching status (open with open, closed with closed).

Example:
  bd duplicates                    # Show all duplicate groups
  bd duplicates --auto-merge       # Automatically merge all duplicates
  bd duplicates --dry-run          # Show what would be merged`,
	Run: func(cmd *cobra.Command, _ []string) {
		// Check daemon mode - not supported yet (merge command limitation)
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: duplicates command not yet supported in daemon mode (see bd-190)\n")
			fmt.Fprintf(os.Stderr, "Use: bd --no-daemon duplicates\n")
			os.Exit(1)
		}

		autoMerge, _ := cmd.Flags().GetBool("auto-merge")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		ctx := context.Background()

		// Get all issues
		allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
			os.Exit(1)
		}

		// Find duplicates
		duplicateGroups := findDuplicateGroups(allIssues)

		if len(duplicateGroups) == 0 {
			if !jsonOutput {
				fmt.Println("No duplicates found!")
			} else {
				outputJSON(map[string]interface{}{
					"duplicate_groups": 0,
					"groups":           []interface{}{},
				})
			}
			return
		}

		// Count references for each issue
		refCounts := countReferences(allIssues)

		// Prepare output
		var mergeCommands []string
		var mergeResults []map[string]interface{}

		for _, group := range duplicateGroups {
			target := chooseMergeTarget(group, refCounts)
			sources := make([]string, 0, len(group)-1)
			for _, issue := range group {
				if issue.ID != target.ID {
					sources = append(sources, issue.ID)
				}
			}

			if autoMerge || dryRun {
				// Perform merge (unless dry-run)
				if !dryRun {
					result, err := performMerge(ctx, target.ID, sources)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error merging %s into %s: %v\n", strings.Join(sources, ", "), target.ID, err)
						continue
					}

					if jsonOutput {
						mergeResults = append(mergeResults, map[string]interface{}{
							"target_id":            target.ID,
							"source_ids":           sources,
							"dependencies_added":   result.depsAdded,
							"dependencies_skipped": result.depsSkipped,
							"text_references":      result.textRefCount,
							"issues_closed":        result.issuesClosed,
							"issues_skipped":       result.issuesSkipped,
						})
					}
				}

				cmd := fmt.Sprintf("bd merge %s --into %s", strings.Join(sources, " "), target.ID)
				mergeCommands = append(mergeCommands, cmd)
			} else {
				cmd := fmt.Sprintf("bd merge %s --into %s", strings.Join(sources, " "), target.ID)
				mergeCommands = append(mergeCommands, cmd)
			}
		}

		// Mark dirty if we performed merges
		if autoMerge && !dryRun && len(mergeCommands) > 0 {
			markDirtyAndScheduleFlush()
		}

		// Output results
		if jsonOutput {
			output := map[string]interface{}{
				"duplicate_groups": len(duplicateGroups),
				"groups":           formatDuplicateGroupsJSON(duplicateGroups, refCounts),
			}
			if autoMerge || dryRun {
				output["merge_commands"] = mergeCommands
				if autoMerge && !dryRun {
					output["merge_results"] = mergeResults
				}
			}
			outputJSON(output)
		} else {
			yellow := color.New(color.FgYellow).SprintFunc()
			cyan := color.New(color.FgCyan).SprintFunc()
			green := color.New(color.FgGreen).SprintFunc()

			fmt.Printf("%s Found %d duplicate group(s):\n\n", yellow("🔍"), len(duplicateGroups))

			for i, group := range duplicateGroups {
				target := chooseMergeTarget(group, refCounts)
				fmt.Printf("%s Group %d: %s\n", cyan("━━"), i+1, group[0].Title)

				for _, issue := range group {
					refs := refCounts[issue.ID]
					marker := "  "
					if issue.ID == target.ID {
						marker = green("→ ")
					}
					fmt.Printf("%s%s (%s, P%d, %d references)\n",
						marker, issue.ID, issue.Status, issue.Priority, refs)
				}

				sources := make([]string, 0, len(group)-1)
				for _, issue := range group {
					if issue.ID != target.ID {
						sources = append(sources, issue.ID)
					}
				}
				fmt.Printf("  %s bd merge %s --into %s\n\n",
					cyan("Suggested:"), strings.Join(sources, " "), target.ID)
			}

			if autoMerge {
				if dryRun {
					fmt.Printf("%s Dry run - would execute %d merge(s)\n", yellow("⚠"), len(mergeCommands))
				} else {
					fmt.Printf("%s Merged %d group(s)\n", green("✓"), len(mergeCommands))
				}
			} else {
				fmt.Printf("%s Run with --auto-merge to execute all suggested merges\n", cyan("💡"))
			}
		}
	},
}

func init() {
	duplicatesCmd.Flags().Bool("auto-merge", false, "Automatically merge all duplicates")
	duplicatesCmd.Flags().Bool("dry-run", false, "Show what would be merged without making changes")
	rootCmd.AddCommand(duplicatesCmd)
}

// contentKey represents the fields we use to identify duplicate issues
type contentKey struct {
	title              string
	description        string
	design             string
	acceptanceCriteria string
	status             string // Only group issues with same status
}

// findDuplicateGroups groups issues by content hash
func findDuplicateGroups(issues []*types.Issue) [][]*types.Issue {
	groups := make(map[contentKey][]*types.Issue)

	for _, issue := range issues {
		key := contentKey{
			title:              issue.Title,
			description:        issue.Description,
			design:             issue.Design,
			acceptanceCriteria: issue.AcceptanceCriteria,
			status:             string(issue.Status),
		}

		groups[key] = append(groups[key], issue)
	}

	// Filter to only groups with duplicates
	var duplicates [][]*types.Issue
	for _, group := range groups {
		if len(group) > 1 {
			duplicates = append(duplicates, group)
		}
	}

	return duplicates
}

// countReferences counts how many times each issue is referenced in text fields
func countReferences(issues []*types.Issue) map[string]int {
	counts := make(map[string]int)
	idPattern := regexp.MustCompile(`\b[a-zA-Z][-a-zA-Z0-9]*-\d+\b`)

	for _, issue := range issues {
		// Search in all text fields
		textFields := []string{
			issue.Description,
			issue.Design,
			issue.AcceptanceCriteria,
			issue.Notes,
		}

		for _, text := range textFields {
			matches := idPattern.FindAllString(text, -1)
			for _, match := range matches {
				counts[match]++
			}
		}
	}

	return counts
}

// chooseMergeTarget selects the best issue to merge into
// Priority: highest reference count, then lexicographically smallest ID
func chooseMergeTarget(group []*types.Issue, refCounts map[string]int) *types.Issue {
	if len(group) == 0 {
		return nil
	}

	target := group[0]
	targetRefs := refCounts[target.ID]

	for _, issue := range group[1:] {
		issueRefs := refCounts[issue.ID]
		if issueRefs > targetRefs || (issueRefs == targetRefs && issue.ID < target.ID) {
			target = issue
			targetRefs = issueRefs
		}
	}

	return target
}

// formatDuplicateGroupsJSON formats duplicate groups for JSON output
func formatDuplicateGroupsJSON(groups [][]*types.Issue, refCounts map[string]int) []map[string]interface{} {
	var result []map[string]interface{}

	for _, group := range groups {
		target := chooseMergeTarget(group, refCounts)
		issues := make([]map[string]interface{}, len(group))

		for i, issue := range group {
			issues[i] = map[string]interface{}{
				"id":              issue.ID,
				"title":           issue.Title,
				"status":          issue.Status,
				"priority":        issue.Priority,
				"references":      refCounts[issue.ID],
				"is_merge_target": issue.ID == target.ID,
			}
		}

		sources := make([]string, 0, len(group)-1)
		for _, issue := range group {
			if issue.ID != target.ID {
				sources = append(sources, issue.ID)
			}
		}

		result = append(result, map[string]interface{}{
			"title":               group[0].Title,
			"issues":              issues,
			"suggested_target":    target.ID,
			"suggested_sources":   sources,
			"suggested_merge_cmd": fmt.Sprintf("bd merge %s --into %s", strings.Join(sources, " "), target.ID),
		})
	}

	return result
}
