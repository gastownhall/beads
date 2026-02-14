package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRenderGraphVisual(t *testing.T) {
	// No t.Parallel() — captureGraphOutput swaps global os.Stdout
	subgraph, layout := makeTestSubgraph()

	output := captureGraphOutput(func() {
		renderGraphVisual(layout, subgraph)
	})

	// Verify layer headers
	if !strings.Contains(output, "LAYER 0") {
		t.Error("Visual output should contain LAYER 0 header")
	}
	if !strings.Contains(output, "ready") {
		t.Error("Visual output should mark layer 0 as ready")
	}

	// Verify all node IDs are present
	for _, id := range []string{"test-a", "test-b", "test-c", "test-d"} {
		if !strings.Contains(output, id) {
			t.Errorf("Visual output should contain node %q", id)
		}
	}

	// Verify node titles are present
	for _, title := range []string{"Root issue", "Child task", "Blocked task", "Done task"} {
		if !strings.Contains(output, title) {
			t.Errorf("Visual output should contain title %q", title)
		}
	}

	// Verify box-drawing characters are used
	if !strings.Contains(output, "┌") || !strings.Contains(output, "┘") {
		t.Error("Visual output should use box-drawing characters for node borders")
	}

	// Verify edge arrows exist (edges between layers)
	if !strings.Contains(output, "▶") {
		t.Error("Visual output should contain arrow characters for edges")
	}

	// Verify summary
	if !strings.Contains(output, "layers") {
		t.Error("Visual output should contain summary with layer count")
	}
}

func TestRenderGraphVisual_Empty(t *testing.T) {
	emptySubgraph := &TemplateSubgraph{
		Root:     &types.Issue{ID: "empty"},
		Issues:   []*types.Issue{},
		IssueMap: map[string]*types.Issue{},
	}
	layout := &GraphLayout{
		Nodes:  map[string]*GraphNode{},
		Layers: [][]string{},
		RootID: "empty",
	}

	output := captureGraphOutput(func() {
		renderGraphVisual(layout, emptySubgraph)
	})

	if !strings.Contains(output, "Empty graph") {
		t.Error("Empty visual output should say 'Empty graph'")
	}
}

func TestRenderGraphVisual_SingleNode(t *testing.T) {
	issue := &types.Issue{
		ID: "solo-1", Title: "Solo issue", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeTask,
	}
	subgraph := &TemplateSubgraph{
		Root:     issue,
		Issues:   []*types.Issue{issue},
		IssueMap: map[string]*types.Issue{"solo-1": issue},
	}
	layout := computeLayout(subgraph)

	output := captureGraphOutput(func() {
		renderGraphVisual(layout, subgraph)
	})

	if !strings.Contains(output, "solo-1") {
		t.Error("Single-node visual should contain the issue ID")
	}
	if !strings.Contains(output, "Solo issue") {
		t.Error("Single-node visual should contain the title")
	}
	// No edges for single node
	if strings.Contains(output, "▶") {
		t.Error("Single-node visual should not contain edge arrows")
	}
}

func TestDAGNodeLine(t *testing.T) {
	t.Parallel()
	node := &GraphNode{
		Issue: &types.Issue{
			ID: "test-1", Title: "Test Node", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		},
	}
	nodeW := 20

	// Top border
	top := dagNodeLine(node, nodeW, 0)
	if !strings.HasPrefix(top, "┌") || !strings.HasSuffix(top, "┐") {
		t.Errorf("Top border should be ┌───┐, got: %s", top)
	}

	// Title line
	title := dagNodeLine(node, nodeW, 1)
	if !strings.HasPrefix(title, "│") || !strings.HasSuffix(title, "│") {
		t.Error("Title line should be bordered with │")
	}
	if !strings.Contains(title, "Test Node") {
		t.Error("Title line should contain the issue title")
	}

	// ID line
	idLine := dagNodeLine(node, nodeW, 2)
	if !strings.Contains(idLine, "test-1") {
		t.Error("ID line should contain the issue ID")
	}
	if !strings.Contains(idLine, "P2") {
		t.Error("ID line should contain priority")
	}

	// Bottom border
	bottom := dagNodeLine(node, nodeW, 3)
	if !strings.HasPrefix(bottom, "└") || !strings.HasSuffix(bottom, "┘") {
		t.Errorf("Bottom border should be └───┘, got: %s", bottom)
	}
}

func TestComputeDAGNodeWidth(t *testing.T) {
	t.Parallel()
	_, layout := makeTestSubgraph()

	w := computeDAGNodeWidth(layout)
	if w < 18 {
		t.Errorf("DAG node width should be at least 18, got %d", w)
	}
}

func TestCollectGutterEdges(t *testing.T) {
	t.Parallel()
	subgraph, layout := makeTestSubgraph()

	numLayers := len(layout.Layers)
	edges := collectGutterEdges(layout, subgraph, numLayers)

	// Should have gutter entries for each layer gap
	if len(edges) != numLayers-1 {
		t.Errorf("Expected %d gutter edge lists, got %d", numLayers-1, len(edges))
	}

	// Should have at least one edge (test-a -> test-b in gutter 0)
	totalEdges := 0
	for _, ge := range edges {
		totalEdges += len(ge)
	}
	if totalEdges == 0 {
		t.Error("Should have at least one edge in gutters")
	}
}

func TestBuildDAGGutterGrid_SameRow(t *testing.T) {
	t.Parallel()
	edges := []dagEdgeInfo{{sourceRow: 0, targetRow: 0}}
	gutterW := 6
	bandH := 5
	totalLines := 5

	lines := buildDAGGutterGrid(edges, gutterW, totalLines, bandH)

	if len(lines) != totalLines {
		t.Errorf("Expected %d gutter lines, got %d", totalLines, len(lines))
	}

	// Content line (offset 1) should have horizontal arrow
	contentLine := lines[1]
	if !strings.Contains(contentLine, "▶") {
		t.Errorf("Same-row edge should have arrow, got: %q", contentLine)
	}
	if !strings.Contains(contentLine, "─") {
		t.Errorf("Same-row edge should have horizontal line, got: %q", contentLine)
	}
}

func TestBuildDAGGutterGrid_DifferentRow(t *testing.T) {
	t.Parallel()
	edges := []dagEdgeInfo{{sourceRow: 0, targetRow: 1}}
	gutterW := 6
	bandH := 5
	totalLines := 10

	lines := buildDAGGutterGrid(edges, gutterW, totalLines, bandH)

	// Source content line (row 0, subline 1) should have outgoing edge
	sourceLine := lines[1]
	if !strings.Contains(sourceLine, "─") {
		t.Errorf("Source row should have horizontal connector, got: %q", sourceLine)
	}
	if !strings.Contains(sourceLine, "╮") {
		t.Errorf("Source row should have corner character, got: %q", sourceLine)
	}

	// Target content line (row 1, subline 1 = line 6) should have incoming edge
	targetLine := lines[6]
	if !strings.Contains(targetLine, "▶") {
		t.Errorf("Target row should have arrow, got: %q", targetLine)
	}
	if !strings.Contains(targetLine, "╰") {
		t.Errorf("Target row should have corner character, got: %q", targetLine)
	}

	// Intermediate lines should have vertical connector
	hasVertical := false
	for y := 2; y < 6; y++ {
		if strings.Contains(lines[y], "│") {
			hasVertical = true
			break
		}
	}
	if !hasVertical {
		t.Error("Intermediate lines should have vertical connector")
	}
}

func TestDAGMergeRune(t *testing.T) {
	t.Parallel()
	tests := []struct {
		existing, incoming rune
		want               rune
	}{
		{' ', '─', '─'},
		{' ', '│', '│'},
		{'│', '─', '┼'},
		{'─', '│', '┼'},
		{'─', '▶', '▶'},
		{'─', '─', '─'},
		{'│', '│', '│'},
		{'│', '╮', '┤'},
		{'│', '╰', '├'},
	}

	for _, tt := range tests {
		got := dagMergeRune(tt.existing, tt.incoming)
		if got != tt.want {
			t.Errorf("dagMergeRune(%c, %c) = %c, want %c", tt.existing, tt.incoming, got, tt.want)
		}
	}
}

func TestCollectGutterEdges_SkipLayers(t *testing.T) {
	t.Parallel()
	// Create a graph where an edge skips a layer and goes to a different row.
	// A(L0,row0) -> B(L1,row0) -> D(L2)
	// A(L0,row0) -> C(L1,row1) -> D(L2)
	// The edge routing should handle multiple layers correctly.
	issueA := &types.Issue{ID: "a", Title: "A", Status: types.StatusOpen, IssueType: types.TypeTask}
	issueB := &types.Issue{ID: "b", Title: "B", Status: types.StatusOpen, IssueType: types.TypeTask}
	issueC := &types.Issue{ID: "c", Title: "C", Status: types.StatusOpen, IssueType: types.TypeTask}
	issueD := &types.Issue{ID: "d", Title: "D", Status: types.StatusOpen, IssueType: types.TypeTask}

	subgraph := &TemplateSubgraph{
		Root:   issueA,
		Issues: []*types.Issue{issueA, issueB, issueC, issueD},
		IssueMap: map[string]*types.Issue{
			"a": issueA, "b": issueB, "c": issueC, "d": issueD,
		},
		Dependencies: []*types.Dependency{
			{IssueID: "b", DependsOnID: "a", Type: types.DepBlocks},
			{IssueID: "c", DependsOnID: "a", Type: types.DepBlocks},
			{IssueID: "d", DependsOnID: "b", Type: types.DepBlocks},
			{IssueID: "d", DependsOnID: "c", Type: types.DepBlocks},
		},
	}

	layout := computeLayout(subgraph)
	numLayers := len(layout.Layers)

	edges := collectGutterEdges(layout, subgraph, numLayers)

	// Should have gutter entries
	if numLayers < 2 {
		t.Fatalf("Expected at least 2 layers, got %d", numLayers)
	}

	// Gutter 0 should have edges for a->b and a->c
	if len(edges[0]) < 2 {
		t.Errorf("Gutter 0 should have at least 2 edges (a->b and a->c), got %d", len(edges[0]))
	}
}
