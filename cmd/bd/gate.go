package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// gateCmd is the parent command for gate operations
var gateCmd = &cobra.Command{
	Use:     "gate",
	GroupID: "issues",
	Short:   "Manage async coordination gates",
	Long: `Gates are async wait conditions that block workflow steps.

Gates are created automatically when a formula step has a gate field.
They must be closed (manually or via watchers) for the blocked step to proceed.

Gate types:
  human   - Requires manual bd close (Phase 1)
  timer   - Expires after timeout (Phase 2)
  gh:run  - Waits for GitHub workflow (Phase 3)
  gh:pr   - Waits for PR merge (Phase 3)
  bead    - Waits for another bead to close (Phase 4)

For bead gates, await_id is a bead ID in this rig's database (e.g., "bd-abc123").
The historical cross-rig form <rig>:<bead-id> can no longer be evaluated
(multi-rig routing removed) and stays pending until resolved manually.

Examples:
  bd gate list           # Show all open gates
  bd gate list --all     # Show all gates including closed
  bd gate check          # Evaluate all open gates
  bd gate check --type=bead  # Evaluate only bead gates
  bd gate resolve <id>   # Close a gate manually`,
}

// gateListCmd lists gate issues
var gateListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List gate issues",
	Long: `List gate issues.

With no argument, lists all gate issues in the current beads database.
With an [issue-id] argument, lists ONLY the gates that block that issue
(its own dependency gates) — not every gate in the database.

By default, shows only open gates. Use --all to include closed gates.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("gate-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runGateListProxiedServer(cmd, rootCtx, args)
		}

		allFlag, _ := cmd.Flags().GetBool("all")
		limit, _ := cmd.Flags().GetInt("limit")

		ctx := rootCtx

		// Bead-scoped: list only the gates that block this specific issue
		// (its dependency gates), never the whole database. Without this an
		// issue-id argument was silently ignored and the DB-wide list was
		// returned, which could lead a caller to act on unrelated gates.
		if len(args) == 1 {
			target, err := store.GetIssue(ctx, args[0])
			if err != nil {
				return HandleErrorRespectJSON("issue not found: %s", args[0])
			}
			deps, err := store.GetDependencies(ctx, target.ID)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			gates := filterIssueGates(deps, allFlag, limit)
			if jsonOutput {
				return outputJSON(gates)
			}
			displayGates(gates, allFlag)
			return nil
		}

		gateType := types.IssueType("gate")
		filter := types.IssueFilter{
			IssueType: &gateType,
			Limit:     limit,
		}

		if !allFlag {
			filter.ExcludeStatus = []types.Status{types.StatusClosed}
		}

		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if jsonOutput {
			return outputJSON(issues)
		}

		displayGates(issues, allFlag)
		return nil
	},
}

// filterIssueGates selects the gate-type issues from an issue's dependency set,
// honoring the same open/closed and limit semantics as the DB-wide list path.
// Pulled out as a pure helper so the bead-scoping logic is unit-testable without
// a live store.
func filterIssueGates(deps []*types.Issue, all bool, limit int) []*types.Issue {
	var gates []*types.Issue
	for _, d := range deps {
		if d == nil || d.IssueType != types.IssueType("gate") {
			continue
		}
		if !all && d.Status == types.StatusClosed {
			continue
		}
		gates = append(gates, d)
		if limit > 0 && len(gates) >= limit {
			break
		}
	}
	return gates
}

// displayGates formats and displays gate issues, separating open and closed gates
func displayGates(gates []*types.Issue, showAll bool) {
	if len(gates) == 0 {
		fmt.Println("No gates found.")
		return
	}

	// Separate open and closed gates
	var openGates, closedGates []*types.Issue
	for _, gate := range gates {
		if gate.Status == types.StatusClosed {
			closedGates = append(closedGates, gate)
		} else {
			openGates = append(openGates, gate)
		}
	}

	// Display open gates
	if len(openGates) > 0 {
		fmt.Printf("\n%s Open Gates (%d):\n\n", ui.RenderAccent("⏳"), len(openGates))
		for _, gate := range openGates {
			displaySingleGate(gate)
		}
	}

	// Display closed gates only if --all was used
	if showAll && len(closedGates) > 0 {
		fmt.Printf("\n%s Closed Gates (%d):\n\n", ui.RenderMuted("●"), len(closedGates))
		for _, gate := range closedGates {
			displaySingleGate(gate)
		}
	}

	if len(openGates) == 0 && (!showAll || len(closedGates) == 0) {
		fmt.Println("No gates found.")
		return
	}

	fmt.Printf("To resolve a gate: bd close <gate-id>\n")
}

// displaySingleGate formats and displays a single gate issue
func displaySingleGate(gate *types.Issue) {
	statusSym := "○"
	if gate.Status == types.StatusClosed {
		statusSym = "●"
	}

	// Format gate info
	gateInfo := gate.AwaitType
	if gate.AwaitID != "" {
		gateInfo = fmt.Sprintf("%s %s", gate.AwaitType, gate.AwaitID)
	}

	// Format timeout if present
	timeoutStr := ""
	if gate.Timeout > 0 {
		timeoutStr = fmt.Sprintf(" (timeout: %s)", gate.Timeout)
	}

	// Find blocked step from ID (gate ID format: parent.gate-stepid)
	blockedStep := ""
	if strings.Contains(gate.ID, ".gate-") {
		parts := strings.Split(gate.ID, ".gate-")
		if len(parts) == 2 {
			blockedStep = fmt.Sprintf("%s.%s", parts[0], parts[1])
		}
	}

	fmt.Printf("%s %s - %s%s\n", statusSym, ui.RenderID(gate.ID), gateInfo, timeoutStr)
	if blockedStep != "" {
		fmt.Printf("  Blocks: %s\n", blockedStep)
	}
	fmt.Println()
}

// gateAddWaiterCmd adds a waiter to a gate
var gateAddWaiterCmd = &cobra.Command{
	Use:   "add-waiter <gate-id> <waiter>",
	Short: "Add a waiter to a gate",
	Long: `Register an agent as a waiter on a gate bead.

When the gate closes, the waiter will receive a wake notification via 'bd gate wake'.
The waiter is typically the worker's address (e.g., "my-project/workers/agent-1").

This is used by 'bd done --phase-complete' to register for gate wake notifications.`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("gate add-waiter is not supported in proxied-server mode")
		}
		CheckReadonly("gate add-waiter")

		evt := metrics.NewCommandEvent("gate-add-waiter")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		waiter := args[1]
		ctx := rootCtx

		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			return HandleError("gate not found: %s", gateID)
		}

		if issue.IssueType != "gate" {
			return HandleError("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		for _, w := range issue.Waiters {
			if w == waiter {
				fmt.Printf("Waiter already registered on gate %s\n", gateID)
				return nil
			}
		}

		newWaiters := append(issue.Waiters, waiter)

		updates := map[string]interface{}{
			"waiters": newWaiters,
		}
		if err := store.UpdateIssue(ctx, gateID, updates, actor); err != nil {
			return HandleError("updating gate: %v", err)
		}

		commandDidWrite.Store(true)

		fmt.Printf("%s Added waiter to gate %s: %s\n", ui.RenderPass("✓"), gateID, waiter)
		return nil
	},
}

// gateCreateCmd creates an ad-hoc gate issue that blocks another issue
var gateCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a gate that blocks an issue",
	Long: `Create an ad-hoc gate issue that blocks another issue until resolved.

The blocked issue will not appear in 'bd ready' until the gate is resolved
via 'bd gate resolve'.

Gate types:
  human   - Requires manual 'bd gate resolve' (default)
  timer   - Auto-resolves after --timeout duration
  gh:run  - Waits for GitHub Actions workflow
  gh:pr   - Waits for PR merge

Examples:
  bd gate create --blocks bd-abc
  bd gate create --type=human --blocks bd-abc --reason="Need design review"
  bd gate create --type=timer --blocks bd-abc --timeout=2h
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("gate create is not supported in proxied-server mode")
		}
		CheckReadonly("gate create")

		evt := metrics.NewCommandEvent("gate-create")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		blocksID, _ := cmd.Flags().GetString("blocks")
		gateType, _ := cmd.Flags().GetString("type")
		reason, _ := cmd.Flags().GetString("reason")
		awaitID, _ := cmd.Flags().GetString("await-id")
		timeoutStr, _ := cmd.Flags().GetString("timeout")

		ctx := rootCtx

		targetIssue, err := store.GetIssue(ctx, blocksID)
		if err != nil {
			return HandleErrorRespectJSON("issue not found: %s", blocksID)
		}

		var timeout time.Duration
		if timeoutStr != "" {
			parsed, err := time.ParseDuration(timeoutStr)
			if err != nil {
				return HandleErrorRespectJSON("invalid timeout: %v", err)
			}
			timeout = parsed
		}

		title := fmt.Sprintf("Gate: %s", gateType)
		if awaitID != "" {
			title = fmt.Sprintf("Gate: %s %s", gateType, awaitID)
		}

		desc := fmt.Sprintf("Ad-hoc gate blocking %s", targetIssue.ID)
		if reason != "" {
			desc = fmt.Sprintf("%s\n\nReason: %s", desc, reason)
		}

		gate := &types.Issue{
			Title:       title,
			Description: desc,
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.IssueType("gate"),
			AwaitType:   gateType,
			AwaitID:     awaitID,
			Timeout:     timeout,
			CreatedBy:   getActorWithGit(),
			Owner:       getOwner(),
		}

		if err := store.CreateIssue(ctx, gate, actor); err != nil {
			return HandleErrorRespectJSON("creating gate: %v", err)
		}

		dep := &types.Dependency{
			IssueID:     targetIssue.ID,
			DependsOnID: gate.ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			return HandleErrorRespectJSON("adding blocking dependency: %v", err)
		}

		commitMsg := fmt.Sprintf("bd: create gate %s blocking %s", gate.ID, targetIssue.ID)
		if err := store.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
			return HandleErrorRespectJSON("failed to commit: %v", err)
		}

		if jsonOutput {
			return outputJSON(gate)
		}

		fmt.Printf("%s Created gate %s (type: %s)\n", ui.RenderPass("✓"), ui.RenderID(gate.ID), gateType)
		fmt.Printf("  Blocks: %s (%s)\n", targetIssue.ID, targetIssue.Title)
		if reason != "" {
			fmt.Printf("  Reason: %s\n", reason)
		}
		if timeout > 0 {
			fmt.Printf("  Timeout: %s\n", timeout)
		}
		fmt.Printf("\nResolve with: bd gate resolve %s\n", gate.ID)
		return nil
	},
}

// gateShowCmd shows a gate issue
var gateShowCmd = &cobra.Command{
	Use:   "show <gate-id>",
	Short: "Show a gate issue",
	Long: `Display details of a gate issue including its waiters.

This is similar to 'bd show' but validates that the issue is a gate.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("gate show is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("gate-show")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		ctx := rootCtx

		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			return HandleErrorRespectJSON("gate not found: %s", gateID)
		}

		if issue.IssueType != "gate" {
			return HandleErrorRespectJSON("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		if jsonOutput {
			return outputJSON(issue)
		}

		statusSym := "○"
		if issue.Status == types.StatusClosed {
			statusSym = "●"
		}

		fmt.Printf("%s %s - %s\n", statusSym, ui.RenderID(issue.ID), issue.Title)
		fmt.Printf("  Status: %s\n", issue.Status)
		fmt.Printf("  Await Type: %s\n", issue.AwaitType)
		if issue.AwaitID != "" {
			fmt.Printf("  Await ID: %s\n", issue.AwaitID)
		}
		if issue.Timeout > 0 {
			fmt.Printf("  Timeout: %s\n", issue.Timeout)
		}
		if len(issue.Waiters) > 0 {
			fmt.Printf("  Waiters:\n")
			for _, w := range issue.Waiters {
				fmt.Printf("    - %s\n", w)
			}
		}
		if issue.Description != "" {
			fmt.Printf("  Description: %s\n", issue.Description)
		}
		return nil
	},
}

// gateResolveCmd manually closes a gate
var gateResolveCmd = &cobra.Command{
	Use:   "resolve <gate-id>",
	Short: "Manually resolve (close) a gate",
	Long: `Close a gate issue to unblock the step waiting on it.

This is equivalent to 'bd close <gate-id>' but with a more explicit name.
Use --reason to provide context for why the gate was resolved.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("gate resolve is not supported in proxied-server mode")
		}
		CheckReadonly("gate resolve")

		evt := metrics.NewCommandEvent("gate-resolve")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		ctx := rootCtx
		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			return HandleError("gate not found: %s", gateID)
		}

		if issue.IssueType != "gate" {
			return HandleError("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		if err := store.CloseIssue(ctx, gateID, reason, actor, ""); err != nil {
			return HandleError("closing gate: %v", err)
		}

		commandDidWrite.Store(true)

		fmt.Printf("%s Gate resolved: %s\n", ui.RenderPass("✓"), gateID)
		if reason != "" {
			fmt.Printf("  Reason: %s\n", reason)
		}
		return nil
	},
}

// gateCheckCmd evaluates gates and closes those that are resolved
var gateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Evaluate gates and close resolved ones",
	Long: `Evaluate gate conditions and automatically close resolved gates.

By default, checks all open gates. Use --type to filter by gate type.

Gate types:
  gh       - Check all GitHub gates (gh:run and gh:pr)
  gh:run   - Check GitHub Actions workflow runs
  gh:pr    - Check pull request merge status
  timer    - Check timer gates (auto-expire based on timeout)
  bead     - Check cross-rig bead gates
  all      - Check all gate types

GitHub gates use the 'gh' CLI to query status:
  - gh:run checks 'gh run view <id> --json status,conclusion'
  - gh:pr checks 'gh pr view <id> --json state,title'

A gate is resolved when:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - timer: current time > created_at + timeout
  - bead: target bead status=closed

A gate is escalated when:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED

Examples:
  bd gate check              # Check all gates
  bd gate check --type=gh    # Check only GitHub gates
  bd gate check --type=gh:run # Check only workflow run gates
  bd gate check --type=timer # Check only timer gates
  bd gate check --type=bead  # Check only cross-rig bead gates
  bd gate check --dry-run    # Show what would happen without changes
  bd gate check --escalate   # Escalate expired/failed gates`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return runGateCheckProxiedServer(cmd, rootCtx)
		}
		CheckReadonly("gate check")

		evt := metrics.NewCommandEvent("gate-check")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateTypeFilter, _ := cmd.Flags().GetString("type")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		escalateFlag, _ := cmd.Flags().GetBool("escalate")
		limit, _ := cmd.Flags().GetInt("limit")

		gateType := types.IssueType("gate")
		filter := types.IssueFilter{
			IssueType:     &gateType,
			ExcludeStatus: []types.Status{types.StatusClosed},
			Limit:         limit,
		}

		ctx := rootCtx

		gates, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		filteredGates := filterCheckableGates(gates, gateTypeFilter)
		if len(filteredGates) == 0 {
			printNoOpenGates(gateTypeFilter)
			return nil
		}

		var persistAwaitID func(gateID, runID string) error
		if !dryRun {
			persistAwaitID = func(gateID, runID string) error {
				return updateGateAwaitIDFunc(nil, gateID, runID)
			}
		}

		results := evaluateGates(ctx, filteredGates, time.Now(), store, persistAwaitID)

		resolvedCount, escalatedCount, errorCount := applyGateCheckResults(
			results, dryRun, escalateFlag,
			func(gate *types.Issue, reason string) error {
				return closeGate(ctx, gate.ID, reason)
			},
		)

		return printGateCheckSummary(len(results), resolvedCount, escalatedCount, errorCount, dryRun)
	},
}

type gateCheckResult struct {
	gate      *types.Issue
	resolved  bool
	escalated bool
	reason    string
	err       error
}

func filterCheckableGates(gates []*types.Issue, typeFilter string) []*types.Issue {
	var out []*types.Issue
	for _, gate := range gates {
		if shouldCheckGate(gate, typeFilter) {
			out = append(out, gate)
		}
	}
	return out
}

func printNoOpenGates(typeFilter string) {
	if typeFilter != "" {
		fmt.Printf("No open gates of type '%s' found.\n", typeFilter)
	} else {
		fmt.Println("No open gates found.")
	}
}

func evaluateGates(ctx context.Context, gates []*types.Issue, now time.Time, getter issueGetter, persistAwaitID func(gateID, runID string) error) []gateCheckResult {
	results := make([]gateCheckResult, 0, len(gates))
	for _, gate := range gates {
		r := gateCheckResult{gate: gate}
		switch {
		case strings.HasPrefix(gate.AwaitType, "gh:run"):
			r.resolved, r.escalated, r.reason, r.err = checkGHRun(gate, persistAwaitID)
		case strings.HasPrefix(gate.AwaitType, "gh:pr"):
			r.resolved, r.escalated, r.reason, r.err = checkGHPR(gate)
		case gate.AwaitType == "timer":
			r.resolved, r.escalated, r.reason, r.err = checkTimer(gate, now)
		case gate.AwaitType == "bead":
			r.resolved, r.reason = checkBeadGate(ctx, getter, gate.AwaitID)
		default:
			continue
		}
		results = append(results, r)
	}
	return results
}

func applyGateCheckResults(results []gateCheckResult, dryRun, escalate bool, closeResolved func(gate *types.Issue, reason string) error) (resolvedCount, escalatedCount, errorCount int) {
	for _, r := range results {
		if r.err != nil {
			errorCount++
			fmt.Fprintf(os.Stderr, "%s %s: error checking - %v\n",
				ui.RenderFail("✗"), r.gate.ID, r.err)
			continue
		}

		switch {
		case r.resolved:
			resolvedCount++
			if dryRun {
				fmt.Printf("%s %s: would resolve - %s\n",
					ui.RenderPass("✓"), r.gate.ID, r.reason)
				continue
			}
			if closeErr := closeResolved(r.gate, r.reason); closeErr != nil {
				fmt.Fprintf(os.Stderr, "%s %s: error closing - %v\n",
					ui.RenderFail("✗"), r.gate.ID, closeErr)
				errorCount++
			} else {
				fmt.Printf("%s %s: resolved - %s\n",
					ui.RenderPass("✓"), r.gate.ID, r.reason)
			}
		case r.escalated:
			escalatedCount++
			if dryRun {
				fmt.Printf("%s %s: would escalate - %s\n",
					ui.RenderWarn("⚠"), r.gate.ID, r.reason)
				continue
			}
			fmt.Printf("%s %s: ESCALATE - %s\n",
				ui.RenderWarn("⚠"), r.gate.ID, r.reason)
			if escalate {
				escalateGate(r.gate, r.reason)
			}
		default:
			fmt.Printf("%s %s: pending - %s\n",
				ui.RenderAccent("○"), r.gate.ID, r.reason)
		}
	}
	return resolvedCount, escalatedCount, errorCount
}

func printGateCheckSummary(checked, resolvedCount, escalatedCount, errorCount int, dryRun bool) error {
	fmt.Println()
	fmt.Printf("Checked %d gates: %d resolved, %d escalated, %d errors\n",
		checked, resolvedCount, escalatedCount, errorCount)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"checked":   checked,
			"resolved":  resolvedCount,
			"escalated": escalatedCount,
			"errors":    errorCount,
			"dry_run":   dryRun,
		})
	}
	return nil
}

// shouldCheckGate returns true if the gate matches the type filter
func shouldCheckGate(gate *types.Issue, typeFilter string) bool {
	if typeFilter == "" || typeFilter == "all" {
		return true
	}
	if typeFilter == "gh" {
		return strings.HasPrefix(gate.AwaitType, "gh:")
	}
	return gate.AwaitType == typeFilter
}

// ghRunStatus holds the JSON response from 'gh run view'
type ghRunStatus struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

// ghPRStatus holds the JSON response from 'gh pr view'
type ghPRStatus struct {
	State string `json:"state"`
	Title string `json:"title"`
}

var (
	discoverRunIDByWorkflowNameFunc = discoverRunIDByWorkflowName
	updateGateAwaitIDFunc           = updateGateAwaitID
	checkGHRunStatusFunc            = checkGHRunStatus
)

// isNumericID returns true if the string contains only digits (a GitHub run ID)
func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// queryGitHubRunsForWorkflow queries recent runs for a specific workflow using gh CLI.
// Returns runs sorted newest-first (GitHub API default).
func queryGitHubRunsForWorkflow(workflow string, limit int) ([]GHWorkflowRun, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found: install from https://cli.github.com")
	}

	args := []string{
		"run", "list",
		"--workflow", workflow,
		"--json", "databaseId,name,status,conclusion,createdAt,workflowName",
		"--limit", fmt.Sprintf("%d", limit),
	}

	cmd := exec.Command("gh", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh run list --workflow=%s failed: %s", workflow, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh run list: %w", err)
	}

	var runs []GHWorkflowRun
	if err := json.Unmarshal(output, &runs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}

	return runs, nil
}

// discoverRunIDByWorkflowName queries GitHub for the most recent run of a workflow.
// Returns (runID, error). This is ZFC-compliant: "most recent run" is deterministic.
func discoverRunIDByWorkflowName(workflowHint string) (string, error) {
	// Query GitHub directly for this workflow (efficient, avoids limit issues)
	runs, err := queryGitHubRunsForWorkflow(workflowHint, 5)
	if err != nil {
		return "", fmt.Errorf("failed to query workflow runs: %w", err)
	}

	if len(runs) == 0 {
		return "", fmt.Errorf("no runs found for workflow '%s'", workflowHint)
	}

	// Take the most recent run (gh returns newest-first)
	// This is deterministic: "most recent" is a total ordering by creation time
	return fmt.Sprintf("%d", runs[0].DatabaseID), nil
}

func checkGHRun(gate *types.Issue, persistAwaitID func(gateID, runID string) error) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no run ID specified - set await_id or use workflow name hint", nil
	}

	runID := gate.AwaitID

	// If await_id is a workflow name hint (non-numeric), auto-discover the run ID
	if !isNumericID(gate.AwaitID) {
		discoveredID, discoverErr := discoverRunIDByWorkflowNameFunc(gate.AwaitID)
		if discoverErr != nil {
			return false, false, fmt.Sprintf("workflow hint '%s': %v", gate.AwaitID, discoverErr), nil
		}

		if persistAwaitID != nil {
			// Non-dry-run flows persist the numeric run ID for future checks.
			if updateErr := persistAwaitID(gate.ID, discoveredID); updateErr != nil {
				return false, false, "", fmt.Errorf("failed to update gate with discovered run ID: %w", updateErr)
			}
		}

		runID = discoveredID
	}

	return checkGHRunStatusFunc(runID)
}

func checkGHRunStatus(runID string) (resolved, escalated bool, reason string, err error) {
	// Run: gh run view <id> --json status,conclusion,name
	cmd := exec.Command("gh", "run", "view", runID, "--json", "status,conclusion,name") // #nosec G204 -- runID is a validated GitHub run ID
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		// Check if gh CLI is not found
		if strings.Contains(stderr.String(), "command not found") ||
			strings.Contains(runErr.Error(), "executable file not found") {
			return false, false, "", fmt.Errorf("gh CLI not installed")
		}
		// Check if run not found
		if strings.Contains(stderr.String(), "not found") {
			return false, true, "workflow run not found", nil
		}
		return false, false, "", fmt.Errorf("gh run view failed: %s", stderr.String())
	}

	var status ghRunStatus
	if parseErr := json.Unmarshal(stdout.Bytes(), &status); parseErr != nil {
		return false, false, "", fmt.Errorf("failed to parse gh output: %w", parseErr)
	}

	// Evaluate status
	switch status.Status {
	case "completed":
		switch status.Conclusion {
		case "success":
			return true, false, fmt.Sprintf("workflow '%s' succeeded", status.Name), nil
		case "failure":
			return false, true, fmt.Sprintf("workflow '%s' failed", status.Name), nil
		case "cancelled", "canceled":
			return false, true, fmt.Sprintf("workflow '%s' was canceled", status.Name), nil
		case "skipped":
			return true, false, fmt.Sprintf("workflow '%s' was skipped", status.Name), nil
		default:
			return false, true, fmt.Sprintf("workflow '%s' concluded with %s", status.Name, status.Conclusion), nil
		}
	case "in_progress", "queued", "pending", "waiting":
		return false, false, fmt.Sprintf("workflow '%s' is %s", status.Name, status.Status), nil
	default:
		return false, false, fmt.Sprintf("workflow '%s' status: %s", status.Name, status.Status), nil
	}
}

// checkGHPR checks a GitHub pull request gate
func checkGHPR(gate *types.Issue) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no PR number specified", nil
	}

	// Run: gh pr view <id> --json state,title
	cmd := exec.Command("gh", "pr", "view", gate.AwaitID, "--json", "state,title") // #nosec G204 -- gate.AwaitID is a validated GitHub PR number
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		// Check if gh CLI is not found
		if strings.Contains(stderr.String(), "command not found") ||
			strings.Contains(runErr.Error(), "executable file not found") {
			return false, false, "", fmt.Errorf("gh CLI not installed")
		}
		// Check if PR not found
		if strings.Contains(stderr.String(), "not found") || strings.Contains(stderr.String(), "Could not resolve") {
			return false, true, "pull request not found", nil
		}
		return false, false, "", fmt.Errorf("gh pr view failed: %s", stderr.String())
	}

	var status ghPRStatus
	if parseErr := json.Unmarshal(stdout.Bytes(), &status); parseErr != nil {
		return false, false, "", fmt.Errorf("failed to parse gh output: %w", parseErr)
	}

	// Evaluate status
	switch status.State {
	case "MERGED":
		return true, false, fmt.Sprintf("PR '%s' was merged", status.Title), nil
	case "CLOSED":
		return false, true, fmt.Sprintf("PR '%s' was closed without merging", status.Title), nil
	case "OPEN":
		return false, false, fmt.Sprintf("PR '%s' is still open", status.Title), nil
	default:
		return false, false, fmt.Sprintf("PR '%s' state: %s", status.Title, status.State), nil
	}
}

// checkTimer checks a timer gate for expiration
// Note: timers resolve but never escalate (escalated is always false by design)
func checkTimer(gate *types.Issue, now time.Time) (resolved, escalated bool, reason string, err error) { //nolint:unparam // escalated intentionally always false
	if gate.Timeout == 0 {
		return false, false, "timer gate without timeout configured", fmt.Errorf("no timeout set")
	}

	expiresAt := gate.CreatedAt.Add(gate.Timeout)
	if now.After(expiresAt) {
		expired := now.Sub(expiresAt).Round(time.Second)
		return true, false, fmt.Sprintf("timer expired %s ago", expired), nil
	}

	remaining := expiresAt.Sub(now).Round(time.Second)
	return false, false, fmt.Sprintf("expires in %s", remaining), nil
}

// issueGetter is the one storage method checkBeadGate needs, split out so
// tests can fake the lookup without standing up a Dolt store.
type issueGetter interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
}

// checkBeadGate checks if a bead gate is satisfied.
// Returns (satisfied, reason).
//
// A plain await_id (no colon) names a bead in THIS rig's database: the gate
// resolves once that bead closes — the common case, an agent idle-waiting on
// local work (wy-hgms2; the old unconditional cross-rig refusal left every
// local bead gate permanently pending and its waiters asleep).
//
// The historical cross-rig form <rig>:<bead-id> cannot be evaluated since
// multi-rig routing was removed; it stays pending with a descriptive message.
func checkBeadGate(ctx context.Context, st issueGetter, awaitID string) (bool, string) {
	if awaitID == "" {
		return false, "bead gate has no await_id"
	}
	if strings.Contains(awaitID, ":") {
		return false, fmt.Sprintf("cross-rig bead gate %q cannot be checked (multi-rig routing removed)", awaitID)
	}
	if st == nil {
		return false, fmt.Sprintf("bead gate %q: no local store available", awaitID)
	}
	issue, err := st.GetIssue(ctx, awaitID)
	if err != nil {
		return false, fmt.Sprintf("bead gate %q: %v", awaitID, err)
	}
	if issue == nil {
		return false, fmt.Sprintf("bead gate %q: bead not found", awaitID)
	}
	if issue.Status == types.StatusClosed {
		return true, fmt.Sprintf("bead %s closed", awaitID)
	}
	return false, fmt.Sprintf("bead %s is %s", awaitID, issue.Status)
}

// closeGate closes a gate issue with the given reason
func closeGate(_ interface{}, gateID, reason string) error {
	if err := store.CloseIssue(rootCtx, gateID, reason, actor, ""); err != nil {
		return err
	}
	commandDidWrite.Store(true)
	return nil
}

// escalateGate sends an escalation for a failed/expired gate
func escalateGate(gate *types.Issue, reason string) {
	topic := fmt.Sprintf("Gate escalation: %s", gate.ID)
	message := fmt.Sprintf("Gate %s needs attention.\nType: %s\nReason: %s\nCreated: %s",
		gate.ID,
		gate.AwaitType,
		reason,
		gate.CreatedAt.Format(time.RFC3339))

	// Call gt escalate if available
	escalateCmd := exec.Command("gt", "escalate", topic, "-s", "HIGH", "-m", message)
	escalateCmd.Stdout = os.Stdout
	escalateCmd.Stderr = os.Stderr
	if err := escalateCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: escalation failed for %s: %v\n", gate.ID, err)
	}
}

func init() {
	// gate list flags
	gateListCmd.Flags().BoolP("all", "a", false, "Show all gates including closed")
	gateListCmd.Flags().IntP("limit", "n", 50, "Limit results (default 50)")

	// gate resolve flags
	gateResolveCmd.Flags().StringP("reason", "r", "", "Reason for resolving the gate")

	// gate check flags
	gateCheckCmd.Flags().StringP("type", "t", "", "Gate type to check (gh, gh:run, gh:pr, timer, bead, all)")
	gateCheckCmd.Flags().Bool("dry-run", false, "Show what would happen without making changes")
	gateCheckCmd.Flags().BoolP("escalate", "e", false, "Escalate failed/expired gates")
	gateCheckCmd.Flags().IntP("limit", "l", 100, "Limit results (default 100)")

	// gate create flags
	gateCreateCmd.Flags().String("blocks", "", "Issue ID to block (required)")
	gateCreateCmd.Flags().StringP("type", "t", "human", "Gate type (human, timer, gh:run, gh:pr)")
	gateCreateCmd.Flags().StringP("reason", "r", "", "Reason for the gate")
	gateCreateCmd.Flags().String("await-id", "", "Condition identifier (run ID, PR number, etc.)")
	gateCreateCmd.Flags().String("timeout", "", "Timeout duration (e.g., 2h, 30m)")
	_ = gateCreateCmd.MarkFlagRequired("blocks")

	// Issue ID completions
	gateShowCmd.ValidArgsFunction = issueIDCompletion
	gateResolveCmd.ValidArgsFunction = issueIDCompletion
	gateAddWaiterCmd.ValidArgsFunction = issueIDCompletion
	gateCreateCmd.ValidArgsFunction = issueIDCompletion

	// Add subcommands
	gateCmd.AddCommand(gateListCmd)
	gateCmd.AddCommand(gateCreateCmd)
	gateCmd.AddCommand(gateShowCmd)
	gateCmd.AddCommand(gateResolveCmd)
	gateCmd.AddCommand(gateCheckCmd)
	gateCmd.AddCommand(gateAddWaiterCmd)

	rootCmd.AddCommand(gateCmd)
}
