package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var duplicateCmd = &cobra.Command{
	Use:     "duplicate <id> --of <canonical>",
	GroupID: "deps",
	Short:   "Mark an issue as a duplicate of another",
	Long: `Mark an issue as a duplicate of a canonical issue.

The duplicate issue is automatically closed with a reference to the canonical.
This is essential for large issue databases with many similar reports.

Examples:
  bd duplicate bd-abc --of bd-xyz    # Mark bd-abc as duplicate of bd-xyz`,
	Args: cobra.ExactArgs(1),
	RunE: runDuplicate,
}

var supersedeCmd = &cobra.Command{
	Use:     "supersede <id> --with <new>",
	GroupID: "deps",
	Short:   "Mark an issue as superseded by a newer one",
	Long: `Mark an issue as superseded by a newer version.

The superseded issue is automatically closed with a reference to the replacement.
Useful for design docs, specs, and evolving artifacts.

Examples:
  bd supersede bd-old --with bd-new    # Mark bd-old as superseded by bd-new`,
	Args: cobra.ExactArgs(1),
	RunE: runSupersede,
}

var (
	duplicateOf    string
	supersededWith string
)

func init() {
	duplicateCmd.Flags().StringVar(&duplicateOf, "of", "", "Canonical issue ID (required)")
	_ = duplicateCmd.MarkFlagRequired("of") // Only fails if flag missing (caught in tests)
	duplicateCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(duplicateCmd)

	supersedeCmd.Flags().StringVar(&supersededWith, "with", "", "Replacement issue ID (required)")
	_ = supersedeCmd.MarkFlagRequired("with") // Only fails if flag missing (caught in tests)
	supersedeCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(supersedeCmd)
}

func runDuplicate(cmd *cobra.Command, args []string) error {
	CheckReadonly("duplicate")

	ctx := rootCtx

	// Resolve partial IDs
	var duplicateID, canonicalID string
	var err error
	duplicateID, err = utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	canonicalID, err = utils.ResolvePartialID(ctx, store, duplicateOf)
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", duplicateOf, err)
	}

	if duplicateID == canonicalID {
		return fmt.Errorf("cannot mark an issue as duplicate of itself")
	}

	// Verify canonical issue exists
	var canonical *types.Issue
	canonical, err = store.GetIssue(ctx, canonicalID)
	if err != nil || canonical == nil {
		return fmt.Errorf("canonical issue not found: %s", canonicalID)
	}

	// Add a "duplicates" dependency edge (duplicate → canonical)
	dep := &types.Dependency{
		IssueID:     duplicateID,
		DependsOnID: canonicalID,
		Type:        types.DepDuplicates,
	}
	if err := store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add duplicate link: %w", err)
	}

	// Close the duplicate issue
	closedStatus := string(types.StatusClosed)
	updates := map[string]interface{}{
		"status": closedStatus,
	}
	if err := store.UpdateIssue(ctx, duplicateID, updates, actor); err != nil {
		return fmt.Errorf("failed to close duplicate: %w", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"duplicate": duplicateID,
			"canonical": canonicalID,
			"status":    "closed",
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Marked %s as duplicate of %s (closed)\n", ui.RenderPass("✓"), duplicateID, canonicalID)
	return nil
}

func runSupersede(cmd *cobra.Command, args []string) error {
	CheckReadonly("supersede")

	ctx := rootCtx

	// Resolve partial IDs
	var oldID, newID string
	var err error
	oldID, err = utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	newID, err = utils.ResolvePartialID(ctx, store, supersededWith)
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", supersededWith, err)
	}

	if oldID == newID {
		return fmt.Errorf("cannot mark an issue as superseded by itself")
	}

	// Verify new issue exists
	var newIssue *types.Issue
	newIssue, err = store.GetIssue(ctx, newID)
	if err != nil || newIssue == nil {
		return fmt.Errorf("replacement issue not found: %s", newID)
	}

	// Add a "supersedes" dependency edge (old → new)
	dep := &types.Dependency{
		IssueID:     oldID,
		DependsOnID: newID,
		Type:        types.DepSupersedes,
	}
	if err := store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add supersede link: %w", err)
	}

	// Close the superseded issue
	closedStatus := string(types.StatusClosed)
	updates := map[string]interface{}{
		"status": closedStatus,
	}
	if err := store.UpdateIssue(ctx, oldID, updates, actor); err != nil {
		return fmt.Errorf("failed to close superseded issue: %w", err)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"superseded":  oldID,
			"replacement": newID,
			"status":      "closed",
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s Marked %s as superseded by %s (closed)\n", ui.RenderPass("✓"), oldID, newID)
	return nil
}
