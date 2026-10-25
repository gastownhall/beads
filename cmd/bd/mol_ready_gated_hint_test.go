package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func renderGatedHint(t *testing.T, molecules []*GatedMolecule) string {
	t.Helper()

	oldStdout := os.Stdout
	oldJSON := jsonOutput
	jsonOutput = false
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = w

	renderErr := renderGatedReadyMolecules(molecules)

	_ = w.Close()
	var b strings.Builder
	_, _ = io.Copy(&b, r)
	_ = r.Close()
	os.Stdout = oldStdout
	jsonOutput = oldJSON

	if renderErr != nil {
		t.Fatalf("renderGatedReadyMolecules: %v", renderErr)
	}
	return b.String()
}

func hintCommandLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "bd ") {
			return trimmed
		}
	}
	t.Fatalf("no dispatch command found in output:\n%s", out)
	return ""
}

// The dispatch hint must name a command bd actually has. It previously printed
// "bd sling", which has never existed in this binary.
func TestGatedReadyHintNamesRealCommand(t *testing.T) {
	out := renderGatedHint(t, []*GatedMolecule{{
		MoleculeID:    "test-mol1",
		MoleculeTitle: "Gated molecule",
		ReadyStep:     &types.Issue{ID: "test-step1", Title: "Resume work"},
	}})

	fields := strings.Fields(hintCommandLine(t, out))
	if len(fields) < 2 {
		t.Fatalf("dispatch hint is not a runnable command: %q", strings.Join(fields, " "))
	}
	if _, _, err := rootCmd.Find(fields[1:2]); err != nil {
		t.Errorf("dispatch hint names unknown command %q: %v", fields[1], err)
	}
}

func TestGatedReadyHintUsesReadyStepID(t *testing.T) {
	out := renderGatedHint(t, []*GatedMolecule{{
		MoleculeID:    "test-mol1",
		MoleculeTitle: "Gated molecule",
		ReadyStep:     &types.Issue{ID: "test-step1", Title: "Resume work"},
	}})

	if got := hintCommandLine(t, out); !strings.Contains(got, "test-step1") {
		t.Errorf("dispatch hint = %q, want it to name ready step test-step1", got)
	}
}

// With more than one molecule listed, the header names which listed entry the
// example came from, so it cannot disagree with the ID printed beneath it. The
// first listed molecule is not necessarily the one that has a ready step.
func TestGatedReadyHintHeaderNamesTheEntryItPrinted(t *testing.T) {
	gated := func(id, step string) *GatedMolecule {
		mol := &GatedMolecule{MoleculeID: id, MoleculeTitle: "Gated molecule"}
		if step != "" {
			mol.ReadyStep = &types.Issue{ID: step, Title: "Resume work"}
		}
		return mol
	}

	tests := []struct {
		name      string
		molecules []*GatedMolecule
		wantEntry string
		wantStep  string
	}{
		{
			name:      "first listed molecule has the ready step",
			molecules: []*GatedMolecule{gated("test-mol1", "test-step1"), gated("test-mol2", "test-step2")},
			wantEntry: "#1 above",
			wantStep:  "test-step1",
		},
		{
			name:      "example comes from a later molecule",
			molecules: []*GatedMolecule{gated("test-mol1", ""), gated("test-mol2", "test-step2")},
			wantEntry: "#2 above",
			wantStep:  "test-step2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderGatedHint(t, tt.molecules)
			if !strings.Contains(out, tt.wantEntry) {
				t.Errorf("hint header does not name listing entry %q:\n%s", tt.wantEntry, out)
			}
			if got := hintCommandLine(t, out); !strings.Contains(got, tt.wantStep) {
				t.Errorf("dispatch hint = %q, want it to name ready step %s", got, tt.wantStep)
			}
		})
	}
}

func TestGatedReadyHintWithoutReadyStep(t *testing.T) {
	out := renderGatedHint(t, []*GatedMolecule{{
		MoleculeID:    "test-mol1",
		MoleculeTitle: "Gated molecule",
	}})

	if got := hintCommandLine(t, out); !strings.Contains(got, "<ready-step-id>") {
		t.Errorf("dispatch hint = %q, want a placeholder when no ready step is known", got)
	}
}
