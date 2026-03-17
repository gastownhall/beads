package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var relateCmd = &cobra.Command{
	Use:   "relate <id1> <id2>",
	Short: "Create a bidirectional relates_to link between issues",
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
	Use:   "unrelate <id1> <id2>",
	Short: "Remove a relates_to link between issues",
	Long: `Remove a relates_to relationship between two issues.

Removes the link in both directions.

Example:
  bd unrelate bd-abc bd-xyz`,
	Args: cobra.ExactArgs(2),
	RunE: runUnrelate,
}

func init() {
	// Issue ID completions
	relateCmd.ValidArgsFunction = issueIDCompletion
	unrelateCmd.ValidArgsFunction = issueIDCompletion

	// Add as subcommands of dep
	depCmd.AddCommand(relateCmd)
	depCmd.AddCommand(unrelateCmd)
}

func runRelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("relate")

	ctx := rootCtx

	// Resolve partial IDs with cross-rig routing
	result1, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
	if result1 != nil {
		defer result1.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	result2, err := resolveAndGetIssueWithRouting(ctx, store, args[1])
	if result2 != nil {
		defer result2.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[1], err)
	}

	id1 := result1.ResolvedID
	id2 := result2.ResolvedID

	if id1 == id2 {
		return fmt.Errorf("cannot relate an issue to itself")
	}

	// Add relates-to dependency: id1 -> id2 (bidirectional, so also id2 -> id1)
	// Per Decision 004, relates-to links are now stored in dependencies table
	// Add id1 -> id2 (in id1's store)
	dep1 := &types.Dependency{
		IssueID:     id1,
		DependsOnID: id2,
		Type:        types.DepRelatesTo,
	}
	if err := result1.Store.AddDependency(ctx, dep1, actor); err != nil {
		return fmt.Errorf("failed to add relates-to %s -> %s: %w", id1, id2, err)
	}
	// Add id2 -> id1 (bidirectional, in id2's store)
	dep2 := &types.Dependency{
		IssueID:     id2,
		DependsOnID: id1,
		Type:        types.DepRelatesTo,
	}
	if err := result2.Store.AddDependency(ctx, dep2, actor); err != nil {
		return fmt.Errorf("failed to add relates-to %s -> %s: %w", id2, id1, err)
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

	// Resolve partial IDs with cross-rig routing
	result1, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
	if result1 != nil {
		defer result1.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	result2, err := resolveAndGetIssueWithRouting(ctx, store, args[1])
	if result2 != nil {
		defer result2.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[1], err)
	}

	id1 := result1.ResolvedID
	id2 := result2.ResolvedID

	// Remove relates-to dependency in both directions
	// Per Decision 004, relates-to links are now stored in dependencies table
	// Remove id1 -> id2 (from id1's store)
	if err := result1.Store.RemoveDependency(ctx, id1, id2, actor); err != nil {
		return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id1, id2, err)
	}
	// Remove id2 -> id1 (bidirectional, from id2's store)
	if err := result2.Store.RemoveDependency(ctx, id2, id1, actor); err != nil {
		return fmt.Errorf("failed to remove relates-to %s -> %s: %w", id2, id1, err)
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
