package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var molDistillCmd = &cobra.Command{
	Use:   "distill <epic-id>",
	Short: "Extract a reusable proto from an existing epic",
	Long: `Distill a molecule by extracting a reusable proto from an existing epic.

This is the reverse of spawn: instead of proto → molecule, it's molecule → proto.

The distill command:
  1. Loads the existing epic and all its children
  2. Clones the structure as a new proto (adds "template" label)
  3. Replaces concrete values with {{variable}} placeholders (via --var flags)

Use cases:
  - Team develops good workflow organically, wants to reuse it
  - Capture tribal knowledge as executable templates
  - Create starting point for similar future work

Variable syntax (both work - we detect which side is the concrete value):
  --var branch=feature-auth    Spawn-style: variable=value (recommended)
  --var feature-auth=branch    Substitution-style: value=variable

Examples:
  bd mol distill bd-o5xe --as "Release Workflow"
  bd mol distill bd-abc --var feature_name=auth-refactor --var version=1.0.0`,
	Args: cobra.ExactArgs(1),
	Run:  runMolDistill,
}

// DistillResult holds the result of a distill operation
type DistillResult struct {
	ProtoID   string            `json:"proto_id"`
	IDMapping map[string]string `json:"id_mapping"` // old ID -> new ID
	Created   int               `json:"created"`    // number of issues created
	Variables []string          `json:"variables"`  // variables introduced
}

// collectSubgraphText gathers all searchable text from a molecule subgraph
func collectSubgraphText(subgraph *MoleculeSubgraph) string {
	var parts []string
	for _, issue := range subgraph.Issues {
		parts = append(parts, issue.Title)
		parts = append(parts, issue.Description)
		parts = append(parts, issue.Design)
		parts = append(parts, issue.AcceptanceCriteria)
		parts = append(parts, issue.Notes)
	}
	return strings.Join(parts, " ")
}

// parseDistillVar parses a --var flag with smart detection of syntax.
// Accepts both spawn-style (variable=value) and substitution-style (value=variable).
// Returns (findText, varName, error).
func parseDistillVar(varFlag, searchableText string) (string, string, error) {
	parts := strings.SplitN(varFlag, "=", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid format '%s', expected 'variable=value' or 'value=variable'", varFlag)
	}

	left, right := parts[0], parts[1]
	leftFound := strings.Contains(searchableText, left)
	rightFound := strings.Contains(searchableText, right)

	switch {
	case rightFound && !leftFound:
		// spawn-style: --var branch=feature-auth
		// left is variable name, right is the value to find
		return right, left, nil
	case leftFound && !rightFound:
		// substitution-style: --var feature-auth=branch
		// left is value to find, right is variable name
		return left, right, nil
	case leftFound && rightFound:
		// Both found - prefer spawn-style (more natural guess)
		// Agent likely typed: --var varname=concrete_value
		return right, left, nil
	default:
		return "", "", fmt.Errorf("neither '%s' nor '%s' found in epic text", left, right)
	}
}

// runMolDistill implements the distill command
func runMolDistill(cmd *cobra.Command, args []string) {
	CheckReadonly("mol distill")

	ctx := rootCtx

	// mol distill requires direct store access
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: mol distill requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol distill %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	customTitle, _ := cmd.Flags().GetString("as")
	varFlags, _ := cmd.Flags().GetStringSlice("var")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Resolve epic ID
	epicID, err := utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: '%s' not found\n", args[0])
		os.Exit(1)
	}

	// Load the epic subgraph (needed for smart var detection)
	subgraph, err := loadTemplateSubgraph(ctx, store, epicID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading epic: %v\n", err)
		os.Exit(1)
	}

	// Parse variable substitutions with smart detection
	// Accepts both spawn-style (variable=value) and substitution-style (value=variable)
	replacements := make(map[string]string)
	if len(varFlags) > 0 {
		searchableText := collectSubgraphText(subgraph)
		for _, v := range varFlags {
			findText, varName, err := parseDistillVar(v, searchableText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			replacements[findText] = varName
		}
	}

	if dryRun {
		fmt.Printf("\nDry run: would distill %d issues from %s into a proto\n\n", len(subgraph.Issues), epicID)
		fmt.Printf("Source: %s\n", subgraph.Root.Title)
		if customTitle != "" {
			fmt.Printf("Proto title: %s\n", customTitle)
		}
		if len(replacements) > 0 {
			fmt.Printf("\nVariable substitutions:\n")
			for value, varName := range replacements {
				fmt.Printf("  \"%s\" → {{%s}}\n", value, varName)
			}
		}
		fmt.Printf("\nStructure:\n")
		for _, issue := range subgraph.Issues {
			title := issue.Title
			for value, varName := range replacements {
				title = strings.ReplaceAll(title, value, "{{"+varName+"}}")
			}
			prefix := "  "
			if issue.ID == subgraph.Root.ID {
				prefix = "→ "
			}
			fmt.Printf("%s%s\n", prefix, title)
		}
		return
	}

	// Distill the molecule into a proto
	result, err := distillMolecule(ctx, store, subgraph, customTitle, replacements, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error distilling molecule: %v\n", err)
		os.Exit(1)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Distilled proto: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Proto ID: %s\n", result.ProtoID)
	if len(result.Variables) > 0 {
		fmt.Printf("  Variables: %s\n", strings.Join(result.Variables, ", "))
	}
	fmt.Printf("\nTo instantiate this proto:\n")
	fmt.Printf("  bd pour %s", result.ProtoID[:8])
	for _, v := range result.Variables {
		fmt.Printf(" --var %s=<value>", v)
	}
	fmt.Println()
}

// distillMolecule creates a new proto from an existing epic
func distillMolecule(ctx context.Context, s storage.Storage, subgraph *MoleculeSubgraph, customTitle string, replacements map[string]string, actorName string) (*DistillResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Build the reverse mapping for tracking variables introduced
	var variables []string
	for _, varName := range replacements {
		variables = append(variables, varName)
	}

	// Generate new IDs and create mapping
	idMapping := make(map[string]string)

	// Helper to apply replacements
	applyReplacements := func(text string) string {
		result := text
		for value, varName := range replacements {
			result = strings.ReplaceAll(result, value, "{{"+varName+"}}")
		}
		return result
	}

	// Use transaction for atomicity
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// First pass: create all issues with new IDs
		for _, oldIssue := range subgraph.Issues {
			// Determine title
			title := applyReplacements(oldIssue.Title)
			if oldIssue.ID == subgraph.Root.ID && customTitle != "" {
				title = customTitle
			}

			// Add template label to all issues
			labels := append([]string{}, oldIssue.Labels...)
			hasTemplateLabel := false
			for _, l := range labels {
				if l == MoleculeLabel {
					hasTemplateLabel = true
					break
				}
			}
			if !hasTemplateLabel {
				labels = append(labels, MoleculeLabel)
			}

			newIssue := &types.Issue{
				Title:              title,
				Description:        applyReplacements(oldIssue.Description),
				Design:             applyReplacements(oldIssue.Design),
				AcceptanceCriteria: applyReplacements(oldIssue.AcceptanceCriteria),
				Notes:              applyReplacements(oldIssue.Notes),
				Status:             types.StatusOpen, // Protos start fresh
				Priority:           oldIssue.Priority,
				IssueType:          oldIssue.IssueType,
				Labels:             labels,
				EstimatedMinutes:   oldIssue.EstimatedMinutes,
			}

			if err := tx.CreateIssue(ctx, newIssue, actorName); err != nil {
				return fmt.Errorf("failed to create proto issue from %s: %w", oldIssue.ID, err)
			}

			idMapping[oldIssue.ID] = newIssue.ID
		}

		// Second pass: recreate dependencies with new IDs
		for _, dep := range subgraph.Dependencies {
			newFromID, ok1 := idMapping[dep.IssueID]
			newToID, ok2 := idMapping[dep.DependsOnID]
			if !ok1 || !ok2 {
				continue // Skip if either end is outside the subgraph
			}

			newDep := &types.Dependency{
				IssueID:     newFromID,
				DependsOnID: newToID,
				Type:        dep.Type,
			}
			if err := tx.AddDependency(ctx, newDep, actorName); err != nil {
				return fmt.Errorf("failed to create dependency: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &DistillResult{
		ProtoID:   idMapping[subgraph.Root.ID],
		IDMapping: idMapping,
		Created:   len(subgraph.Issues),
		Variables: variables,
	}, nil
}

func init() {
	molDistillCmd.Flags().String("as", "", "Custom title for the new proto")
	molDistillCmd.Flags().StringSlice("var", []string{}, "Replace value with {{variable}} placeholder (value=variable)")
	molDistillCmd.Flags().Bool("dry-run", false, "Preview what would be created")

	molCmd.AddCommand(molDistillCmd)
}
