package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/steveyegge/beads/internal/types"
)

func listOutputFixture() ([]*types.Issue, map[string][]*types.Dependency) {
	issues := []*types.Issue{
		{
			ID:        "list-a",
			Title:     "Alpha",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeBug,
		},
		{
			ID:        "list-b",
			Title:     "Beta",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
		},
	}
	deps := map[string][]*types.Dependency{
		"list-a": {
			{
				IssueID:     "list-a",
				DependsOnID: "list-b",
				Type:        types.DepBlocks,
			},
		},
	}
	return issues, deps
}

func TestOutputDotFormatExactBytes(t *testing.T) {
	t.Parallel()
	issues, deps := listOutputFixture()
	var out bytes.Buffer

	if err := outputDotFormat(&out, issues, deps); err != nil {
		t.Fatalf("outputDotFormat: %v", err)
	}

	const want = `digraph dependencies {
  rankdir=TB;
  node [shape=box, style=rounded];

  "list-a" [label="list-a\n[bug P1]\nAlpha\n(open)", style="rounded,filled", fillcolor="white", fontcolor="black"];
  "list-b" [label="list-b\n[task P2]\nBeta\n(closed)", style="rounded,filled", fillcolor="lightgray", fontcolor="dimgray"];

  "list-a" -> "list-b" [label="blocks", color=red, style=bold];
}
`
	if got := out.String(); got != want {
		t.Fatalf("DOT output bytes differ\ngot:\n%q\nwant:\n%q", got, want)
	}
}

// TestOutputDotFormatCanonicalOrder pins that the DOT rendering is a function
// of the graph alone. The page it is handed is in display order (priority,
// then created_at DESC at one-second precision, then id), so the same two
// equal-priority issues arrive in either order depending on whether they were
// created in the same second; and the proxied route's edge read does not order
// one issue's edges. Every order of the page, with each issue's edges either
// way round, must print these bytes: nodes by id, edges by source, target and
// type, all in byte order (so dot-10 sorts before dot-2). It also pins that the
// caller's page and edge slices are not reordered.
func TestOutputDotFormatCanonicalOrder(t *testing.T) {
	t.Parallel()

	issues := []*types.Issue{
		{ID: "dot-2", Title: "Two", Status: types.StatusInProgress, Priority: 0, IssueType: types.TypeBug},
		{ID: "dot-10", Title: "Ten", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
		{ID: "dot-1", Title: "One", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
	}
	edges := map[string][]*types.Dependency{
		"dot-2": {
			{IssueID: "dot-2", DependsOnID: "dot-10", Type: types.DepRelated},
			{IssueID: "dot-2", DependsOnID: "dot-unlisted", Type: types.DepBlocks},
			{IssueID: "dot-2", DependsOnID: "dot-1", Type: types.DepBlocks},
		},
		"dot-1": {{IssueID: "dot-1", DependsOnID: "dot-10", Type: types.DepParentChild}},
	}
	const want = `digraph dependencies {
  rankdir=TB;
  node [shape=box, style=rounded];

  "dot-1" [label="dot-1\n[task P2]\nOne\n(closed)", style="rounded,filled", fillcolor="lightgray", fontcolor="dimgray"];
  "dot-10" [label="dot-10\n[epic P1]\nTen\n(open)", style="rounded,filled", fillcolor="white", fontcolor="black"];
  "dot-2" [label="dot-2\n[bug P0]\nTwo\n(in_progress)", style="rounded,filled", fillcolor="lightyellow", fontcolor="black"];

  "dot-1" -> "dot-10" [label="parent-child", color=blue, style=solid];
  "dot-2" -> "dot-1" [label="blocks", color=red, style=bold];
  "dot-2" -> "dot-10" [label="related", color=gray, style=dashed];
}
`
	for _, perm := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		for _, reversed := range []bool{false, true} {
			page := []*types.Issue{issues[perm[0]], issues[perm[1]], issues[perm[2]]}
			pageEdges := map[string][]*types.Dependency{}
			for id, deps := range edges {
				deps = slices.Clone(deps)
				if reversed {
					slices.Reverse(deps)
				}
				pageEdges[id] = deps
			}
			wantPage := slices.Clone(page)
			wantEdges := map[string][]*types.Dependency{}
			for id, deps := range pageEdges {
				wantEdges[id] = slices.Clone(deps)
			}

			var out bytes.Buffer
			if err := outputDotFormat(&out, page, pageEdges); err != nil {
				t.Fatalf("outputDotFormat: %v", err)
			}
			name := fmt.Sprintf("page order %v, edges reversed %v", perm, reversed)
			if got := out.String(); got != want {
				t.Errorf("%s: DOT output bytes differ\ngot:\n%s\nwant:\n%s", name, got, want)
			}
			if !slices.Equal(page, wantPage) {
				t.Errorf("%s: outputDotFormat reordered the caller's page", name)
			}
			for id, deps := range wantEdges {
				if !slices.Equal(pageEdges[id], deps) {
					t.Errorf("%s: outputDotFormat reordered the caller's edges of %s", name, id)
				}
			}
		}
	}
}

// dotNodeDecl and dotEdgeDecl match the node and edge lines outputDotFormat
// writes.
var (
	dotNodeDecl = regexp.MustCompile(`(?m)^  "([^"]+)" \[label=`)
	dotEdgeDecl = regexp.MustCompile(`(?m)^  "([^"]+)" -> "([^"]+)" \[label="([^"]+)"`)
)

// assertDotCanonicalOrder is TestOutputDotFormatCanonicalOrder's check for a
// real `bd list --format dot` run: nodes in byte order of id, edges in byte
// order of source, target and type. The embedded and proxied list seeds mint
// their ids across five priorities, so a rendering that followed the page's
// priority order fails it.
func assertDotCanonicalOrder(t *testing.T, out string) {
	t.Helper()
	var nodes []string
	for _, m := range dotNodeDecl.FindAllStringSubmatch(out, -1) {
		nodes = append(nodes, m[1])
	}
	if len(nodes) < 2 {
		t.Fatalf("dot output declares %d nodes, want every listed issue:\n%s", len(nodes), out)
	}
	if !slices.IsSorted(nodes) {
		t.Errorf("dot output declares its nodes out of id order: %v", nodes)
	}
	type edge struct{ from, to, typ string }
	var edges []edge
	for _, m := range dotEdgeDecl.FindAllStringSubmatch(out, -1) {
		edges = append(edges, edge{m[1], m[2], m[3]})
	}
	if !slices.IsSortedFunc(edges, func(a, b edge) int {
		return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.to, b.to), cmp.Compare(a.typ, b.typ))
	}) {
		t.Errorf("dot output declares its edges out of (source, target, type) order: %v", edges)
	}
}

func TestOutputFormattedListExactBytes(t *testing.T) {
	t.Parallel()
	issues, deps := listOutputFixture()

	for _, tc := range []struct {
		name   string
		format string
		want   string
	}{
		{name: "digraph preset", format: "digraph", want: "list-a list-b\n"},
		{name: "custom template", format: "{{.IssueID}} -> {{.DependsOnID}} [{{.Type}}]", want: "list-a -> list-b [blocks]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := outputFormattedList(&out, issues, deps, tc.format); err != nil {
				t.Fatalf("outputFormattedList: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("formatted output bytes differ: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOutputFormattedListFormatNamesIgnoreCase pins GH#6279: the built-in
// format names fold case like 'json' does, while a custom template is never
// lowercased.
func TestOutputFormattedListFormatNamesIgnoreCase(t *testing.T) {
	t.Parallel()
	issues, deps := listOutputFixture()

	var dot bytes.Buffer
	if err := outputDotFormat(&dot, issues, deps); err != nil {
		t.Fatalf("outputDotFormat: %v", err)
	}

	for _, tc := range []struct {
		format string
		want   string
	}{
		{format: "DOT", want: dot.String()},
		{format: "Dot", want: dot.String()},
		{format: "DIGRAPH", want: "list-a list-b\n"},
		{format: "DiGraph", want: "list-a list-b\n"},
		{format: "{{.IssueID}} DependsOn {{.DependsOnID}} AS {{.Type}}", want: "list-a DependsOn list-b AS blocks\n"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := outputFormattedList(&out, issues, deps, tc.format); err != nil {
				t.Fatalf("outputFormattedList: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("formatted output bytes differ: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOutputFormattedListEdgeScope pins what --format's help now states: every
// non-dot format renders the dependency edges BETWEEN the listed issues, so it
// prints nothing both when there are no dependencies and when the only
// dependency leaves the listed set. The second case is what covers the
// endpoint-membership guard; without it, a listing would spill edges to issues
// the caller filtered out. Only 'dot' declares nodes, which is why it alone
// still prints on an edgeless listing.
func TestOutputFormattedListEdgeScope(t *testing.T) {
	t.Parallel()
	issues, _ := listOutputFixture()

	for _, tc := range []struct {
		name string
		deps map[string][]*types.Dependency
	}{
		{name: "no dependencies", deps: nil},
		{
			name: "dependency leaves the listed set",
			deps: map[string][]*types.Dependency{
				"list-a": {{IssueID: "list-a", DependsOnID: "list-unlisted", Type: types.DepBlocks}},
			},
		},
	} {
		for _, format := range []string{"digraph", "{{.IssueID}} -> {{.DependsOnID}}"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				t.Parallel()
				var out bytes.Buffer
				if err := outputFormattedList(&out, issues, tc.deps, format); err != nil {
					t.Fatalf("outputFormattedList: %v", err)
				}
				if got := out.String(); got != "" {
					t.Fatalf("format %q wrote %q, want no output", format, got)
				}
			})
		}
	}

	t.Run("dot declares nodes without edges", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		if err := outputFormattedList(&out, issues, nil, "dot"); err != nil {
			t.Fatalf("outputFormattedList: %v", err)
		}
		for _, id := range []string{"list-a", "list-b"} {
			if !strings.Contains(out.String(), id) {
				t.Fatalf("dot output with no dependencies omitted node %q:\n%s", id, out.String())
			}
		}
	})
}

func TestOutputFormattedListWriterErrors(t *testing.T) {
	t.Parallel()
	issues, deps := listOutputFixture()

	for _, tc := range []struct {
		name        string
		format      string
		failAt      int
		wantContext string
	}{
		{name: "DOT", format: "dot", failAt: 3, wantContext: "writing DOT output"},
		{name: "digraph preset", format: "digraph", failAt: 1, wantContext: "writing formatted list output"},
		{name: "custom template", format: "{{.IssueID}} {{.DependsOnID}}", failAt: 1, wantContext: "writing formatted list output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: tc.failAt}

			err := outputFormattedList(writer, issues, deps, tc.format)
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("outputFormattedList error = %v, want %v", err, io.ErrClosedPipe)
			}
			if !strings.Contains(err.Error(), tc.wantContext) {
				t.Fatalf("outputFormattedList error = %q, want context %q", err, tc.wantContext)
			}
			if writer.writes != writer.failAt {
				t.Fatalf("outputFormattedList made %d writes after failure at %d", writer.writes, writer.failAt)
			}
		})
	}
}

func TestOutputFormattedListPreservesFirstWriterError(t *testing.T) {
	t.Parallel()
	issues := []*types.Issue{{ID: "first"}, {ID: "second"}, {ID: "target"}}
	deps := map[string][]*types.Dependency{
		"first":  {{IssueID: "first", DependsOnID: "target", Type: types.DepBlocks}},
		"second": {{IssueID: "second", DependsOnID: "target", Type: types.DepBlocks}},
	}
	writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}
	format := `{{if eq .IssueID "second"}}{{index .Issue.Labels 0}}{{else}}{{.IssueID}}{{end}}`

	err := outputFormattedList(writer, issues, deps, format)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("outputFormattedList error = %v, want first writer error %v", err, io.ErrClosedPipe)
	}
	if writer.writes != 1 {
		t.Fatalf("outputFormattedList made %d writes, want one failing write", writer.writes)
	}
}

func TestOutputFormattedListBuffersTemplateExecution(t *testing.T) {
	t.Parallel()
	issues := []*types.Issue{{ID: "source"}, {ID: "target"}}
	deps := map[string][]*types.Dependency{
		"source": {{IssueID: "source", DependsOnID: "target", Type: types.DepBlocks}},
	}
	var out bytes.Buffer

	err := outputFormattedList(&out, issues, deps, "prefix{{index .Issue.Labels 0}}")
	if err == nil || !strings.Contains(err.Error(), "template execution error") {
		t.Fatalf("outputFormattedList error = %v, want template execution error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed template leaked partial output %q", out.String())
	}
}

func TestOutputFormattedListWithNoEdgesDoesNotWrite(t *testing.T) {
	t.Parallel()
	writer := &graphFailWriter{err: io.ErrClosedPipe, failAt: 1}

	if err := outputFormattedList(writer, []*types.Issue{{ID: "solo"}}, nil, "digraph"); err != nil {
		t.Fatalf("outputFormattedList: %v", err)
	}
	if writer.writes != 0 {
		t.Fatalf("zero-edge output made %d writes, want 0", writer.writes)
	}
}

func TestFormatTruncationHintExactBytes(t *testing.T) {
	t.Parallel()

	got := formatTruncationHint(2)
	plain := stripANSIForTest(got)
	const want = "\nShowing 2 issues; more results matched but were hidden by --limit. Use --limit 0 for all, or --limit N to raise the cap.\n"
	if plain != want {
		t.Fatalf("truncation hint bytes differ\ngot:\n%q\nwant:\n%q", plain, want)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatal("truncation hint missing trailing newline")
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatal("truncation hint ended with a double newline")
	}
	assertNoWhitespaceOnlyLine(t, plain)

	got10 := stripANSIForTest(formatTruncationHint(10))
	if !strings.Contains(got10, "Showing 10 issues;") {
		t.Fatalf("limit interpolation missing: %q", got10)
	}
	if strings.HasPrefix(got10, "\n\n") {
		t.Fatalf("leading double newline: %q", got10)
	}
}

func TestComposeTruncationHintRejectsLipglossBlankLinePadding(t *testing.T) {
	t.Parallel()

	// Empty lipgloss Style (props==0) is identity — the colorless test
	// default, and why RenderWarn("\n"+text+"\n") did not catch GH#5685.
	// Foreground sets props so alignTextHorizontal pads blank lines to
	// the widest-line width (not terminal width; no PTY required).
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	render := func(s string) string { return style.Render(s) }
	text := truncationHintText(2)

	buggy := render("\n" + text + "\n")
	got := composeTruncationHint(render, text)

	buggyPlain := stripANSIForTest(buggy)
	gotPlain := stripANSIForTest(got)

	padWidth := whitespaceOnlyLineWidth(buggyPlain)
	if padWidth == 0 {
		t.Fatalf("fixture: styled Render of newline-wrapped text should pad a blank line; got %q", buggyPlain)
	}
	if padWidth != len(text) {
		t.Fatalf("lipgloss pad width = %d, want hint length %d (widest-line width, not terminal width)\nbuggy=%q", padWidth, len(text), buggyPlain)
	}
	widest := widestNonBlankLineWidth(buggyPlain)
	if padWidth != widest {
		t.Fatalf("lipgloss pad width = %d, want widest-line width %d\nbuggy=%q", padWidth, widest, buggyPlain)
	}

	assertNoWhitespaceOnlyLine(t, gotPlain)
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("composeTruncationHint missing trailing newline: %q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatal("composeTruncationHint ended with a double newline")
	}
	if got == buggy {
		t.Fatalf("composeTruncationHint still uses newline-inside-Render bytes:\n%q", got)
	}
}

func stripANSIForTest(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}

func whitespaceOnlyLineWidth(s string) int {
	width := 0
	for _, line := range strings.Split(s, "\n") {
		if line != "" && strings.TrimSpace(line) == "" && len(line) > width {
			width = len(line)
		}
	}
	return width
}

func widestNonBlankLineWidth(s string) int {
	width := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" && len(line) > width {
			width = len(line)
		}
	}
	return width
}

func assertNoWhitespaceOnlyLine(t *testing.T, s string) {
	t.Helper()
	for i, line := range strings.Split(s, "\n") {
		if line != "" && strings.TrimSpace(line) == "" {
			t.Fatalf("whitespace-only line at index %d (len=%d): %q", i, len(line), line)
		}
	}
}
