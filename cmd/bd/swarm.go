// Package main implements the bd CLI swarm management commands.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var swarmCmd = &cobra.Command{
	Use:     "swarm",
	GroupID: "deps",
	Short:   "Swarm management for structured epics",
	Long: `Swarm management commands for coordinating parallel work on epics.

A swarm is a structured body of work defined by an epic and its children,
with dependencies forming a DAG (directed acyclic graph) of work.`,
}

// SwarmAnalysis holds the results of analyzing an epic's structure for swarming.
type SwarmAnalysis struct {
	EpicID          string                  `json:"epic_id"`
	EpicTitle       string                  `json:"epic_title"`
	TotalIssues     int                     `json:"total_issues"`
	ClosedIssues    int                     `json:"closed_issues"`
	ReadyFronts     []ReadyFront            `json:"ready_fronts"`
	MaxParallelism  int                     `json:"max_parallelism"`
	EstimatedSessions int                   `json:"estimated_sessions"`
	Warnings        []string                `json:"warnings"`
	Errors          []string                `json:"errors"`
	Swarmable       bool                    `json:"swarmable"`
	Issues          map[string]*IssueNode   `json:"issues,omitempty"` // Only included with --verbose
}

// ReadyFront represents a group of issues that can be worked on in parallel.
type ReadyFront struct {
	Wave    int      `json:"wave"`
	Issues  []string `json:"issues"`
	Titles  []string `json:"titles,omitempty"` // Only for human output
}

// IssueNode represents an issue in the dependency graph.
type IssueNode struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Priority     int      `json:"priority"`
	DependsOn    []string `json:"depends_on"`     // What this issue depends on
	DependedOnBy []string `json:"depended_on_by"` // What depends on this issue
	Wave         int      `json:"wave"`           // Which ready front this belongs to (-1 if blocked by cycle)
}

// SwarmStorage defines the storage interface needed by swarm commands.
type SwarmStorage interface {
	GetIssue(context.Context, string) (*types.Issue, error)
	GetDependents(context.Context, string) ([]*types.Issue, error)
	GetDependencyRecords(context.Context, string) ([]*types.Dependency, error)
}

// findExistingSwarm returns the swarm molecule for an epic, if one exists.
// Returns nil if no swarm molecule is linked to the epic.
func findExistingSwarm(ctx context.Context, s SwarmStorage, epicID string) (*types.Issue, error) {
	// Get all issues that depend on the epic
	dependents, err := s.GetDependents(ctx, epicID)
	if err != nil {
		return nil, fmt.Errorf("failed to get epic dependents: %w", err)
	}

	// Find a swarm molecule with relates-to dependency to this epic
	for _, dep := range dependents {
		// Only consider molecules (GetDependents doesn't populate mol_type, so we fetch full issue)
		if dep.IssueType != types.TypeMolecule {
			continue
		}

		// Get full issue to check mol_type
		fullIssue, err := s.GetIssue(ctx, dep.ID)
		if err != nil || fullIssue == nil {
			continue
		}
		if fullIssue.MolType != types.MolTypeSwarm {
			continue
		}

		// Verify it's linked via relates-to
		deps, err := s.GetDependencyRecords(ctx, dep.ID)
		if err != nil {
			continue
		}
		for _, d := range deps {
			if d.DependsOnID == epicID && d.Type == types.DepRelatesTo {
				return fullIssue, nil
			}
		}
	}

	return nil, nil
}

// getEpicChildren returns all child issues of an epic (via parent-child dependencies).
func getEpicChildren(ctx context.Context, s SwarmStorage, epicID string) ([]*types.Issue, error) {
	// Get all issues that depend on the epic
	allDependents, err := s.GetDependents(ctx, epicID)
	if err != nil {
		return nil, fmt.Errorf("failed to get epic dependents: %w", err)
	}

	// Filter to only parent-child relationships by checking each dependent's dependency records
	var children []*types.Issue
	for _, dependent := range allDependents {
		deps, err := s.GetDependencyRecords(ctx, dependent.ID)
		if err != nil {
			continue // Skip issues we can't query
		}
		for _, dep := range deps {
			if dep.DependsOnID == epicID && dep.Type == types.DepParentChild {
				children = append(children, dependent)
				break
			}
		}
	}

	return children, nil
}

var swarmValidateCmd = &cobra.Command{
	Use:   "validate [epic-id]",
	Short: "Validate epic structure for swarming",
	Long: `Validate an epic's structure to ensure it's ready for swarm execution.

Checks for:
- Correct dependency direction (requirement-based, not temporal)
- Orphaned issues (roots with no dependents)
- Missing dependencies (leaves that should depend on something)
- Cycles (impossible to resolve)
- Disconnected subgraphs

Reports:
- Ready fronts (waves of parallel work)
- Estimated worker-sessions
- Maximum parallelism
- Warnings for potential issues

Examples:
  bd swarm validate gt-epic-123           # Validate epic structure
  bd swarm validate gt-epic-123 --verbose # Include detailed issue graph`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		verbose, _ := cmd.Flags().GetBool("verbose")

		// Swarm commands require direct store access
		if store == nil {
			if daemonClient != nil {
				var err error
				store, err = sqlite.New(ctx, dbPath)
				if err != nil {
					FatalErrorRespectJSON("failed to open database: %v", err)
				}
				defer func() { _ = store.Close() }()
			} else {
				FatalErrorRespectJSON("no database connection")
			}
		}

		// Resolve epic ID
		epicID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalErrorRespectJSON("epic '%s' not found: %v", args[0], err)
		}

		// Get the epic
		epic, err := store.GetIssue(ctx, epicID)
		if err != nil {
			FatalErrorRespectJSON("failed to get epic: %v", err)
		}
		if epic == nil {
			FatalErrorRespectJSON("epic '%s' not found", epicID)
		}

		// Verify it's an epic
		if epic.IssueType != types.TypeEpic && epic.IssueType != types.TypeMolecule {
			FatalErrorRespectJSON("'%s' is not an epic or molecule (type: %s)", epicID, epic.IssueType)
		}

		// Analyze the epic structure
		analysis, err := analyzeEpicForSwarm(ctx, store, epic)
		if err != nil {
			FatalErrorRespectJSON("failed to analyze epic: %v", err)
		}

		// Include detailed graph only in verbose mode
		if !verbose {
			analysis.Issues = nil
		}

		if jsonOutput {
			outputJSON(analysis)
			if !analysis.Swarmable {
				os.Exit(1)
			}
			return
		}

		// Human-readable output
		renderSwarmAnalysis(analysis)

		if !analysis.Swarmable {
			os.Exit(1)
		}
	},
}

// analyzeEpicForSwarm performs structural analysis of an epic for swarm execution.
func analyzeEpicForSwarm(ctx context.Context, s SwarmStorage, epic *types.Issue) (*SwarmAnalysis, error) {
	analysis := &SwarmAnalysis{
		EpicID:    epic.ID,
		EpicTitle: epic.Title,
		Swarmable: true,
		Issues:    make(map[string]*IssueNode),
	}

	// Get all child issues of the epic
	childIssues, err := getEpicChildren(ctx, s, epic.ID)
	if err != nil {
		return nil, err
	}

	if len(childIssues) == 0 {
		analysis.Warnings = append(analysis.Warnings, "Epic has no children")
		return analysis, nil
	}

	analysis.TotalIssues = len(childIssues)

	// Build the issue graph
	for _, issue := range childIssues {
		node := &IssueNode{
			ID:           issue.ID,
			Title:        issue.Title,
			Status:       string(issue.Status),
			Priority:     issue.Priority,
			DependsOn:    []string{},
			DependedOnBy: []string{},
			Wave:         -1, // Will be set later
		}
		analysis.Issues[issue.ID] = node

		if issue.Status == types.StatusClosed {
			analysis.ClosedIssues++
		}
	}

	// Build dependency relationships (only within the epic's children)
	childIDSet := make(map[string]bool)
	for _, issue := range childIssues {
		childIDSet[issue.ID] = true
	}

	for _, issue := range childIssues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}

		node := analysis.Issues[issue.ID]
		for _, dep := range deps {
			// Only consider dependencies within the epic (not parent-child to epic itself)
			if dep.DependsOnID == epic.ID && dep.Type == types.DepParentChild {
				continue // Skip the parent relationship to the epic
			}
			// Only track blocking dependencies
			if !dep.Type.AffectsReadyWork() {
				continue
			}
			// Only track dependencies within the epic's children
			if childIDSet[dep.DependsOnID] {
				node.DependsOn = append(node.DependsOn, dep.DependsOnID)
				if targetNode, ok := analysis.Issues[dep.DependsOnID]; ok {
					targetNode.DependedOnBy = append(targetNode.DependedOnBy, issue.ID)
				}
			}
			// External dependencies to issues outside the epic
			if !childIDSet[dep.DependsOnID] && dep.DependsOnID != epic.ID {
				// Check if it's an external ref
				if strings.HasPrefix(dep.DependsOnID, "external:") {
					analysis.Warnings = append(analysis.Warnings,
						fmt.Sprintf("%s has external dependency: %s", issue.ID, dep.DependsOnID))
				} else {
					analysis.Warnings = append(analysis.Warnings,
						fmt.Sprintf("%s depends on %s (outside epic)", issue.ID, dep.DependsOnID))
				}
			}
		}
	}

	// Detect structural issues
	detectStructuralIssues(analysis, childIssues)

	// Compute ready fronts (waves of parallel work)
	computeReadyFronts(analysis)

	// Set swarmable based on errors
	analysis.Swarmable = len(analysis.Errors) == 0

	return analysis, nil
}

// detectStructuralIssues looks for common problems in the dependency graph.
//
//nolint:unparam // issues reserved for future use
func detectStructuralIssues(analysis *SwarmAnalysis, _ []*types.Issue) {
	// 1. Find roots (issues with no dependencies within the epic)
	//    These are the starting points. Having multiple roots is normal.
	var roots []string
	for id, node := range analysis.Issues {
		if len(node.DependsOn) == 0 {
			roots = append(roots, id)
		}
	}

	// 2. Find leaves (issues that nothing depends on within the epic)
	//    Multiple leaves might indicate missing dependencies or just multiple end points.
	var leaves []string
	for id, node := range analysis.Issues {
		if len(node.DependedOnBy) == 0 {
			leaves = append(leaves, id)
		}
	}

	// 3. Detect potential dependency inversions
	//    Heuristic: If a "foundation" or "setup" issue has no dependents, it might be inverted.
	//    Heuristic: If an "integration" or "final" issue depends on nothing, it might be inverted.
	for id, node := range analysis.Issues {
		lowerTitle := strings.ToLower(node.Title)

		// Foundation-like issues should have dependents
		if len(node.DependedOnBy) == 0 {
			if strings.Contains(lowerTitle, "foundation") ||
				strings.Contains(lowerTitle, "setup") ||
				strings.Contains(lowerTitle, "base") ||
				strings.Contains(lowerTitle, "core") {
				analysis.Warnings = append(analysis.Warnings,
					fmt.Sprintf("%s (%s) has no dependents - should other issues depend on it?",
						id, node.Title))
			}
		}

		// Integration-like issues should have dependencies
		if len(node.DependsOn) == 0 {
			if strings.Contains(lowerTitle, "integration") ||
				strings.Contains(lowerTitle, "final") ||
				strings.Contains(lowerTitle, "test") {
				analysis.Warnings = append(analysis.Warnings,
					fmt.Sprintf("%s (%s) has no dependencies - should it depend on implementation?",
						id, node.Title))
			}
		}
	}

	// 4. Check for disconnected subgraphs
	// Start from roots and see if we can reach all nodes
	visited := make(map[string]bool)
	var dfs func(id string)
	dfs = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		if node, ok := analysis.Issues[id]; ok {
			for _, depID := range node.DependedOnBy {
				dfs(depID)
			}
		}
	}

	// Visit from all roots
	for _, root := range roots {
		dfs(root)
	}

	// Check for unvisited nodes (disconnected from roots)
	var disconnected []string
	for id := range analysis.Issues {
		if !visited[id] {
			disconnected = append(disconnected, id)
		}
	}

	if len(disconnected) > 0 {
		analysis.Warnings = append(analysis.Warnings,
			fmt.Sprintf("Disconnected issues (not reachable from roots): %v", disconnected))
	}

	// 5. Detect cycles using simple DFS
	// (The main DetectCycles in storage is more sophisticated, but we do a simple check here)
	inProgress := make(map[string]bool)
	completed := make(map[string]bool)
	var cyclePath []string
	hasCycle := false

	var detectCycle func(id string) bool
	detectCycle = func(id string) bool {
		if completed[id] {
			return false
		}
		if inProgress[id] {
			hasCycle = true
			return true
		}
		inProgress[id] = true
		cyclePath = append(cyclePath, id)

		if node, ok := analysis.Issues[id]; ok {
			for _, depID := range node.DependsOn {
				if detectCycle(depID) {
					return true
				}
			}
		}

		cyclePath = cyclePath[:len(cyclePath)-1]
		inProgress[id] = false
		completed[id] = true
		return false
	}

	for id := range analysis.Issues {
		if !completed[id] {
			if detectCycle(id) {
				break
			}
		}
	}

	if hasCycle {
		analysis.Errors = append(analysis.Errors,
			fmt.Sprintf("Dependency cycle detected involving: %v", cyclePath))
	}
}

// computeReadyFronts calculates the waves of parallel work.
func computeReadyFronts(analysis *SwarmAnalysis) {
	if len(analysis.Errors) > 0 {
		// Can't compute ready fronts if there are cycles
		return
	}

	// Use Kahn's algorithm for topological sort with level tracking
	inDegree := make(map[string]int)
	for id, node := range analysis.Issues {
		inDegree[id] = len(node.DependsOn)
	}

	// Start with all nodes that have no dependencies (wave 0)
	var currentWave []string
	for id, degree := range inDegree {
		if degree == 0 {
			currentWave = append(currentWave, id)
			analysis.Issues[id].Wave = 0
		}
	}

	wave := 0
	for len(currentWave) > 0 {
		// Sort for deterministic output
		sort.Strings(currentWave)

		// Build titles for this wave
		var titles []string
		for _, id := range currentWave {
			if node, ok := analysis.Issues[id]; ok {
				titles = append(titles, node.Title)
			}
		}

		front := ReadyFront{
			Wave:   wave,
			Issues: currentWave,
			Titles: titles,
		}
		analysis.ReadyFronts = append(analysis.ReadyFronts, front)

		// Track max parallelism
		if len(currentWave) > analysis.MaxParallelism {
			analysis.MaxParallelism = len(currentWave)
		}

		// Find next wave
		var nextWave []string
		for _, id := range currentWave {
			if node, ok := analysis.Issues[id]; ok {
				for _, dependentID := range node.DependedOnBy {
					inDegree[dependentID]--
					if inDegree[dependentID] == 0 {
						nextWave = append(nextWave, dependentID)
						analysis.Issues[dependentID].Wave = wave + 1
					}
				}
			}
		}

		currentWave = nextWave
		wave++
	}

	// Estimated sessions = total issues (each issue is roughly one session)
	analysis.EstimatedSessions = analysis.TotalIssues
}

// renderSwarmAnalysis outputs human-readable analysis.
func renderSwarmAnalysis(analysis *SwarmAnalysis) {
	fmt.Printf("\n%s Swarm Analysis: %s\n", ui.RenderAccent("🐝"), analysis.EpicTitle)
	fmt.Printf("   Epic ID: %s\n", analysis.EpicID)
	fmt.Printf("   Total issues: %d (%d closed)\n", analysis.TotalIssues, analysis.ClosedIssues)

	if analysis.TotalIssues == 0 {
		fmt.Printf("\n%s Epic has no children to swarm\n\n", ui.RenderWarn("⚠"))
		return
	}

	// Ready fronts
	if len(analysis.ReadyFronts) > 0 {
		fmt.Printf("\n%s Ready Fronts (waves of parallel work):\n", ui.RenderPass("📊"))
		for _, front := range analysis.ReadyFronts {
			fmt.Printf("   Wave %d: %d issues\n", front.Wave+1, len(front.Issues))
			for i, id := range front.Issues {
				title := ""
				if i < len(front.Titles) {
					title = front.Titles[i]
				}
				fmt.Printf("      • %s: %s\n", ui.RenderID(id), title)
			}
		}
	}

	// Summary stats
	fmt.Printf("\n%s Summary:\n", ui.RenderAccent("📈"))
	fmt.Printf("   Estimated worker-sessions: %d\n", analysis.EstimatedSessions)
	fmt.Printf("   Max parallelism: %d\n", analysis.MaxParallelism)
	fmt.Printf("   Total waves: %d\n", len(analysis.ReadyFronts))

	// Warnings
	if len(analysis.Warnings) > 0 {
		fmt.Printf("\n%s Warnings:\n", ui.RenderWarn("⚠"))
		for _, warning := range analysis.Warnings {
			fmt.Printf("   • %s\n", warning)
		}
	}

	// Errors
	if len(analysis.Errors) > 0 {
		fmt.Printf("\n%s Errors:\n", ui.RenderFail("❌"))
		for _, err := range analysis.Errors {
			fmt.Printf("   • %s\n", err)
		}
	}

	// Final verdict
	fmt.Println()
	if analysis.Swarmable {
		fmt.Printf("%s Swarmable: YES\n\n", ui.RenderPass("✓"))
	} else {
		fmt.Printf("%s Swarmable: NO (fix errors first)\n\n", ui.RenderFail("✗"))
	}
}

// SwarmStatus holds the current status of a swarm (computed from beads).
type SwarmStatus struct {
	EpicID       string        `json:"epic_id"`
	EpicTitle    string        `json:"epic_title"`
	TotalIssues  int           `json:"total_issues"`
	Completed    []StatusIssue `json:"completed"`
	Active       []StatusIssue `json:"active"`
	Ready        []StatusIssue `json:"ready"`
	Blocked      []StatusIssue `json:"blocked"`
	Progress     float64       `json:"progress_percent"`
	ActiveCount  int           `json:"active_count"`
	ReadyCount   int           `json:"ready_count"`
	BlockedCount int           `json:"blocked_count"`
}

// StatusIssue represents an issue in swarm status output.
type StatusIssue struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Assignee  string   `json:"assignee,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
	ClosedAt  string   `json:"closed_at,omitempty"`
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status [epic-or-swarm-id]",
	Short: "Show current swarm status",
	Long: `Show the current status of a swarm, computed from beads.

Accepts either:
- An epic ID (shows status for that epic's children)
- A swarm molecule ID (follows the link to find the epic)

Displays issues grouped by state:
- Completed: Closed issues
- Active: Issues currently in_progress (with assignee)
- Ready: Open issues with all dependencies satisfied
- Blocked: Open issues waiting on dependencies

The status is COMPUTED from beads, not stored separately.
If beads changes, status changes.

Examples:
  bd swarm status gt-epic-123       # Show swarm status by epic
  bd swarm status gt-swarm-456      # Show status via swarm molecule
  bd swarm status gt-epic-123 --json  # Machine-readable output`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// Swarm commands require direct store access
		if store == nil {
			if daemonClient != nil {
				var err error
				store, err = sqlite.New(ctx, dbPath)
				if err != nil {
					FatalErrorRespectJSON("failed to open database: %v", err)
				}
				defer func() { _ = store.Close() }()
			} else {
				FatalErrorRespectJSON("no database connection")
			}
		}

		// Resolve ID
		issueID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalErrorRespectJSON("issue '%s' not found: %v", args[0], err)
		}

		// Get the issue
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			FatalErrorRespectJSON("failed to get issue: %v", err)
		}
		if issue == nil {
			FatalErrorRespectJSON("issue '%s' not found", issueID)
		}

		var epic *types.Issue

		// Check if it's a swarm molecule - if so, follow the link to the epic
		if issue.IssueType == types.TypeMolecule && issue.MolType == types.MolTypeSwarm {
			// Find linked epic via relates-to dependency
			deps, err := store.GetDependencyRecords(ctx, issue.ID)
			if err != nil {
				FatalErrorRespectJSON("failed to get swarm dependencies: %v", err)
			}
			for _, dep := range deps {
				if dep.Type == types.DepRelatesTo {
					epic, err = store.GetIssue(ctx, dep.DependsOnID)
					if err != nil {
						FatalErrorRespectJSON("failed to get linked epic: %v", err)
					}
					break
				}
			}
			if epic == nil {
				FatalErrorRespectJSON("swarm molecule '%s' has no linked epic", issueID)
			}
		} else if issue.IssueType == types.TypeEpic || issue.IssueType == types.TypeMolecule {
			epic = issue
		} else {
			FatalErrorRespectJSON("'%s' is not an epic or swarm molecule (type: %s)", issueID, issue.IssueType)
		}

		// Get swarm status
		status, err := getSwarmStatus(ctx, store, epic)
		if err != nil {
			FatalErrorRespectJSON("failed to get swarm status: %v", err)
		}

		if jsonOutput {
			outputJSON(status)
			return
		}

		// Human-readable output
		renderSwarmStatus(status)
	},
}

// getSwarmStatus computes current swarm status from beads.
func getSwarmStatus(ctx context.Context, s SwarmStorage, epic *types.Issue) (*SwarmStatus, error) {
	status := &SwarmStatus{
		EpicID:    epic.ID,
		EpicTitle: epic.Title,
		Completed: []StatusIssue{},
		Active:    []StatusIssue{},
		Ready:     []StatusIssue{},
		Blocked:   []StatusIssue{},
	}

	// Get all child issues of the epic
	childIssues, err := getEpicChildren(ctx, s, epic.ID)
	if err != nil {
		return nil, err
	}

	status.TotalIssues = len(childIssues)
	if len(childIssues) == 0 {
		return status, nil
	}

	// Build set of child IDs for filtering
	childIDSet := make(map[string]bool)
	for _, issue := range childIssues {
		childIDSet[issue.ID] = true
	}

	// Build dependency map (within epic children only)
	dependsOn := make(map[string][]string)
	for _, issue := range childIssues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range deps {
			// Skip parent-child to epic itself
			if dep.DependsOnID == epic.ID && dep.Type == types.DepParentChild {
				continue
			}
			// Only track blocking dependencies within children
			if !dep.Type.AffectsReadyWork() {
				continue
			}
			if childIDSet[dep.DependsOnID] {
				dependsOn[issue.ID] = append(dependsOn[issue.ID], dep.DependsOnID)
			}
		}
	}

	// Categorize each issue
	for _, issue := range childIssues {
		si := StatusIssue{
			ID:       issue.ID,
			Title:    issue.Title,
			Assignee: issue.Assignee,
		}

		switch issue.Status {
		case types.StatusClosed:
			if issue.ClosedAt != nil {
				si.ClosedAt = issue.ClosedAt.Format("2006-01-02 15:04")
			}
			status.Completed = append(status.Completed, si)

		case types.StatusInProgress:
			status.Active = append(status.Active, si)

		default: // open or other
			// Check if blocked by open dependencies
			deps := dependsOn[issue.ID]
			var blockers []string
			for _, depID := range deps {
				depIssue, _ := s.GetIssue(ctx, depID)
				if depIssue != nil && depIssue.Status != types.StatusClosed {
					blockers = append(blockers, depID)
				}
			}

			if len(blockers) > 0 {
				si.BlockedBy = blockers
				status.Blocked = append(status.Blocked, si)
			} else {
				status.Ready = append(status.Ready, si)
			}
		}
	}

	// Sort each category by ID for consistent output
	sort.Slice(status.Completed, func(i, j int) bool {
		return status.Completed[i].ID < status.Completed[j].ID
	})
	sort.Slice(status.Active, func(i, j int) bool {
		return status.Active[i].ID < status.Active[j].ID
	})
	sort.Slice(status.Ready, func(i, j int) bool {
		return status.Ready[i].ID < status.Ready[j].ID
	})
	sort.Slice(status.Blocked, func(i, j int) bool {
		return status.Blocked[i].ID < status.Blocked[j].ID
	})

	// Compute counts and progress
	status.ActiveCount = len(status.Active)
	status.ReadyCount = len(status.Ready)
	status.BlockedCount = len(status.Blocked)
	if status.TotalIssues > 0 {
		status.Progress = float64(len(status.Completed)) / float64(status.TotalIssues) * 100
	}

	return status, nil
}

// renderSwarmStatus outputs human-readable swarm status.
func renderSwarmStatus(status *SwarmStatus) {
	fmt.Printf("\n%s Ready Front Analysis: %s\n\n", ui.RenderAccent("🐝"), status.EpicTitle)

	// Completed
	fmt.Printf("Completed:     ")
	if len(status.Completed) == 0 {
		fmt.Printf("(none)\n")
	} else {
		for i, issue := range status.Completed {
			if i > 0 {
				fmt.Printf("               ")
			}
			fmt.Printf("%s %s\n", ui.RenderPass("✓"), ui.RenderID(issue.ID))
		}
	}

	// Active
	fmt.Printf("Active:        ")
	if len(status.Active) == 0 {
		fmt.Printf("(none)\n")
	} else {
		var parts []string
		for _, issue := range status.Active {
			part := fmt.Sprintf("⟳ %s", issue.ID)
			if issue.Assignee != "" {
				part += fmt.Sprintf(" [%s]", issue.Assignee)
			}
			parts = append(parts, part)
		}
		fmt.Printf("%s\n", strings.Join(parts, ", "))
	}

	// Ready
	fmt.Printf("Ready:         ")
	if len(status.Ready) == 0 {
		if len(status.Blocked) > 0 {
			// Find what's blocking
			needed := make(map[string]bool)
			for _, b := range status.Blocked {
				for _, dep := range b.BlockedBy {
					needed[dep] = true
				}
			}
			var neededList []string
			for dep := range needed {
				neededList = append(neededList, dep)
			}
			sort.Strings(neededList)
			fmt.Printf("(none - waiting for %s)\n", strings.Join(neededList, ", "))
		} else {
			fmt.Printf("(none)\n")
		}
	} else {
		var parts []string
		for _, issue := range status.Ready {
			parts = append(parts, fmt.Sprintf("○ %s", issue.ID))
		}
		fmt.Printf("%s\n", strings.Join(parts, ", "))
	}

	// Blocked
	fmt.Printf("Blocked:       ")
	if len(status.Blocked) == 0 {
		fmt.Printf("(none)\n")
	} else {
		for i, issue := range status.Blocked {
			if i > 0 {
				fmt.Printf("               ")
			}
			blockerStr := strings.Join(issue.BlockedBy, ", ")
			fmt.Printf("◌ %s (needs %s)\n", issue.ID, blockerStr)
		}
	}

	// Progress summary
	fmt.Printf("\nProgress: %d/%d complete", len(status.Completed), status.TotalIssues)
	if status.ActiveCount > 0 {
		fmt.Printf(", %d/%d active", status.ActiveCount, status.TotalIssues)
	}
	fmt.Printf(" (%.0f%%)\n\n", status.Progress)
}

var swarmCreateCmd = &cobra.Command{
	Use:   "create [epic-id]",
	Short: "Create a swarm molecule from an epic",
	Long: `Create a swarm molecule to orchestrate parallel work on an epic.

The swarm molecule:
- Links to the epic it orchestrates
- Has mol_type=swarm for discovery
- Specifies a coordinator (optional)
- Can be picked up by any coordinator agent

If given a single issue (not an epic), it will be auto-wrapped:
- Creates an epic with that issue as its only child
- Then creates the swarm molecule for that epic

Examples:
  bd swarm create gt-epic-123                          # Create swarm for epic
  bd swarm create gt-epic-123 --coordinator=witness/   # With specific coordinator
  bd swarm create gt-task-456                          # Auto-wrap single issue`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("swarm create")
		ctx := rootCtx
		coordinator, _ := cmd.Flags().GetString("coordinator")
		force, _ := cmd.Flags().GetBool("force")

		// Swarm commands require direct store access
		if store == nil {
			if daemonClient != nil {
				var err error
				store, err = sqlite.New(ctx, dbPath)
				if err != nil {
					FatalErrorRespectJSON("failed to open database: %v", err)
				}
				defer func() { _ = store.Close() }()
			} else {
				FatalErrorRespectJSON("no database connection")
			}
		}

		// Resolve the input ID
		inputID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			FatalErrorRespectJSON("issue '%s' not found: %v", args[0], err)
		}

		// Get the issue
		issue, err := store.GetIssue(ctx, inputID)
		if err != nil {
			FatalErrorRespectJSON("failed to get issue: %v", err)
		}
		if issue == nil {
			FatalErrorRespectJSON("issue '%s' not found", inputID)
		}

		var epicID string
		var epicTitle string

		// Check if it's an epic or single issue that needs wrapping
		if issue.IssueType == types.TypeEpic || issue.IssueType == types.TypeMolecule {
			epicID = issue.ID
			epicTitle = issue.Title
		} else {
			// Auto-wrap: create an epic with this issue as child
			if !jsonOutput {
				fmt.Printf("Auto-wrapping single issue as epic...\n")
			}

			wrapperEpic := &types.Issue{
				Title:       fmt.Sprintf("Swarm Epic: %s", issue.Title),
				Description: fmt.Sprintf("Auto-generated epic to wrap single issue %s for swarm execution.", issue.ID),
				Status:      types.StatusOpen,
				Priority:    issue.Priority,
				IssueType:   types.TypeEpic,
				CreatedBy:   actor,
			}

			if err := store.CreateIssue(ctx, wrapperEpic, actor); err != nil {
				FatalErrorRespectJSON("failed to create wrapper epic: %v", err)
			}

			// Add parent-child dependency: issue depends on epic (epic is parent)
			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: wrapperEpic.ID,
				Type:        types.DepParentChild,
				CreatedBy:   actor,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				FatalErrorRespectJSON("failed to link issue to epic: %v", err)
			}

			epicID = wrapperEpic.ID
			epicTitle = wrapperEpic.Title

			if !jsonOutput {
				fmt.Printf("Created wrapper epic: %s\n", epicID)
			}
		}

		// Check for existing swarm molecule
		existingSwarm, err := findExistingSwarm(ctx, store, epicID)
		if err != nil {
			FatalErrorRespectJSON("failed to check for existing swarm: %v", err)
		}
		if existingSwarm != nil && !force {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":         "swarm already exists",
					"existing_id":   existingSwarm.ID,
					"existing_title": existingSwarm.Title,
				})
			} else {
				fmt.Printf("%s Swarm already exists: %s\n", ui.RenderWarn("⚠"), ui.RenderID(existingSwarm.ID))
				fmt.Printf("   Use --force to create another.\n")
			}
			os.Exit(1)
		}

		// Validate the epic structure
		epic, err := store.GetIssue(ctx, epicID)
		if err != nil {
			FatalErrorRespectJSON("failed to get epic: %v", err)
		}

		analysis, err := analyzeEpicForSwarm(ctx, store, epic)
		if err != nil {
			FatalErrorRespectJSON("failed to analyze epic: %v", err)
		}

		if !analysis.Swarmable {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"error":    "epic is not swarmable",
					"analysis": analysis,
				})
			} else {
				fmt.Printf("\n%s Epic is not swarmable. Fix errors first:\n", ui.RenderFail("✗"))
				for _, e := range analysis.Errors {
					fmt.Printf("  • %s\n", e)
				}
			}
			os.Exit(1)
		}

		// Create the swarm molecule
		swarmMol := &types.Issue{
			Title:       fmt.Sprintf("Swarm: %s", epicTitle),
			Description: fmt.Sprintf("Swarm molecule orchestrating epic %s.\n\nEpic: %s\nCoordinator: %s", epicID, epicID, coordinator),
			Status:      types.StatusOpen,
			Priority:    epic.Priority,
			IssueType:   types.TypeMolecule,
			MolType:     types.MolTypeSwarm,
			Assignee:    coordinator,
			CreatedBy:   actor,
		}

		if err := store.CreateIssue(ctx, swarmMol, actor); err != nil {
			FatalErrorRespectJSON("failed to create swarm molecule: %v", err)
		}

		// Link swarm molecule to epic with relates-to dependency
		dep := &types.Dependency{
			IssueID:     swarmMol.ID,
			DependsOnID: epicID,
			Type:        types.DepRelatesTo,
			CreatedBy:   actor,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			FatalErrorRespectJSON("failed to link swarm to epic: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"swarm_id":    swarmMol.ID,
				"epic_id":     epicID,
				"coordinator": coordinator,
				"analysis":    analysis,
			})
		} else {
			fmt.Printf("\n%s Created swarm molecule: %s\n", ui.RenderPass("✓"), ui.RenderID(swarmMol.ID))
			fmt.Printf("   Epic: %s (%s)\n", epicID, epicTitle)
			if coordinator != "" {
				fmt.Printf("   Coordinator: %s\n", coordinator)
			}
			fmt.Printf("   Total issues: %d\n", analysis.TotalIssues)
			fmt.Printf("   Max parallelism: %d\n", analysis.MaxParallelism)
			fmt.Printf("   Waves: %d\n", len(analysis.ReadyFronts))
		}
	},
}

var swarmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all swarm molecules",
	Long: `List all swarm molecules with their status.

Shows each swarm molecule with:
- Progress (completed/total issues)
- Active workers
- Epic ID and title

Examples:
  bd swarm list         # List all swarms
  bd swarm list --json  # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// Swarm commands require direct store access
		if store == nil {
			if daemonClient != nil {
				var err error
				store, err = sqlite.New(ctx, dbPath)
				if err != nil {
					FatalErrorRespectJSON("failed to open database: %v", err)
				}
				defer func() { _ = store.Close() }()
			} else {
				FatalErrorRespectJSON("no database connection")
			}
		}

		// Query for all swarm molecules
		swarmType := types.MolTypeSwarm
		filter := types.IssueFilter{
			MolType: &swarmType,
		}
		swarms, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			FatalErrorRespectJSON("failed to list swarms: %v", err)
		}

		if len(swarms) == 0 {
			if jsonOutput {
				outputJSON(map[string]interface{}{"swarms": []interface{}{}})
			} else {
				fmt.Printf("No swarm molecules found.\n")
			}
			return
		}

		// Build output with status for each swarm
		type SwarmListItem struct {
			ID          string  `json:"id"`
			Title       string  `json:"title"`
			EpicID      string  `json:"epic_id"`
			EpicTitle   string  `json:"epic_title"`
			Status      string  `json:"status"`
			Coordinator string  `json:"coordinator"`
			Total       int     `json:"total_issues"`
			Completed   int     `json:"completed_issues"`
			Active      int     `json:"active_issues"`
			Progress    float64 `json:"progress_percent"`
		}

		var items []SwarmListItem
		for _, swarm := range swarms {
			item := SwarmListItem{
				ID:          swarm.ID,
				Title:       swarm.Title,
				Status:      string(swarm.Status),
				Coordinator: swarm.Assignee,
			}

			// Find linked epic via relates-to dependency
			deps, err := store.GetDependencyRecords(ctx, swarm.ID)
			if err == nil {
				for _, dep := range deps {
					if dep.Type == types.DepRelatesTo {
						item.EpicID = dep.DependsOnID
						epic, err := store.GetIssue(ctx, dep.DependsOnID)
						if err == nil && epic != nil {
							item.EpicTitle = epic.Title
							// Get swarm status for this epic
							status, err := getSwarmStatus(ctx, store, epic)
							if err == nil {
								item.Total = status.TotalIssues
								item.Completed = len(status.Completed)
								item.Active = status.ActiveCount
								item.Progress = status.Progress
							}
						}
						break
					}
				}
			}

			items = append(items, item)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{"swarms": items})
			return
		}

		// Human-readable output
		fmt.Printf("\n%s Active Swarms (%d)\n\n", ui.RenderAccent("🐝"), len(items))
		for _, item := range items {
			// Progress indicator
			progressStr := fmt.Sprintf("%d/%d", item.Completed, item.Total)
			if item.Active > 0 {
				progressStr += fmt.Sprintf(", %d active", item.Active)
			}

			fmt.Printf("%s %s\n", ui.RenderID(item.ID), item.Title)
			if item.EpicID != "" {
				fmt.Printf("   Epic: %s (%s)\n", item.EpicID, item.EpicTitle)
			}
			fmt.Printf("   Progress: %s (%.0f%%)\n", progressStr, item.Progress)
			if item.Coordinator != "" {
				fmt.Printf("   Coordinator: %s\n", item.Coordinator)
			}
			fmt.Println()
		}
	},
}

func init() {
	swarmValidateCmd.Flags().Bool("verbose", false, "Include detailed issue graph in output")
	swarmCreateCmd.Flags().String("coordinator", "", "Coordinator address (e.g., gastown/witness)")
	swarmCreateCmd.Flags().Bool("force", false, "Create new swarm even if one already exists")

	swarmCmd.AddCommand(swarmValidateCmd)
	swarmCmd.AddCommand(swarmStatusCmd)
	swarmCmd.AddCommand(swarmCreateCmd)
	swarmCmd.AddCommand(swarmListCmd)
	rootCmd.AddCommand(swarmCmd)
}
