package main

import "github.com/spf13/cobra"

// registerCommonIssueFlags registers flags common to create and update commands.
func registerCommonIssueFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("assignee", "a", "", "Assignee")
	cmd.Flags().StringP("description", "d", "", "Issue description")
	cmd.Flags().String("design", "", "Design notes")
	cmd.Flags().String("acceptance", "", "Acceptance criteria")
	cmd.Flags().String("external-ref", "", "External reference (e.g., 'gh-9', 'jira-ABC')")
}

// registerPriorityFlag registers the priority flag with a specific default value.
func registerPriorityFlag(cmd *cobra.Command, defaultVal string) {
	cmd.Flags().StringP("priority", "p", defaultVal, "Priority (0-4 or P0-P4, 0=highest)")
}
