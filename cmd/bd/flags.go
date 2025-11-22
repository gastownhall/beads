package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// registerCommonIssueFlags registers flags common to create and update commands.
func registerCommonIssueFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("assignee", "a", "", "Assignee")
	cmd.Flags().StringP("description", "d", "", "Issue description")
	cmd.Flags().String("body", "", "Alias for --description (GitHub CLI convention)")
	_ = cmd.Flags().MarkHidden("body") // Hidden alias for agent/CLI ergonomics
	cmd.Flags().String("design", "", "Design notes")
	cmd.Flags().String("acceptance", "", "Acceptance criteria")
	cmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
}

// getDescriptionFlag retrieves the description value, checking both --description and --body.
// Returns the value and whether either flag was explicitly changed.
func getDescriptionFlag(cmd *cobra.Command) (string, bool) {
	descChanged := cmd.Flags().Changed("description")
	bodyChanged := cmd.Flags().Changed("body")

	// Error if both are specified with different values
	if descChanged && bodyChanged {
		desc, _ := cmd.Flags().GetString("description")
		body, _ := cmd.Flags().GetString("body")
		if desc != body {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both --description and --body with different values\n")
			fmt.Fprintf(os.Stderr, "  --description: %q\n", desc)
			fmt.Fprintf(os.Stderr, "  --body:        %q\n", body)
			os.Exit(1)
		}
	}

	// Return whichever was set (or description's value if neither)
	if bodyChanged {
		body, _ := cmd.Flags().GetString("body")
		return body, true
	}

	desc, _ := cmd.Flags().GetString("description")
	return desc, descChanged
}

// registerPriorityFlag registers the priority flag with a specific default value.
func registerPriorityFlag(cmd *cobra.Command, defaultVal string) {
	cmd.Flags().StringP("priority", "p", defaultVal, "Priority (0-4 or P0-P4, 0=highest)")
}
