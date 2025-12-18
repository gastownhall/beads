package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// GraphNode represents a node in the rendered graph
type GraphNode struct {
	Issue    *types.Issue
	Layer    int      // Horizontal layer (topological order)
	Position int      // Vertical position within layer
	DependsOn []string // IDs this node depends on (blocks dependencies only)
}

// GraphLayout holds the computed graph layout
type GraphLayout struct {
	Nodes      map[string]*GraphNode
	Layers     [][]string // Layer index -> node IDs in that layer
	MaxLayer   int
	RootID     string
}

var graphCmd = &cobra.Command{
	Use:   "graph <issue-id>",
	Short: "Display issue dependency graph",
	Long: `Display an ASCII visualization of an issue's dependency graph.

For epics, shows all children and their dependencies.
For regular issues, shows the issue and its direct dependencies.

The graph shows execution order left-to-right:
- Leftmost nodes have no dependencies (can start immediately)
- Rightmost nodes depend on everything to their left
- Nodes in the same column can run in parallel

Colors indicate status:
- White: open (ready to work)
- Yellow: in progress
- Red: blocked
- Green: closed`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		var issueID string

		// Resolve the issue ID
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: issue '%s' not found\n", args[0])
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &issueID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store != nil {
			var err error
			issueID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: issue '%s' not found\n", args[0])
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		// Load the subgraph
		subgraph, err := loadGraphSubgraph(ctx, store, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading graph: %v\n", err)
			os.Exit(1)
		}

		// Compute layout
		layout := computeLayout(subgraph)

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"root":    subgraph.Root,
				"issues":  subgraph.Issues,
				"layout":  layout,
			})
			return
		}

		// Render ASCII graph
		renderGraph(layout, subgraph)
	},
}

func init() {
	rootCmd.AddCommand(graphCmd)
}

// loadGraphSubgraph loads an issue and its subgraph for visualization
// Reuses template subgraph loading logic
func loadGraphSubgraph(ctx context.Context, s storage.Storage, issueID string) (*TemplateSubgraph, error) {
	return loadTemplateSubgraph(ctx, s, issueID)
}

// computeLayout assigns layers to nodes using topological sort
func computeLayout(subgraph *TemplateSubgraph) *GraphLayout {
	layout := &GraphLayout{
		Nodes:  make(map[string]*GraphNode),
		RootID: subgraph.Root.ID,
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

	cyan := color.New(color.FgCyan).SprintFunc()
	fmt.Printf("\n%s Dependency graph for %s:\n\n", cyan("📊"), layout.RootID)

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

	// Render layers left to right
	layerBoxes := make([][]string, len(layout.Layers))

	for layerIdx, layer := range layout.Layers {
		var boxes []string
		for _, id := range layer {
			node := layout.Nodes[id]
			box := renderNodeBox(node, boxWidth)
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

	// Show summary
	fmt.Printf("  Total: %d issues across %d layers\n\n", len(layout.Nodes), len(layout.Layers))
}

// renderNodeBox renders a single node as an ASCII box
func renderNodeBox(node *GraphNode, width int) string {
	// Status indicator
	var statusIcon string
	var colorFn func(a ...interface{}) string

	switch node.Issue.Status {
	case types.StatusOpen:
		statusIcon = "○"
		colorFn = color.New(color.FgWhite).SprintFunc()
	case types.StatusInProgress:
		statusIcon = "◐"
		colorFn = color.New(color.FgYellow).SprintFunc()
	case types.StatusBlocked:
		statusIcon = "●"
		colorFn = color.New(color.FgRed).SprintFunc()
	case types.StatusClosed:
		statusIcon = "✓"
		colorFn = color.New(color.FgGreen).SprintFunc()
	default:
		statusIcon = "?"
		colorFn = color.New(color.FgWhite).SprintFunc()
	}

	title := truncateTitle(node.Issue.Title, width-4)
	id := node.Issue.ID

	// Build the box
	topBottom := "  ┌" + strings.Repeat("─", width) + "┐"
	middle := fmt.Sprintf("  │ %s %s │", statusIcon, colorFn(padRight(title, width-4)))
	idLine := fmt.Sprintf("  │ %s │", color.New(color.FgHiBlack).Sprint(padRight(id, width-2)))
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
