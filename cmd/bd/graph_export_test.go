package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

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
	t.Parallel()
	subgraph, layout := makeTestSubgraph()

	var output bytes.Buffer
	if err := renderGraphDOT(&output, layout, subgraph); err != nil {
		t.Fatalf("renderGraphDOT: %v", err)
	}
	got := output.String()

	// Verify DOT structure
	if !strings.HasPrefix(got, "digraph beads {") {
		t.Error("DOT output should start with 'digraph beads {'")
	}
	if !strings.Contains(got, "rankdir=LR") {
		t.Error("DOT output should specify left-to-right layout")
	}

	// Verify nodes are present
	for _, id := range []string{"test-a", "test-b", "test-c", "test-d"} {
		if !strings.Contains(got, fmt.Sprintf("\"%s\"", id)) {
			t.Errorf("DOT output should contain node %q", id)
		}
	}

	// Verify edges exist
	if !strings.Contains(got, "\"test-a\" -> \"test-b\"") {
		t.Error("DOT output should contain edge test-a -> test-b")
	}
	if !strings.Contains(got, "\"test-b\" -> \"test-c\"") {
		t.Error("DOT output should contain edge test-b -> test-c")
	}

	// Verify it ends with closing brace
	if !strings.HasSuffix(strings.TrimSpace(got), "}") {
		t.Error("DOT output should end with '}'")
	}
}

func TestRenderGraphDOT_Empty(t *testing.T) {
	t.Parallel()
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

	var output bytes.Buffer
	if err := renderGraphDOT(&output, layout, emptySubgraph); err != nil {
		t.Fatalf("renderGraphDOT: %v", err)
	}

	if got, want := output.String(), "digraph beads { }\n"; got != want {
		t.Errorf("Empty DOT output = %q, want %q", got, want)
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
	t.Parallel()
	subgraph, layout := makeTestSubgraph()

	var output bytes.Buffer
	if err := renderGraphHTML(&output, layout, subgraph); err != nil {
		t.Fatalf("renderGraphHTML: %v", err)
	}
	got := output.String()

	// Verify HTML structure
	if !strings.Contains(got, "<!DOCTYPE html>") {
		t.Error("HTML output should contain DOCTYPE")
	}
	if !strings.Contains(got, "d3.v7.min.js") {
		t.Error("HTML output should reference D3.js")
	}

	// Verify node data is embedded
	for _, id := range []string{"test-a", "test-b", "test-c", "test-d"} {
		if !strings.Contains(got, id) {
			t.Errorf("HTML output should contain node %q", id)
		}
	}

	// Verify it contains all statuses
	if !strings.Contains(got, "open") {
		t.Error("HTML should contain open status")
	}
	if !strings.Contains(got, "in_progress") {
		t.Error("HTML should contain in_progress status")
	}

	// Verify interactive elements
	if !strings.Contains(got, "forceSimulation") {
		t.Error("HTML should contain D3 force simulation")
	}
	if !strings.Contains(got, "tooltip") {
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
	t.Parallel()

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

	var output bytes.Buffer
	if err := renderGraphHTML(&output, layout, merged); err != nil {
		t.Fatalf("renderGraphHTML: %v", err)
	}
	got := output.String()

	// Must contain exactly one DOCTYPE declaration
	count := strings.Count(got, "<!DOCTYPE html>")
	if count != 1 {
		t.Errorf("expected exactly 1 <!DOCTYPE html>, got %d", count)
	}

	// Both issues must appear in the single document
	if !strings.Contains(got, "comp-a") {
		t.Error("merged HTML should contain comp-a")
	}
	if !strings.Contains(got, "comp-b") {
		t.Error("merged HTML should contain comp-b")
	}

	// links must be [] not null — null breaks d3.forceLink (GH#3592)
	if strings.Contains(got, "const links = null") {
		t.Error("links must be [] not null for d3 compatibility")
	}
	if !strings.Contains(got, "const links = []") {
		t.Error("empty links should serialize as [] not null")
	}
}

func TestRenderGraphHTML_EmptyEdgesNotNull(t *testing.T) {
	t.Parallel()
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

	var output bytes.Buffer
	if err := renderGraphHTML(&output, layout, subgraph); err != nil {
		t.Fatalf("renderGraphHTML: %v", err)
	}
	got := output.String()

	if strings.Contains(got, "const links = null") {
		t.Error("single-node graph must emit const links = [] not null")
	}
	if !strings.Contains(got, "const links = []") {
		t.Error("single-node graph should have const links = []")
	}
	if strings.Contains(got, "const nodes = null") {
		t.Error("nodes must never be null")
	}
}

type graphFailWriter struct {
	err    error
	failAt int
	writes int
}

func (w *graphFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestRenderGraphDOTWriterErrors(t *testing.T) {
	t.Parallel()

	t.Run("nonempty", func(t *testing.T) {
		t.Parallel()
		subgraph, layout := makeTestSubgraph()
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 3}

		err := renderGraphDOT(writer, layout, subgraph)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphDOT error = %v, want %v", err, io.ErrClosedPipe)
		}
		if writer.writes != writer.failAt {
			t.Fatalf("renderGraphDOT made %d writes after failure at %d", writer.writes, writer.failAt)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		subgraph := &TemplateSubgraph{IssueMap: map[string]*types.Issue{}}
		layout := &GraphLayout{Nodes: map[string]*GraphNode{}}
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

		err := renderGraphDOT(writer, layout, subgraph)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphDOT error = %v, want %v", err, io.ErrClosedPipe)
		}
	})
}

func TestRenderGraphHTMLWriterError(t *testing.T) {
	t.Parallel()
	subgraph, layout := makeTestSubgraph()
	writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

	err := renderGraphHTML(writer, layout, subgraph)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("renderGraphHTML error = %v, want %v", err, io.ErrClosedPipe)
	}
}

func TestGraphExportDispatchPropagatesWriterErrors(t *testing.T) {
	oldDOT, oldHTML := graphDOT, graphHTML
	oldOpen, oldCompact, oldBox := graphOpen, graphCompact, graphBox
	oldJSON := jsonOutput
	t.Cleanup(func() {
		graphDOT, graphHTML = oldDOT, oldHTML
		graphOpen, graphCompact, graphBox = oldOpen, oldCompact, oldBox
		jsonOutput = oldJSON
	})
	graphOpen, graphCompact, graphBox, jsonOutput = false, false, false, false

	t.Run("single DOT", func(t *testing.T) {
		graphDOT, graphHTML = true, false
		subgraph, _ := makeTestSubgraph()
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

		err := renderGraphSingleSubgraph(writer, subgraph)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphSingleSubgraph error = %v, want %v", err, io.ErrClosedPipe)
		}
	})

	t.Run("all HTML", func(t *testing.T) {
		graphDOT, graphHTML = false, true
		subgraph, _ := makeTestSubgraph()
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

		err := renderGraphAllSubgraphs(writer, []*TemplateSubgraph{subgraph})
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphAllSubgraphs error = %v, want %v", err, io.ErrClosedPipe)
		}
	})

	t.Run("all empty message", func(t *testing.T) {
		graphDOT, graphHTML, graphOpen = false, false, false
		var out bytes.Buffer

		if err := renderGraphAllSubgraphs(&out, nil); err != nil {
			t.Fatalf("renderGraphAllSubgraphs: %v", err)
		}
		if got, want := out.String(), "No open issues found\n"; got != want {
			t.Fatalf("empty all output = %q, want %q", got, want)
		}
	})

	t.Run("all empty message error", func(t *testing.T) {
		graphDOT, graphHTML, graphOpen = false, false, false
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

		err := renderGraphAllSubgraphs(writer, nil)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphAllSubgraphs error = %v, want %v", err, io.ErrClosedPipe)
		}
	})

	t.Run("single empty open message", func(t *testing.T) {
		graphDOT, graphHTML, graphOpen = false, false, true
		subgraph := &TemplateSubgraph{Issues: []*types.Issue{{ID: "closed", Status: types.StatusClosed}}}
		var out bytes.Buffer

		if err := renderGraphSingleSubgraph(&out, subgraph); err != nil {
			t.Fatalf("renderGraphSingleSubgraph: %v", err)
		}
		if got, want := out.String(), "No open issues in subgraph\n"; got != want {
			t.Fatalf("empty single output = %q, want %q", got, want)
		}
	})

	t.Run("single empty open message error", func(t *testing.T) {
		graphDOT, graphHTML, graphOpen = false, false, true
		subgraph := &TemplateSubgraph{Issues: []*types.Issue{{ID: "closed", Status: types.StatusClosed}}}
		writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

		err := renderGraphSingleSubgraph(writer, subgraph)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("renderGraphSingleSubgraph error = %v, want %v", err, io.ErrClosedPipe)
		}
	})
}
