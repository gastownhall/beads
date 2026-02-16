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
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage/dolt"
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
  bead    - Waits for cross-rig bead to close (Phase 4)

For bead gates, await_id format is <rig>:<bead-id> (e.g., "gastown:gt-abc123").

Examples:
  bd gate list           # Show all open gates
  bd gate list --all     # Show all gates including closed
  bd gate check          # Evaluate all open gates
  bd gate check --type=bead  # Evaluate only bead gates
  bd gate resolve <id>   # Close a gate manually`,
}

// gateListCmd lists gate issues
var gateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List gate issues",
	Long: `List all gate issues in the current beads database.

By default, shows only open gates. Use --all to include closed gates.`,
	Run: func(cmd *cobra.Command, args []string) {
		allFlag, _ := cmd.Flags().GetBool("all")
		limit, _ := cmd.Flags().GetInt("limit")

		// Build filter for gate type issues
		gateType := types.IssueType("gate")
		filter := types.IssueFilter{
			IssueType: &gateType,
			Limit:     limit,
		}

		// By default, exclude closed gates
		if !allFlag {
			filter.ExcludeStatus = []types.Status{types.StatusClosed}
		}

		ctx := rootCtx


		// Direct mode
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(issues)
			return
		}

		displayGates(issues, allFlag)
	},
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

When the gate closes, the waiter will receive a wake notification via 'gt gate wake'.
The waiter is typically the polecat's address (e.g., "gastown/polecats/Toast").

This is used by 'gt done --phase-complete' to register for gate wake notifications.`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("gate add-waiter")

		gateID := args[0]
		waiter := args[1]
		ctx := rootCtx

		// Get the gate issue
		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: gate not found: %s\n", gateID)
			os.Exit(1)
		}

		if issue.IssueType != "gate" {
			fmt.Fprintf(os.Stderr, "Error: %s is not a gate issue (type=%s)\n", gateID, issue.IssueType)
			os.Exit(1)
		}

		// Check if waiter is already registered
		for _, w := range issue.Waiters {
			if w == waiter {
				fmt.Printf("Waiter already registered on gate %s\n", gateID)
				return
			}
		}

		// Add waiter to the waiters list
		newWaiters := append(issue.Waiters, waiter)

		// Update the gate
		updates := map[string]interface{}{
			"waiters": newWaiters,
		}
		if err := store.UpdateIssue(ctx, gateID, updates, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating gate: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%s Added waiter to gate %s: %s\n", ui.RenderPass("✓"), gateID, waiter)
	},
}

// gateShowCmd shows a gate issue
var gateShowCmd = &cobra.Command{
	Use:   "show <gate-id>",
	Short: "Show a gate issue",
	Long: `Display details of a gate issue including its waiters.

This is similar to 'bd show' but validates that the issue is a gate.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		gateID := args[0]
		ctx := rootCtx

		// Get the gate issue
		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: gate not found: %s\n", gateID)
			os.Exit(1)
		}

		if issue.IssueType != "gate" {
			fmt.Fprintf(os.Stderr, "Error: %s is not a gate issue (type=%s)\n", gateID, issue.IssueType)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(issue)
			return
		}

		// Display gate details
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
	},
}

// gateResolveCmd manually closes a gate
var gateResolveCmd = &cobra.Command{
	Use:   "resolve <gate-id>",
	Short: "Manually resolve (close) a gate",
	Long: `Close a gate issue to unblock the step waiting on it.

This is equivalent to 'bd close <gate-id>' but with a more explicit name.
Use --reason to provide context for why the gate was resolved.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("gate resolve")

		gateID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		// Verify it's a gate issue
		ctx := rootCtx
		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: gate not found: %s\n", gateID)
			os.Exit(1)
		}

		if issue.IssueType != "gate" {
			fmt.Fprintf(os.Stderr, "Error: %s is not a gate issue (type=%s)\n", gateID, issue.IssueType)
			os.Exit(1)
		}

		// Close the gate
		if err := store.CloseIssue(ctx, gateID, reason, actor, ""); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing gate: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%s Gate resolved: %s\n", ui.RenderPass("✓"), gateID)
		if reason != "" {
			fmt.Printf("  Reason: %s\n", reason)
		}
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
  - gh:pr checks 'gh pr view <id> --json state,merged'

A gate is resolved when:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - timer: current time > created_at + timeout
  - bead: target bead status=closed

A gate is escalated when:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED AND merged=false

Examples:
  bd gate check              # Check all gates
  bd gate check --type=gh    # Check only GitHub gates
  bd gate check --type=gh:run # Check only workflow run gates
  bd gate check --type=timer # Check only timer gates
  bd gate check --type=bead  # Check only cross-rig bead gates
  bd gate check --dry-run    # Show what would happen without changes
  bd gate check --escalate   # Escalate expired/failed gates`,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("gate check")

		gateTypeFilter, _ := cmd.Flags().GetString("type")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		escalateFlag, _ := cmd.Flags().GetBool("escalate")
		limit, _ := cmd.Flags().GetInt("limit")

		// Get open gates
		gateType := types.IssueType("gate")
		filter := types.IssueFilter{
			IssueType:     &gateType,
			ExcludeStatus: []types.Status{types.StatusClosed},
			Limit:         limit,
		}

		ctx := rootCtx
		var gates []*types.Issue
		var err error

		gates, err = store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Filter by type if specified
		var filteredGates []*types.Issue
		for _, gate := range gates {
			if shouldCheckGate(gate, gateTypeFilter) {
				filteredGates = append(filteredGates, gate)
			}
		}

		if len(filteredGates) == 0 {
			if gateTypeFilter != "" {
				fmt.Printf("No open gates of type '%s' found.\n", gateTypeFilter)
			} else {
				fmt.Println("No open gates found.")
			}
			return
		}

		// Results tracking
		type checkResult struct {
			gate      *types.Issue
			resolved  bool
			escalated bool
			reason    string
			err       error
		}
		results := make([]checkResult, 0, len(filteredGates))

		// Check each gate
		now := time.Now()
		for _, gate := range filteredGates {
			result := checkResult{gate: gate}

			switch {
			case strings.HasPrefix(gate.AwaitType, "gh:run"):
				result.resolved, result.escalated, result.reason, result.err = checkGHRun(gate)
			case strings.HasPrefix(gate.AwaitType, "gh:pr"):
				result.resolved, result.escalated, result.reason, result.err = checkGHPR(gate)
			case gate.AwaitType == "timer":
				result.resolved, result.escalated, result.reason, result.err = checkTimer(gate, now)
			case gate.AwaitType == "bead":
				result.resolved, result.reason = checkBeadGate(ctx, gate.AwaitID)
			default:
				// Skip unsupported gate types (human gates need manual resolution)
				continue
			}

			results = append(results, result)
		}

		// Process results
		resolvedCount := 0
		escalatedCount := 0
		errorCount := 0

		for _, r := range results {
			if r.err != nil {
				errorCount++
				fmt.Fprintf(os.Stderr, "%s %s: error checking - %v\n",
					ui.RenderFail("✗"), r.gate.ID, r.err)
				continue
			}

			if r.resolved {
				resolvedCount++
				if dryRun {
					fmt.Printf("%s %s: would resolve - %s\n",
						ui.RenderPass("✓"), r.gate.ID, r.reason)
				} else {
					// Close the gate
					closeErr := closeGate(ctx, r.gate.ID, r.reason)
					if closeErr != nil {
						fmt.Fprintf(os.Stderr, "%s %s: error closing - %v\n",
							ui.RenderFail("✗"), r.gate.ID, closeErr)
						errorCount++
					} else {
						fmt.Printf("%s %s: resolved - %s\n",
							ui.RenderPass("✓"), r.gate.ID, r.reason)
					}
				}
			} else if r.escalated {
				escalatedCount++
				if dryRun {
					fmt.Printf("%s %s: would escalate - %s\n",
						ui.RenderWarn("⚠"), r.gate.ID, r.reason)
				} else {
					fmt.Printf("%s %s: ESCALATE - %s\n",
						ui.RenderWarn("⚠"), r.gate.ID, r.reason)
					// Actually escalate if flag is set
					if escalateFlag {
						escalateGate(r.gate, r.reason)
					}
				}
			} else {
				// Still pending
				fmt.Printf("%s %s: pending - %s\n",
					ui.RenderAccent("○"), r.gate.ID, r.reason)
			}
		}

		// Summary
		fmt.Println()
		fmt.Printf("Checked %d gates: %d resolved, %d escalated, %d errors\n",
			len(results), resolvedCount, escalatedCount, errorCount)

		if jsonOutput {
			summary := map[string]interface{}{
				"checked":   len(results),
				"resolved":  resolvedCount,
				"escalated": escalatedCount,
				"errors":    errorCount,
				"dry_run":   dryRun,
			}
			outputJSON(summary)
		}
	},
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
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Title  string `json:"title"`
}

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

// checkGHRun checks a GitHub Actions workflow run gate
func checkGHRun(gate *types.Issue) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no run ID specified - set await_id or use workflow name hint", nil
	}

	runID := gate.AwaitID

	// If await_id is a workflow name hint (non-numeric), auto-discover the run ID
	if !isNumericID(gate.AwaitID) {
		discoveredID, discoverErr := discoverRunIDByWorkflowName(gate.AwaitID)
		if discoverErr != nil {
			return false, false, fmt.Sprintf("workflow hint '%s': %v", gate.AwaitID, discoverErr), nil
		}

		// Update the gate with the discovered run ID
		if updateErr := updateGateAwaitID(nil, gate.ID, discoveredID); updateErr != nil {
			return false, false, "", fmt.Errorf("failed to update gate with discovered run ID: %w", updateErr)
		}

		runID = discoveredID
	}

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

	// Run: gh pr view <id> --json state,merged,title
	cmd := exec.Command("gh", "pr", "view", gate.AwaitID, "--json", "state,merged,title") // #nosec G204 -- gate.AwaitID is a validated GitHub PR number
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
		if status.Merged {
			return true, false, fmt.Sprintf("PR '%s' was merged", status.Title), nil
		}
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

// checkBeadGate checks if a cross-rig bead gate is satisfied.
// await_id format: <rig>:<bead-id> (e.g., "gastown:gt-abc123")
// Returns (satisfied, reason).
func checkBeadGate(ctx context.Context, awaitID string) (bool, string) {
	// Parse await_id format: <rig>:<bead-id>
	parts := strings.SplitN(awaitID, ":", 2)
	if len(parts) != 2 {
		return false, fmt.Sprintf("invalid await_id format: expected <rig>:<bead-id>, got %q", awaitID)
	}

	rigName := parts[0]
	beadID := parts[1]

	if rigName == "" || beadID == "" {
		return false, "await_id missing rig name or bead ID"
	}

	// Resolve the target rig's beads directory
	currentBeadsDir := beads.FindBeadsDir()
	if currentBeadsDir == "" {
		return false, "could not find current beads directory"
	}
	targetBeadsDir, _, err := routing.ResolveBeadsDirForRig(rigName, currentBeadsDir)
	if err != nil {
		return false, fmt.Sprintf("rig %q not found: %v", rigName, err)
	}

	// Open the target database (read-only) using storage factory
	// This supports both Dolt and legacy SQLite backends in the target rig.
	targetStore, err := dolt.NewFromConfigWithOptions(ctx, targetBeadsDir, &dolt.Config{ReadOnly: true})
	if err != nil {
		return false, fmt.Sprintf("failed to open database for rig %q: %v", rigName, err)
	}
	defer func() { _ = targetStore.Close() }()

	// Check if the target bead exists and is closed
	issue, err := targetStore.GetIssue(ctx, beadID)
	if err != nil {
		return false, fmt.Sprintf("bead %s not found in rig %s: %v", beadID, rigName, err)
	}

	if issue.Status == types.StatusClosed {
		return true, fmt.Sprintf("target bead %s is closed", beadID)
	}

	return false, fmt.Sprintf("target bead %s status is %q (waiting for closed)", beadID, string(issue.Status))
}

// closeGate closes a gate issue with the given reason
func closeGate(_ interface{}, gateID, reason string) error {
	if err := store.CloseIssue(rootCtx, gateID, reason, actor, ""); err != nil {
		return err
	}
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

	// Issue ID completions
	gateShowCmd.ValidArgsFunction = issueIDCompletion
	gateResolveCmd.ValidArgsFunction = issueIDCompletion
	gateAddWaiterCmd.ValidArgsFunction = issueIDCompletion

	// Add subcommands
	gateCmd.AddCommand(gateListCmd)
	gateCmd.AddCommand(gateShowCmd)
	gateCmd.AddCommand(gateResolveCmd)
	gateCmd.AddCommand(gateCheckCmd)
	gateCmd.AddCommand(gateAddWaiterCmd)

	rootCmd.AddCommand(gateCmd)
}
