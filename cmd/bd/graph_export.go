package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// renderGraphDOT renders the graph in Graphviz DOT format.
// Output can be piped to graphviz: bd graph --dot <id> | dot -Tsvg > graph.svg
func renderGraphDOT(layout *GraphLayout, subgraph *TemplateSubgraph) {
	if len(layout.Nodes) == 0 {
		fmt.Println("digraph beads { }")
		return
	}

	fmt.Println("digraph beads {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\", fontsize=11];")
	fmt.Println("  edge [color=\"#666666\"];")
	fmt.Println()

	// Emit nodes grouped by layer using subgraph clusters for rank alignment
	for layerIdx, layer := range layout.Layers {
		fmt.Printf("  subgraph cluster_layer_%d {\n", layerIdx)
		fmt.Println("    style=invis;")
		fmt.Printf("    rank=same;\n")
		for _, id := range layer {
			node := layout.Nodes[id]
			if node == nil {
				continue
			}
			label, fillColor, fontColor := dotNodeAttrs(node)
			// Escape quotes in label
			label = strings.ReplaceAll(label, "\"", "\\\"")
			fmt.Printf("    \"%s\" [label=\"%s\", fillcolor=\"%s\", fontcolor=\"%s\"];\n",
				dotEscapeID(id), label, fillColor, fontColor)
		}
		fmt.Println("  }")
	}
	fmt.Println()

	// Emit edges
	for _, dep := range subgraph.Dependencies {
		// Only include blocking dependencies in the graph
		if dep.Type != types.DepBlocks && dep.Type != types.DepParentChild {
			continue
		}
		// Ensure both endpoints exist in the subgraph
		if layout.Nodes[dep.IssueID] == nil || layout.Nodes[dep.DependsOnID] == nil {
			continue
		}
		edgeStyle := dotEdgeStyle(dep.Type)
		// dep.DependsOnID -> dep.IssueID (blocker points to blocked)
		fmt.Printf("  \"%s\" -> \"%s\"%s;\n",
			dotEscapeID(dep.DependsOnID), dotEscapeID(dep.IssueID), edgeStyle)
	}

	fmt.Println("}")
}

// dotNodeAttrs returns the DOT label, fill color, and font color for a node
func dotNodeAttrs(node *GraphNode) (label, fillColor, fontColor string) {
	icon := statusPlainIcon(node.Issue.Status)
	title := truncateTitle(node.Issue.Title, 40)
	label = fmt.Sprintf("%s %s\\nP%d | %s", icon, node.Issue.ID, node.Issue.Priority, title)

	switch node.Issue.Status {
	case types.StatusOpen:
		fillColor = "#e8f4fd"
		fontColor = "#1a1a1a"
	case types.StatusInProgress:
		fillColor = "#fff3cd"
		fontColor = "#664d03"
	case types.StatusBlocked:
		fillColor = "#f8d7da"
		fontColor = "#842029"
	case types.StatusClosed:
		fillColor = "#d4edda"
		fontColor = "#888888"
	default: // deferred, hooked, etc.
		fillColor = "#e2e3e5"
		fontColor = "#41464b"
	}
	return
}

// dotEdgeStyle returns DOT edge attributes for a dependency type
func dotEdgeStyle(depType types.DependencyType) string {
	switch depType {
	case types.DepBlocks:
		return " [style=solid, arrowhead=normal]"
	case types.DepParentChild:
		return " [style=dashed, arrowhead=empty, color=\"#999999\"]"
	default:
		return ""
	}
}

// dotEscapeID escapes an ID for DOT format by replacing characters
// that could break quoted strings (backslash, double-quote).
func dotEscapeID(id string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return r.Replace(id)
}

// statusPlainIcon returns a plain text status icon (no ANSI colors) for export formats
func statusPlainIcon(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "○"
	case types.StatusInProgress:
		return "◐"
	case types.StatusBlocked:
		return "●"
	case types.StatusClosed:
		return "✓"
	default:
		return "❄"
	}
}

// renderGraphHTML generates a self-contained HTML file with an interactive D3.js
// force-directed graph visualization. The output is a complete HTML document that
// can be opened in any browser.
func renderGraphHTML(layout *GraphLayout, subgraph *TemplateSubgraph) {
	nodes := buildHTMLGraphData(layout, subgraph)
	edges := buildHTMLEdgeData(layout, subgraph)

	nodesJSON, err := json.Marshal(nodes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling nodes: %v\n", err)
		return
	}
	edgesJSON, err := json.Marshal(edges)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling edges: %v\n", err)
		return
	}

	title := "Beads Dependency Graph"
	if subgraph.Root != nil {
		title = fmt.Sprintf("Beads: %s (%s)", subgraph.Root.Title, subgraph.Root.ID)
	}

	if _, err := fmt.Fprintf(os.Stdout, htmlTemplate, html.EscapeString(title), string(nodesJSON), string(edgesJSON)); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing HTML output: %v\n", err)
	}
}

// HTMLNode is the JSON structure for a node in the HTML visualization
type HTMLNode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
	Layer    int    `json:"layer"`
	Assignee string `json:"assignee,omitempty"`
}

// HTMLEdge is the JSON structure for an edge in the HTML visualization
type HTMLEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

func buildHTMLGraphData(layout *GraphLayout, _ *TemplateSubgraph) []HTMLNode {
	var nodes []HTMLNode
	for _, node := range layout.Nodes {
		nodes = append(nodes, HTMLNode{
			ID:       node.Issue.ID,
			Title:    node.Issue.Title,
			Status:   string(node.Issue.Status),
			Priority: node.Issue.Priority,
			Type:     string(node.Issue.IssueType),
			Layer:    node.Layer,
			Assignee: node.Issue.Assignee,
		})
	}
	return nodes
}

func buildHTMLEdgeData(layout *GraphLayout, subgraph *TemplateSubgraph) []HTMLEdge {
	var edges []HTMLEdge
	for _, dep := range subgraph.Dependencies {
		if dep.Type != types.DepBlocks && dep.Type != types.DepParentChild {
			continue
		}
		if layout.Nodes[dep.IssueID] == nil || layout.Nodes[dep.DependsOnID] == nil {
			continue
		}
		edges = append(edges, HTMLEdge{
			Source: dep.DependsOnID,
			Target: dep.IssueID,
			Type:   string(dep.Type),
		})
	}
	return edges
}

// htmlTemplate is the self-contained HTML template with embedded D3.js visualization.
// Uses CDN-hosted D3.js v7 for the force-directed graph.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>%s</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; background: #1a1a2e; color: #eee; overflow: hidden; }
svg { width: 100vw; height: 100vh; display: block; }
.node rect { rx: 6; ry: 6; stroke-width: 1.5; cursor: pointer; }
.node text { font-size: 11px; pointer-events: none; }
.node .id-text { font-size: 9px; fill: #999; }
.link { fill: none; stroke-width: 1.5; marker-end: url(#arrow); }
.link.blocks { stroke: #666; }
.link.parent-child { stroke: #555; stroke-dasharray: 5,3; }
#tooltip { position: absolute; background: #16213e; border: 1px solid #444; border-radius: 6px; padding: 10px 14px; font-size: 12px; pointer-events: none; opacity: 0; transition: opacity 0.15s; max-width: 320px; z-index: 10; }
#tooltip .tt-id { color: #7ec8e3; font-weight: bold; }
#tooltip .tt-status { display: inline-block; padding: 1px 6px; border-radius: 3px; font-size: 10px; margin-left: 6px; }
#legend { position: absolute; top: 12px; right: 12px; background: rgba(22,33,62,0.9); border: 1px solid #333; border-radius: 8px; padding: 12px 16px; font-size: 11px; }
#legend h3 { font-size: 12px; margin-bottom: 6px; color: #7ec8e3; }
.legend-item { display: flex; align-items: center; gap: 6px; margin: 3px 0; }
.legend-swatch { width: 14px; height: 14px; border-radius: 3px; display: inline-block; }
#controls { position: absolute; bottom: 12px; left: 12px; background: rgba(22,33,62,0.9); border: 1px solid #333; border-radius: 8px; padding: 8px 12px; font-size: 11px; }
#controls button { background: #2a2a4a; color: #ccc; border: 1px solid #444; border-radius: 4px; padding: 4px 10px; cursor: pointer; margin: 0 2px; font-size: 11px; }
#controls button:hover { background: #3a3a5a; }
</style>
</head>
<body>
<div id="tooltip"></div>
<div id="legend">
  <h3>Status</h3>
  <div class="legend-item"><span class="legend-swatch" style="background:#4a9eff"></span> Open</div>
  <div class="legend-item"><span class="legend-swatch" style="background:#f0ad4e"></span> In Progress</div>
  <div class="legend-item"><span class="legend-swatch" style="background:#d9534f"></span> Blocked</div>
  <div class="legend-item"><span class="legend-swatch" style="background:#5cb85c"></span> Closed</div>
  <div class="legend-item"><span class="legend-swatch" style="background:#777"></span> Deferred</div>
  <h3 style="margin-top:8px">Edges</h3>
  <div class="legend-item"><svg width="30" height="10"><line x1="0" y1="5" x2="30" y2="5" stroke="#888" stroke-width="1.5"/></svg> blocks</div>
  <div class="legend-item"><svg width="30" height="10"><line x1="0" y1="5" x2="30" y2="5" stroke="#666" stroke-width="1.5" stroke-dasharray="5,3"/></svg> parent-child</div>
</div>
<div id="controls">
  <button onclick="resetZoom()">Reset View</button>
  <button onclick="toggleLabels()">Toggle Labels</button>
  Drag nodes to rearrange. Scroll to zoom. Click for details.
</div>
<svg id="graph"></svg>
<script src="https://d3js.org/d3.v7.min.js"></script>
<script>
const nodes = %s;
const links = %s;
const statusColors = { open:"#4a9eff", in_progress:"#f0ad4e", blocked:"#d9534f", closed:"#5cb85c", deferred:"#777", hooked:"#9966cc" };
const width = window.innerWidth, height = window.innerHeight;
let showLabels = true;

const svg = d3.select("#graph");
const defs = svg.append("defs");
defs.append("marker").attr("id","arrow").attr("viewBox","0 -5 10 10").attr("refX",20).attr("refY",0)
  .attr("markerWidth",6).attr("markerHeight",6).attr("orient","auto")
  .append("path").attr("d","M0,-4L8,0L0,4").attr("fill","#666");

const g = svg.append("g");
const zoom = d3.zoom().scaleExtent([0.1, 4]).on("zoom", e => g.attr("transform", e.transform));
svg.call(zoom);

const simulation = d3.forceSimulation(nodes)
  .force("link", d3.forceLink(links).id(d => d.id).distance(140).strength(0.7))
  .force("charge", d3.forceManyBody().strength(-400))
  .force("x", d3.forceX(d => 150 + d.layer * 220).strength(0.3))
  .force("y", d3.forceY(height / 2).strength(0.05))
  .force("collision", d3.forceCollide(50));

const link = g.append("g").selectAll("line").data(links).join("line")
  .attr("class", d => "link " + d.type)
  .attr("stroke-dasharray", d => d.type === "parent-child" ? "5,3" : null);

const node = g.append("g").selectAll("g").data(nodes).join("g").attr("class","node")
  .call(d3.drag().on("start", dragStart).on("drag", dragged).on("end", dragEnd));

const nodeW = 130, nodeH = 40;
node.append("rect").attr("width", nodeW).attr("height", nodeH)
  .attr("x", -nodeW/2).attr("y", -nodeH/2)
  .attr("fill", d => statusColors[d.status] || "#555")
  .attr("stroke", d => d3.color(statusColors[d.status] || "#555").darker(0.5));

node.append("text").attr("class","title-text").attr("text-anchor","middle").attr("dy", -3)
  .text(d => d.title.length > 18 ? d.title.substring(0,17) + "\u2026" : d.title);

node.append("text").attr("class","id-text").attr("text-anchor","middle").attr("dy", 12)
  .text(d => d.id + " P" + d.priority);

const tooltip = d3.select("#tooltip");
node.on("mouseover", (e, d) => {
  tooltip.selectAll("*").remove();
  tooltip.text("");
  const idSpan = tooltip.append("span").attr("class","tt-id").text(d.id);
  tooltip.append("span").attr("class","tt-status").style("background", statusColors[d.status]||"#555").text(d.status);
  tooltip.append("br");
  tooltip.append("strong").text(d.title);
  tooltip.append("br");
  tooltip.append("span").text("Priority: P" + d.priority + " | Type: " + d.type);
  if (d.assignee) { tooltip.append("br"); tooltip.append("span").text("Assignee: " + d.assignee); }
  tooltip.append("br");
  tooltip.append("span").text("Layer: " + d.layer);
  tooltip.style("opacity", 1).style("left", (e.pageX+12)+"px").style("top", (e.pageY-10)+"px");
}).on("mouseout", () => { tooltip.style("opacity", 0); tooltip.selectAll("*").remove(); });

simulation.on("tick", () => {
  link.attr("x1",d=>d.source.x).attr("y1",d=>d.source.y).attr("x2",d=>d.target.x).attr("y2",d=>d.target.y);
  node.attr("transform", d => "translate("+d.x+","+d.y+")");
});

function dragStart(e, d) { if (!e.active) simulation.alphaTarget(0.3).restart(); d.fx = d.x; d.fy = d.y; }
function dragged(e, d) { d.fx = e.x; d.fy = e.y; }
function dragEnd(e, d) { if (!e.active) simulation.alphaTarget(0); d.fx = null; d.fy = null; }
function resetZoom() { svg.transition().duration(500).call(zoom.transform, d3.zoomIdentity); }
function toggleLabels() { showLabels = !showLabels; node.selectAll("text").style("opacity", showLabels ? 1 : 0); }

// Initial zoom to fit
svg.call(zoom.transform, d3.zoomIdentity.translate(width/4, height/4).scale(0.8));
</script>
</body>
</html>
`
