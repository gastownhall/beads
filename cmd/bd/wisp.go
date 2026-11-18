package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Wisp commands - manage ephemeral molecules
//
// Wisps are ephemeral issues with Ephemeral=true in the main database.
// They're used for patrol cycles and operational loops that shouldn't
// be synced via git.
//
// Commands:
//   bd mol wisp list    - List all wisps in current context
//   bd mol wisp gc      - Garbage collect orphaned wisps

var wispCmd = &cobra.Command{
	Use:   "wisp [proto-id]",
	Short: "Create or manage wisps (ephemeral molecules)",
	Long: `Create or manage wisps - EPHEMERAL molecules for operational workflows.

When called with a proto-id argument, creates a wisp from that proto.
When called with a subcommand (list, gc), manages existing wisps.

Wisps are issues with Ephemeral=true in the main database. They're stored
locally but NOT synced via git.

WHEN TO USE WISP vs POUR:
  wisp (vapor): Ephemeral work that auto-cleans up
    - Release workflows (one-time execution)
    - Operational loops and recurring cycles
    - Health checks and diagnostics
    - Any operational workflow without audit value

  pour (liquid): Persistent work that needs audit trail
    - Feature implementations spanning multiple sessions
    - Work you may need to reference later
    - Anything worth preserving in git history

TIP: Formulas can specify phase:"vapor" to recommend wisp usage.
     If you use pour on a vapor-phase formula, you'll get a warning.

The wisp lifecycle:
  1. Create: bd mol wisp <proto> or bd create --ephemeral
  2. Execute: Normal bd operations work on wisp issues
  3. Squash: bd mol squash <id> (clears Ephemeral flag, promotes to persistent)
  4. Or burn: bd mol burn <id> (deletes without creating digest)

Examples:
  bd mol wisp beads-release --var version=1.0  # Release workflow
  bd mol wisp mol-my-workflow                  # Ephemeral operational cycle
  bd mol wisp list                             # List all wisps
  bd mol wisp gc                               # Garbage collect old wisps

Subcommands:
  list  List all wisps in current context
  gc    Garbage collect orphaned wisps`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWisp,
}

// WispListItem represents a wisp in list output
type WispListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Priority  int       `json:"priority"`
	Type      string    `json:"type"`
	Labels    []string  `json:"labels,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Old       bool      `json:"old,omitempty"` // Not updated in 24+ hours
}

// WispListResult is the JSON output for wisp list
type WispListResult struct {
	Wisps    []WispListItem `json:"wisps"`
	Count    int            `json:"count"`
	OldCount int            `json:"old_count,omitempty"`
}

// OldThreshold is how old a wisp must be to be flagged as old (time-based, for ephemeral cleanup)
const OldThreshold = 24 * time.Hour

func runWisp(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if len(args) == 0 {
		_ = cmd.Help()
		return nil
	}
	// Delegate to the non-emitting core so `bd wisp <name>` records exactly one
	// cli_command event ("wisp"), not also "wisp-create".
	return runWispCreateCore(cmd, args)
}

// wispCreateCmd instantiates a proto as an ephemeral wisp (kept for backwards compat)
var wispCreateCmd = &cobra.Command{
	Use:   "create <proto-id>",
	Short: "Instantiate a proto as a wisp (solid -> vapor)",
	Long: `Create a wisp from a proto - sublimation from solid to vapor.

This is the chemistry-inspired command for creating ephemeral work from templates.
The resulting wisp is stored in the main database with Ephemeral=true and NOT synced via git.

Phase transition: Proto (solid) -> Wisp (vapor)

Use wisp for:
  - Operational loops and recurring cycles
  - Health checks and monitoring
  - One-shot orchestration runs
  - Routine operations with no audit value

The wisp will:
  - Be stored in main database with Ephemeral=true flag
  - NOT be synced via git
  - Either evaporate (burn) or condense to digest (squash)

Examples:
  bd mol wisp create mol-patrol                    # Ephemeral patrol cycle
  bd mol wisp create mol-health-check              # One-time health check
  bd mol wisp create mol-diagnostics --var target=db  # Diagnostic run`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispCreate,
}

func runWispCreate(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp-create")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	return runWispCreateCore(cmd, args)
}

// runWispCreateCore instantiates a proto as a wisp without emitting a metrics
// event, so the caller owns emission: the standalone `bd mol wisp create`
// entrypoint records "wisp-create", while the bare `bd wisp <name>` alias records
// "wisp". This keeps each invocation to exactly one cli_command event.
type wispCreateInput struct {
	protoArg string
	dryRun   bool
	rootOnly bool
	varFlags []string
}

func gatherWispCreateInput(cmd *cobra.Command, args []string) wispCreateInput {
	in := wispCreateInput{protoArg: args[0]}
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")
	in.rootOnly, _ = cmd.Flags().GetBool("root-only")
	in.varFlags, _ = cmd.Flags().GetStringArray("var")
	return in
}

func runWispCreateCore(cmd *cobra.Command, args []string) error {
	CheckReadonly("wisp create")

	in := gatherWispCreateInput(cmd, args)

	if usesProxiedServer() {
		return runWispCreateProxiedServer(rootCtx, in)
	}

	ctx := rootCtx

	if store == nil {
		return HandleErrorWithHint("no database connection", diagHint())
	}

	vars, err := parseVarFlags(in.varFlags)
	if err != nil {
		return HandleError("%v", err)
	}
	args = []string{in.protoArg}
	dryRun := in.dryRun
	rootOnly := in.rootOnly

	// Try to load as formula first (ephemeral proto)
	// If that fails, fall back to loading from DB (legacy proto beads)
	var subgraph *TemplateSubgraph
	var protoID string

	// Try to cook formula inline (ephemeral protos)
	// This works for any valid formula name, not just "mol-" prefixed ones
	// Pass vars for step condition filtering (bd-7zka.1)
	sg, err := resolveAndCookFormulaWithVars(args[0], nil, vars)
	if err == nil {
		subgraph = sg
		protoID = sg.Root.ID
	} else if errors.Is(err, formula.ErrVarValidation) || errors.Is(err, formula.ErrValidation) {
		// args[0] IS a formula; either the formula itself does not
		// validate, or the --var values it was given fail enum/pattern/
		// required-empty constraints. Report that directly
		// instead of falling through to the legacy proto-ID lookup below,
		// which would otherwise mask this as "not found as formula or proto".
		return HandleError("%v", err)
	}

	if subgraph == nil {
		// Resolve proto ID (legacy path)
		protoID = args[0]
		// Try to resolve partial ID if it doesn't look like a full ID
		if !strings.HasPrefix(protoID, "bd-") && !strings.HasPrefix(protoID, "gt-") && !strings.HasPrefix(protoID, "mol-") {
			// Might be a partial ID, try to resolve
			if resolved, err := resolvePartialIDDirect(ctx, protoID); err == nil {
				protoID = resolved
			}
		}

		if strings.HasPrefix(protoID, "mol-") {
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
				Labels: []string{MoleculeLabel},
			})
			if err != nil {
				return HandleError("searching for proto: %v", err)
			}
			found := false
			for _, issue := range issues {
				if strings.Contains(issue.Title, protoID) || issue.ID == protoID {
					protoID = issue.ID
					found = true
					break
				}
			}
			if !found {
				return HandleErrorWithHint(fmt.Sprintf("'%s' not found as formula or proto", args[0]), "run 'bd formula list' to see available formulas")
			}
		}

		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return HandleError("proto not found: %s", protoID)
			}
			return HandleError("loading proto %s: %v", protoID, err)
		}
		if !isProtoIssue(protoIssue) {
			return HandleError("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
		}

		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			return HandleError("loading proto: %v", err)
		}
	}

	vars = applyVariableDefaults(vars, subgraph)

	if err := checkRequiredVars(subgraph, vars); err != nil {
		return handleVarErrorWithHint(err, firstMissingVar(subgraph, vars))
	}

	if dryRun {
		renderWispCreateDryRun(protoID, subgraph, vars, rootOnly)
		return nil
	}

	result, err := spawnMoleculeWithOptions(ctx, store, subgraph, CloneOptions{
		Vars:      vars,
		Actor:     currentActor(),
		Ephemeral: true,
		Prefix:    types.IDPrefixWisp,
		RootOnly:  rootOnly,
	})
	if err != nil {
		return HandleError("creating wisp: %v", err)
	}

	return renderWispCreateResult(result)
}

func checkRequiredVars(subgraph *TemplateSubgraph, vars map[string]string) error {
	var missingVars []string
	for _, v := range extractRequiredVariables(subgraph) {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		return fmt.Errorf("missing required variables: %s", strings.Join(missingVars, ", "))
	}
	return checkUnknownVars(subgraph, nil, vars)
}

func firstMissingVar(subgraph *TemplateSubgraph, vars map[string]string) string {
	for _, v := range extractRequiredVariables(subgraph) {
		if _, ok := vars[v]; !ok {
			return v
		}
	}
	return ""
}

func renderWispCreateDryRun(protoID string, subgraph *TemplateSubgraph, vars map[string]string, rootOnly bool) {
	if rootOnly {
		skipped := len(subgraph.Issues) - 1
		fmt.Printf("\nDry run: would create wisp with 1 issue (root only) from proto %s\n", protoID)
		if skipped > 0 {
			fmt.Printf("  Note: %d child step(s) skipped (--root-only)\n", skipped)
		}
	} else {
		fmt.Printf("\nDry run: would create wisp with %d issues from proto %s\n\n", len(subgraph.Issues), protoID)
	}
	fmt.Printf("Storage: main database (ephemeral=true, not synced via git)\n\n")
	issuesToShow := subgraph.Issues
	if rootOnly && len(issuesToShow) > 0 {
		issuesToShow = issuesToShow[:1]
	}
	for _, issue := range issuesToShow {
		newTitle := substituteVariables(issue.Title, vars)
		fmt.Printf("  - %s (from %s)\n", newTitle, issue.ID)
	}
}

func renderWispCreateResult(result *InstantiateResult) error {
	if jsonOutput {
		type wispCreateResult struct {
			*InstantiateResult
			Phase string `json:"phase"`
		}
		return outputJSON(wispCreateResult{result, "vapor"})
	}

	fmt.Printf("%s Created wisp: %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	fmt.Printf("  Phase: vapor (ephemeral, not synced via git)\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  bd close %s.<step>       # Complete steps\n", result.NewEpicID)
	fmt.Printf("  bd mol squash %s         # Condense to digest (promotes to persistent)\n", result.NewEpicID)
	fmt.Printf("  bd mol burn %s           # Discard without creating digest\n", result.NewEpicID)
	return nil
}

// isProtoIssue checks if an issue is a proto (has the template label)
func isProtoIssue(issue *types.Issue) bool {
	for _, label := range issue.Labels {
		if label == MoleculeLabel {
			return true
		}
	}
	return false
}

// resolvePartialIDDirect resolves a partial ID directly from store
func resolvePartialIDDirect(ctx context.Context, partial string) (string, error) {
	// Try direct lookup first
	if issue, err := store.GetIssue(ctx, partial); err == nil {
		return issue.ID, nil
	}
	// Search by prefix
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IDs: []string{partial + "*"},
	})
	if err != nil {
		return "", err
	}
	if len(issues) == 1 {
		return issues[0].ID, nil
	}
	if len(issues) > 1 {
		return "", fmt.Errorf("ambiguous ID: %s matches %d issues", partial, len(issues))
	}
	return "", fmt.Errorf("not found: %s", partial)
}

var wispListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all wisps in current context",
	Long: `List all wisps (ephemeral molecules) in the current context.

Wisps are issues with Ephemeral=true in the main database. They are stored
locally but not synced via git.

The list shows:
  - ID: Issue ID of the wisp
  - Title: Wisp title
  - Status: Current status (open, in_progress, closed)
  - Started: When the wisp was created
  - Updated: Last modification time

Old wisp detection:
  - Old wisps haven't been updated in 24+ hours
  - Use 'bd mol wisp gc' to clean up old/abandoned wisps

Examples:
  bd mol wisp list              # List all wisps
  bd mol wisp list --json       # JSON output for programmatic use
  bd mol wisp list --all        # Include closed wisps`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispList,
}

func wispListFilter(typeFilter string) types.IssueFilter {
	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral: &ephemeralFlag,
		Limit:     5000,
	}
	if typeFilter != "" {
		it := types.IssueType(typeFilter)
		filter.IssueType = &it
	}
	return filter
}

func buildWispListResult(issues []*types.Issue, showAll bool) WispListResult {
	if !showAll {
		var filtered []*types.Issue
		for _, issue := range issues {
			if issue.Status != types.StatusClosed {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}

	now := time.Now()
	items := make([]WispListItem, 0, len(issues))
	oldCount := 0

	for _, issue := range issues {
		item := WispListItem{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  issue.Priority,
			Type:      string(issue.IssueType),
			Labels:    issue.Labels,
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
		}
		if now.Sub(issue.UpdatedAt) > OldThreshold {
			item.Old = true
			oldCount++
		}
		items = append(items, item)
	}

	slices.SortFunc(items, func(a, b WispListItem) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})

	return WispListResult{
		Wisps:    items,
		Count:    len(items),
		OldCount: oldCount,
	}
}

func renderWispListResult(result WispListResult) error {
	if jsonOutput {
		return outputJSON(result)
	}

	if len(result.Wisps) == 0 {
		fmt.Println("No wisps found")
		return nil
	}

	fmt.Printf("Wisps (%d):\n\n", len(result.Wisps))
	fmt.Printf("%-12s %-10s %-4s %-10s %-46s %s\n",
		"ID", "STATUS", "PRI", "TYPE", "TITLE", "UPDATED")
	fmt.Println(strings.Repeat("-", 100))

	for _, item := range result.Wisps {
		title := item.Title
		if len(title) > 44 {
			title = title[:41] + "..."
		}
		status := ui.RenderStatus(item.Status)
		updated := formatTimeAgo(item.UpdatedAt)
		if item.Old {
			updated = ui.RenderWarn(updated + " ⚠")
		}
		fmt.Printf("%-12s %-10s P%-3d %-10s %-46s %s\n",
			item.ID, status, item.Priority, item.Type, title, updated)
	}

	if result.OldCount > 0 {
		fmt.Printf("\n%s %d old wisp(s) (not updated in 24+ hours)\n",
			ui.RenderWarn("⚠"), result.OldCount)
		fmt.Println("  Hint: Use 'bd mol wisp gc' to clean up old wisps")
	}
	return nil
}

func runWispList(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp-list")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	showAll, _ := cmd.Flags().GetBool("all")
	typeFilter, _ := cmd.Flags().GetString("type")

	if usesProxiedServer() {
		return runWispListProxiedServer(rootCtx, showAll, typeFilter)
	}

	ctx := rootCtx

	if store == nil {
		if jsonOutput {
			return outputJSON(WispListResult{
				Wisps: []WispListItem{},
				Count: 0,
			})
		}
		fmt.Println("No database connection")
		return nil
	}

	issues, err := store.SearchIssues(ctx, "", wispListFilter(typeFilter))
	if err != nil {
		return HandleError("listing wisps: %v", err)
	}

	return renderWispListResult(buildWispListResult(issues, showAll))
}

// formatTimeAgo returns a human-readable relative time
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}

// formatTimeUntil returns a human-readable relative time for a future instant,
// the forward-looking mirror of formatTimeAgo. Used for lease expiry in bd show.
// A past (or present) instant renders as "expired".
func formatTimeUntil(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Minute:
		return "in <1 min"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "in 1 min"
		}
		return fmt.Sprintf("in %d mins", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "in 1 hour"
		}
		return fmt.Sprintf("in %d hours", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "in 1 day"
		}
		return fmt.Sprintf("in %d days", days)
	}
}

func init() {
	// Wisp command flags (for direct create: bd mol wisp <proto>)
	wispCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value); a name the proto cannot consume is an error")
	wispCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	wispCmd.Flags().Bool("root-only", false, "Create only the root issue (no child step issues)")

	// Wisp create command flags (kept for backwards compat: bd mol wisp create <proto>)
	wispCreateCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value); a name the proto cannot consume is an error")
	wispCreateCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	wispCreateCmd.Flags().Bool("root-only", false, "Create only the root issue (no child step issues)")

	wispListCmd.Flags().Bool("all", false, "Include closed wisps")
	wispListCmd.Flags().String("type", "", "Filter by issue type (e.g., agent, task, patrol)")

	wispGCCmd.Flags().Bool("dry-run", false, "Preview what would be cleaned")
	wispGCCmd.Flags().String("age", "1h", "Age threshold for abandoned wisp detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")
	wispGCCmd.Flags().Bool("closed", false, "Delete all closed wisps (ignores --age threshold)")
	wispGCCmd.Flags().BoolP("force", "f", false, "Actually delete (default: preview only)")
	wispGCCmd.Flags().StringSlice("exclude-type", nil, "Exclude wisps of these types from GC (comma-separated, e.g., agent,rig)")

	wispCmd.AddCommand(wispCreateCmd)
	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	molCmd.AddCommand(wispCmd)
}
