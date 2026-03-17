package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
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

	// Resolve partial IDs with cross-rig routing
	dupResult, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
	if dupResult != nil {
		defer dupResult.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	canonResult, err := resolveAndGetIssueWithRouting(ctx, store, duplicateOf)
	if canonResult != nil {
		defer canonResult.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", duplicateOf, err)
	}

	duplicateID := dupResult.ResolvedID
	canonicalID := canonResult.ResolvedID

	if duplicateID == canonicalID {
		return fmt.Errorf("cannot mark an issue as duplicate of itself")
	}

	// Add a "duplicates" dependency edge (duplicate → canonical) in duplicate's store
	dep := &types.Dependency{
		IssueID:     duplicateID,
		DependsOnID: canonicalID,
		Type:        types.DepDuplicates,
	}
	if err := dupResult.Store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add duplicate link: %w", err)
	}

	// Close the duplicate issue
	closedStatus := string(types.StatusClosed)
	updates := map[string]interface{}{
		"status": closedStatus,
	}
	if err := dupResult.Store.UpdateIssue(ctx, duplicateID, updates, actor); err != nil {
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

	// Resolve partial IDs with cross-rig routing
	oldResult, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
	if oldResult != nil {
		defer oldResult.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	newResult, err := resolveAndGetIssueWithRouting(ctx, store, supersededWith)
	if newResult != nil {
		defer newResult.Close()
	}
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", supersededWith, err)
	}

	oldID := oldResult.ResolvedID
	newID := newResult.ResolvedID

	if oldID == newID {
		return fmt.Errorf("cannot mark an issue as superseded by itself")
	}

	// Add a "supersedes" dependency edge (old → new) in old's store
	dep := &types.Dependency{
		IssueID:     oldID,
		DependsOnID: newID,
		Type:        types.DepSupersedes,
	}
	if err := oldResult.Store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add supersede link: %w", err)
	}

	// Close the superseded issue
	closedStatus := string(types.StatusClosed)
	updates := map[string]interface{}{
		"status": closedStatus,
	}
	if err := oldResult.Store.UpdateIssue(ctx, oldID, updates, actor); err != nil {
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
