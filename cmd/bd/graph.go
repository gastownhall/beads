package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// GraphNode represents a node in the rendered graph
type GraphNode struct {
	Issue     *types.Issue
	Layer     int      // Horizontal layer (topological order)
	Position  int      // Vertical position within layer
	DependsOn []string // IDs this node depends on (blocks dependencies only)
}

// GraphLayout holds the computed graph layout
type GraphLayout struct {
	Nodes    map[string]*GraphNode
	Layers   [][]string // Layer index -> node IDs in that layer
	MaxLayer int
	RootID   string
}

var (
	graphCompact bool
	graphBox     bool
	graphAll     bool
	graphDOT     bool
	graphHTML    bool
	graphOpen    bool
)

var graphCmd = &cobra.Command{
	Use:     "graph [issue-id]",
	GroupID: "deps",
	Short:   "Display issue dependency graph",
	Long: `Display a visualization of an issue's dependency graph.

For epics, shows all children and their dependencies.
For regular issues, shows the issue and its direct dependencies.

With --all, shows all open issues grouped by connected component.
With --open, filters to only open/actionable issues (compact layer format).

Display formats:
  (default)        DAG with columns and box-drawing edges (terminal-native)
  --box            ASCII boxes showing layers, more detailed
  --compact        Tree format, one line per issue, more scannable
  --dot            Graphviz DOT format (pipe to dot -Tsvg > graph.svg)
  --html           Self-contained interactive HTML with D3.js visualization
  --open           Open issues only, compact layers (LLM-friendly)

The graph shows execution order:
- Layer 0 / leftmost = no dependencies (can start immediately)
- Higher layers depend on lower layers
- Nodes in the same layer can run in parallel

Status icons: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred

Examples:
  bd graph issue-id              # Terminal DAG visualization (default)
  bd graph --box issue-id        # ASCII boxes with layer grouping
  bd graph --dot issue-id | dot -Tsvg > graph.svg  # SVG via Graphviz
  bd graph --dot issue-id | dot -Tpng > graph.png  # PNG via Graphviz
  bd graph --html issue-id > graph.html  # Interactive browser view
  bd graph --all --html > all.html       # All issues, interactive
  bd graph --open issue-id       # Open issues only, layered by blocking order
  bd graph --all --open          # All open issues, compact layers`,
	Args:          cobra.RangeArgs(0, 1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("graph")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if graphAll && len(args) > 0 {
			return HandleErrorRespectJSON("cannot specify issue ID with --all flag")
		}
		if !graphAll && len(args) == 0 {
			return HandleErrorWithHintRespectJSON("issue ID required", "Use --all for all open issues")
		}

		if usesProxiedServer() {
			return runGraphProxiedServer(rootCtx, args)
		}

		ctx := rootCtx
		if store == nil {
			return HandleErrorRespectJSON("no database connection")
		}

		if graphAll {
			subgraphs, err := loadAllGraphSubgraphs(ctx, store)
			if err != nil {
				return HandleErrorRespectJSON("loading all issues: %v", err)
			}
			return renderGraphAllSubgraphs(subgraphs)
		}

		issueID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("issue '%s' not found", args[0])
		}

		subgraph, err := loadGraphSubgraph(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("loading graph: %v", err)
		}
		return renderGraphSingleSubgraph(subgraph)
	},
}

func renderGraphAllSubgraphs(subgraphs []*TemplateSubgraph) error {
	if graphOpen {
		var filtered []*TemplateSubgraph
		for _, sg := range subgraphs {
			f := filterSubgraphOpen(sg)
			if f != nil && len(f.Issues) > 0 {
				filtered = append(filtered, f)
			}
		}
		subgraphs = filtered
	}

	if len(subgraphs) == 0 {
		fmt.Println("No open issues found")
		return nil
	}

	if jsonOutput {
		return outputJSON(subgraphs)
	}

	if graphHTML && !graphOpen {
		merged := mergeSubgraphsForHTML(subgraphs)
		layout := computeLayout(merged)
		renderGraphHTML(layout, merged)
		return nil
	}

	if graphOpen {
		for i, subgraph := range subgraphs {
			layout := computeLayout(subgraph)
			renderGraphCompact(layout, subgraph)
			if i < len(subgraphs)-1 {
				fmt.Println(strings.Repeat("─", 60))
			}
		}
		return nil
	}

	for i, subgraph := range subgraphs {
		layout := computeLayout(subgraph)
		if graphDOT {
			renderGraphDOT(layout, subgraph)
		} else if graphCompact {
			renderGraphCompact(layout, subgraph)
		} else if graphBox {
			renderGraph(layout, subgraph)
		} else {
			renderGraphVisual(layout, subgraph)
		}
		if !graphDOT && i < len(subgraphs)-1 {
			fmt.Println(strings.Repeat("─", 60))
		}
	}
	return nil
}

func renderGraphSingleSubgraph(subgraph *TemplateSubgraph) error {
	if graphOpen {
		subgraph = filterSubgraphOpen(subgraph)
		if subgraph == nil || len(subgraph.Issues) == 0 {
			fmt.Println("No open issues in subgraph")
			return nil
		}
	}

	layout := computeLayout(subgraph)

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"root":   subgraph.Root,
			"issues": subgraph.Issues,
			"layout": layout,
		})
	}

	if graphOpen {
		renderGraphCompact(layout, subgraph)
		return nil
	}

	if graphDOT {
		renderGraphDOT(layout, subgraph)
	} else if graphHTML {
		renderGraphHTML(layout, subgraph)
	} else if graphCompact {
		renderGraphCompact(layout, subgraph)
	} else if graphBox {
		renderGraph(layout, subgraph)
	} else {
		renderGraphVisual(layout, subgraph)
	}
	return nil
}

var graphCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check dependency graph integrity",
	Long: `Check the dependency graph for cycles, orphans, and other integrity issues.

Returns exit code 0 if the graph is clean, 1 if issues are found.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("graph-check")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runGraphCheckProxiedServer(rootCtx)
		}

		cycles, err := store.DetectCycles(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("cycle detection failed: %v", err)
		}
		return renderGraphCheck(cycles)
	},
}

func renderGraphCheck(cycles [][]*types.Issue) error {
	type GraphCheckResult struct {
		Clean   bool       `json:"clean"`
		Cycles  [][]string `json:"cycles"`
		Summary struct {
			CycleCount int `json:"cycle_count"`
		} `json:"summary"`
	}

	result := GraphCheckResult{Clean: true}

	for _, cycle := range cycles {
		ids := make([]string, len(cycle))
		for i, issue := range cycle {
			ids[i] = issue.ID
		}
		result.Cycles = append(result.Cycles, ids)
	}
	result.Summary.CycleCount = len(cycles)

	if len(cycles) > 0 {
		result.Clean = false
	}

	if jsonOutput {
		if err := outputJSON(result); err != nil {
			return err
		}
		if !result.Clean {
			return SilentExit()
		}
		return nil
	}

	if result.Clean {
		fmt.Printf("\n%s Graph integrity check passed\n\n", ui.RenderPass("✓"))
	} else {
		fmt.Printf("\n%s Graph integrity issues found\n\n", ui.RenderFail("✗"))
	}

	if len(result.Cycles) > 0 {
		fmt.Printf("%s Cycles (%d):\n\n", ui.RenderFail("⚠"), len(result.Cycles))
		for _, cycle := range result.Cycles {
			fmt.Printf("  %s → %s\n", strings.Join(cycle, " → "), cycle[0])
		}
		fmt.Println()
	} else {
		fmt.Printf("  %s No dependency cycles\n", ui.RenderPass("✓"))
	}

	fmt.Println()

	if !result.Clean {
		return SilentExit()
	}
	return nil
}

func init() {
	graphCmd.Flags().BoolVar(&graphAll, "all", false, "Show graph for all open issues")
	graphCmd.Flags().BoolVar(&graphCompact, "compact", false, "Tree format, one line per issue, more scannable")
	graphCmd.Flags().BoolVar(&graphBox, "box", false, "ASCII boxes showing layers")
	graphCmd.Flags().BoolVar(&graphDOT, "dot", false, "Output Graphviz DOT format (pipe to: dot -Tsvg > graph.svg)")
	graphCmd.Flags().BoolVar(&graphHTML, "html", false, "Output self-contained interactive HTML (redirect to file)")
	graphCmd.Flags().BoolVar(&graphOpen, "open", false, "Show only open issues (filters out closed/deferred), forces compact layer format")
	graphCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(graphCmd)
	graphCmd.AddCommand(graphCheckCmd)
}

// loadGraphSubgraph loads an issue and its subgraph for visualization
// Unlike template loading, this includes ALL dependency types (not just parent-child)
func loadGraphSubgraph(ctx context.Context, s storage.DoltStorage, issueID string) (*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the root issue
	root, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// BFS to find all connected issues (via any dependency type).
	// Traverse both directions: dependents AND dependencies (GH#2145).
	queue := []string{root.ID}
	visited := map[string]bool{root.ID: true}

	for len(queue) > 0 {
		currentID := queue[0]
		queue = queue[1:]

		// Get issues that depend on this one (dependents)
		dependents, err := s.GetDependents(ctx, currentID)
		if err != nil {
			continue
		}
		for _, dep := range dependents {
			if !visited[dep.ID] {
				visited[dep.ID] = true
				subgraph.Issues = append(subgraph.Issues, dep)
				subgraph.IssueMap[dep.ID] = dep
				queue = append(queue, dep.ID)
			}
		}

		// Get issues this one depends on (dependencies)
		dependencies, err := s.GetDependencies(ctx, currentID)
		if err != nil {
			continue
		}
		for _, dep := range dependencies {
			if !visited[dep.ID] {
				visited[dep.ID] = true
				subgraph.Issues = append(subgraph.Issues, dep)
				subgraph.IssueMap[dep.ID] = dep
				queue = append(queue, dep.ID)
			}
		}
	}

	// Load all dependencies within the subgraph
	for _, issue := range subgraph.Issues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range deps {
			// Resolve external deps via routing (bd-k0pfm)
			if strings.HasPrefix(dep.DependsOnID, "external:") {
				parts := strings.SplitN(dep.DependsOnID, ":", 3)
				if len(parts) == 3 && parts[2] != "" {
					targetID := parts[2]
					if _, exists := subgraph.IssueMap[targetID]; !exists {
						result, routeErr := resolveAndGetIssueWithRouting(ctx, store, targetID)
						if routeErr == nil && result != nil && result.Issue != nil {
							subgraph.Issues = append(subgraph.Issues, result.Issue)
							subgraph.IssueMap[result.Issue.ID] = result.Issue
							// Rewrite dep to use the resolved issue ID
							dep.DependsOnID = result.Issue.ID
							result.Close()
						} else {
							if result != nil {
								result.Close()
							}
							continue
						}
					} else {
						dep.DependsOnID = targetID
					}
				}
			}
			// Only include dependencies where both ends are in the subgraph
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadAllGraphSubgraphs loads all open issues and groups them by connected component
// Each component is a subgraph of issues that share dependencies
func loadAllGraphSubgraphs(ctx context.Context, s storage.DoltStorage) ([]*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get all open issues (open, in_progress, blocked)
	// We need to make multiple calls since IssueFilter takes a single status
	var allIssues []*types.Issue
	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked} {
		statusCopy := status
		issues, err := s.SearchIssues(ctx, "", types.IssueFilter{
			Status: &statusCopy,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to search issues: %w", err)
		}
		allIssues = append(allIssues, issues...)
	}

	if len(allIssues) == 0 {
		return nil, nil
	}

	// Build issue map
	issueMap := make(map[string]*types.Issue)
	for _, issue := range allIssues {
		issueMap[issue.ID] = issue
	}

	// Load all dependencies between these issues
	allDeps := make([]*types.Dependency, 0)
	for _, issue := range allIssues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range deps {
			// Resolve external deps via routing (bd-k0pfm)
			if strings.HasPrefix(dep.DependsOnID, "external:") {
				parts := strings.SplitN(dep.DependsOnID, ":", 3)
				if len(parts) == 3 && parts[2] != "" {
					targetID := parts[2]
					if _, exists := issueMap[targetID]; !exists {
						result, routeErr := resolveAndGetIssueWithRouting(ctx, store, targetID)
						if routeErr == nil && result != nil && result.Issue != nil {
							allIssues = append(allIssues, result.Issue)
							issueMap[result.Issue.ID] = result.Issue
							dep.DependsOnID = result.Issue.ID
							result.Close()
						} else {
							if result != nil {
								result.Close()
							}
							continue
						}
					} else {
						dep.DependsOnID = targetID
					}
				}
			}
			// Only include deps where both ends are in our issue set
			if _, ok := issueMap[dep.DependsOnID]; ok {
				allDeps = append(allDeps, dep)
			}
		}
	}

	return assembleAllSubgraphs(allIssues, issueMap, allDeps), nil
}

func assembleAllSubgraphs(allIssues []*types.Issue, issueMap map[string]*types.Issue, allDeps []*types.Dependency) []*TemplateSubgraph {
	// Build adjacency list for union-find
	adj := make(map[string][]string)
	for _, dep := range allDeps {
		adj[dep.IssueID] = append(adj[dep.IssueID], dep.DependsOnID)
		adj[dep.DependsOnID] = append(adj[dep.DependsOnID], dep.IssueID)
	}

	// Find connected components using BFS
	visited := make(map[string]bool)
	var components [][]string

	for _, issue := range allIssues {
		if visited[issue.ID] {
			continue
		}

		// BFS to find all connected issues
		var component []string
		queue := []string{issue.ID}
		visited[issue.ID] = true

		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)

			for _, neighbor := range adj[current] {
				if !visited[neighbor] {
					visited[neighbor] = true
					queue = append(queue, neighbor)
				}
			}
		}

		components = append(components, component)
	}

	// Sort components by size (largest first) and then by priority of first issue
	sort.Slice(components, func(i, j int) bool {
		// First by size (descending)
		if len(components[i]) != len(components[j]) {
			return len(components[i]) > len(components[j])
		}
		// Then by priority of first issue (ascending = higher priority first)
		issueI := issueMap[components[i][0]]
		issueJ := issueMap[components[j][0]]
		return issueI.Priority < issueJ.Priority
	})

	// Create subgraph for each component
	var subgraphs []*TemplateSubgraph
	for _, component := range components {
		if len(component) == 0 {
			continue
		}

		// Find the best "root" for this component
		// Prefer: epics > highest priority > oldest
		var root *types.Issue
		for _, id := range component {
			issue := issueMap[id]
			if root == nil {
				root = issue
				continue
			}
			// Prefer epics
			if issue.IssueType == types.TypeEpic && root.IssueType != types.TypeEpic {
				root = issue
				continue
			}
			if root.IssueType == types.TypeEpic && issue.IssueType != types.TypeEpic {
				continue
			}
			// Prefer higher priority (lower number)
			if issue.Priority < root.Priority {
				root = issue
			}
		}

		subgraph := &TemplateSubgraph{
			Root:     root,
			IssueMap: make(map[string]*types.Issue),
		}

		for _, id := range component {
			issue := issueMap[id]
			subgraph.Issues = append(subgraph.Issues, issue)
			subgraph.IssueMap[id] = issue
		}

		// Add dependencies for this component
		for _, dep := range allDeps {
			if _, inComponent := subgraph.IssueMap[dep.IssueID]; inComponent {
				if _, depInComponent := subgraph.IssueMap[dep.DependsOnID]; depInComponent {
					subgraph.Dependencies = append(subgraph.Dependencies, dep)
				}
			}
		}

		subgraphs = append(subgraphs, subgraph)
	}

	return subgraphs
}

// isOpenStatus returns true for statuses considered "open" / actionable.
// Closed and deferred (frozen) issues are excluded by --open.
func isOpenStatus(s types.Status) bool {
	switch s {
	case types.StatusOpen, types.StatusInProgress, types.StatusBlocked:
		return true
	default:
		return false
	}
}

// filterSubgraphOpen removes closed/deferred issues from a subgraph and
// computes a transitive closure of blocking edges through removed nodes so
// that indirect blocking relationships are preserved.
//
// Example: if A(open) blocks B(closed) blocks C(open), the filtered graph
// contains A and C with a synthetic edge A→C.
func filterSubgraphOpen(subgraph *TemplateSubgraph) *TemplateSubgraph {
	if subgraph == nil {
		return nil
	}

	openSet := make(map[string]bool)
	for _, issue := range subgraph.Issues {
		if isOpenStatus(issue.Status) {
			openSet[issue.ID] = true
		}
	}

	if len(openSet) == 0 {
		return nil
	}

	// Build full blocking graph (all nodes, including closed) for transitive
	// closure. blocksAdj: A→B means "A blocks B" (B depends on A).
	blocksAdj := make(map[string][]string)
	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepBlocks {
			blocksAdj[dep.DependsOnID] = append(blocksAdj[dep.DependsOnID], dep.IssueID)
		}
	}

	// For each open node, BFS forward through the blocking graph to find all
	// reachable open nodes. Each such reachable open node has a transitive
	// blocking relationship.
	type edge struct{ from, to string }
	syntheticEdges := make(map[edge]bool)

	for src := range openSet {
		visited := map[string]bool{src: true}
		queue := []string{src}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, next := range blocksAdj[cur] {
				if visited[next] {
					continue
				}
				visited[next] = true
				if openSet[next] {
					syntheticEdges[edge{src, next}] = true
				}
				if !openSet[next] {
					queue = append(queue, next)
				}
			}
		}
	}

	// Build filtered subgraph
	filtered := &TemplateSubgraph{
		IssueMap: make(map[string]*types.Issue, len(openSet)),
	}
	for _, issue := range subgraph.Issues {
		if openSet[issue.ID] {
			filtered.Issues = append(filtered.Issues, issue)
			filtered.IssueMap[issue.ID] = issue
		}
	}

	// Keep non-blocks deps where both ends are open, and rebuild blocks
	// deps from the transitive closure.
	seen := make(map[edge]bool)
	for _, dep := range subgraph.Dependencies {
		if !openSet[dep.IssueID] || !openSet[dep.DependsOnID] {
			continue
		}
		if dep.Type == types.DepBlocks {
			e := edge{dep.DependsOnID, dep.IssueID}
			if !seen[e] {
				seen[e] = true
				filtered.Dependencies = append(filtered.Dependencies, dep)
			}
		} else {
			filtered.Dependencies = append(filtered.Dependencies, dep)
		}
	}
	// Add synthetic transitive edges
	for e := range syntheticEdges {
		if seen[e] {
			continue
		}
		seen[e] = true
		filtered.Dependencies = append(filtered.Dependencies, &types.Dependency{
			IssueID:     e.to,
			DependsOnID: e.from,
			Type:        types.DepBlocks,
		})
	}

	// Pick root: prefer original root if still open, else highest-priority open issue
	if subgraph.Root != nil && openSet[subgraph.Root.ID] {
		filtered.Root = subgraph.Root
	} else {
		for _, issue := range filtered.Issues {
			if filtered.Root == nil || issue.Priority < filtered.Root.Priority {
				filtered.Root = issue
			}
		}
	}

	return filtered
}

// computeLayout assigns layers to nodes using topological sort
// mergeSubgraphsForHTML joins disconnected components into one subgraph so
// `bd graph --all --html` emits a single valid HTML document.
func mergeSubgraphsForHTML(subgraphs []*TemplateSubgraph) *TemplateSubgraph {
	switch len(subgraphs) {
	case 0:
		return &TemplateSubgraph{IssueMap: make(map[string]*types.Issue)}
	case 1:
		return subgraphs[0]
	}
	merged := &TemplateSubgraph{
		IssueMap: make(map[string]*types.Issue),
	}
	for _, sg := range subgraphs {
		for _, issue := range sg.Issues {
			merged.IssueMap[issue.ID] = issue
		}
		merged.Dependencies = append(merged.Dependencies, sg.Dependencies...)
	}
	merged.Issues = make([]*types.Issue, 0, len(merged.IssueMap))
	for _, issue := range merged.IssueMap {
		merged.Issues = append(merged.Issues, issue)
	}
	sort.Slice(merged.Issues, func(i, j int) bool {
		return merged.Issues[i].ID < merged.Issues[j].ID
	})
	return merged
}

func computeLayout(subgraph *TemplateSubgraph) *GraphLayout {
	layout := &GraphLayout{
		Nodes: make(map[string]*GraphNode),
	}
	if subgraph.Root != nil {
		layout.RootID = subgraph.Root.ID
	}

	// Build dependency map (only "blocks" dependencies, not parent-child)
	dependsOn := make(map[string][]string)
	blockedBy := make(map[string][]string)

	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepBlocks {
			// dep.IssueID depends on dep.DependsOnID
			dependsOn[dep.IssueID] = append(dependsOn[dep.IssueID], dep.DependsOnID)
			blockedBy[dep.DependsOnID] = append(blockedBy[dep.DependsOnID], dep.IssueID)
		}
	}

	// Initialize nodes
	for _, issue := range subgraph.Issues {
		layout.Nodes[issue.ID] = &GraphNode{
			Issue:     issue,
			Layer:     -1, // Unassigned
			DependsOn: dependsOn[issue.ID],
		}
	}

	// Assign layers using longest path from sources
	// Layer 0 = nodes with no dependencies
	changed := true
	for changed {
		changed = false
		for id, node := range layout.Nodes {
			if node.Layer >= 0 {
				continue // Already assigned
			}

			deps := dependsOn[id]
			if len(deps) == 0 {
				// No dependencies - layer 0
				node.Layer = 0
				changed = true
			} else {
				// Check if all dependencies have layers assigned
				maxDepLayer := -1
				allAssigned := true
				for _, depID := range deps {
					depNode := layout.Nodes[depID]
					if depNode == nil || depNode.Layer < 0 {
						allAssigned = false
						break
					}
					if depNode.Layer > maxDepLayer {
						maxDepLayer = depNode.Layer
					}
				}
				if allAssigned {
					node.Layer = maxDepLayer + 1
					changed = true
				}
			}
		}
	}

	// Handle any unassigned nodes (cycles or disconnected)
	for _, node := range layout.Nodes {
		if node.Layer < 0 {
			node.Layer = 0
		}
	}

	// Lift children to at least their parent's layer (GH#1748).
	// Parent-child deps are not blocking deps, but children logically belong
	// to their parent's scope. If a parent epic is blocked (higher layer),
	// its children should appear in the same layer, not float in Layer 0.
	parentOf := make(map[string]string) // childID -> parentID
	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepParentChild {
			parentOf[dep.IssueID] = dep.DependsOnID
		}
	}
	if len(parentOf) > 0 {
		// Iterate until stable — handles nested parent-child hierarchies
		changed = true
		for changed {
			changed = false
			for childID, parentID := range parentOf {
				childNode := layout.Nodes[childID]
				parentNode := layout.Nodes[parentID]
				if childNode != nil && parentNode != nil && childNode.Layer < parentNode.Layer {
					childNode.Layer = parentNode.Layer
					changed = true
				}
			}
		}
	}

	// Build layers array
	for _, node := range layout.Nodes {
		if node.Layer > layout.MaxLayer {
			layout.MaxLayer = node.Layer
		}
	}

	layout.Layers = make([][]string, layout.MaxLayer+1)
	for id, node := range layout.Nodes {
		layout.Layers[node.Layer] = append(layout.Layers[node.Layer], id)
	}

	// Sort nodes within each layer for consistent ordering
	for i := range layout.Layers {
		sort.Strings(layout.Layers[i])
	}

	// Assign vertical positions within layers
	for _, layer := range layout.Layers {
		for pos, id := range layer {
			layout.Nodes[id].Position = pos
		}
	}

	return layout
}

// renderGraph renders the ASCII visualization
func renderGraph(layout *GraphLayout, subgraph *TemplateSubgraph) {
	if len(layout.Nodes) == 0 {
		fmt.Println("Empty graph")
		return
	}

	fmt.Printf("\n%s Dependency graph for %s:\n\n", ui.RenderAccent("📊"), layout.RootID)

	// Calculate box width based on longest title
	maxTitleLen := 0
	for _, node := range layout.Nodes {
		titleLen := len(truncateTitle(node.Issue.Title, 30))
		if titleLen > maxTitleLen {
			maxTitleLen = titleLen
		}
	}
	boxWidth := maxTitleLen + 4 // padding

	// Render each layer
	// For simplicity, we'll render layer by layer with arrows between them

	// First, show the legend
	fmt.Println("  Status: ○ open  ◐ in_progress  ● blocked  ✓ closed")
	fmt.Println()

	// Build dependency counts from subgraph
	blocksCounts, blockedByCounts := computeDependencyCounts(subgraph)

	// Render layers left to right
	layerBoxes := make([][]string, len(layout.Layers))

	for layerIdx, layer := range layout.Layers {
		var boxes []string
		for _, id := range layer {
			node := layout.Nodes[id]
			box := renderNodeBoxWithDeps(node, boxWidth, blocksCounts[id], blockedByCounts[id])
			boxes = append(boxes, box)
		}
		layerBoxes[layerIdx] = boxes
	}

	// Find max height per layer
	maxHeight := 0
	for _, boxes := range layerBoxes {
		h := len(boxes) * 4 // Each box is ~3 lines + 1 gap
		if h > maxHeight {
			maxHeight = h
		}
	}

	// Render horizontally (simplified - just show boxes with arrows)
	for layerIdx, boxes := range layerBoxes {
		// Print layer header
		fmt.Printf("  Layer %d", layerIdx)
		if layerIdx == 0 {
			fmt.Print(" (ready)")
		}
		fmt.Println()

		for _, box := range boxes {
			fmt.Println(box)
		}

		// Print arrows to next layer if not last
		if layerIdx < len(layerBoxes)-1 {
			fmt.Println("      │")
			fmt.Println("      ▼")
		}
		fmt.Println()
	}

	// Show dependency summary
	if len(subgraph.Dependencies) > 0 {
		blocksDeps := 0
		for _, dep := range subgraph.Dependencies {
			if dep.Type == types.DepBlocks {
				blocksDeps++
			}
		}
		if blocksDeps > 0 {
			fmt.Printf("  Dependencies: %d blocking relationships\n", blocksDeps)
		}
	}

	// Show summary
	fmt.Printf("  Total: %d issues across %d layers\n\n", len(layout.Nodes), len(layout.Layers))
}

// renderGraphCompact renders the graph in compact tree format
// One line per issue, more scannable, uses tree connectors (├──, └──, │)
func renderGraphCompact(layout *GraphLayout, subgraph *TemplateSubgraph) {
	if len(layout.Nodes) == 0 {
		fmt.Println("Empty graph")
		return
	}

	fmt.Printf("\n%s Dependency graph for %s (%d issues, %d layers)\n\n",
		ui.RenderAccent("📊"), layout.RootID, len(layout.Nodes), len(layout.Layers))

	// Legend
	fmt.Println("  Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred")
	fmt.Println()

	// Build parent-child map from subgraph dependencies
	children := make(map[string][]string) // parent -> children
	childSet := make(map[string]bool)     // track which issues are children

	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepParentChild {
			children[dep.DependsOnID] = append(children[dep.DependsOnID], dep.IssueID)
			childSet[dep.IssueID] = true
		}
	}

	// Sort children by priority then ID for consistent output
	for parentID := range children {
		sort.Slice(children[parentID], func(i, j int) bool {
			nodeI := layout.Nodes[children[parentID][i]]
			nodeJ := layout.Nodes[children[parentID][j]]
			if nodeI.Issue.Priority != nodeJ.Issue.Priority {
				return nodeI.Issue.Priority < nodeJ.Issue.Priority
			}
			return utils.NaturalCompareIDs(nodeI.Issue.ID, nodeJ.Issue.ID) < 0
		})
	}

	// Render by layer with tree structure
	for layerIdx, layer := range layout.Layers {
		// Layer header
		layerHeader := fmt.Sprintf("LAYER %d", layerIdx)
		if layerIdx == 0 {
			layerHeader += " (ready)"
		}
		fmt.Printf("  %s\n", ui.RenderAccent(layerHeader))

		for i, id := range layer {
			node := layout.Nodes[id]
			isLast := i == len(layer)-1

			// Format node line
			line := formatCompactNode(node)

			// Tree connector
			connector := "├── "
			if isLast {
				connector = "└── "
			}

			fmt.Printf("  %s%s\n", connector, line)

			// Render children (if this issue has children in the subgraph)
			if childIDs, ok := children[id]; ok && len(childIDs) > 0 {
				childPrefix := "│   "
				if isLast {
					childPrefix = "    "
				}
				renderCompactChildren(layout, childIDs, children, childPrefix, 1)
			}
		}
		fmt.Println()
	}
}

// renderCompactChildren recursively renders children in tree format
func renderCompactChildren(layout *GraphLayout, childIDs []string, children map[string][]string, prefix string, depth int) {
	for i, childID := range childIDs {
		node := layout.Nodes[childID]
		if node == nil {
			continue
		}

		isLast := i == len(childIDs)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		line := formatCompactNode(node)
		fmt.Printf("  %s%s%s\n", prefix, connector, line)

		// Recurse for nested children
		if grandchildren, ok := children[childID]; ok && len(grandchildren) > 0 {
			childPrefix := prefix
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
			renderCompactChildren(layout, grandchildren, children, childPrefix, depth+1)
		}
	}
}

// formatCompactNode formats a single node for compact output
// Format: STATUS_ICON ID PRIORITY Title
func formatCompactNode(node *GraphNode) string {
	status := string(node.Issue.Status)

	// Use shared status icon with semantic color
	statusIcon := ui.RenderStatusIcon(status)

	// Priority with icon
	priorityTag := ui.RenderPriority(node.Issue.Priority)

	// Title - truncate if too long
	title := truncateTitle(node.Issue.Title, 50)

	// Build line - apply status style to entire line for closed issues
	style := ui.GetStatusStyle(status)
	if node.Issue.Status == types.StatusClosed {
		return fmt.Sprintf("%s %s %s %s",
			statusIcon,
			style.Render(node.Issue.ID),
			style.Render(fmt.Sprintf("● P%d", node.Issue.Priority)),
			style.Render(title))
	}

	return fmt.Sprintf("%s %s %s %s", statusIcon, node.Issue.ID, priorityTag, title)
}

// renderNodeBox renders a single node as an ASCII box
// Uses semantic status styles from ui package for consistency
func renderNodeBox(node *GraphNode, width int) string {
	title := truncateTitle(node.Issue.Title, width-4)
	paddedTitle := padRight(title, width-4)
	status := string(node.Issue.Status)

	// Use shared status icon and style
	statusIcon := ui.RenderStatusIcon(status)
	style := ui.GetStatusStyle(status)

	// Apply style to title for actionable statuses
	var titleStr string
	if node.Issue.Status == types.StatusOpen {
		titleStr = paddedTitle // no color for open - available but not urgent
	} else {
		titleStr = style.Render(paddedTitle)
	}

	id := node.Issue.ID

	// Build the box
	topBottom := "  ┌" + strings.Repeat("─", width) + "┐"
	middle := fmt.Sprintf("  │ %s %s │", statusIcon, titleStr)
	idLine := fmt.Sprintf("  │ %s │", ui.RenderMuted(padRight(id, width-2)))
	bottom := "  └" + strings.Repeat("─", width) + "┘"

	return topBottom + "\n" + middle + "\n" + idLine + "\n" + bottom
}

// truncateTitle truncates a title to max length (rune-safe)
func truncateTitle(title string, maxLen int) string {
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}
	return string(runes[:maxLen-1]) + "…"
}

// padRight pads a string to the right with spaces (rune-safe)
func padRight(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return s + strings.Repeat(" ", width-len(runes))
}

// computeDependencyCounts calculates how many issues each issue blocks and is blocked by
// Excludes parent-child relationships and the root issue from counts to reduce cognitive noise
func computeDependencyCounts(subgraph *TemplateSubgraph) (blocks map[string]int, blockedBy map[string]int) {
	blocks = make(map[string]int)
	blockedBy = make(map[string]int)

	if subgraph == nil {
		return blocks, blockedBy
	}

	rootID := ""
	if subgraph.Root != nil {
		rootID = subgraph.Root.ID
	}

	for _, dep := range subgraph.Dependencies {
		// Only count "blocks" dependencies (not parent-child, related, etc.)
		if dep.Type != types.DepBlocks {
			continue
		}

		// Skip if the blocker is the root issue - this is obvious from graph structure
		// and showing "needs:1" when it's just the parent epic is cognitive noise
		if dep.DependsOnID == rootID {
			continue
		}

		// dep.DependsOnID blocks dep.IssueID
		// So dep.DependsOnID "blocks" count increases
		blocks[dep.DependsOnID]++
		// And dep.IssueID "blocked by" count increases
		blockedBy[dep.IssueID]++
	}

	return blocks, blockedBy
}

// renderNodeBoxWithDeps renders a node box with dependency information
// Uses semantic status styles from ui package for consistency across commands
// Design principle: only actionable states get color, closed items fade
func renderNodeBoxWithDeps(node *GraphNode, width int, blocksCount int, blockedByCount int) string {
	title := truncateTitle(node.Issue.Title, width-4)
	paddedTitle := padRight(title, width-4)
	status := string(node.Issue.Status)

	// Use shared status icon and style from ui package
	statusIcon := ui.RenderStatusIcon(status)
	style := ui.GetStatusStyle(status)

	// Apply style to title for actionable statuses
	var titleStr string
	if node.Issue.Status == types.StatusOpen {
		titleStr = paddedTitle // no color for open - available but not urgent
	} else {
		titleStr = style.Render(paddedTitle)
	}

	id := node.Issue.ID

	// Build dependency info string - only show if meaningful counts exist
	// Note: we build the plain text version first for padding, then apply colors
	var depInfoPlain string
	var depInfoStyled string
	if blocksCount > 0 || blockedByCount > 0 {
		plainParts := []string{}
		styledParts := []string{}
		if blocksCount > 0 {
			plainText := fmt.Sprintf("blocks:%d", blocksCount)
			plainParts = append(plainParts, plainText)
			// Use semantic color for blocks indicator - attention-grabbing
			styledParts = append(styledParts, ui.StatusBlockedStyle.Render(plainText))
		}
		if blockedByCount > 0 {
			plainText := fmt.Sprintf("needs:%d", blockedByCount)
			plainParts = append(plainParts, plainText)
			// Use muted color for needs indicator - informational
			styledParts = append(styledParts, ui.MutedStyle.Render(plainText))
		}
		depInfoPlain = strings.Join(plainParts, " ")
		depInfoStyled = strings.Join(styledParts, " ")
	}

	// Build the box
	topBottom := "  ┌" + strings.Repeat("─", width) + "┐"
	middle := fmt.Sprintf("  │ %s %s │", statusIcon, titleStr)
	idLine := fmt.Sprintf("  │ %s │", ui.RenderMuted(padRight(id, width-2)))

	var result string
	if depInfoPlain != "" {
		// Pad based on plain text length, then render with styled version
		padding := width - 2 - len([]rune(depInfoPlain))
		if padding < 0 {
			padding = 0
		}
		depLine := fmt.Sprintf("  │ %s%s │", depInfoStyled, strings.Repeat(" ", padding))
		bottom := "  └" + strings.Repeat("─", width) + "┘"
		result = topBottom + "\n" + middle + "\n" + idLine + "\n" + depLine + "\n" + bottom
	} else {
		bottom := "  └" + strings.Repeat("─", width) + "┘"
		result = topBottom + "\n" + middle + "\n" + idLine + "\n" + bottom
	}

	return result
}
