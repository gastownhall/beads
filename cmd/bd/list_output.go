package main

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// printTruncationHint emits a one-line notice to stderr when the list output
// was truncated by --limit, so users and agents can't mistake a partial view
// for a complete one (GH#3212, GH#788).
func printTruncationHint(truncated bool, effectiveLimit int) {
	if !truncated || effectiveLimit <= 0 || !ui.IsStderrTerminal() {
		return
	}
	fmt.Fprint(os.Stderr, formatTruncationHint(effectiveLimit))
}

func truncationHintText(effectiveLimit int) string {
	return fmt.Sprintf("Showing %d issues; more results matched but were hidden by --limit. Use --limit 0 for all, or --limit N to raise the cap.", effectiveLimit)
}

func formatTruncationHint(effectiveLimit int) string {
	return composeTruncationHint(ui.RenderWarn, truncationHintText(effectiveLimit))
}

// composeTruncationHint keeps newlines outside the renderer. lipgloss pads
// blank lines to the widest-line width (alignTextHorizontal shortAmount =
// widestLine - lineWidth) and drops the trailing newline (GH#5685).
func composeTruncationHint(render func(string) string, text string) string {
	rendered := strings.TrimRight(render(text), " \t\r\n")
	return "\n" + rendered + "\n"
}

// outputDotFormat renders the listed issues, and the dependency edges between
// them, as a Graphviz digraph.
//
// The bytes depend on the graph alone: nodes are declared in byte order of id,
// and each node's edges in byte order of target id, then type (the order
// issueops.EdgeReader pins). The page arrives in display order, by default
// priority, then created_at DESC at one-second precision, then id, so two
// equal-priority issues swapped places depending on whether they were created
// in the same second; and the proxied route's edge query orders by source
// only. Declaration order means nothing in the graph but steers Graphviz's
// layout and shows in every diff, so it is pinned here, as bd graph --dot pins
// its layers. --sort and --limit still choose which issues are listed.
//
// Byte order, not the utils.NaturalCompareIDs order --sort id uses: that
// comparator is not a total order (x-9 < x-10 < x-1a < x-9; bd-01 ties bd-1),
// so a sort under it could still depend on the order the page arrived in.
// Both sorts work on copies; the caller's slices keep their order.
func outputDotFormat(out io.Writer, issues []*types.Issue, depsByIssueID map[string][]*types.Dependency) error {
	issues = slices.Clone(issues)
	slices.SortFunc(issues, func(a, b *types.Issue) int { return cmp.Compare(a.ID, b.ID) })

	w := &graphExportWriter{out: out}
	w.println("digraph dependencies {")
	w.println("  rankdir=TB;")
	w.println("  node [shape=box, style=rounded];")
	w.println()

	// Build map of all issues for quick lookup
	issueMap := make(map[string]*types.Issue)
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// Output nodes with labels including ID, type, priority, and status
	for _, issue := range issues {
		// Build label with ID, type, priority, and title (using actual newlines)
		label := fmt.Sprintf("%s\n[%s P%d]\n%s\n(%s)",
			issue.ID,
			issue.IssueType,
			issue.Priority,
			issue.Title,
			issue.Status)

		// Color by status only - keep it simple
		fillColor := "white"
		fontColor := "black"

		switch issue.Status {
		case "closed":
			fillColor = "lightgray"
			fontColor = "dimgray"
		case "in_progress":
			fillColor = "lightyellow"
		case "blocked":
			fillColor = "lightcoral"
		}

		w.printf("  %q [label=%q, style=\"rounded,filled\", fillcolor=%q, fontcolor=%q];\n",
			issue.ID, label, fillColor, fontColor)
	}
	w.println()

	// Output edges with labels for dependency type
	for _, issue := range issues {
		deps := slices.Clone(depsByIssueID[issue.ID])
		slices.SortFunc(deps, func(a, b *types.Dependency) int {
			return cmp.Or(cmp.Compare(a.DependsOnID, b.DependsOnID), cmp.Compare(a.Type, b.Type))
		})
		for _, dep := range deps {
			// Only output edges where both nodes are in the filtered list
			if issueMap[dep.DependsOnID] != nil {
				// Color code by dependency type
				color := "black"
				style := "solid"
				switch dep.Type {
				case "blocks":
					color = "red"
					style = "bold"
				case "parent-child":
					color = "blue"
				case "discovered-from":
					color = "green"
					style = "dashed"
				case "related":
					color = "gray"
					style = "dashed"
				}
				w.printf("  %q -> %q [label=%q, color=%s, style=%s];\n",
					issue.ID, dep.DependsOnID, dep.Type, color, style)
			}
		}
	}

	w.println("}")
	return w.wrapError("DOT")
}

func outputFormattedList(out io.Writer, issues []*types.Issue, depsByIssueID map[string][]*types.Dependency, formatStr string) error {
	// Format names fold case like 'json' does (GH#6279); a custom template
	// is still parsed from formatStr, so its case is never changed.
	formatKeyword := strings.ToLower(formatStr)
	// Handle special 'dot' format (Graphviz output)
	if formatKeyword == "dot" {
		return outputDotFormat(out, issues, depsByIssueID)
	}
	w := &graphExportWriter{out: out}

	// Built-in format presets
	presets := map[string]string{
		"digraph": "{{.IssueID}} {{.DependsOnID}}",
	}

	// Check if it's a preset
	templateStr, isPreset := presets[formatKeyword]
	if !isPreset {
		templateStr = formatStr
	}

	// Parse template
	tmpl, err := template.New("format").Parse(templateStr)
	if err != nil {
		return fmt.Errorf("invalid format template: %w", err)
	}

	// Build map of all issues for quick lookup
	issueMap := make(map[string]bool)
	for _, issue := range issues {
		issueMap[issue.ID] = true
	}

	// For each issue, output its dependencies using the template
	for _, issue := range issues {
		for _, dep := range depsByIssueID[issue.ID] {
			// Only output edges where both nodes are in the filtered list
			if issueMap[dep.DependsOnID] {
				// Template data includes both issue and dependency info
				data := map[string]interface{}{
					"IssueID":     issue.ID,
					"DependsOnID": dep.DependsOnID,
					"Type":        dep.Type,
					"Issue":       issue,
					"Dependency":  dep,
				}

				var buf bytes.Buffer
				if err := tmpl.Execute(&buf, data); err != nil {
					return fmt.Errorf("template execution error: %w", err)
				}
				w.println(buf.String())
				if err := w.wrapError("formatted list"); err != nil {
					return err
				}
			}
		}
	}

	return w.wrapError("formatted list")
}
