package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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

This is the chemistry-inspired command for creating persistent work from templates.
The resulting mol lives in .beads/ (permanent storage) and is synced with git.

Phase transition: Proto (solid) -> pour -> Mol (liquid)

Use pour for:
  - Feature work that spans sessions
  - Important work needing audit trail
  - Anything you might need to reference later

Examples:
  bd pour mol-feature --var name=auth    # Create persistent mol from proto
  bd pour mol-release --var version=1.0  # Release workflow
  bd pour mol-review --var pr=123        # Code review workflow`,
	Args: cobra.ExactArgs(1),
	Run:  runPour,
}

func runPour(cmd *cobra.Command, args []string) {
	CheckReadonly("pour")

	ctx := rootCtx

	// Pour requires direct store access for cloning
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: pour requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon pour %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringSlice("var")
	assignee, _ := cmd.Flags().GetString("assignee")
	attachFlags, _ := cmd.Flags().GetStringSlice("attach")
	attachType, _ := cmd.Flags().GetString("attach-type")

	// Parse variables
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
			os.Exit(1)
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
	sg, err := resolveAndCookFormula(args[0], nil)
	if err == nil {
		subgraph = sg
		protoID = sg.Root.ID
		isFormula = true
	}

	if subgraph == nil {
		// Try to load as existing proto bead (legacy path)
		resolvedID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s not found as formula or proto ID\n", args[0])
			os.Exit(1)
		}
		protoID = resolvedID

		// Verify it's a proto
		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading proto %s: %v\n", protoID, err)
			os.Exit(1)
		}
		if !isProto(protoIssue) {
			fmt.Fprintf(os.Stderr, "Error: %s is not a proto (missing '%s' label)\n", protoID, MoleculeLabel)
			os.Exit(1)
		}

		// Load the proto subgraph from DB
		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading proto: %v\n", err)
			os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error resolving attachment ID %s: %v\n", attachArg, err)
			os.Exit(1)
		}
		attachIssue, err := store.GetIssue(ctx, attachID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading attachment %s: %v\n", attachID, err)
			os.Exit(1)
		}
		if !isProto(attachIssue) {
			fmt.Fprintf(os.Stderr, "Error: %s is not a proto (missing '%s' label)\n", attachID, MoleculeLabel)
			os.Exit(1)
		}
		attachSubgraph, err := loadTemplateSubgraph(ctx, store, attachID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading attachment subgraph %s: %v\n", attachID, err)
			os.Exit(1)
		}
		attachments = append(attachments, attachmentInfo{
			id:       attachID,
			issue:    attachIssue,
			subgraph: attachSubgraph,
		})
	}

	// Apply variable defaults from formula (gt-4v1eo)
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
		fmt.Fprintf(os.Stderr, "Error: missing required variables: %s\n", strings.Join(missingVars, ", "))
		fmt.Fprintf(os.Stderr, "Provide them with: --var %s=<value>\n", missingVars[0])
		os.Exit(1)
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
	// bd-hobo: Use "mol" prefix for distinct visual recognition
	result, err := spawnMolecule(ctx, store, subgraph, vars, assignee, actor, false, "mol")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error pouring proto: %v\n", err)
		os.Exit(1)
	}

	// Attach bonded protos
	totalAttached := 0
	if len(attachments) > 0 {
		spawnedMol, err := store.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading spawned mol: %v\n", err)
			os.Exit(1)
		}

		for _, attach := range attachments {
			// pour command always creates persistent (Wisp=false) issues
			bondResult, err := bondProtoMol(ctx, store, attach.issue, spawnedMol, attachType, vars, "", actor, false, true)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error attaching %s: %v\n", attach.id, err)
				os.Exit(1)
			}
			totalAttached += bondResult.Spawned
		}
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

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
	pourCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	pourCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	pourCmd.Flags().String("assignee", "", "Assign the root issue to this agent/user")
	pourCmd.Flags().StringSlice("attach", []string{}, "Proto to attach after spawning (repeatable)")
	pourCmd.Flags().String("attach-type", types.BondTypeSequential, "Bond type for attachments: sequential, parallel, or conditional")

	rootCmd.AddCommand(pourCmd)
}
