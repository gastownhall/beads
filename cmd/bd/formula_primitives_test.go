package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestFormulaPrimitiveExamples is the smoke harness for
// examples/formulas/primitives/. Each entry parses a fixture, cooks it
// through the full transformation pipeline, and asserts the primitive's
// observable effect on the resulting subgraph.
//
// A new fixture added to the directory without a registered case here is
// a deliberate failure (subTestForUnregisteredFixtures): the harness
// exists to prove primitives are wired, not that fixtures parse.
func TestFormulaPrimitiveExamples(t *testing.T) {
	primDir := primitivesDir(t)

	cases := []struct {
		name   string
		vars   map[string]string // non-nil enables Step.Condition filtering with formula defaults merged in
		assert func(t *testing.T, sg *TemplateSubgraph)
	}{
		{
			name: "loop-count",
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				for _, want := range []string{
					"loop-count.process.iter1.work",
					"loop-count.process.iter2.work",
					"loop-count.process.iter3.work",
				} {
					if !hasIssueID(sg, want) {
						t.Errorf("missing issue %q; have %v", want, allIssueIDs(sg))
					}
				}
			},
		},
		{
			name: "loop-range",
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				for _, want := range []string{"Move 1", "Move 2", "Move 3"} {
					if !hasIssueTitle(sg, want) {
						t.Errorf("missing title %q; have %v", want, allIssueTitles(sg))
					}
				}
			},
		},
		{
			name: "children-epic",
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				// Children get the parent step's ID prefixed to their own.
				for _, want := range []string{
					"children-epic.ship-feature",
					"children-epic.ship-feature.draft",
					"children-epic.ship-feature.polish",
				} {
					if !hasIssueID(sg, want) {
						t.Errorf("missing issue %q; have %v", want, allIssueIDs(sg))
					}
				}
				if !hasBlockingDep(sg, "children-epic.ship-feature.polish", "children-epic.ship-feature.draft") {
					t.Errorf("polish must block on draft; deps: %v", allDeps(sg))
				}
			},
		},
		{
			name: "branch-fanin",
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				for _, src := range []string{"test", "lint", "build"} {
					if !hasBlockingDep(sg, "branch-fanin.deploy", "branch-fanin."+src) {
						t.Errorf("deploy must block on %s; deps: %v", src, allDeps(sg))
					}
				}
			},
		},
		{
			name: "condition",
			vars: map[string]string{}, // trigger filtering with formula's deploy=false default
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				children := childIssueIDs(sg, "condition")
				if len(children) != 2 {
					t.Errorf("default deploy=false → want 2 child issues, got %d: %v", len(children), children)
				}
				if hasIssueID(sg, "condition.deploy") {
					t.Error("deploy must be excluded when deploy=false")
				}
			},
		},
		{
			name: "condition-true",
			vars: map[string]string{"deploy": "true"},
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				children := childIssueIDs(sg, "condition")
				if len(children) != 3 {
					t.Errorf("deploy=true → want 3 child issues, got %d: %v", len(children), children)
				}
				if !hasIssueID(sg, "condition.deploy") {
					t.Error("deploy must be included when deploy=true")
				}
			},
		},
		{
			name: "gate-timer",
			assert: func(t *testing.T, sg *TemplateSubgraph) {
				gate := findGate(sg)
				if gate == nil {
					t.Fatalf("no gate-typed sibling issue in subgraph; issues: %v", allIssueIDs(sg))
				}
				if gate.AwaitType != "timer" {
					t.Errorf("gate.AwaitType = %q, want %q", gate.AwaitType, "timer")
				}
				if !hasBlockingDep(sg, "gate-timer.verify", gate.ID) {
					t.Errorf("verify must block on gate %s; deps: %v", gate.ID, allDeps(sg))
				}
			},
		},
	}

	registered := map[string]bool{}
	for _, c := range cases {
		// "condition-true" is a second pass on the "condition" fixture.
		formulaName := strings.TrimSuffix(c.name, "-true")
		registered[formulaName] = true

		t.Run(c.name, func(t *testing.T) {
			sg, err := resolveAndCookFormulaWithVars(formulaName, []string{primDir}, c.vars)
			if err != nil {
				t.Fatalf("cook %q: %v", formulaName, err)
			}
			c.assert(t, sg)
		})
	}

	t.Run("every_fixture_has_a_registered_case", func(t *testing.T) {
		entries, err := os.ReadDir(primDir)
		if err != nil {
			t.Fatalf("readdir %s: %v", primDir, err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".formula.toml") && !strings.HasSuffix(e.Name(), ".formula.json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".formula.toml")
			name = strings.TrimSuffix(name, ".formula.json")
			if !registered[name] {
				t.Errorf("fixture %q has no smoke assertion in cases[]; add one or delete the fixture", e.Name())
			}
		}
	})
}

func primitivesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "formulas", "primitives"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("primitives dir missing: %v", err)
	}
	return dir
}

func hasIssueID(sg *TemplateSubgraph, id string) bool {
	for _, i := range sg.Issues {
		if i.ID == id {
			return true
		}
	}
	return false
}

func hasIssueTitle(sg *TemplateSubgraph, title string) bool {
	for _, i := range sg.Issues {
		if i.Title == title {
			return true
		}
	}
	return false
}

func hasBlockingDep(sg *TemplateSubgraph, from, to string) bool {
	for _, d := range sg.Dependencies {
		if d.IssueID == from && d.DependsOnID == to && d.Type == "blocks" {
			return true
		}
	}
	return false
}

func findGate(sg *TemplateSubgraph) *types.Issue {
	for _, i := range sg.Issues {
		if i.IssueType == "gate" {
			return i
		}
	}
	return nil
}

func childIssueIDs(sg *TemplateSubgraph, rootID string) []string {
	var out []string
	for _, i := range sg.Issues {
		if i.ID != rootID {
			out = append(out, i.ID)
		}
	}
	return out
}

func allIssueIDs(sg *TemplateSubgraph) []string {
	out := make([]string, 0, len(sg.Issues))
	for _, i := range sg.Issues {
		out = append(out, i.ID)
	}
	return out
}

func allIssueTitles(sg *TemplateSubgraph) []string {
	out := make([]string, 0, len(sg.Issues))
	for _, i := range sg.Issues {
		out = append(out, i.Title)
	}
	return out
}

func allDeps(sg *TemplateSubgraph) []string {
	out := make([]string, 0, len(sg.Dependencies))
	for _, d := range sg.Dependencies {
		out = append(out, d.IssueID+" -> "+d.DependsOnID+" ("+string(d.Type)+")")
	}
	return out
}
