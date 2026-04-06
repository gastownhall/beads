package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/tracker"
)

// registerSelectiveSyncFlags adds --issues and --parent flags to a tracker sync command.
// These two flags are mutually exclusive: use --issues to sync specific beads by ID,
// or --parent to sync an entire subtree. Combining them is an error.
func registerSelectiveSyncFlags(cmd *cobra.Command) {
	cmd.Flags().String("issues", "", "Comma-separated bead IDs to sync selectively (e.g., bd-abc,bd-def). Mutually exclusive with --parent.")
	if cmd.Flags().Lookup("parent") == nil {
		cmd.Flags().String("parent", "", "Limit push to this bead and its descendants (push only). Mutually exclusive with --issues.")
	}
}

// applySelectiveSyncFlags parses --issues and --parent from cmd and applies them to opts.
//
// Rules:
//   - --parent requires push mode (incompatible with --pull-only).
//   - --issues and --parent are mutually exclusive: --issues targets specific beads by ID
//     while --parent scopes by subtree. Combining them would produce confusing AND semantics
//     (only issues that are BOTH in the ID list AND in the subtree would be synced).
//     To sync a subtree plus additional individual issues, use --issues with all desired IDs.
func applySelectiveSyncFlags(cmd *cobra.Command, opts *tracker.SyncOptions, push bool) error {
	issuesFlag, _ := cmd.Flags().GetString("issues")
	parentID, _ := cmd.Flags().GetString("parent")

	if issuesFlag != "" && parentID != "" {
		return fmt.Errorf("--issues and --parent are mutually exclusive: use --issues to target specific beads by ID, or --parent to sync a subtree")
	}

	if issuesFlag != "" {
		opts.IssueIDs = splitCSV(issuesFlag)
	}
	if parentID != "" {
		if !push {
			return fmt.Errorf("--parent requires push (cannot use with --pull-only)")
		}
		opts.ParentID = parentID
	}
	return nil
}
