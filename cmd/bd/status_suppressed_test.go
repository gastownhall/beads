package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestSuppressedTypeSummary(t *testing.T) {
	tests := []struct {
		name  string
		stats *types.Statistics
		want  string
	}{
		{
			name:  "nothing suppressed",
			stats: &types.Statistics{TotalIssues: 3},
			want:  "",
		},
		{
			name:  "one gate",
			stats: &types.Statistics{TotalIssues: 2, GateIssues: 1},
			want:  "1 gate (--include-gates)",
		},
		{
			name:  "gates are pluralized",
			stats: &types.Statistics{TotalIssues: 4, GateIssues: 3},
			want:  "3 gates (--include-gates)",
		},
		{
			name:  "gates and templates",
			stats: &types.Statistics{TotalIssues: 5, GateIssues: 1, TemplateIssues: 2},
			want:  "1 gate (--include-gates), 2 templates (--include-templates)",
		},
		{
			name:  "templates only",
			stats: &types.Statistics{TotalIssues: 5, TemplateIssues: 1},
			want:  "1 template (--include-templates)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suppressedTypeSummary(tt.stats); got != tt.want {
				t.Errorf("suppressedTypeSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The disclosure exists so the operator can account for a total that no listing
// reproduces; it must name the flag that reveals the rows.
func TestSuppressedTypeSummaryNamesTheRevealingFlag(t *testing.T) {
	got := suppressedTypeSummary(&types.Statistics{TotalIssues: 2, GateIssues: 1})
	if !strings.Contains(got, "--include-gates") {
		t.Errorf("suppressedTypeSummary() = %q, want it to name --include-gates", got)
	}
}

// renderStatusOutput captures what renderStatus writes to stdout.
func renderStatusOutput(t *testing.T, stats *types.Statistics) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}

	oldStdout := os.Stdout
	oldJSON := jsonOutput
	var renderErr error
	func() {
		defer func() {
			os.Stdout = oldStdout
			jsonOutput = oldJSON
		}()
		os.Stdout = f
		jsonOutput = false
		renderErr = renderStatus(stats, nil)
	}()

	if err := f.Close(); err != nil {
		t.Fatalf("close stdout capture: %v", err)
	}
	if renderErr != nil {
		t.Fatalf("renderStatus: %v", renderErr)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	return string(out)
}

// The summary formatter being right is not the same as the line reaching the
// operator: suppressedTypeSummary is a pure function over a hand-built struct,
// and every assertion above passes with the renderStatus call site deleted.
func TestRenderStatusEmitsTheSuppressedRowsLine(t *testing.T) {
	blocked, ready := 0, 2
	stats := &types.Statistics{
		TotalIssues:    9,
		OpenIssues:     4,
		BlockedIssues:  &blocked,
		ReadyIssues:    &ready,
		GateIssues:     2,
		TemplateIssues: 1,
	}

	out := renderStatusOutput(t, stats)

	if !strings.Contains(out, "Not shown by bd list:") {
		t.Fatalf("renderStatus did not emit the disclosure line:\n%s", out)
	}
	for _, want := range []string{"2 gates (--include-gates)", "1 template (--include-templates)"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderStatus output missing %q:\n%s", want, out)
		}
	}
	// The disclosure has to sit with the totals it reconciles, not down in the
	// extended block that only renders when other counters are non-zero.
	if idx, total := strings.Index(out, "Not shown by bd list:"), strings.Index(out, "Total Issues:"); idx < total {
		t.Errorf("disclosure line precedes the totals it reconciles:\n%s", out)
	}
}

// A workspace with neither kind of row must not gain a line that says nothing.
func TestRenderStatusOmitsTheLineWhenNothingIsSuppressed(t *testing.T) {
	blocked, ready := 0, 1
	stats := &types.Statistics{
		TotalIssues:   3,
		OpenIssues:    1,
		BlockedIssues: &blocked,
		ReadyIssues:   &ready,
	}

	if out := renderStatusOutput(t, stats); strings.Contains(out, "Not shown by bd list") {
		t.Errorf("renderStatus emitted the disclosure with nothing suppressed:\n%s", out)
	}
}
