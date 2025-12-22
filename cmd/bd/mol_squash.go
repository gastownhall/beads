package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molSquashCmd = &cobra.Command{
	Use:   "squash <molecule-id>",
	Short: "Compress molecule execution into a digest",
	Long: `Squash a molecule's wisp children into a single digest issue.

This command collects all wisp child issues of a molecule, generates
a summary digest, and optionally deletes the wisps.

The squash operation:
  1. Loads the molecule and all its children
  2. Filters to only wisps (ephemeral vapor from the Steam Engine)
  3. Generates a digest (summary of work done)
  4. Creates a permanent digest issue
  5. Deletes the wisps (unless --keep-children)

AGENT INTEGRATION:
Use --summary to provide an AI-generated summary. This keeps bd as a pure
tool - the calling agent (Gas Town polecat, Claude Code, etc.) is responsible
for generating intelligent summaries. Without --summary, a basic concatenation
of child issue content is used.

This is part of the wisp workflow: spawn creates wisps,
execution happens, squash compresses the trace into an outcome.

Example:
  bd mol squash bd-abc123              # Squash with auto-generated digest
  bd mol squash bd-abc123 --dry-run    # Preview what would be squashed
  bd mol squash bd-abc123 --keep-children  # Create digest but keep children
  bd mol squash bd-abc123 --summary "Agent-generated summary of work done"`,
	Args: cobra.ExactArgs(1),
	Run:  runMolSquash,
}

// SquashResult holds the result of a squash operation
type SquashResult struct {
	MoleculeID    string   `json:"molecule_id"`
	DigestID      string   `json:"digest_id"`
	SquashedIDs   []string `json:"squashed_ids"`
	SquashedCount int      `json:"squashed_count"`
	DeletedCount  int      `json:"deleted_count"`
	KeptChildren  bool     `json:"kept_children"`
	WispSquash    bool     `json:"wisp_squash,omitempty"` // True if this was a wisp→digest squash
}

func runMolSquash(cmd *cobra.Command, args []string) {
	CheckReadonly("mol squash")

	ctx := rootCtx

	// mol squash requires direct store access
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: mol squash requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol squash %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	keepChildren, _ := cmd.Flags().GetBool("keep-children")
	summary, _ := cmd.Flags().GetString("summary")

	// Try to resolve molecule ID in main store first
	moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])

	// If not found in main store, check wisp storage
	if err != nil {
		// Try wisp storage
		wispStore, wispErr := beads.NewWispStorage(ctx)
		if wispErr != nil {
			// No wisp storage available, report original error
			fmt.Fprintf(os.Stderr, "Error resolving molecule ID %s: %v\n", args[0], err)
			os.Exit(1)
		}
		defer wispStore.Close()

		wispMolID, wispResolveErr := utils.ResolvePartialID(ctx, wispStore, args[0])
		if wispResolveErr != nil {
			// Not found in either store
			fmt.Fprintf(os.Stderr, "Error resolving molecule ID %s: %v\n", args[0], err)
			os.Exit(1)
		}

		// Found in wisp storage - do cross-store squash
		runWispSquash(ctx, cmd, wispStore, store, wispMolID, dryRun, keepChildren, summary)
		return
	}

	// Found in main store - check if it's actually a wisp by looking at root
	issue, err := store.GetIssue(ctx, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
		os.Exit(1)
	}

	// If the root itself is a wisp, check if it should be in wisp storage
	// This handles the case where someone created a wisp in main store by mistake
	if issue.Wisp {
		// Check if there's a corresponding wisp in wisp storage
		wispStore, wispErr := beads.NewWispStorage(ctx)
		if wispErr == nil {
			defer wispStore.Close()
			if wispIssue, _ := wispStore.GetIssue(ctx, moleculeID); wispIssue != nil {
				// Found in wisp storage - do cross-store squash
				runWispSquash(ctx, cmd, wispStore, store, moleculeID, dryRun, keepChildren, summary)
				return
			}
		}
	}

	// Load the molecule subgraph from main store
	subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
		os.Exit(1)
	}

	// Filter to only wisp children (exclude root)
	var wispChildren []*types.Issue
	for _, issue := range subgraph.Issues {
		if issue.ID == subgraph.Root.ID {
			continue // Skip root
		}
		if issue.Wisp {
			wispChildren = append(wispChildren, issue)
		}
	}

	if len(wispChildren) == 0 {
		if jsonOutput {
			outputJSON(SquashResult{
				MoleculeID:    moleculeID,
				SquashedCount: 0,
			})
		} else {
			fmt.Printf("No wisp children found for molecule %s\n", moleculeID)
		}
		return
	}

	if dryRun {
		fmt.Printf("\nDry run: would squash %d wisp children of %s\n\n", len(wispChildren), moleculeID)
		fmt.Printf("Root: %s\n", subgraph.Root.Title)
		fmt.Printf("\nWisp children to squash:\n")
		for _, issue := range wispChildren {
			status := string(issue.Status)
			fmt.Printf("  - [%s] %s (%s)\n", status, issue.Title, issue.ID)
		}
		fmt.Printf("\nDigest preview:\n")
		digest := generateDigest(subgraph.Root, wispChildren)
		// Show first 500 chars of digest
		if len(digest) > 500 {
			fmt.Printf("%s...\n", digest[:500])
		} else {
			fmt.Printf("%s\n", digest)
		}
		if keepChildren {
			fmt.Printf("\n--keep-children: children would NOT be deleted\n")
		} else {
			fmt.Printf("\nChildren would be deleted after digest creation.\n")
		}
		return
	}

	// Perform the squash
	result, err := squashMolecule(ctx, store, subgraph.Root, wispChildren, keepChildren, summary, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error squashing molecule: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Squashed molecule: %d children → 1 digest\n", ui.RenderPass("✓"), result.SquashedCount)
	fmt.Printf("  Digest ID: %s\n", result.DigestID)
	if result.DeletedCount > 0 {
		fmt.Printf("  Deleted: %d wisps\n", result.DeletedCount)
	} else if result.KeptChildren {
		fmt.Printf("  Children preserved (--keep-children)\n")
	}
}

// runWispSquash handles squashing a wisp from wisp storage into permanent storage.
// This is the cross-store squash operation: load from wisp, create digest in permanent, delete wisp.
func runWispSquash(ctx context.Context, cmd *cobra.Command, wispStore, permanentStore storage.Storage, moleculeID string, dryRun, keepChildren bool, summary string) {
	// Load the molecule subgraph from wisp storage
	subgraph, err := loadTemplateSubgraph(ctx, wispStore, moleculeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading wisp molecule: %v\n", err)
		os.Exit(1)
	}

	// Collect all issues in the wisp (including root)
	allIssues := subgraph.Issues

	if dryRun {
		fmt.Printf("\nDry run: would squash wisp %s → permanent digest\n\n", moleculeID)
		fmt.Printf("Root: %s\n", subgraph.Root.Title)
		fmt.Printf("Storage: .beads-wisp/ → .beads/\n")
		fmt.Printf("\nIssues to squash (%d total):\n", len(allIssues))
		for _, issue := range allIssues {
			status := string(issue.Status)
			if issue.ID == subgraph.Root.ID {
				fmt.Printf("  - [%s] %s (%s) [ROOT]\n", status, issue.Title, issue.ID)
			} else {
				fmt.Printf("  - [%s] %s (%s)\n", status, issue.Title, issue.ID)
			}
		}
		fmt.Printf("\nDigest preview:\n")
		// For wisp squash, we generate a digest from all issues
		var children []*types.Issue
		for _, issue := range allIssues {
			if issue.ID != subgraph.Root.ID {
				children = append(children, issue)
			}
		}
		digest := generateDigest(subgraph.Root, children)
		if len(digest) > 500 {
			fmt.Printf("%s...\n", digest[:500])
		} else {
			fmt.Printf("%s\n", digest)
		}
		if keepChildren {
			fmt.Printf("\n--keep-children: wisp would NOT be deleted from .beads-wisp/\n")
		} else {
			fmt.Printf("\nWisp will be deleted from .beads-wisp/ after digest creation.\n")
		}
		return
	}

	// Perform the cross-store squash
	result, err := squashWispToPermanent(ctx, wispStore, permanentStore, subgraph, keepChildren, summary, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error squashing wisp: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush for permanent store
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Squashed wisp → permanent digest\n", ui.RenderPass("✓"))
	fmt.Printf("  Wisp: %s (.beads-wisp/)\n", moleculeID)
	fmt.Printf("  Digest: %s (.beads/)\n", result.DigestID)
	fmt.Printf("  Squashed: %d issues\n", result.SquashedCount)
	if result.DeletedCount > 0 {
		fmt.Printf("  Deleted: %d issues from wisp storage\n", result.DeletedCount)
	} else if result.KeptChildren {
		fmt.Printf("  Wisp preserved (--keep-children)\n")
	}
}

// generateDigest creates a summary from the molecule execution
// Tier 2: Simple concatenation of titles and descriptions
// Tier 3 (future): AI-powered summarization using Haiku
func generateDigest(root *types.Issue, children []*types.Issue) string {
	var sb strings.Builder

	sb.WriteString("## Molecule Execution Summary\n\n")
	sb.WriteString(fmt.Sprintf("**Molecule**: %s\n", root.Title))
	sb.WriteString(fmt.Sprintf("**Steps**: %d\n\n", len(children)))

	// Count completed vs other statuses
	completed := 0
	inProgress := 0
	for _, c := range children {
		switch c.Status {
		case types.StatusClosed:
			completed++
		case types.StatusInProgress:
			inProgress++
		}
	}
	sb.WriteString(fmt.Sprintf("**Completed**: %d/%d\n", completed, len(children)))
	if inProgress > 0 {
		sb.WriteString(fmt.Sprintf("**In Progress**: %d\n", inProgress))
	}
	sb.WriteString("\n---\n\n")

	// List each step with its outcome
	sb.WriteString("### Steps\n\n")
	for i, child := range children {
		status := string(child.Status)
		sb.WriteString(fmt.Sprintf("%d. **[%s]** %s\n", i+1, status, child.Title))
		if child.Description != "" {
			// Include first 200 chars of description
			desc := child.Description
			if len(desc) > 200 {
				desc = desc[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("   %s\n", desc))
		}
		if child.CloseReason != "" {
			sb.WriteString(fmt.Sprintf("   *Outcome: %s*\n", child.CloseReason))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// squashMolecule performs the squash operation
// If summary is provided (non-empty), it's used as the digest content.
// Otherwise, generateDigest() creates a basic concatenation.
// This enables agents to provide AI-generated summaries while keeping bd as a pure tool.
func squashMolecule(ctx context.Context, s storage.Storage, root *types.Issue, children []*types.Issue, keepChildren bool, summary string, actorName string) (*SquashResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Collect child IDs
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}

	// Use agent-provided summary if available, otherwise generate basic digest
	var digestContent string
	if summary != "" {
		digestContent = summary
	} else {
		digestContent = generateDigest(root, children)
	}

	// Create digest issue (permanent, not a wisp)
	now := time.Now()
	digestIssue := &types.Issue{
		Title:       fmt.Sprintf("Digest: %s", root.Title),
		Description: digestContent,
		Status:      types.StatusClosed,
		CloseReason: fmt.Sprintf("Squashed from %d wisps", len(children)),
		Priority:    root.Priority,
		IssueType:   types.TypeTask,
		Wisp:        false, // Digest is permanent, not a wisp
		ClosedAt:    &now,
	}

	result := &SquashResult{
		MoleculeID:    root.ID,
		SquashedIDs:   childIDs,
		SquashedCount: len(children),
		KeptChildren:  keepChildren,
	}

	// Use transaction for atomicity
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// Create digest issue
		if err := tx.CreateIssue(ctx, digestIssue, actorName); err != nil {
			return fmt.Errorf("failed to create digest issue: %w", err)
		}
		result.DigestID = digestIssue.ID

		// Link digest to root as parent-child
		dep := &types.Dependency{
			IssueID:     digestIssue.ID,
			DependsOnID: root.ID,
			Type:        types.DepParentChild,
		}
		if err := tx.AddDependency(ctx, dep, actorName); err != nil {
			return fmt.Errorf("failed to link digest to root: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Delete wisp children (outside transaction for better error handling)
	if !keepChildren {
		deleted, err := deleteWispChildren(ctx, s, childIDs)
		if err != nil {
			// Log but don't fail - digest was created successfully
			fmt.Fprintf(os.Stderr, "Warning: failed to delete some children: %v\n", err)
		}
		result.DeletedCount = deleted
	}

	return result, nil
}

// deleteWispChildren removes the wisp issues from the database
func deleteWispChildren(ctx context.Context, s storage.Storage, ids []string) (int, error) {
	// Type assert to SQLite storage for delete access
	d, ok := s.(*sqlite.SQLiteStorage)
	if !ok {
		return 0, fmt.Errorf("delete not supported by this storage backend")
	}

	deleted := 0
	var lastErr error
	for _, id := range ids {
		if err := d.DeleteIssue(ctx, id); err != nil {
			lastErr = err
			continue
		}
		deleted++
	}

	return deleted, lastErr
}

// squashWispToPermanent performs a cross-store squash: wisp → permanent digest.
// This is the key operation for wisp lifecycle management:
// 1. Creates a digest issue in permanent storage summarizing the wisp's work
// 2. Deletes the entire wisp molecule from wisp storage
//
// The digest captures the outcome of the ephemeral work without preserving
// the full execution trace (which would accumulate unbounded over time).
func squashWispToPermanent(ctx context.Context, wispStore, permanentStore storage.Storage, subgraph *MoleculeSubgraph, keepChildren bool, summary string, actorName string) (*SquashResult, error) {
	if wispStore == nil || permanentStore == nil {
		return nil, fmt.Errorf("both wisp and permanent stores are required")
	}

	root := subgraph.Root

	// Collect all issue IDs (including root)
	var allIDs []string
	var children []*types.Issue
	for _, issue := range subgraph.Issues {
		allIDs = append(allIDs, issue.ID)
		if issue.ID != root.ID {
			children = append(children, issue)
		}
	}

	// Use agent-provided summary if available, otherwise generate basic digest
	var digestContent string
	if summary != "" {
		digestContent = summary
	} else {
		digestContent = generateDigest(root, children)
	}

	// Create digest issue in permanent storage (not a wisp)
	now := time.Now()
	digestIssue := &types.Issue{
		Title:       fmt.Sprintf("Digest: %s @ %s", root.Title, now.Format("2006-01-02 15:04")),
		Description: digestContent,
		Status:      types.StatusClosed,
		CloseReason: fmt.Sprintf("Squashed from wisp %s (%d issues)", root.ID, len(subgraph.Issues)),
		Priority:    root.Priority,
		IssueType:   types.TypeTask,
		Wisp:        false, // Digest is permanent
		ClosedAt:    &now,
	}

	result := &SquashResult{
		MoleculeID:    root.ID,
		SquashedIDs:   allIDs,
		SquashedCount: len(subgraph.Issues),
		KeptChildren:  keepChildren,
		WispSquash:    true,
	}

	// Create digest in permanent storage
	err := permanentStore.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, digestIssue, actorName); err != nil {
			return fmt.Errorf("failed to create digest issue: %w", err)
		}
		result.DigestID = digestIssue.ID
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Delete wisp issues from wisp storage (unless --keep-children)
	if !keepChildren {
		deleted, err := deleteWispChildren(ctx, wispStore, allIDs)
		if err != nil {
			// Log but don't fail - digest was created successfully
			fmt.Fprintf(os.Stderr, "Warning: failed to delete some wisp issues: %v\n", err)
		}
		result.DeletedCount = deleted
	}

	return result, nil
}

func init() {
	molSquashCmd.Flags().Bool("dry-run", false, "Preview what would be squashed")
	molSquashCmd.Flags().Bool("keep-children", false, "Don't delete wisp children after squash")
	molSquashCmd.Flags().String("summary", "", "Agent-provided summary (bypasses auto-generation)")

	molCmd.AddCommand(molSquashCmd)
}
