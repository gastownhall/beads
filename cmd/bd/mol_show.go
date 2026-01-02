package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var (
	molShowParallel bool // --parallel flag for parallel detection
)

var molShowCmd = &cobra.Command{
	Use:   "show <molecule-id>",
	Short: "Show molecule details",
	Long: `Show molecule structure and details.

The --parallel flag highlights parallelizable steps:
  - Steps with no blocking dependencies can run in parallel
  - Shows which steps are ready to start now
  - Identifies parallel groups (steps that can run concurrently)

Example:
  bd mol show bd-patrol --parallel`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// mol show requires direct store access for subgraph loading
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: mol show requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol show %s\n", args[0])
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}

		moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: molecule '%s' not found\n", args[0])
			os.Exit(1)
		}

		subgraph, err := loadTemplateSubgraph(ctx, store, moleculeID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
			os.Exit(1)
		}

		if molShowParallel {
			showMoleculeWithParallel(subgraph)
		} else {
			showMolecule(subgraph)
		}
	},
}

func showMolecule(subgraph *MoleculeSubgraph) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"root":         subgraph.Root,
			"issues":       subgraph.Issues,
			"dependencies": subgraph.Dependencies,
			"variables":    extractAllVariables(subgraph),
			"is_compound":  subgraph.Root.IsCompound(),
			"bonded_from":  subgraph.Root.BondedFrom,
		})
		return
	}

	// Determine molecule type label
	moleculeType := "Molecule"
	if subgraph.Root.IsCompound() {
		moleculeType = "Compound"
	}

	fmt.Printf("\n%s %s: %s\n", ui.RenderAccent("🧪"), moleculeType, subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", subgraph.Root.ID)
	fmt.Printf("   Steps: %d\n", len(subgraph.Issues))

	// Show compound bonding info if this is a compound molecule
	if subgraph.Root.IsCompound() {
		showCompoundBondingInfo(subgraph.Root)
	}

	vars := extractAllVariables(subgraph)
	if len(vars) > 0 {
		fmt.Printf("\n%s Variables:\n", ui.RenderWarn("📝"))
		for _, v := range vars {
			fmt.Printf("   {{%s}}\n", v)
		}
	}

	fmt.Printf("\n%s Structure:\n", ui.RenderPass("🌲"))
	printMoleculeTree(subgraph, subgraph.Root.ID, 0, true)
	fmt.Println()
}

// showCompoundBondingInfo displays the bonding lineage for compound molecules
func showCompoundBondingInfo(root *types.Issue) {
	if !root.IsCompound() {
		return
	}

	constituents := root.GetConstituents()
	fmt.Printf("\n%s Bonded from:\n", ui.RenderAccent("🔗"))

	for i, ref := range constituents {
		connector := "├──"
		if i == len(constituents)-1 {
			connector = "└──"
		}

		// Format bond type for display
		bondTypeDisplay := formatBondType(ref.BondType)

		// Show source ID with bond type
		if ref.BondPoint != "" {
			fmt.Printf("   %s %s (%s, at %s)\n", connector, ref.SourceID, bondTypeDisplay, ref.BondPoint)
		} else {
			fmt.Printf("   %s %s (%s)\n", connector, ref.SourceID, bondTypeDisplay)
		}
	}
}

// formatBondType returns a human-readable bond type description
func formatBondType(bondType string) string {
	switch bondType {
	case types.BondTypeSequential:
		return "sequential"
	case types.BondTypeParallel:
		return "parallel"
	case types.BondTypeConditional:
		return "on-failure"
	case types.BondTypeRoot:
		return "root"
	default:
		if bondType == "" {
			return "default"
		}
		return bondType
	}
}

// ParallelInfo holds parallel analysis information for a step
type ParallelInfo struct {
	StepID        string   `json:"step_id"`
	Status        string   `json:"status"`
	IsReady       bool     `json:"is_ready"`        // Can start now (no blocking deps)
	ParallelGroup string   `json:"parallel_group"`  // Group ID (steps with same group can parallelize)
	BlockedBy     []string `json:"blocked_by"`      // IDs of open steps blocking this one
	Blocks        []string `json:"blocks"`          // IDs of steps this one blocks
	CanParallel   []string `json:"can_parallel"`    // IDs of steps that can run in parallel with this
}

// ParallelAnalysis holds the complete parallel analysis for a molecule
type ParallelAnalysis struct {
	MoleculeID     string                  `json:"molecule_id"`
	TotalSteps     int                     `json:"total_steps"`
	ReadySteps     int                     `json:"ready_steps"`
	ParallelGroups map[string][]string     `json:"parallel_groups"` // group ID -> step IDs
	Steps          map[string]*ParallelInfo `json:"steps"`
}

// analyzeMoleculeParallel performs parallel detection on a molecule subgraph.
// Returns analysis of which steps can run in parallel.
func analyzeMoleculeParallel(subgraph *MoleculeSubgraph) *ParallelAnalysis {
	analysis := &ParallelAnalysis{
		MoleculeID:     subgraph.Root.ID,
		TotalSteps:     len(subgraph.Issues),
		ParallelGroups: make(map[string][]string),
		Steps:          make(map[string]*ParallelInfo),
	}

	// Build dependency maps
	// blockedBy[id] = set of issue IDs that block this issue
	// blocks[id] = set of issue IDs that this issue blocks
	blockedBy := make(map[string]map[string]bool)
	blocks := make(map[string]map[string]bool)

	for _, issue := range subgraph.Issues {
		blockedBy[issue.ID] = make(map[string]bool)
		blocks[issue.ID] = make(map[string]bool)
	}

	// Process dependencies to find blocking relationships
	for _, dep := range subgraph.Dependencies {
		// Only blocking dependencies affect parallel execution
		if dep.Type == types.DepBlocks || dep.Type == types.DepConditionalBlocks {
			// dep.IssueID depends on (is blocked by) dep.DependsOnID
			if _, ok := blockedBy[dep.IssueID]; ok {
				blockedBy[dep.IssueID][dep.DependsOnID] = true
			}
			if _, ok := blocks[dep.DependsOnID]; ok {
				blocks[dep.DependsOnID][dep.IssueID] = true
			}
		}
	}

	// Identify which steps are ready (no open blockers)
	readySteps := make(map[string]bool)
	for _, issue := range subgraph.Issues {
		info := &ParallelInfo{
			StepID:    issue.ID,
			Status:    string(issue.Status),
			BlockedBy: []string{},
			Blocks:    []string{},
		}

		// Check what blocks this step
		for blockerID := range blockedBy[issue.ID] {
			blocker := subgraph.IssueMap[blockerID]
			if blocker != nil && blocker.Status != types.StatusClosed {
				info.BlockedBy = append(info.BlockedBy, blockerID)
			}
		}

		// Check what this step blocks
		for blockedID := range blocks[issue.ID] {
			info.Blocks = append(info.Blocks, blockedID)
		}

		// A step is ready if it's open/in_progress and has no open blockers
		info.IsReady = (issue.Status == types.StatusOpen || issue.Status == types.StatusInProgress) &&
			len(info.BlockedBy) == 0

		if info.IsReady {
			readySteps[issue.ID] = true
			analysis.ReadySteps++
		}

		// Sort for consistent output
		sort.Strings(info.BlockedBy)
		sort.Strings(info.Blocks)

		analysis.Steps[issue.ID] = info
	}

	// Identify parallel groups: steps that can run concurrently
	// Two steps can parallelize if:
	// 1. Both are ready (or will be ready at same time)
	// 2. Neither blocks the other (directly or transitively)
	// 3. They share the same blocking depth (distance from root)

	// Calculate blocking depth for each step
	depths := calculateBlockingDepths(subgraph, blockedBy)

	// Group steps by depth - steps at same depth can potentially parallelize
	depthGroups := make(map[int][]string)
	for id, depth := range depths {
		depthGroups[depth] = append(depthGroups[depth], id)
	}

	// For each depth level, identify parallel groups
	groupCounter := 0
	for depth := 0; depth <= len(subgraph.Issues); depth++ {
		stepsAtDepth := depthGroups[depth]
		if len(stepsAtDepth) == 0 {
			continue
		}

		// Group steps that can parallelize (no blocking between them)
		// Use union-find approach: start with each step in its own group
		parent := make(map[string]string)
		for _, id := range stepsAtDepth {
			parent[id] = id
		}

		find := func(x string) string {
			for parent[x] != x {
				parent[x] = parent[parent[x]]
				x = parent[x]
			}
			return x
		}

		union := func(x, y string) {
			px, py := find(x), find(y)
			if px != py {
				parent[px] = py
			}
		}

		// Merge steps that CAN parallelize (no mutual blocking)
		for i, id1 := range stepsAtDepth {
			for j := i + 1; j < len(stepsAtDepth); j++ {
				id2 := stepsAtDepth[j]
				// Can parallelize if neither blocks the other
				if !blocks[id1][id2] && !blocks[id2][id1] &&
					!blockedBy[id1][id2] && !blockedBy[id2][id1] {
					union(id1, id2)
				}
			}
		}

		// Collect groups
		groups := make(map[string][]string)
		for _, id := range stepsAtDepth {
			root := find(id)
			groups[root] = append(groups[root], id)
		}

		// Assign group names and record can_parallel relationships
		for _, members := range groups {
			if len(members) > 1 {
				groupCounter++
				groupName := fmt.Sprintf("group-%d", groupCounter)
				analysis.ParallelGroups[groupName] = members

				// Update each step's parallel info
				for _, id := range members {
					info := analysis.Steps[id]
					info.ParallelGroup = groupName
					// Record all other members as can_parallel
					for _, otherId := range members {
						if otherId != id {
							info.CanParallel = append(info.CanParallel, otherId)
						}
					}
					sort.Strings(info.CanParallel)
				}
			}
		}
	}

	return analysis
}

// calculateBlockingDepths calculates the "blocking depth" of each step.
// Depth 0 = no blockers, Depth 1 = blocked by depth-0 steps, etc.
func calculateBlockingDepths(subgraph *MoleculeSubgraph, blockedBy map[string]map[string]bool) map[string]int {
	depths := make(map[string]int)
	visited := make(map[string]bool)

	var calculateDepth func(id string) int
	calculateDepth = func(id string) int {
		if d, ok := depths[id]; ok {
			return d
		}
		if visited[id] {
			// Cycle detected, return 0 to break
			return 0
		}
		visited[id] = true

		maxBlockerDepth := -1
		for blockerID := range blockedBy[id] {
			// Only count open blockers
			blocker := subgraph.IssueMap[blockerID]
			if blocker != nil && blocker.Status != types.StatusClosed {
				blockerDepth := calculateDepth(blockerID)
				if blockerDepth > maxBlockerDepth {
					maxBlockerDepth = blockerDepth
				}
			}
		}

		depth := maxBlockerDepth + 1
		depths[id] = depth
		return depth
	}

	for _, issue := range subgraph.Issues {
		calculateDepth(issue.ID)
	}

	return depths
}

// showMoleculeWithParallel displays molecule structure with parallel annotations
func showMoleculeWithParallel(subgraph *MoleculeSubgraph) {
	analysis := analyzeMoleculeParallel(subgraph)

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"root":         subgraph.Root,
			"issues":       subgraph.Issues,
			"dependencies": subgraph.Dependencies,
			"variables":    extractAllVariables(subgraph),
			"parallel":     analysis,
			"is_compound":  subgraph.Root.IsCompound(),
			"bonded_from":  subgraph.Root.BondedFrom,
		})
		return
	}

	// Determine molecule type label
	moleculeType := "Molecule"
	if subgraph.Root.IsCompound() {
		moleculeType = "Compound"
	}

	fmt.Printf("\n%s %s: %s\n", ui.RenderAccent("🧪"), moleculeType, subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", subgraph.Root.ID)
	fmt.Printf("   Steps: %d (%d ready)\n", analysis.TotalSteps, analysis.ReadySteps)

	// Show compound bonding info if this is a compound molecule
	if subgraph.Root.IsCompound() {
		showCompoundBondingInfo(subgraph.Root)
	}

	// Show parallel groups summary
	if len(analysis.ParallelGroups) > 0 {
		fmt.Printf("\n%s Parallel Groups:\n", ui.RenderPass("⚡"))
		for groupName, members := range analysis.ParallelGroups {
			fmt.Printf("   %s: %s\n", groupName, strings.Join(members, ", "))
		}
	}

	vars := extractAllVariables(subgraph)
	if len(vars) > 0 {
		fmt.Printf("\n%s Variables:\n", ui.RenderWarn("📝"))
		for _, v := range vars {
			fmt.Printf("   {{%s}}\n", v)
		}
	}

	fmt.Printf("\n%s Structure:\n", ui.RenderPass("🌲"))
	printMoleculeTreeWithParallel(subgraph, analysis, subgraph.Root.ID, 0, true)
	fmt.Println()
}

// printMoleculeTreeWithParallel prints the molecule structure with parallel annotations
func printMoleculeTreeWithParallel(subgraph *MoleculeSubgraph, analysis *ParallelAnalysis, parentID string, depth int, isRoot bool) {
	indent := strings.Repeat("  ", depth)

	// Print root with parallel info
	if isRoot {
		rootInfo := analysis.Steps[subgraph.Root.ID]
		annotation := getParallelAnnotation(rootInfo)
		fmt.Printf("%s   %s%s\n", indent, subgraph.Root.Title, annotation)
	}

	// Find children of this parent
	var children []*types.Issue
	for _, dep := range subgraph.Dependencies {
		if dep.DependsOnID == parentID && dep.Type == types.DepParentChild {
			if child, ok := subgraph.IssueMap[dep.IssueID]; ok {
				children = append(children, child)
			}
		}
	}

	// Print children
	for i, child := range children {
		connector := "├──"
		if i == len(children)-1 {
			connector = "└──"
		}

		info := analysis.Steps[child.ID]
		annotation := getParallelAnnotation(info)

		fmt.Printf("%s   %s %s%s\n", indent, connector, child.Title, annotation)
		printMoleculeTreeWithParallel(subgraph, analysis, child.ID, depth+1, false)
	}
}

// getParallelAnnotation returns the annotation string for a step's parallel status
func getParallelAnnotation(info *ParallelInfo) string {
	if info == nil {
		return ""
	}

	parts := []string{}

	// Status indicator
	switch info.Status {
	case string(types.StatusOpen):
		if info.IsReady {
			parts = append(parts, ui.RenderPass("ready"))
		} else {
			parts = append(parts, ui.RenderFail("blocked"))
		}
	case string(types.StatusInProgress):
		parts = append(parts, ui.RenderWarn("in_progress"))
	case string(types.StatusClosed):
		parts = append(parts, ui.RenderPass("completed"))
	}

	// Parallel group
	if info.ParallelGroup != "" {
		parts = append(parts, ui.RenderAccent(info.ParallelGroup))
	}

	// Blocking info
	if len(info.BlockedBy) > 0 {
		parts = append(parts, fmt.Sprintf("needs: %s", strings.Join(info.BlockedBy, ", ")))
	}

	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " | ") + "]"
}

func init() {
	molShowCmd.Flags().BoolVarP(&molShowParallel, "parallel", "p", false, "Show parallel step analysis")
	molCmd.AddCommand(molShowCmd)
}
