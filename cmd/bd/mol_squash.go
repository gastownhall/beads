package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molSquashCmd = &cobra.Command{
	Use:   "squash <molecule-id>",
	Short: "Compress molecule execution into a digest",
	Long: `Squash a molecule's ephemeral children into a single digest issue.

This command collects all ephemeral child issues of a molecule (Ephemeral=true),
generates a summary digest, and promotes the wisps to persistent by
clearing their Wisp flag (or optionally deletes them).

The squash operation:
  1. Loads the molecule and all its children
  2. Filters to only wisps (ephemeral issues with Ephemeral=true)
  3. Generates a digest (summary of work done)
  4. Creates a permanent digest issue (Ephemeral=false)
  5. Clears Wisp flag on children (promotes to persistent)
     OR keeps them with --keep-children (default: delete)

Squash never deletes a closed child that a live bead still waits on. A
conditional-blocks target closed with a reason that is not a failure holds its
fallback back (for example a step that 'bd mol bond <arm> <step> --type
conditional' bonded an arm onto), and deleting it would put the fallback in
'bd ready' as if the step had failed. Squash keeps such a child, still
summarized in the digest, and lists it as kept; close or delete the unused
fallback, and 'bd mol wisp gc --closed' collects it.

AGENT INTEGRATION:
Use --summary to provide an AI-generated summary. This keeps bd as a pure
tool - the calling agent (orchestrator worker, Claude Code, etc.) is responsible
for generating intelligent summaries. Without --summary, a basic concatenation
of child issue content is used.

This is part of the wisp workflow: spawn creates wisps,
execution happens, squash compresses the trace into an outcome (digest).

Example:
  bd mol squash bd-abc123                    # Squash and promote children
  bd mol squash bd-abc123 --dry-run          # Preview what would be squashed
  bd mol squash bd-abc123 --keep-children    # Keep wisps after digest
  bd mol squash bd-abc123 --summary "Agent-generated summary of work done"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runMolSquash,
}

// SquashResult holds the result of a squash operation
type SquashResult struct {
	MoleculeID    string   `json:"molecule_id"`
	DigestID      string   `json:"digest_id"`
	SquashedIDs   []string `json:"squashed_ids"`
	SquashedCount int      `json:"squashed_count"`
	DeletedCount  int      `json:"deleted_count"`
	KeptChildren  bool     `json:"kept_children"`
	// KeptIDs are the closed children the squash did not delete because a
	// live conditional-blocks fallback still waits on them (squashKeptSteps).
	// The digest still summarizes them and SquashedCount counts them.
	KeptIDs    []string `json:"kept_ids,omitempty"`
	WispSquash bool     `json:"wisp_squash,omitempty"` // True if this was a wisp→digest squash
}

type molSquashInput struct {
	moleculeArg  string
	dryRun       bool
	keepChildren bool
	summary      string
}

func gatherMolSquashInput(cmd *cobra.Command, args []string) molSquashInput {
	in := molSquashInput{moleculeArg: args[0]}
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")
	in.keepChildren, _ = cmd.Flags().GetBool("keep-children")
	in.summary, _ = cmd.Flags().GetString("summary")
	return in
}

func wispChildrenOf(subgraph *TemplateSubgraph) []*types.Issue {
	var wispChildren []*types.Issue
	for _, issue := range subgraph.Issues {
		if issue.ID == subgraph.Root.ID {
			continue
		}
		if issue.Ephemeral {
			wispChildren = append(wispChildren, issue)
		}
	}
	return wispChildren
}

// squashKeptSteps returns the ids of the closed children a squash keeps
// instead of deleting. Each is the target of a conditional-blocks edge from a
// bead that is not closed and that this squash does not delete, and its close
// reason is not a failure, so it still holds that fallback back
// (types.BlockingEdgeHolds). Deleting it drops the edge, which puts the
// fallback in ready work as if the step had failed (GH#5577). The usual shape
// is an arm bonded onto a step (bd mol bond <arm> <step> --type conditional):
// the arm is outside the molecule's subgraph, so squash deletes the step and
// never the arm.
//
// It reads each closed child's incoming edges rather than the blocked listing
// bd mol wisp gc consults, so a fallback whose stored is_blocked flag an older
// bd left at 0 still keeps its step. A molecule has few steps, so one read per
// closed child stays cheap. With keepChildren the squash deletes nothing, and
// nothing is read.
func squashKeptSteps(ctx context.Context, r molReader, children []*types.Issue, keepChildren bool) (map[string]bool, error) {
	if keepChildren {
		return nil, nil
	}
	squashed := make(map[string]bool, len(children))
	for _, c := range children {
		squashed[c.ID] = true
	}
	kept := make(map[string]bool)
	for _, c := range children {
		if c.Status != types.StatusClosed || !types.BlockingEdgeHolds(types.DepConditionalBlocks, c.Status, c.CloseReason) {
			continue
		}
		dependents, err := r.GetDependentsWithMetadata(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("reading what waits on %s: %w", c.ID, err)
		}
		for _, d := range dependents {
			// A source among the children goes with this squash, an open
			// one included, and takes its edge with it.
			if d != nil && d.DependencyType == types.DepConditionalBlocks &&
				d.Issue.Status != types.StatusClosed && !squashed[d.Issue.ID] {
				kept[c.ID] = true
				break
			}
		}
	}
	return kept, nil
}

// keptStepIDs lists the children squashKeptSteps kept, in the children's order.
func keptStepIDs(children []*types.Issue, kept map[string]bool) []string {
	var ids []string
	for _, c := range children {
		if kept[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// keptStepsLine tells a text reader which closed children the squash keeps.
func keptStepsLine(ids []string) string {
	return fmt.Sprintf("Kept: %d closed wisp(s) a conditional-blocks fallback still waits on: %s", len(ids), strings.Join(ids, ", "))
}

func renderSquashDryRun(moleculeID string, subgraph *TemplateSubgraph, wispChildren []*types.Issue, kept map[string]bool, keepChildren bool) {
	fmt.Printf("\nDry run: would squash %d ephemeral children of %s\n\n", len(wispChildren), moleculeID)
	fmt.Printf("Root: %s\n", subgraph.Root.Title)
	fmt.Printf("\nWisp children to squash:\n")
	for _, issue := range wispChildren {
		status := string(issue.Status)
		fmt.Printf("  - [%s] %s (%s)\n", status, issue.Title, issue.ID)
	}
	fmt.Printf("\nDigest preview:\n")
	digest := generateDigest(subgraph.Root, wispChildren)
	if len(digest) > 500 {
		fmt.Printf("%s...\n", digest[:500])
	} else {
		fmt.Printf("%s\n", digest)
	}
	if keepChildren {
		fmt.Printf("\n--keep-children: children would NOT be deleted\n")
	} else {
		fmt.Printf("\nChildren would be deleted after digest creation.\n")
		if ids := keptStepIDs(wispChildren, kept); len(ids) > 0 {
			fmt.Println(keptStepsLine(ids))
		}
	}
}

func renderSquashResult(result *SquashResult) error {
	if jsonOutput {
		return outputJSON(result)
	}
	fmt.Printf("%s Squashed molecule: %d children → 1 digest\n", ui.RenderPass("✓"), result.SquashedCount)
	fmt.Printf("  Digest ID: %s\n", result.DigestID)
	if result.DeletedCount > 0 {
		fmt.Printf("  Deleted: %d wisps\n", result.DeletedCount)
	} else if result.KeptChildren {
		fmt.Printf("  Children preserved (--keep-children)\n")
	}
	if len(result.KeptIDs) > 0 {
		fmt.Printf("  %s\n", keptStepsLine(result.KeptIDs))
	}
	if result.WispSquash {
		fmt.Printf("  Root auto-closed: %s\n", result.MoleculeID)
	}
	return nil
}

func runMolSquash(cmd *cobra.Command, args []string) error {
	CheckReadonly("mol squash")

	evt := metrics.NewCommandEvent("mol-squash")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	in := gatherMolSquashInput(cmd, args)

	if usesProxiedServer() {
		return runMolSquashProxiedServer(rootCtx, in)
	}

	ctx := rootCtx

	if store == nil {
		return HandleErrorWithHint("no database connection", diagHint())
	}

	moleculeID, err := utils.ResolvePartialID(ctx, store, in.moleculeArg)
	if err != nil {
		return HandleErrorRespectJSON("resolving molecule ID %s: %v", in.moleculeArg, err)
	}

	subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
	if err != nil {
		return HandleErrorRespectJSON("loading molecule: %v", err)
	}

	wispChildren := wispChildrenOf(subgraph)
	if len(wispChildren) == 0 {
		if jsonOutput {
			return outputJSON(SquashResult{
				MoleculeID:    moleculeID,
				SquashedCount: 0,
			})
		}
		fmt.Printf("No ephemeral children found for molecule %s\n", moleculeID)
		return nil
	}

	if in.dryRun {
		kept, err := squashKeptSteps(ctx, store, wispChildren, in.keepChildren)
		if err != nil {
			return HandleErrorRespectJSON("squashing molecule: %v", err)
		}
		renderSquashDryRun(moleculeID, subgraph, wispChildren, kept, in.keepChildren)
		return nil
	}

	result, err := squashMolecule(ctx, store, subgraph.Root, wispChildren, in.keepChildren, in.summary, currentActor())
	if err != nil {
		return HandleErrorRespectJSON("squashing molecule: %v", err)
	}
	return renderSquashResult(result)
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
// It keeps every closed child a live conditional-blocks fallback still waits
// on (squashKeptSteps) and reports it in SquashResult.KeptIDs.
func squashMolecule(ctx context.Context, s storage.DoltStorage, root *types.Issue, children []*types.Issue, keepChildren bool, summary string, actorName string) (*SquashResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Read through the store before the transaction opens: during it a store
	// read needs a second connection, and the embedded store's transaction
	// holds its only one (see storeMolWriter.GetConfig).
	kept, err := squashKeptSteps(ctx, s, children, keepChildren)
	if err != nil {
		return nil, err
	}

	var result *SquashResult
	err = transact(ctx, s, fmt.Sprintf("bd: squash molecule %s", root.ID), func(tx storage.Transaction) error {
		r, err := squashMoleculeInto(ctx, storeMolWriter{DoltStorage: s, tx: tx}, root, children, kept, keepChildren, summary, actorName)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// squashMoleculeInto writes the squash through w. kept names the closed
// children it must not delete (squashKeptSteps): the caller reads it on its
// own route, the direct one before its transaction opens.
func squashMoleculeInto(ctx context.Context, w molWriter, root *types.Issue, children []*types.Issue, kept map[string]bool, keepChildren bool, summary string, actorName string) (*SquashResult, error) {
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
		Ephemeral:   false, // Digest is permanent, not a wisp
		ClosedAt:    &now,
	}

	result := &SquashResult{
		MoleculeID:    root.ID,
		SquashedIDs:   childIDs,
		SquashedCount: len(children),
		KeptChildren:  keepChildren,
	}

	// Create digest issue
	if err := w.CreateIssue(ctx, digestIssue, actorName); err != nil {
		return nil, fmt.Errorf("failed to create digest issue: %w", err)
	}
	result.DigestID = digestIssue.ID

	// Link digest to root as parent-child
	dep := &types.Dependency{
		IssueID:     digestIssue.ID,
		DependsOnID: root.ID,
		Type:        types.DepParentChild,
	}
	if err := w.AddDependency(ctx, dep, actorName); err != nil {
		return nil, fmt.Errorf("failed to link digest to root: %w", err)
	}

	// Delete ephemeral children within the same transaction, except the closed
	// ones a live conditional-blocks fallback still waits on: deleting one
	// drops the edge and puts the fallback in ready work (GH#5577).
	if !keepChildren {
		for _, id := range childIDs {
			if kept[id] {
				result.KeptIDs = append(result.KeptIDs, id)
				continue
			}
			if err := w.DeleteIssue(ctx, id, actorName); err != nil {
				return nil, fmt.Errorf("failed to delete child %s: %w", id, err)
			}
			result.DeletedCount++
		}
	}

	// Auto-close the root if it's a wisp — squash completes the molecule lifecycle
	if root.Ephemeral {
		reason := fmt.Sprintf("Squashed: %d steps → digest %s", len(children), result.DigestID)
		if err := w.CloseIssue(ctx, root.ID, reason, actorName); err != nil {
			return nil, fmt.Errorf("failed to close wisp root %s: %w", root.ID, err)
		}
		// Clear ephemeral so the closed root stops being re-emitted by every wisp-table export cycle.
		if err := w.UpdateIssue(ctx, root.ID, map[string]interface{}{"wisp": false}, actorName); err != nil {
			return nil, fmt.Errorf("failed to clear ephemeral flag on root %s: %w", root.ID, err)
		}
		result.WispSquash = true
	}

	return result, nil
}

func init() {
	molSquashCmd.Flags().Bool("dry-run", false, "Preview what would be squashed")
	molSquashCmd.Flags().Bool("keep-children", false, "Don't delete ephemeral children after squash")
	molSquashCmd.Flags().String("summary", "", "Agent-provided summary (bypasses auto-generation)")

	molCmd.AddCommand(molSquashCmd)
}
