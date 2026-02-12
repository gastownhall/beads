package main

import (
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// dagEdgeInfo represents a directed edge routed through a gutter column
type dagEdgeInfo struct {
	sourceRow int
	targetRow int
}

// renderGraphVisual renders a terminal-native DAG with nodes arranged in
// layer columns (left-to-right) and box-drawing edges between them.
// Each layer is a vertical column of node boxes, with edges drawn in
// gutter areas between columns.
func renderGraphVisual(layout *GraphLayout, subgraph *TemplateSubgraph) {
	if len(layout.Nodes) == 0 {
		fmt.Println("Empty graph")
		return
	}

	fmt.Printf("\n%s Dependency graph for %s:\n\n", ui.RenderAccent("📊"), layout.RootID)
	fmt.Println("  Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred")
	fmt.Println()

	numLayers := len(layout.Layers)
	if numLayers == 0 {
		return
	}

	// Calculate consistent node box width
	nodeW := computeDAGNodeWidth(layout)

	// Max rows in any layer
	maxRows := 0
	for _, layer := range layout.Layers {
		if len(layer) > maxRows {
			maxRows = len(layer)
		}
	}

	// Collect edges for each gutter between adjacent layers
	gutterEdges := collectGutterEdges(layout, subgraph, numLayers)

	// Rendering dimensions
	const nodeH = 4  // top border, title, id, bottom border
	const rowGap = 1 // gap between vertically stacked nodes
	bandH := nodeH + rowGap
	gutterW := 6

	totalLines := maxRows*bandH - rowGap // no trailing gap after last row

	// Precompute gutter grids (one string per output line per gutter)
	gutterGrids := make([][]string, numLayers-1)
	for g := 0; g < numLayers-1; g++ {
		gutterGrids[g] = buildDAGGutterGrid(gutterEdges[g], gutterW, totalLines, bandH)
	}

	// Render layer headers
	var headerLine strings.Builder
	headerLine.WriteString("  ")
	for layerIdx := 0; layerIdx < numLayers; layerIdx++ {
		header := fmt.Sprintf("LAYER %d", layerIdx)
		if layerIdx == 0 {
			header += " (ready)"
		}
		colW := nodeW + 2 // node content + border chars
		headerLine.WriteString(ui.RenderAccent(padRight(header, colW)))
		if layerIdx < numLayers-1 {
			headerLine.WriteString(strings.Repeat(" ", gutterW))
		}
	}
	fmt.Println(headerLine.String())
	fmt.Println()

	// Render each output line
	for y := 0; y < totalLines; y++ {
		row := y / bandH
		subLine := y % bandH

		var line strings.Builder
		line.WriteString("  ") // indent

		for layerIdx := 0; layerIdx < numLayers; layerIdx++ {
			layer := layout.Layers[layerIdx]

			if subLine < nodeH && row < len(layer) {
				id := layer[row]
				node := layout.Nodes[id]
				line.WriteString(dagNodeLine(node, nodeW, subLine))
			} else {
				// Empty space (no node at this position)
				line.WriteString(strings.Repeat(" ", nodeW+2))
			}

			// Render gutter between this layer and next
			if layerIdx < numLayers-1 {
				if y < len(gutterGrids[layerIdx]) {
					line.WriteString(gutterGrids[layerIdx][y])
				} else {
					line.WriteString(strings.Repeat(" ", gutterW))
				}
			}
		}

		fmt.Println(strings.TrimRight(line.String(), " "))
	}

	fmt.Println()

	// Summary
	blocksDeps := 0
	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepBlocks {
			blocksDeps++
		}
	}
	if blocksDeps > 0 {
		fmt.Printf("  Dependencies: %d blocking relationships\n", blocksDeps)
	}
	fmt.Printf("  Total: %d issues across %d layers\n\n", len(layout.Nodes), len(layout.Layers))
}

// computeDAGNodeWidth calculates a consistent width for all DAG node boxes
func computeDAGNodeWidth(layout *GraphLayout) int {
	maxW := 0
	for _, node := range layout.Nodes {
		titleLen := len([]rune(truncateTitle(node.Issue.Title, 22)))
		contentW := titleLen + 3      // icon(1) + space(1) + trailing(1)
		idW := len(node.Issue.ID) + 4 // space + ID + "  Pn"
		if idW > contentW {
			contentW = idW
		}
		if contentW > maxW {
			maxW = contentW
		}
	}
	w := maxW + 2 // inner padding
	if w < 18 {
		w = 18
	}
	return w
}

// collectGutterEdges organizes blocking dependencies by gutter index.
// For edges spanning multiple layers, intermediate gutters get pass-through entries.
func collectGutterEdges(layout *GraphLayout, subgraph *TemplateSubgraph, numLayers int) [][]dagEdgeInfo {
	result := make([][]dagEdgeInfo, numLayers-1)

	// Deduplicate edges per gutter
	type edgeKey struct{ s, t int }
	seen := make([]map[edgeKey]bool, numLayers-1)
	for i := range seen {
		seen[i] = make(map[edgeKey]bool)
	}

	for _, dep := range subgraph.Dependencies {
		if dep.Type != types.DepBlocks {
			continue
		}
		src := layout.Nodes[dep.DependsOnID]
		tgt := layout.Nodes[dep.IssueID]
		if src == nil || tgt == nil {
			continue
		}
		if tgt.Layer <= src.Layer {
			continue
		}

		// Route through each gutter between source and target layers
		for g := src.Layer; g < tgt.Layer; g++ {
			var sRow, tRow int

			if g == src.Layer {
				sRow = src.Position // edge exits from source row
			} else {
				sRow = tgt.Position // intermediate: pass through at target's row
			}

			if g == tgt.Layer-1 {
				tRow = tgt.Position // edge arrives at target row
			} else {
				tRow = tgt.Position // intermediate: route toward target
			}

			key := edgeKey{sRow, tRow}
			if !seen[g][key] {
				seen[g][key] = true
				result[g] = append(result[g], dagEdgeInfo{sourceRow: sRow, targetRow: tRow})
			}
		}
	}

	return result
}

// dagNodeLine renders one line of a DAG node box with status colors
func dagNodeLine(node *GraphNode, nodeW, lineIdx int) string {
	switch lineIdx {
	case 0: // top border
		return "┌" + strings.Repeat("─", nodeW) + "┐"

	case 1: // status icon + title
		icon := ui.RenderStatusIcon(string(node.Issue.Status))
		title := truncateTitle(node.Issue.Title, nodeW-4) // room for icon + spaces
		padded := padRight(title, nodeW-4)

		status := string(node.Issue.Status)
		style := ui.GetStatusStyle(status)
		styled := padded
		if node.Issue.Status != types.StatusOpen {
			styled = style.Render(padded)
		}
		return fmt.Sprintf("│ %s %s │", icon, styled)

	case 2: // ID + priority
		idPri := fmt.Sprintf("%s P%d", node.Issue.ID, node.Issue.Priority)
		return "│ " + ui.RenderMuted(padRight(idPri, nodeW-2)) + " │"

	case 3: // bottom border
		return "└" + strings.Repeat("─", nodeW) + "┘"

	default:
		return strings.Repeat(" ", nodeW+2)
	}
}

// buildDAGGutterGrid precomputes the edge routing display for a gutter.
// Returns one string per output line, containing box-drawing characters
// that connect nodes between adjacent layer columns.
func buildDAGGutterGrid(edges []dagEdgeInfo, gutterW, totalLines, bandH int) []string {
	// Create a rune grid
	grid := make([][]rune, totalLines)
	for i := range grid {
		grid[i] = make([]rune, gutterW)
		for j := range grid[i] {
			grid[i][j] = ' '
		}
	}

	contentOffset := 1 // subLine 1 within each band is the content/title line

	// Assign channels (x positions) to edges needing vertical routing
	var verticalEdgeIndices []int
	for i, e := range edges {
		if e.sourceRow != e.targetRow {
			verticalEdgeIndices = append(verticalEdgeIndices, i)
		}
	}

	channelPositions := make(map[int]int) // edge index -> x position in gutter
	for ci, ei := range verticalEdgeIndices {
		if len(verticalEdgeIndices) == 1 {
			channelPositions[ei] = gutterW / 2
		} else {
			// Spread channels evenly, leaving margins
			channelPositions[ei] = 1 + ci*(gutterW-3)/(len(verticalEdgeIndices)-1)
		}
	}

	for i, edge := range edges {
		sourceY := edge.sourceRow*bandH + contentOffset
		targetY := edge.targetRow*bandH + contentOffset

		// Bounds check
		if sourceY >= totalLines || targetY >= totalLines {
			continue
		}

		if sourceY == targetY {
			// Same row: straight horizontal arrow
			for x := 0; x < gutterW-1; x++ {
				grid[sourceY][x] = dagMergeRune(grid[sourceY][x], '─')
			}
			grid[sourceY][gutterW-1] = '▶'
		} else {
			chX := channelPositions[i]
			minY, maxY := sourceY, targetY
			if minY > maxY {
				minY, maxY = maxY, minY
			}

			// Horizontal from left edge to channel at sourceY
			for x := 0; x < chX; x++ {
				grid[sourceY][x] = dagMergeRune(grid[sourceY][x], '─')
			}

			// Corner at (chX, sourceY)
			if sourceY < targetY {
				grid[sourceY][chX] = dagMergeRune(grid[sourceY][chX], '╮')
			} else {
				grid[sourceY][chX] = dagMergeRune(grid[sourceY][chX], '╯')
			}

			// Vertical line between source and target
			for y := minY + 1; y < maxY; y++ {
				grid[y][chX] = dagMergeRune(grid[y][chX], '│')
			}

			// Corner at (chX, targetY)
			if sourceY < targetY {
				grid[targetY][chX] = dagMergeRune(grid[targetY][chX], '╰')
			} else {
				grid[targetY][chX] = dagMergeRune(grid[targetY][chX], '╭')
			}

			// Horizontal from channel to right edge at targetY
			for x := chX + 1; x < gutterW-1; x++ {
				grid[targetY][x] = dagMergeRune(grid[targetY][x], '─')
			}
			grid[targetY][gutterW-1] = '▶'
		}
	}

	// Convert rune grid to strings
	result := make([]string, totalLines)
	for y := range grid {
		result[y] = string(grid[y])
	}
	return result
}

// dagMergeRune merges a new character into an existing cell, handling overlaps
func dagMergeRune(existing, incoming rune) rune {
	if existing == ' ' {
		return incoming
	}

	// Handle crossing: horizontal meets vertical
	if (existing == '│' && incoming == '─') || (existing == '─' && incoming == '│') {
		return '┼'
	}

	// Arrow always wins
	if incoming == '▶' {
		return '▶'
	}

	// Horizontal merges
	if existing == '─' && incoming == '─' {
		return '─'
	}

	// Vertical merges
	if existing == '│' && incoming == '│' {
		return '│'
	}

	// T-junctions: vertical + corner
	if existing == '│' && (incoming == '╮' || incoming == '╯') {
		return '┤'
	}
	if existing == '│' && (incoming == '╰' || incoming == '╭') {
		return '├'
	}

	// Horizontal + corner
	if existing == '─' && (incoming == '╮' || incoming == '╭') {
		return '┬'
	}
	if existing == '─' && (incoming == '╰' || incoming == '╯') {
		return '┴'
	}

	// Default: new character wins
	return incoming
}
