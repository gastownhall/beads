package main

import (
	"github.com/spf13/cobra"
)

// childrenCmd lists child beads of a parent.
// This is a convenience alias for 'bd list --parent <id>'.
var childrenCmd = &cobra.Command{
	Use:     "children <parent-id>",
	GroupID: "issues",
	Short:   "List child beads of a parent",
	Long: `List all beads that are children of the specified parent bead.

This is a convenience alias for 'bd list --parent <id>'.

Examples:
  bd children hq-abc123        # List children of hq-abc123
  bd children hq-abc123 --json # List children in JSON format
  bd children hq-abc123 --pretty # Show children in tree format`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		parentID := args[0]

		// Set the parent flag on listCmd, run it, then reset
		listCmd.Flags().Set("parent", parentID)
		defer listCmd.Flags().Set("parent", "")
		listCmd.Run(listCmd, []string{})
	},
}

func init() {
	rootCmd.AddCommand(childrenCmd)
}
