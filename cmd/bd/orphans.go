package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/ui"
)

var doctorFindOrphanedIssues = doctor.FindOrphanedIssues

var closeIssueRunner = func(issueID string) error {
	cmd := exec.Command("bd", "close", issueID, "--reason", "Implemented")
	return cmd.Run()
}

var orphansCmd = &cobra.Command{
	Use:   "orphans",
	Short: "Identify orphaned issues (referenced in commits but still open)",
	Long: `Identify orphaned issues - issues that are referenced in commit messages but remain open or in_progress in the database.

This helps identify work that has been implemented but not formally closed.

Examples:
  bd orphans              # Show orphaned issues
  bd orphans --json       # Machine-readable output
  bd orphans --details    # Show full commit information
  bd orphans --fix        # Close orphaned issues with confirmation`,
	Run: func(cmd *cobra.Command, args []string) {
		path := "."
		orphans, err := findOrphanedIssues(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fix, _ := cmd.Flags().GetBool("fix")
		details, _ := cmd.Flags().GetBool("details")

		if jsonOutput {
			outputJSON(orphans)
			return
		}

		if len(orphans) == 0 {
			fmt.Printf("%s No orphaned issues found\n", ui.RenderPass("✓"))
			return
		}

		fmt.Printf("\n%s Found %d orphaned issue(s):\n\n", ui.RenderWarn("⚠"), len(orphans))

		// Sort by issue ID for consistent output
		sort.Slice(orphans, func(i, j int) bool {
			return orphans[i].IssueID < orphans[j].IssueID
		})

		for i, orphan := range orphans {
			fmt.Printf("%d. %s: %s\n", i+1, ui.RenderID(orphan.IssueID), orphan.Title)
			fmt.Printf("   Status: %s\n", orphan.Status)
			if details && orphan.LatestCommit != "" {
				fmt.Printf("   Latest commit: %s - %s\n", orphan.LatestCommit, orphan.LatestCommitMessage)
			}
		}

		if fix {
			fmt.Println()
			fmt.Printf("This will close %d orphaned issue(s). Continue? (Y/n): ", len(orphans))
			var response string
			_, _ = fmt.Scanln(&response)
			response = strings.ToLower(strings.TrimSpace(response))
			if response != "" && response != "y" && response != "yes" {
				fmt.Println("Canceled.")
				return
			}

			// Close orphaned issues
			closedCount := 0
			for _, orphan := range orphans {
				err := closeIssue(orphan.IssueID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", orphan.IssueID, err)
				} else {
					fmt.Printf("✓ Closed %s\n", orphan.IssueID)
					closedCount++
				}
			}
			fmt.Printf("\nClosed %d issue(s)\n", closedCount)
		}
	},
}

// orphanIssueOutput is the JSON output format for orphaned issues
type orphanIssueOutput struct {
	IssueID             string `json:"issue_id"`
	Title               string `json:"title"`
	Status              string `json:"status"`
	LatestCommit        string `json:"latest_commit,omitempty"`
	LatestCommitMessage string `json:"latest_commit_message,omitempty"`
}

// findOrphanedIssues wraps the shared doctor package function and converts to output format
func findOrphanedIssues(path string) ([]orphanIssueOutput, error) {
	orphans, err := doctorFindOrphanedIssues(path)
	if err != nil {
		return nil, fmt.Errorf("unable to find orphaned issues: %w", err)
	}

	var output []orphanIssueOutput
	for _, orphan := range orphans {
		output = append(output, orphanIssueOutput{
			IssueID:             orphan.IssueID,
			Title:               orphan.Title,
			Status:              orphan.Status,
			LatestCommit:        orphan.LatestCommit,
			LatestCommitMessage: orphan.LatestCommitMessage,
		})
	}
	return output, nil
}

// closeIssue closes an issue using bd close
func closeIssue(issueID string) error {
	return closeIssueRunner(issueID)
}

func init() {
	orphansCmd.Flags().BoolP("fix", "f", false, "Close orphaned issues with confirmation")
	orphansCmd.Flags().Bool("details", false, "Show full commit information")
	rootCmd.AddCommand(orphansCmd)
}
