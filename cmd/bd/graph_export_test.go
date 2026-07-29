package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// captureGraphOutput captures stdout output during f() execution
func captureGraphOutput(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func makeTestSubgraph() (*TemplateSubgraph, *GraphLayout) {
	issueA := &types.Issue{
		ID: "test-a", Title: "Root issue", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeEpic,
	}
	issueB := &types.Issue{
		ID: "test-b", Title: "Child task", Status: types.StatusInProgress,
		Priority: 1, IssueType: types.TypeTask, Assignee: "alice",
	}
	issueC := &types.Issue{
		ID: "test-c", Title: "Blocked task", Status: types.StatusBlocked,
		Priority: 2, IssueType: types.TypeBug,
	}
	issueD := &types.Issue{
		ID: "test-d", Title: "Done task", Status: types.StatusClosed,
		Priority: 1, IssueType: types.TypeTask,
	}

	subgraph := &TemplateSubgraph{
		Root:   issueA,
		Issues: []*types.Issue{issueA, issueB, issueC, issueD},
		IssueMap: map[string]*types.Issue{
			"test-a": issueA, "test-b": issueB,
			"test-c": issueC, "test-d": issueD,
		},
		Dependencies: []*types.Dependency{
			{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepBlocks},
			{IssueID: "test-c", DependsOnID: "test-b", Type: types.DepBlocks},
			{IssueID: "test-b", DependsOnID: "test-a", Type: types.DepParentChild},
		},
	}

	layout := computeLayout(subgraph)
	return subgraph, layout
}

func TestRenderGraphDOT(t *testing.T) {
	// Not parallel: captureGraphOutput redirects global os.Stdout
	subgraph, layout := makeTestSubgraph()

	output := captureGraphOutput(func() {
		renderGraphDOT(layout, subgraph)
	})

	// Verify DOT structure
	if !strings.HasPrefix(output, "digraph beads {") {
		t.Error("DOT output should start with 'digraph beads {'")
	}
	if !strings.Contains(output, "rankdir=LR") {
		t.Error("DOT output should specify left-to-right layout")
	}

	// Verify nodes are present
	for _, id := range []string{"test-a", "test-b", "test-c", "test-d"} {
		if !strings.Contains(output, fmt.Sprintf("\"%s\"", id)) {
			t.Errorf("DOT output should contain node %q", id)
		}
	}

	// Verify edges exist
	if !strings.Contains(output, "\"test-a\" -> \"test-b\"") {
		t.Error("DOT output should contain edge test-a -> test-b")
	}
	if !strings.Contains(output, "\"test-b\" -> \"test-c\"") {
		t.Error("DOT output should contain edge test-b -> test-c")
	}

	// Verify it ends with closing brace
	if !strings.HasSuffix(strings.TrimSpace(output), "}") {
		t.Error("DOT output should end with '}'")
	}
}

func TestRenderGraphDOT_Empty(t *testing.T) {
	// Not parallel: captureGraphOutput redirects global os.Stdout
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
		renderGraphDOT(layout, emptySubgraph)
	})

	if !strings.Contains(output, "digraph beads { }") {
		t.Errorf("Empty DOT output should be 'digraph beads { }', got: %s", output)
	}
}

func TestDotNodeAttrs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    types.Status
		wantColor string
	}{
		{types.StatusOpen, "#e8f4fd"},
		{types.StatusInProgress, "#fff3cd"},
		{types.StatusBlocked, "#f8d7da"},
		{types.StatusClosed, "#d4edda"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			node := &GraphNode{
				Issue: &types.Issue{
					ID: "test", Title: "Test", Status: tt.status,
					Priority: 1, IssueType: types.TypeTask,
				},
			}
			_, fillColor, _ := dotNodeAttrs(node)
			if fillColor != tt.wantColor {
				t.Errorf("status %s: fillColor = %s, want %s", tt.status, fillColor, tt.wantColor)
			}
		})
	}
}

func TestStatusPlainIcon(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status types.Status
		want   string
	}{
		{types.StatusOpen, "○"},
		{types.StatusInProgress, "◐"},
		{types.StatusBlocked, "●"},
		{types.StatusClosed, "✓"},
	}

	for _, tt := range tests {
		got := statusPlainIcon(tt.status)
		if got != tt.want {
			t.Errorf("statusPlainIcon(%s) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestRenderGraphHTML(t *testing.T) {
	// Not parallel: captureGraphOutput redirects global os.Stdout
	subgraph, layout := makeTestSubgraph()

	output := captureGraphOutput(func() {
		renderGraphHTML(layout, subgraph)
	})

	// Verify HTML structure
	if !strings.Contains(output, "<!DOCTYPE html>") {
		t.Error("HTML output should contain DOCTYPE")
	}
	if !strings.Contains(output, "d3.v7.min.js") {
		t.Error("HTML output should reference D3.js")
	}

	// Verify node data is embedded
	for _, id := range []string{"test-a", "test-b", "test-c", "test-d"} {
		if !strings.Contains(output, id) {
			t.Errorf("HTML output should contain node %q", id)
		}
	}

	// Verify it contains all statuses
	if !strings.Contains(output, "open") {
		t.Error("HTML should contain open status")
	}
	if !strings.Contains(output, "in_progress") {
		t.Error("HTML should contain in_progress status")
	}

	// Verify interactive elements
	if !strings.Contains(output, "forceSimulation") {
		t.Error("HTML should contain D3 force simulation")
	}
	if !strings.Contains(output, "tooltip") {
		t.Error("HTML should contain tooltip")
	}
}

func TestBuildHTMLGraphData(t *testing.T) {
	t.Parallel()
	subgraph, layout := makeTestSubgraph()

	nodes := buildHTMLGraphData(layout, subgraph)

	if len(nodes) != 4 {
		t.Errorf("Expected 4 nodes, got %d", len(nodes))
	}

	// Find the in_progress node and check assignee
	found := false
	for _, n := range nodes {
		if n.ID == "test-b" {
			found = true
			if n.Assignee != "alice" {
				t.Errorf("test-b assignee = %q, want 'alice'", n.Assignee)
			}
			if n.Status != "in_progress" {
				t.Errorf("test-b status = %q, want 'in_progress'", n.Status)
			}
		}
	}
	if !found {
		t.Error("test-b node not found in HTML data")
	}
}

func TestBuildHTMLEdgeData(t *testing.T) {
	t.Parallel()
	subgraph, layout := makeTestSubgraph()

	edges := buildHTMLEdgeData(layout, subgraph)

	// Should have 3 edges (2 blocks + 1 parent-child)
	if len(edges) != 3 {
		t.Errorf("Expected 3 edges, got %d", len(edges))
	}

	// Verify edge types
	hasBlocks := false
	hasParentChild := false
	for _, e := range edges {
		if e.Type == "blocks" {
			hasBlocks = true
		}
		if e.Type == "parent-child" {
			hasParentChild = true
		}
	}
	if !hasBlocks {
		t.Error("Should have blocks edge")
	}
	if !hasParentChild {
		t.Error("Should have parent-child edge")
	}
}

func TestDotEdgeStyle(t *testing.T) {
	t.Parallel()
	blocks := dotEdgeStyle(types.DepBlocks)
	if !strings.Contains(blocks, "solid") {
		t.Error("blocks edge should be solid")
	}

	parentChild := dotEdgeStyle(types.DepParentChild)
	if !strings.Contains(parentChild, "dashed") {
		t.Error("parent-child edge should be dashed")
	}

	related := dotEdgeStyle(types.DepRelated)
	if related != "" {
		t.Errorf("related edge should have no style, got %q", related)
	}
}

func TestMergeSubgraphsForHTML_SingleDOCTYPE(t *testing.T) {
	// Not parallel: captureGraphOutput redirects global os.Stdout

	// Create two disconnected subgraphs (separate components)
	issueA := &types.Issue{
		ID: "comp-a", Title: "Component A", Status: types.StatusOpen,
		Priority: 1, IssueType: types.TypeTask,
	}
	issueB := &types.Issue{
		ID: "comp-b", Title: "Component B", Status: types.StatusInProgress,
		Priority: 2, IssueType: types.TypeTask,
	}

	sg1 := &TemplateSubgraph{
		Root:     issueA,
		Issues:   []*types.Issue{issueA},
		IssueMap: map[string]*types.Issue{"comp-a": issueA},
	}
	sg2 := &TemplateSubgraph{
		Root:     issueB,
		Issues:   []*types.Issue{issueB},
		IssueMap: map[string]*types.Issue{"comp-b": issueB},
	}

	merged := mergeSubgraphsForHTML([]*TemplateSubgraph{sg1, sg2})
	layout := computeLayout(merged)

	output := captureGraphOutput(func() {
		renderGraphHTML(layout, merged)
	})

	// Must contain exactly one DOCTYPE declaration
	count := strings.Count(output, "<!DOCTYPE html>")
	if count != 1 {
		t.Errorf("expected exactly 1 <!DOCTYPE html>, got %d", count)
	}

	// Both issues must appear in the single document
	if !strings.Contains(output, "comp-a") {
		t.Error("merged HTML should contain comp-a")
	}
	if !strings.Contains(output, "comp-b") {
		t.Error("merged HTML should contain comp-b")
	}

	// links must be [] not null — null breaks d3.forceLink (GH#3592)
	if strings.Contains(output, "const links = null") {
		t.Error("links must be [] not null for d3 compatibility")
	}
	if !strings.Contains(output, "const links = []") {
		t.Error("empty links should serialize as [] not null")
	}
}

func TestRenderGraphHTML_EmptyEdgesNotNull(t *testing.T) {
	// Verify that a single-node graph emits [] not null for links (GH#3592)
	issue := &types.Issue{
		ID: "solo-1", Title: "Solo node", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}

	subgraph := &TemplateSubgraph{
		Root:     issue,
		Issues:   []*types.Issue{issue},
		IssueMap: map[string]*types.Issue{"solo-1": issue},
	}
	layout := computeLayout(subgraph)

	output := captureGraphOutput(func() {
		renderGraphHTML(layout, subgraph)
	})

	if strings.Contains(output, "const links = null") {
		t.Error("single-node graph must emit const links = [] not null")
	}
	if !strings.Contains(output, "const links = []") {
		t.Error("single-node graph should have const links = []")
	}
	if strings.Contains(output, "const nodes = null") {
		t.Error("nodes must never be null")
	}
}
