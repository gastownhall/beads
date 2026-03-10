package main

import (
	"github.com/spf13/cobra"
)

// childrenCmd lists child beads of a parent.
// This is a convenience alias for 'bd list --parent <id> --status all'.
// Unlike plain 'bd list', children includes closed issues by default (GH#2477).
var childrenCmd = &cobra.Command{
	Use:     "children <parent-id>",
	GroupID: "issues",
	Short:   "List child beads of a parent",
	Long: `List all beads that are children of the specified parent bead.

This is a convenience alias for 'bd list --parent <id> --status all'.
Unlike plain 'bd list', children includes closed issues by default,
since the primary use case is inspecting all work under a parent.

Examples:
  bd children hq-abc123        # List all children of hq-abc123
  bd children hq-abc123 --json # List children in JSON format
  bd children hq-abc123 --pretty # Show children in tree format`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		parentID := args[0]

		// Set the parent flag on listCmd, run it, then reset
		_ = listCmd.Flags().Set("parent", parentID)
		defer func() { _ = listCmd.Flags().Set("parent", "") }()

		// Include all statuses by default so closed children are visible (GH#2477).
		_ = listCmd.Flags().Set("status", "all")
		defer func() { _ = listCmd.Flags().Set("status", "") }()

		listCmd.Run(listCmd, []string{})
	},
}

func init() {
	rootCmd.AddCommand(childrenCmd)
}
