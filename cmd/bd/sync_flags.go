package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/tracker"
)

// registerSelectiveSyncFlags adds --issues and --parent flags to a tracker sync command.
func registerSelectiveSyncFlags(cmd *cobra.Command) {
	cmd.Flags().String("issues", "", "Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def)")
	if cmd.Flags().Lookup("parent") == nil {
		cmd.Flags().String("parent", "", "Limit push to this bead and its descendants")
	}
}

// applySelectiveSyncFlags parses --issues and --parent from cmd and applies them to opts.
// Returns an error if --parent is used without push.
func applySelectiveSyncFlags(cmd *cobra.Command, opts *tracker.SyncOptions, push bool) error {
	if issuesFlag, _ := cmd.Flags().GetString("issues"); issuesFlag != "" {
		opts.IssueIDs = splitCSV(issuesFlag)
	}
	if parentID, _ := cmd.Flags().GetString("parent"); parentID != "" {
		if !push {
			return fmt.Errorf("--parent requires push (cannot use with --pull-only)")
		}
		opts.ParentID = parentID
	}
	return nil
}
