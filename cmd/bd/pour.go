package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/types"
	"github.com/gastownhall/beads/internal/ui"
	"github.com/gastownhall/beads/internal/utils"
)

// pourCmd is a top-level command for instantiating protos as persistent mols.
//
// In the molecular chemistry metaphor:
//   - Proto (solid) -> pour -> Mol (liquid)
//   - Pour creates persistent, auditable work in .beads/
var pourCmd = &cobra.Command{
	Use:   "pour <proto-id>",
	Short: "Instantiate a proto as a persistent mol (solid -> liquid)",
	Long: `Pour a proto into a persistent mol - like pouring molten metal into a mold.

This is the chemistry-inspired command for creating PERSISTENT work from templates.
The resulting mol lives in .beads/ (permanent storage) and is synced with git.

Phase transition: Proto (solid) -> pour -> Mol (liquid)

WHEN TO USE POUR vs WISP:
  pour (liquid): Persistent work that needs audit trail
    - Feature implementations spanning multiple sessions
    - Work you may need to reference later
    - Anything worth preserving in git history

  wisp (vapor): Ephemeral work that auto-cleans up
    - Release workflows (one-time execution)
    - Patrol cycles (deacon, witness, refinery)
    - Health checks and diagnostics
    - Any operational workflow without audit value

TIP: Formulas can specify phase:"vapor" to recommend wisp usage.
     If you pour a vapor-phase formula, you'll get a warning.

Examples:
  bd mol pour mol-feature --var name=auth    # Persistent feature work
  bd mol pour mol-review --var pr=123        # Persistent code review`,
	Args: cobra.ExactArgs(1),
	Run:  runPour,
}

func runPour(cmd *cobra.Command, args []string) {
	CheckReadonly("pour")

	ctx := rootCtx

	// Pour requires direct store access for cloning
	if store == nil {
		FatalError("no database connection")
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringArray("var")
	assignee, _ := cmd.Flags().GetString("assignee")
	attachFlags, _ := cmd.Flags().GetStringSlice("attach")
	attachType, _ := cmd.Flags().GetString("attach-type")

	// Parse variables
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			FatalError("invalid variable format '%s', expected 'key=value'", v)
		}
		vars[parts[0]] = parts[1]
	}

	// Try to load as formula first (ephemeral proto - gt-4v1eo)
	// If that fails, fall back to loading from DB (legacy proto beads)
	var subgraph *TemplateSubgraph
	var protoID string
	isFormula := false

	// Try to cook formula inline (gt-4v1eo: ephemeral protos)
	// This works for any valid formula name, not just "mol-" prefixed ones
	// Pass vars for step condition filtering (bd-7zka.1)
	sg, err := resolveAndCookFormulaWithVars(args[0], nil, vars)
	if err == nil {
		subgraph = sg
		protoID = sg.Root.ID
		isFormula = true

		// Warn if formula recommends vapor phase (bd-mol cleanup)
		if sg.Phase == "vapor" {
			fmt.Fprintf(os.Stderr, "%s Formula %q recommends vapor phase (ephemeral)\n", ui.RenderWarn("⚠"), args[0])
			fmt.Fprintf(os.Stderr, "  Consider using: bd mol wisp %s", args[0])
			for _, v := range varFlags {
				fmt.Fprintf(os.Stderr, " --var %s", v)
			}
			fmt.Fprintf(os.Stderr, "\n")
			fmt.Fprintf(os.Stderr, "  Pour creates persistent issues that sync to git.\n")
			fmt.Fprintf(os.Stderr, "  Wisp creates ephemeral issues that auto-cleanup.\n\n")
		}
	}

	if subgraph == nil {
		// Try to load as existing proto bead (legacy path)
		resolvedID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalError("%s not found as formula or proto ID", args[0])
		}
		protoID = resolvedID

		// Verify it's a proto
		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			FatalError("loading proto %s: %v", protoID, err)
		}
		if !isProto(protoIssue) {
			FatalError("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
		}

		// Load the proto subgraph from DB
		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			FatalError("loading proto: %v", err)
		}
	}

	_ = isFormula // For future use (e.g., logging)

	// Resolve and load attached protos
	type attachmentInfo struct {
		id       string
		issue    *types.Issue
		subgraph *TemplateSubgraph
	}
	var attachments []attachmentInfo
	for _, attachArg := range attachFlags {
		attachID, err := utils.ResolvePartialID(ctx, store, attachArg)
		if err != nil {
			FatalError("resolving attachment ID %s: %v", attachArg, err)
		}
		attachIssue, err := store.GetIssue(ctx, attachID)
		if err != nil {
			FatalError("loading attachment %s: %v", attachID, err)
		}
		if !isProto(attachIssue) {
			FatalError("%s is not a proto (missing '%s' label)", attachID, MoleculeLabel)
		}
		attachSubgraph, err := loadTemplateSubgraph(ctx, store, attachID)
		if err != nil {
			FatalError("loading attachment subgraph %s: %v", attachID, err)
		}
		attachments = append(attachments, attachmentInfo{
			id:       attachID,
			issue:    attachIssue,
			subgraph: attachSubgraph,
		})
	}

	// Apply variable defaults from formula
	vars = applyVariableDefaults(vars, subgraph)

	// Check for missing required variables (those without defaults)
	requiredVars := extractRequiredVariables(subgraph)
	for _, attach := range attachments {
		attachVars := extractRequiredVariables(attach.subgraph)
		for _, v := range attachVars {
			found := false
			for _, rv := range requiredVars {
				if rv == v {
					found = true
					break
				}
			}
			if !found {
				requiredVars = append(requiredVars, v)
			}
		}
	}
	var missingVars []string
	for _, v := range requiredVars {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		FatalErrorWithHint(
			fmt.Sprintf("missing required variables: %s", strings.Join(missingVars, ", ")),
			fmt.Sprintf("Provide them with: --var %s=<value>", missingVars[0]),
		)
	}

	if dryRun {
		fmt.Printf("\nDry run: would pour %d issues from proto %s\n\n", len(subgraph.Issues), protoID)
		fmt.Printf("Storage: permanent (.beads/)\n\n")
		for _, issue := range subgraph.Issues {
			newTitle := substituteVariables(issue.Title, vars)
			suffix := ""
			if issue.ID == subgraph.Root.ID && assignee != "" {
				suffix = fmt.Sprintf(" (assignee: %s)", assignee)
			}
			fmt.Printf("  - %s (from %s)%s\n", newTitle, issue.ID, suffix)
		}
		if len(attachments) > 0 {
			fmt.Printf("\nAttachments (%s bonding):\n", attachType)
			for _, attach := range attachments {
				fmt.Printf("  + %s (%d issues)\n", attach.issue.Title, len(attach.subgraph.Issues))
			}
		}
		return
	}

	// Spawn as persistent mol (ephemeral=false)
	// Use mol prefix for distinct visual recognition (see types.IDPrefixMol)
	result, err := spawnMolecule(ctx, store, subgraph, vars, assignee, actor, false, types.IDPrefixMol)
	if err != nil {
		FatalError("pouring proto: %v", err)
	}

	// Attach bonded protos
	totalAttached := 0
	if len(attachments) > 0 {
		spawnedMol, err := store.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			FatalError("loading spawned mol: %v", err)
		}

		for _, attach := range attachments {
			// pour command always creates persistent (Wisp=false) issues
			bondResult, err := bondProtoMol(ctx, store, attach.issue, spawnedMol, attachType, vars, "", actor, false, true)
			if err != nil {
				FatalError("attaching %s: %v", attach.id, err)
			}
			totalAttached += bondResult.Spawned
		}
	}

	if jsonOutput {
		type pourResult struct {
			*InstantiateResult
			Attached int    `json:"attached"`
			Phase    string `json:"phase"`
		}
		outputJSON(pourResult{result, totalAttached, "liquid"})
		return
	}

	fmt.Printf("%s Poured mol: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	fmt.Printf("  Phase: liquid (persistent in .beads/)\n")
	if totalAttached > 0 {
		fmt.Printf("  Attached: %d issues from %d protos\n", totalAttached, len(attachments))
	}
}

func init() {
	// Pour command flags
	pourCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value)")
	pourCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	pourCmd.Flags().String("assignee", "", "Assign the root issue to this agent/user")
	pourCmd.Flags().StringSlice("attach", []string{}, "Proto to attach after spawning (repeatable)")
	pourCmd.Flags().String("attach-type", types.BondTypeSequential, "Bond type for attachments: sequential, parallel, or conditional")

	molCmd.AddCommand(pourCmd)
}
