package main

import (
	"bytes"
	"errors"
	"io"
	"regexp"
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
