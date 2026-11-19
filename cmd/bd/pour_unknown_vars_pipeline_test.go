package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// has_spike appears ONLY in a step condition: it is not declared in [vars] and
// no issue field references it. spike_area appears ONLY in the text of the step
// that same condition can remove, so it is undeclared too and survives into the
// cooked subgraph only while has_spike is truthy.
const conditionVarFormula = `formula = "condvar-pour"
version = 1
type = "workflow"

[vars.story]
required = true

[[steps]]
id = "design"
title = "Design {{story}}"
type = "task"

[[steps]]
id = "spike"
title = "Spike {{spike_area}} first"
type = "task"
condition = "{{has_spike}}"
`

// writePipelineFormula puts a formula where the cook pipeline will find it.
func writePipelineFormula(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	return dir
}

// The unknown-var check runs against a subgraph produced by the REAL cook
// pipeline, which is where its known set is actually assembled.
//
// The unit tests above hand-build subgraphs, so they cannot see the ordering
// that matters most here: formula.FilterStepsByCondition consumes step
// conditions and drops the steps carrying them BEFORE the cook, so a var used
// only in a condition leaves no trace in the cooked subgraph. cook records
// those names ahead of the filter for exactly this reason.
//
// Pouring with the condition var set to a FALSEY value is the case that
// regresses if the collection ever moves after the filter: the step is gone,
// and the var that removed it must still count as consumable. Reviewing this
// fix, that was the case a plausible-looking fix one call later would have
// missed.
func TestCookedSubgraphAcceptsAVarUsedOnlyByAStepCondition(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "condvar-pour", conditionVarFormula)}

	for _, spike := range []string{"true", "false"} {
		t.Run("has_spike_"+spike, func(t *testing.T) {
			vars := map[string]string{"story": "s1", "has_spike": spike}

			subgraph, err := resolveAndCookFormulaWithVars("condvar-pour", searchPaths, vars)
			if err != nil {
				t.Fatalf("cook: %v", err)
			}
			// The premise of the falsey case: the step really is gone, so
			// nothing in the subgraph mentions has_spike any more.
			if spike == "false" {
				for _, issue := range subgraph.Issues {
					if strings.Contains(issue.Title, "Spike") {
						t.Fatalf("the conditional step survived a falsey condition, so this case is not testing the ordering: %q", issue.Title)
					}
				}
			}

			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("pour rejected a var its own step condition consumes (has_spike=%s): %v", spike, err)
			}
		})
	}

	// A var referenced only inside the step the condition removes must not lose
	// its validity when that step goes: otherwise `--var has_spike=false --var
	// spike_area=parser` fails while `has_spike=true` succeeds, and whether a
	// name is accepted depends on another name's VALUE. The falsey case is the
	// one that regresses if collection ever moves after the filter.
	for _, spike := range []string{"true", "false"} {
		t.Run("dropped_step_text_var_has_spike_"+spike, func(t *testing.T) {
			vars := map[string]string{"story": "s1", "has_spike": spike, "spike_area": "parser"}

			subgraph, err := resolveAndCookFormulaWithVars("condvar-pour", searchPaths, vars)
			if err != nil {
				t.Fatalf("cook: %v", err)
			}
			if spike == "false" {
				for _, issue := range subgraph.Issues {
					if strings.Contains(issue.Title, "spike_area") {
						t.Fatalf("the conditional step survived, so this case is not testing the dropped-step path: %q", issue.Title)
					}
				}
			}

			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("pour rejected a var referenced only by a step it dropped (has_spike=%s): %v", spike, err)
			}
		})
	}

	// The condition var widens the known set; it does not disable the check.
	t.Run("typo_in_the_condition_var_is_still_rejected", func(t *testing.T) {
		vars := map[string]string{"story": "s1", "has_spke": "true"}

		subgraph, err := resolveAndCookFormulaWithVars("condvar-pour", searchPaths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}

		err = checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph))
		if err == nil {
			t.Fatal("pour accepted a typo'd var name")
		}
		if !strings.Contains(err.Error(), "has_spke") {
			t.Errorf("error does not name the unusable var: %v", err)
		}
		// The real name has to be offered, which is the whole point of
		// carrying condition vars into the known set.
		if !strings.Contains(err.Error(), "has_spike") {
			t.Errorf("error does not offer the condition var among the available names: %v", err)
		}
	})
}

// run_id and gate_repo appear ONLY in the gate of a step that deploy can
// remove, and neither is declared in [vars]. createGateIssue turns that gate
// into its own issue - await_id mirrored into its title and AwaitID, and a
// gh:* gate's repo selector into metadata.repo - which goes with the step.
const gateVarFormula = `formula = "gatevar-pour"
version = 1
type = "workflow"

[[steps]]
id = "build"
title = "Build"
type = "task"

[[steps]]
id = "await-ci"
title = "Wait for CI"
type = "task"
condition = "{{deploy}}"

[steps.gate]
type = "gh:run"
await_id = "{{run_id}}"
repo = "{{gate_repo}}"
`

// A var referenced only by the GATE of a step the condition filter drops stays
// consumable, for the same reason dropped step text does: otherwise whether
// run_id is accepted would depend on deploy's value. cook reads a step's gate
// fields ahead of the filter along with its text, and the falsey case is the
// one that regresses if it ever stops - the gate issue is gone, so nothing
// else in the subgraph names either var.
func TestCookedSubgraphAcceptsAVarUsedOnlyByADroppedStepsGate(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "gatevar-pour", gateVarFormula)}

	for _, deploy := range []string{"true", "false"} {
		t.Run("deploy_"+deploy, func(t *testing.T) {
			vars := map[string]string{"deploy": deploy, "run_id": "ci.yml", "gate_repo": "octo/app"}

			subgraph, err := resolveAndCookFormulaWithVars("gatevar-pour", searchPaths, vars)
			if err != nil {
				t.Fatalf("cook: %v", err)
			}
			// The premise, both ways: the gate issue exists exactly while
			// its step does.
			gates := 0
			for _, issue := range subgraph.Issues {
				if issue.AwaitType != "" {
					gates++
				}
			}
			if want := map[string]int{"true": 1, "false": 0}[deploy]; gates != want {
				t.Fatalf("deploy=%s cooked %d gate issues, want %d, so this case is not testing the dropped-gate path", deploy, gates, want)
			}

			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("pour rejected a var referenced only by a step's gate (deploy=%s): %v", deploy, err)
			}
		})
	}
}

// release_owner, release_train and change_ticket appear ONLY in the assignee, a
// label and a nested metadata value of the step deploy can remove, and none is
// declared in [vars]. The pour substitutes all three fields (GH#5110,
// GH#5754), but no prose field mirrors them.
const fieldVarFormula = `formula = "fieldvar-pour"
version = 1
type = "workflow"

[[steps]]
id = "build"
title = "Build"
type = "task"

[[steps]]
id = "release"
title = "Release"
type = "task"
condition = "{{deploy}}"
assignee = "{{release_owner}}"
labels = ["train:{{release_train}}"]
metadata = { change = { ticket = "{{change_ticket}}" } }
`

// A var referenced only by the assignee, a label or a metadata value of a step
// the condition filter drops stays consumable too: cook reads those fields
// ahead of the filter along with the step's text, and the falsey case is the
// one that regresses if it ever stops - the step is gone, so nothing else in
// the subgraph names any of the three.
func TestCookedSubgraphAcceptsAVarUsedOnlyByADroppedStepsNonProseFields(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "fieldvar-pour", fieldVarFormula)}

	for _, deploy := range []string{"true", "false"} {
		t.Run("deploy_"+deploy, func(t *testing.T) {
			vars := map[string]string{"deploy": deploy, "release_owner": "alice", "release_train": "r42", "change_ticket": "CHG-7"}

			subgraph, err := resolveAndCookFormulaWithVars("fieldvar-pour", searchPaths, vars)
			if err != nil {
				t.Fatalf("cook: %v", err)
			}
			// The premise, both ways: the release step - and with it every
			// field naming these vars - exists exactly while deploy is truthy.
			released := false
			for _, issue := range subgraph.Issues {
				if issue.Title != "Release" {
					continue
				}
				released = true
				if issue.Assignee == "" || len(issue.Labels) == 0 || len(issue.Metadata) == 0 {
					t.Fatalf("the release step lost its assignee, labels or metadata in the cook, so this case is not testing them: %+v", issue)
				}
			}
			if released != (deploy == "true") {
				t.Fatalf("deploy=%s cooked the release step = %v, so this case is not testing the dropped-step path", deploy, released)
			}

			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("pour rejected a var referenced only by a step's assignee, label or metadata (deploy=%s): %v", deploy, err)
			}
		})
	}
}

// A standalone expansion formula has no [[steps]]: formula.MaterializeExpansion
// builds them from its [[template]] before the cook, substituting --var values
// into the template's single-brace {name} placeholders as it goes. component
// is not declared in [vars], so once it has been substituted nothing in the
// cooked subgraph - no issue field, not VarDefs - names it any more.
const expansionVarFormula = `formula = "expansion-pour"
version = 1
type = "expansion"

[[template]]
id = "{target}.build"
title = "Build {component}"
type = "task"
`

// A var that a standalone expansion formula's template consumes is accepted,
// even though consuming it is what erased every trace of it: like a step
// condition, the placeholder is used up before the cook, so cook has to record
// the name ahead of time. That widens the known set without disabling the
// check - a typo is still refused, and offered the real name.
func TestCookedSubgraphAcceptsAVarAStandaloneExpansionTemplateConsumes(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "expansion-pour", expansionVarFormula)}

	t.Run("template_placeholder_var", func(t *testing.T) {
		vars := map[string]string{"component": "api"}

		subgraph, err := resolveAndCookFormulaWithVars("expansion-pour", searchPaths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		// The premise: the template really did consume the var before the
		// cook, so the subgraph no longer mentions it.
		consumed := false
		for _, issue := range subgraph.Issues {
			if issue.Title == "Build api" {
				consumed = true
			}
		}
		if !consumed {
			t.Fatal("the template did not substitute component, so this case is not testing a consumed var")
		}

		if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
			t.Errorf("pour rejected a var the expansion template consumed: %v", err)
		}
	})

	t.Run("typo_is_still_rejected", func(t *testing.T) {
		vars := map[string]string{"componnet": "api"}

		subgraph, err := resolveAndCookFormulaWithVars("expansion-pour", searchPaths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}

		err = checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph))
		if err == nil {
			t.Fatal("pour accepted a typo'd var name")
		}
		if !strings.Contains(err.Error(), "componnet") {
			t.Errorf("error does not name the unusable var: %v", err)
		}
		if !strings.Contains(err.Error(), "available: component") {
			t.Errorf("error does not offer the template's placeholder among the available names: %v", err)
		}
	})
}

// standaloneGatedFormula is a standalone expansion formula whose template
// carries a gate, a waits_for edge onto a spawner named through {target},
// notes and metadata, and conditions: has_opt is undeclared and appears only
// in a condition, opt_area only in the text of the step that condition can
// remove, and flavor is a declared default.
const standaloneGatedFormula = `formula = "standalone-gated"
version = 1
type = "expansion"

[vars.flavor]
default = "plain"

[[template]]
id = "{target}.spawn"
title = "Spawn {flavor}"
notes = "spawned for {{story}}"
[template.metadata]
owner = "{flavor}"

[[template]]
id = "{target}.collect"
title = "Collect"
needs = ["{target}.spawn"]
waits_for = "children-of({target}.spawn)"
[template.gate]
type = "timer"
timeout = "1h"

[[template]]
id = "{target}.optional"
title = "Optional {{opt_area}}"
needs = ["{target}.spawn"]
condition = "{{has_opt}}"

[[template]]
id = "{target}.plain-only"
title = "Plain only"
condition = "{{flavor}} == plain"
`

// standaloneAllConditionalFormula's template steps all carry a condition.
const standaloneAllConditionalFormula = `formula = "standalone-allcond"
version = 1
type = "expansion"

[[template]]
id = "{target}.only"
title = "Only {{opt_area}}"
condition = "{{has_opt}}"
`

// standaloneWithStepsFormula is an expansion formula that also has steps:
// the template is materialized only when the condition filter removes every
// one of them.
const standaloneWithStepsFormula = `formula = "standalone-withsteps"
version = 1
type = "expansion"

[[steps]]
id = "only"
title = "Step only"
condition = "{{use_steps}}"

[[template]]
id = "{target}.tmpl"
title = "Template step"

[[template]]
id = "{target}.tmpl-opt"
title = "Template optional"
condition = "{{has_opt}}"
`

func subgraphIssuesByTitle(subgraph *TemplateSubgraph) map[string]*types.Issue {
	byTitle := make(map[string]*types.Issue, len(subgraph.Issues))
	for _, issue := range subgraph.Issues {
		byTitle[issue.Title] = issue
	}
	return byTitle
}

func subgraphHasDep(subgraph *TemplateSubgraph, issueID, dependsOnID string, depType types.DependencyType) bool {
	for _, dep := range subgraph.Dependencies {
		if dep.IssueID == issueID && dep.DependsOnID == dependsOnID && dep.Type == depType {
			return true
		}
	}
	return false
}

// TestPourStandaloneExpansionKeepsTemplateGatesAndConditions: pouring a
// standalone expansion formula keeps its template steps' gates, waits_for
// edges, notes and metadata, and filters the materialized steps by their
// conditions; a var used only by a template condition, or only by the text of
// a step that condition removes, is still accepted (bd-w153uq / #6433).
func TestPourStandaloneExpansionKeepsTemplateGatesAndConditions(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "standalone-gated", standaloneGatedFormula)}

	cook := func(t *testing.T, vars map[string]string) *TemplateSubgraph {
		t.Helper()
		subgraph, err := resolveAndCookFormulaWithVars("standalone-gated", searchPaths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		return subgraph
	}

	t.Run("gate_waits_for_and_content", func(t *testing.T) {
		// No story value: materializing would substitute {story} inside
		// {{story}} (single-brace matching), and the notes are checked
		// verbatim.
		subgraph := cook(t, map[string]string{"has_opt": "true"})
		byTitle := subgraphIssuesByTitle(subgraph)
		spawn, collect := byTitle["Spawn plain"], byTitle["Collect"]
		if spawn == nil || collect == nil {
			t.Fatalf("missing template issues; have %v", sortedTitles(byTitle))
		}
		if spawn.Notes != "spawned for {{story}}" {
			t.Errorf("spawn notes = %q, want the template notes", spawn.Notes)
		}
		if !strings.Contains(string(spawn.Metadata), `"owner":"plain"`) {
			t.Errorf("spawn metadata = %s, want owner substituted to plain", spawn.Metadata)
		}

		var gate *types.Issue
		for _, issue := range subgraph.Issues {
			if issue.IssueType == "gate" {
				gate = issue
			}
		}
		if gate == nil || gate.AwaitType != "timer" {
			t.Fatalf("no timer gate issue in the subgraph: %+v", gate)
		}
		if !subgraphHasDep(subgraph, collect.ID, gate.ID, types.DepBlocks) {
			t.Errorf("gate %s does not block %s", gate.ID, collect.ID)
		}
		if !subgraphHasDep(subgraph, collect.ID, spawn.ID, types.DepWaitsFor) {
			t.Errorf("no waits-for edge from %s onto %s", collect.ID, spawn.ID)
		}
	})

	for _, tc := range []struct {
		name         string
		vars         map[string]string
		wantOptional bool
		wantPlain    bool
	}{
		{"has_opt_true", map[string]string{"story": "s1", "has_opt": "true", "opt_area": "db"}, true, true},
		{"has_opt_false", map[string]string{"story": "s1", "has_opt": "false", "opt_area": "db"}, false, true},
		{"empty_vars_use_defaults", map[string]string{}, false, true},
		{"declared_default_overridden", map[string]string{"flavor": "fancy"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subgraph := cook(t, tc.vars)
			byTitle := subgraphIssuesByTitle(subgraph)
			// Materializing substitutes {opt_area} inside {{opt_area}} too
			// (single-brace matching), so match the title by its prefix.
			gotOptional := hasTitlePrefix(byTitle, "Optional ")
			_, gotPlain := byTitle["Plain only"]
			if gotOptional != tc.wantOptional || gotPlain != tc.wantPlain {
				t.Errorf("optional present %v (want %v), plain-only present %v (want %v); issues %v",
					gotOptional, tc.wantOptional, gotPlain, tc.wantPlain, sortedTitles(byTitle))
			}
			if err := checkPourVars(subgraph, nil, applyVariableDefaults(tc.vars, subgraph)); err != nil {
				t.Errorf("pour rejected a var the standalone template uses: %v", err)
			}
		})
	}

	t.Run("nil_vars_do_not_filter", func(t *testing.T) {
		byTitle := subgraphIssuesByTitle(cook(t, nil))
		if !hasTitlePrefix(byTitle, "Optional ") || byTitle["Plain only"] == nil {
			t.Errorf("a conditional step was filtered without condition vars; issues %v", sortedTitles(byTitle))
		}
	})

	t.Run("filter_removes_every_materialized_step", func(t *testing.T) {
		paths := []string{writePipelineFormula(t, "standalone-allcond", standaloneAllConditionalFormula)}
		vars := map[string]string{"has_opt": "false", "opt_area": "db"}
		subgraph, err := resolveAndCookFormulaWithVars("standalone-allcond", paths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		if len(subgraph.Issues) != 1 {
			t.Errorf("issues = %v, want only the root", sortedTitles(subgraphIssuesByTitle(subgraph)))
		}
		if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
			t.Errorf("pour rejected a var of the removed template step: %v", err)
		}
	})

	t.Run("pre_existing_steps", func(t *testing.T) {
		paths := []string{writePipelineFormula(t, "standalone-withsteps", standaloneWithStepsFormula)}
		for _, tc := range []struct {
			useSteps string
			vars     map[string]string
			want     []string
		}{
			// The steps survive the filter, so the template is never
			// materialized and its condition consumes nothing.
			{"true", map[string]string{"use_steps": "true"}, []string{"Step only"}},
			// The filter removes every step; the template is materialized
			// and its own conditions are filtered once.
			{"false", map[string]string{"use_steps": "false", "has_opt": "false"}, []string{"Template step"}},
		} {
			vars := tc.vars
			subgraph, err := resolveAndCookFormulaWithVars("standalone-withsteps", paths, vars)
			if err != nil {
				t.Fatalf("use_steps=%s: cook: %v", tc.useSteps, err)
			}
			byTitle := subgraphIssuesByTitle(subgraph)
			for _, title := range []string{"Step only", "Template step", "Template optional"} {
				want := slices.Contains(tc.want, title)
				if _, got := byTitle[title]; got != want {
					t.Errorf("use_steps=%s: %q present %v, want %v; issues %v", tc.useSteps, title, got, want, sortedTitles(byTitle))
				}
			}
			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("use_steps=%s: pour rejected a condition var: %v", tc.useSteps, err)
			}
		}
	})
}

func hasTitlePrefix(byTitle map[string]*types.Issue, prefix string) bool {
	for title := range byTitle {
		if strings.HasPrefix(title, prefix) {
			return true
		}
	}
	return false
}

func sortedTitles(m map[string]*types.Issue) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
