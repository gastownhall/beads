package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/metrics"
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

This is the chemistry-inspired command for creating PERSISTENT work from templates.
The resulting mol is stored as persistent beads in the issue database and
syncs like any other bead (bd dolt push / pull).

Phase transition: Proto (solid) -> pour -> Mol (liquid)

WHEN TO USE POUR vs WISP:
  pour (liquid): Persistent work that needs audit trail
    - Feature implementations spanning multiple sessions
    - Work you may need to reference later
    - Anything worth preserving in git history

  wisp (vapor): Ephemeral work that auto-cleans up
    - Release workflows (one-time execution)
    - Operational loops and recurring cycles
    - Health checks and diagnostics
    - Any operational workflow without audit value

TIP: Formulas can specify phase:"vapor" to recommend wisp usage.
     If you pour a vapor-phase formula, you'll get a warning.

Examples:
  bd mol pour mol-feature --var name=auth    # Persistent feature work
  bd mol pour mol-review --var pr=123        # Persistent code review`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runPour,
}

type pourInput struct {
	protoArg   string
	dryRun     bool
	varFlags   []string
	assignee   string
	attachArgs []string
	attachType string
}

func gatherPourInput(cmd *cobra.Command, args []string) pourInput {
	in := pourInput{protoArg: args[0]}
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")
	in.varFlags, _ = cmd.Flags().GetStringArray("var")
	in.assignee, _ = cmd.Flags().GetString("assignee")
	in.attachArgs, _ = cmd.Flags().GetStringSlice("attach")
	in.attachType, _ = cmd.Flags().GetString("attach-type")
	return in
}

func parseVarFlags(varFlags []string) (map[string]string, error) {
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid variable format '%s', expected 'key=value'", v)
		}
		vars[parts[0]] = parts[1]
	}
	return vars, nil
}

func runPour(cmd *cobra.Command, args []string) error {
	CheckReadonly("pour")

	evt := metrics.NewCommandEvent("pour")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	in := gatherPourInput(cmd, args)

	if usesProxiedServer() {
		return runPourProxiedServer(rootCtx, in)
	}

	ctx := rootCtx

	if store == nil {
		return HandleError("no database connection")
	}

	vars, err := parseVarFlags(in.varFlags)
	if err != nil {
		return HandleError("%v", err)
	}

	var subgraph *TemplateSubgraph
	var protoID string

	sg, err := resolveAndCookFormulaWithVars(in.protoArg, nil, vars)
	if err == nil {
		subgraph = sg
		protoID = sg.Root.ID

		if sg.Phase == "vapor" {
			warnPourVaporFormula(in.protoArg, in.varFlags)
		}
	} else if errors.Is(err, formula.ErrVarValidation) || errors.Is(err, formula.ErrValidation) {
		// in.protoArg IS a formula; either the formula itself does not
		// validate, or the --var values it was given fail enum/pattern/
		// required-empty constraints. Report that directly
		// instead of falling through to the proto-ID lookup below, which
		// would otherwise mask this as "not found as formula or proto ID".
		return HandleError("%v", err)
	}

	if subgraph == nil {
		resolvedID, err := utils.ResolvePartialID(ctx, store, in.protoArg)
		if err != nil {
			return HandleError("%s not found as formula or proto ID", in.protoArg)
		}
		protoID = resolvedID

		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			return HandleError("loading proto %s: %v", protoID, err)
		}
		if !isProto(protoIssue) {
			return HandleError("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
		}

		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			return HandleError("loading proto: %v", err)
		}
	}

	type attachmentInfo struct {
		id       string
		issue    *types.Issue
		subgraph *TemplateSubgraph
	}
	var attachments []attachmentInfo
	for _, attachArg := range in.attachArgs {
		attachID, err := utils.ResolvePartialID(ctx, store, attachArg)
		if err != nil {
			return HandleError("resolving attachment ID %s: %v", attachArg, err)
		}
		attachIssue, err := store.GetIssue(ctx, attachID)
		if err != nil {
			return HandleError("loading attachment %s: %v", attachID, err)
		}
		if !isProto(attachIssue) {
			return HandleError("%s is not a proto (missing '%s' label)", attachID, MoleculeLabel)
		}
		attachSubgraph, err := loadTemplateSubgraph(ctx, store, attachID)
		if err != nil {
			return HandleError("loading attachment subgraph %s: %v", attachID, err)
		}
		attachments = append(attachments, attachmentInfo{attachID, attachIssue, attachSubgraph})
	}

	vars = applyVariableDefaults(vars, subgraph)

	var attachSubgraphs []*TemplateSubgraph
	for _, a := range attachments {
		attachSubgraphs = append(attachSubgraphs, a.subgraph)
	}
	if err := checkPourVars(subgraph, attachSubgraphs, vars); err != nil {
		return handleVarErrorWithHint(err, missingVarHint(subgraph, attachSubgraphs, vars))
	}

	if in.dryRun {
		var previews []pourAttachPreview
		for _, a := range attachments {
			previews = append(previews, pourAttachPreview{title: a.issue.Title, steps: len(a.subgraph.Issues)})
		}
		renderPourDryRun(protoID, subgraph, vars, in.assignee, in.attachType, previews)
		return nil
	}

	result, err := spawnMolecule(ctx, store, subgraph, vars, in.assignee, actor, false, types.IDPrefixMol)
	if err != nil {
		return HandleError("pouring proto: %v", err)
	}

	totalAttached := 0
	if len(attachments) > 0 {
		spawnedMol, err := store.GetIssue(ctx, result.NewEpicID)
		if err != nil {
			return HandleError("loading spawned mol: %v", err)
		}

		for _, attach := range attachments {
			bondResult, err := bondProtoMol(ctx, store, attach.issue, spawnedMol, in.attachType, vars, "", actor, false, true)
			if err != nil {
				return HandleError("attaching %s: %v", attach.id, err)
			}
			totalAttached += bondResult.Spawned
		}
	}

	return renderPourResult(result, totalAttached, len(attachments))
}

func warnPourVaporFormula(protoArg string, varFlags []string) {
	fmt.Fprintf(os.Stderr, "%s Formula %q recommends vapor phase (ephemeral)\n", ui.RenderWarn("⚠"), protoArg)
	fmt.Fprintf(os.Stderr, "  Consider using: bd mol wisp %s", protoArg)
	for _, v := range varFlags {
		fmt.Fprintf(os.Stderr, " --var %s", v)
	}
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Pour creates persistent issues that sync like any other bead.\n")
	fmt.Fprintf(os.Stderr, "  Wisp creates ephemeral issues that auto-cleanup.\n\n")
}

func requiredVarsAcross(subgraph *TemplateSubgraph, attachSubgraphs []*TemplateSubgraph) []string {
	requiredVars := extractRequiredVariables(subgraph)
	for _, attachSubgraph := range attachSubgraphs {
		for _, v := range extractRequiredVariables(attachSubgraph) {
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
	return requiredVars
}

func checkPourVars(subgraph *TemplateSubgraph, attachSubgraphs []*TemplateSubgraph, vars map[string]string) error {
	var missingVars []string
	for _, v := range requiredVarsAcross(subgraph, attachSubgraphs) {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		return fmt.Errorf("missing required variables: %s", strings.Join(missingVars, ", "))
	}
	return checkUnknownVars(subgraph, attachSubgraphs, vars)
}

// checkUnknownVars rejects --var names the protos being poured cannot consume.
// A name is accepted if any proto declares it in [vars], references it as a
// {{handlebar}} in a field the pour substitutes (including the assignee,
// labels, a gate's AwaitID and metadata values), references it from a step
// condition, or - in a standalone expansion formula - uses it as a {name}
// placeholder in the template; so documentation handlebars and vars belonging
// to an --attach proto still pass. What is left over cannot affect the pour, and silently dropping it
// turns a typo in an optional var into a conditional step that quietly never
// appears.
//
// The check applies only when the known set is complete - see knownVarsAcross.
// Where it is not, no name is refused at all: a wrong refusal breaks a pour that
// works, which is worse than the silent drop this replaces.
func checkUnknownVars(subgraph *TemplateSubgraph, attachSubgraphs []*TemplateSubgraph, vars map[string]string) error {
	known, complete := knownVarsAcross(subgraph, attachSubgraphs)
	if !complete {
		return nil
	}

	var unknown []string
	for name := range vars {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	knownNames := make([]string, 0, len(known))
	for name := range known {
		knownNames = append(knownNames, name)
	}
	sort.Strings(knownNames)

	if len(knownNames) == 0 {
		return fmt.Errorf("unknown variables: %s (this proto takes no variables)", strings.Join(unknown, ", "))
	}
	return fmt.Errorf("unknown variables: %s (available: %s)", strings.Join(unknown, ", "), strings.Join(knownNames, ", "))
}

// knownVarsAcross returns the variable names the protos being poured can
// consume, and whether that set is COMPLETE.
//
// It is incomplete as soon as one participating proto was loaded from the
// database instead of cooked from a formula. Persistence keeps the issues but
// not the formula, so VarDefs is gone and all that survives is the handlebars
// visible in issue text: a variable the formula declared in [vars] but never
// wrote into a substituted field is then indistinguishable from a typo, and
// refusing it would reject a --var the pour does consume. That covers every
// `bd cook --persist` proto and every --attach proto, which is always DB-loaded.
func knownVarsAcross(subgraph *TemplateSubgraph, attachSubgraphs []*TemplateSubgraph) (map[string]bool, bool) {
	known := make(map[string]bool)
	complete := true
	add := func(sg *TemplateSubgraph) {
		if sg == nil {
			return
		}
		if !sg.DeclaredVarsKnown {
			complete = false
		}
		for name := range sg.VarDefs {
			known[name] = true
		}
		// Every field the clone substitutes, not just the prose: the
		// assignee, labels, a gate's await_id and every metadata value carry
		// handlebars that the pour fills in, so names appearing only there
		// are consumable too.
		for _, name := range extractConsumableVariables(sg) {
			known[name] = true
		}
		// Names the formula referenced that were consumed or erased before the
		// cook - a step condition, the text of a step the condition filter
		// dropped, or a standalone expansion template's {name} placeholder. All
		// are consumable: a condition decides which steps get poured at all,
		// dropped text must not stop being valid just because another var
		// switched its step off, and a placeholder was substituted while the
		// steps were being built.
		for _, name := range sg.FormulaVarRefs {
			known[name] = true
		}
	}

	add(subgraph)
	for _, attachSubgraph := range attachSubgraphs {
		add(attachSubgraph)
	}
	return known, complete
}

// handleVarErrorWithHint reports a --var error, attaching the "Provide them
// with" hint only when there is a name to put in it. An unknown-var error has no
// missing name to suggest, and an unguarded hint renders as the nonsense
// `--var =<value>`.
//
// Shared by every route that reports one of these errors (bd mol pour, bd mol
// wisp, and the proxied wisp server) so the guard cannot be dropped at one site
// while the others keep it.
func handleVarErrorWithHint(err error, hint string) error {
	if hint != "" {
		return HandleErrorWithHint(err.Error(), fmt.Sprintf("Provide them with: --var %s=<value>", hint))
	}
	return HandleError("%v", err)
}

func missingVarHint(subgraph *TemplateSubgraph, attachSubgraphs []*TemplateSubgraph, vars map[string]string) string {
	for _, v := range requiredVarsAcross(subgraph, attachSubgraphs) {
		if _, ok := vars[v]; !ok {
			return v
		}
	}
	return ""
}

type pourAttachPreview struct {
	title string
	steps int
}

func renderPourDryRun(protoID string, subgraph *TemplateSubgraph, vars map[string]string, assignee, attachType string, attachments []pourAttachPreview) {
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
			fmt.Printf("  + %s (%d issues)\n", attach.title, attach.steps)
		}
	}
}

func renderPourResult(result *InstantiateResult, totalAttached, attachCount int) error {
	if jsonOutput {
		type pourResult struct {
			*InstantiateResult
			Attached int    `json:"attached"`
			Phase    string `json:"phase"`
		}
		return outputJSON(pourResult{result, totalAttached, "liquid"})
	}

	fmt.Printf("%s Poured mol: created %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	fmt.Printf("  Phase: liquid (persistent in the issue database)\n")
	if totalAttached > 0 {
		fmt.Printf("  Attached: %d issues from %d protos\n", totalAttached, attachCount)
	}
	return nil
}

func init() {
	// Pour command flags
	pourCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value); a name the proto cannot consume is an error")
	pourCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	pourCmd.Flags().String("assignee", "", "Assign the root issue to this agent/user")
	pourCmd.Flags().StringSlice("attach", []string{}, "Proto to attach after spawning (repeatable)")
	pourCmd.Flags().String("attach-type", types.BondTypeSequential, "Bond type for attachments: sequential, parallel, or conditional")

	molCmd.AddCommand(pourCmd)
}
