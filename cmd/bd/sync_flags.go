package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
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

// buildSyncDescendantSet resolves opts.ParentID into the set of issue IDs
// --parent selects: the parent itself plus every bead reachable from it
// through parent-child dependency edges. A nil result means no subtree scope
// was requested; callers filtering by it must treat a non-nil set as a
// membership requirement.
//
// Shared by the GitLab and GitHub relationship passes so a subtree-scoped push
// and its relationship pass agree on what "in scope" means. The engine has its
// own copy for the content push (Engine.buildDescendantSet,
// internal/tracker/engine.go); these two must stay in agreement. The ADO
// relationship pass is not converted: pushADOLinks (ado.go) takes no sync
// options, so --parent and --issues do not narrow the ADO relations it writes.
func buildSyncDescendantSet(ctx context.Context, st storage.Storage, parentID string) (map[string]bool, error) {
	result := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		dependents, err := st.GetDependentsWithMetadata(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("getting dependents of %s: %w", current, err)
		}
		for _, dep := range dependents {
			if dep.DependencyType == types.DepParentChild && !result[dep.Issue.ID] {
				result[dep.Issue.ID] = true
				queue = append(queue, dep.Issue.ID)
			}
		}
	}
	return result, nil
}
