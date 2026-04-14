package main

import "github.com/spf13/cobra"

// handoffCmd is the parent command for cross-project issue transfer.
// It groups send and inbox operations under a single namespace to avoid
// collision with the existing bd mail commands.
var handoffCmd = &cobra.Command{
	Use:     "handoff",
	GroupID: "sync",
	Short:   "Cross-project issue transfer",
	Long: `Transfer issues between projects on a shared Dolt server.

Use 'bd handoff send' to deliver issues to another project's inbox, and
'bd handoff inbox' to manage incoming items.

Examples:
  bd handoff send bd-123 --to upstream
  bd handoff inbox                        # list pending items
  bd handoff inbox import                 # import all pending
  bd handoff inbox reject <id> "reason"   # reject an item`,
}

func init() {
	handoffCmd.AddCommand(handoffSendCmd)
	handoffCmd.AddCommand(handoffInboxCmd)
	rootCmd.AddCommand(handoffCmd)
}
