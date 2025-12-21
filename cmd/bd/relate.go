package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var relateCmd = &cobra.Command{
	Use:     "relate <id1> <id2>",
	GroupID: "deps",
	Short:   "Create a bidirectional relates_to link between issues",
	Long: `Create a loose 'see also' relationship between two issues.

The relates_to link is bidirectional - both issues will reference each other.
This enables knowledge graph connections without blocking or hierarchy.

Examples:
  bd relate bd-abc bd-xyz    # Link two related issues
  bd relate bd-123 bd-456    # Create see-also connection`,
	Args: cobra.ExactArgs(2),
	RunE: runRelate,
}

var unrelateCmd = &cobra.Command{
	Use:     "unrelate <id1> <id2>",
	GroupID: "deps",
	Short:   "Remove a relates_to link between issues",
	Long: `Remove a relates_to relationship between two issues.

Removes the link in both directions.

Example:
  bd unrelate bd-abc bd-xyz`,
	Args: cobra.ExactArgs(2),
	RunE: runUnrelate,
}

func init() {
	rootCmd.AddCommand(relateCmd)
	rootCmd.AddCommand(unrelateCmd)
}

func runRelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("relate")

	ctx := rootCtx

	// Resolve partial IDs
	var id1, id2 string
	if daemonClient != nil {
		resp1, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		if err := json.Unmarshal(resp1.Data, &id1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[1]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
		if err := json.Unmarshal(resp2.Data, &id2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		id1, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		id2, err = utils.ResolvePartialID(ctx, store, args[1])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
	}

	if id1 == id2 {
		return fmt.Errorf("cannot relate an issue to itself")
	}

	// Get both issues
	var issue1, issue2 *types.Issue
	if daemonClient != nil {
		resp1, err := daemonClient.Show(&rpc.ShowArgs{ID: id1})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.Show(&rpc.ShowArgs{ID: id2})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
		if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		issue1, err = store.GetIssue(ctx, id1)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		issue2, err = store.GetIssue(ctx, id2)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
	}

	if issue1 == nil {
		return fmt.Errorf("issue not found: %s", id1)
	}
	if issue2 == nil {
		return fmt.Errorf("issue not found: %s", id2)
	}

	// Add relates-to dependency: id1 -> id2 (bidirectional, so also id2 -> id1)
	// Per Decision 004, relates-to links are now stored in dependencies table
	if daemonClient != nil {
		// Add id1 -> id2
		_, err := daemonClient.AddDependency(&rpc.DepAddArgs{
			FromID:  id1,
			ToID:    id2,
			DepType: string(types.DepRelatesTo),
		})
		if err != nil {
			return fmt.Errorf("failed to add relates-to %s -> %s: %w", id1, id2, err)
		}
		// Add id2 -> id1 (bidirectional)
		_, err = daemonClient.AddDependency(&rpc.DepAddArgs{
			FromID:  id2,
			ToID:    id1,
			DepType: string(types.DepRelatesTo),
		})
		if err != nil {
			return fmt.Errorf("failed to add relates-to %s -> %s: %w", id2, id1, err)
		}
	} else {
		// Add id1 -> id2
		dep1 := &types.Dependency{
			IssueID:     id1,
			DependsOnID: id2,
			Type:        types.DepRelatesTo,
		}
		if err := store.AddDependency(ctx, dep1, actor); err != nil {
			return fmt.Errorf("failed to add relates-to %s -> %s: %w", id1, id2, err)
		}
		// Add id2 -> id1 (bidirectional)
		dep2 := &types.Dependency{
			IssueID:     id2,
			DependsOnID: id1,
			Type:        types.DepRelatesTo,
		}
		if err := store.AddDependency(ctx, dep2, actor); err != nil {
			return fmt.Errorf("failed to add relates-to %s -> %s: %w", id2, id1, err)
		}
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":     id1,
			"id2":     id2,
			"related": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Linked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}

func runUnrelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("unrelate")

	ctx := rootCtx

	// Resolve partial IDs
	var id1, id2 string
	if daemonClient != nil {
		resp1, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		if err := json.Unmarshal(resp1.Data, &id1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[1]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
		if err := json.Unmarshal(resp2.Data, &id2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		id1, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		id2, err = utils.ResolvePartialID(ctx, store, args[1])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
	}

	// Get both issues
	var issue1, issue2 *types.Issue
	if daemonClient != nil {
		resp1, err := daemonClient.Show(&rpc.ShowArgs{ID: id1})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.Show(&rpc.ShowArgs{ID: id2})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
		if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		issue1, err = store.GetIssue(ctx, id1)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		issue2, err = store.GetIssue(ctx, id2)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
	}

	if issue1 == nil {
		return fmt.Errorf("issue not found: %s", id1)
	}
	if issue2 == nil {
		return fmt.Errorf("issue not found: %s", id2)
	}

	// Remove relates-to dependency in both directions
	// Per Decision 004, relates-to links are now stored in dependencies table
	if daemonClient != nil {
		// Remove id1 -> id2
		_, err := daemonClient.RemoveDependency(&rpc.DepRemoveArgs{
			FromID:  id1,
			ToID:    id2,
			DepType: string(types.DepRelatesTo),
		})
		if err != nil {
			return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id1, id2, err)
		}
		// Remove id2 -> id1 (bidirectional)
		_, err = daemonClient.RemoveDependency(&rpc.DepRemoveArgs{
			FromID:  id2,
			ToID:    id1,
			DepType: string(types.DepRelatesTo),
		})
		if err != nil {
			return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id2, id1, err)
		}
	} else {
		// Remove id1 -> id2
		if err := store.RemoveDependency(ctx, id1, id2, actor); err != nil {
			return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id1, id2, err)
		}
		// Remove id2 -> id1 (bidirectional)
		if err := store.RemoveDependency(ctx, id2, id1, actor); err != nil {
			return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id2, id1, err)
		}
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":       id1,
			"id2":       id2,
			"unrelated": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Unlinked %s ↔ %s\n", ui.RenderPass("✓"), id1, id2)
	return nil
}

// Note: contains, remove, formatRelatesTo functions removed per Decision 004
// relates-to links now use dependencies API instead of Issue.RelatesTo field
