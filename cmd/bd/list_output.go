package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
	msg := fmt.Sprintf("\nShowing %d issues; more results matched but were hidden by --limit. Use --limit 0 for all, or --limit N to raise the cap.\n", effectiveLimit)
	fmt.Fprint(os.Stderr, ui.RenderWarn(msg))
}

func outputDotFormat(out io.Writer, issues []*types.Issue, depsByIssueID map[string][]*types.Dependency) error {
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
		for _, dep := range depsByIssueID[issue.ID] {
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
	// Handle special 'dot' format (Graphviz output)
	if formatStr == "dot" {
		return outputDotFormat(out, issues, depsByIssueID)
	}
	w := &graphExportWriter{out: out}

	// Built-in format presets
	presets := map[string]string{
		"digraph": "{{.IssueID}} {{.DependsOnID}}",
	}

	// Check if it's a preset
	templateStr, isPreset := presets[formatStr]
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
