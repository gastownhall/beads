package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/formula"
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
story = "{{story}}"
nested = ["{flavor}/{{story}}"]

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
		// A story value: the {{story}} references are the pour's to fill
		// in, so materializing the template leaves them whole (it used to
		// replace the {story} inside them, leaving {s1}).
		subgraph := cook(t, map[string]string{"has_opt": "true", "story": "s1"})
		byTitle := subgraphIssuesByTitle(subgraph)
		spawn, collect := byTitle["Spawn plain"], byTitle["Collect"]
		if spawn == nil || collect == nil {
			t.Fatalf("missing template issues; have %v", sortedTitles(byTitle))
		}
		if spawn.Notes != "spawned for {{story}}" {
			t.Errorf("spawn notes = %q, want the template notes", spawn.Notes)
		}
		for _, want := range []string{`"owner":"plain"`, `"story":"{{story}}"`, `"nested":["plain/{{story}}"]`} {
			if !strings.Contains(string(spawn.Metadata), want) {
				t.Errorf("spawn metadata = %s, want it to contain %s", spawn.Metadata, want)
			}
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
			_, gotOptional := byTitle["Optional {{opt_area}}"]
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

// writePipelineFormulaSet writes each named formula file into one directory
// the cook pipeline searches, and returns it.
func writePipelineFormulaSet(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write formula %s: %v", name, err)
		}
	}
	return dir
}

// composedFormulaJSON is a workflow formula whose step "work" is expanded
// with the expansion formula with, through compose.expand, compose.map or an
// inline expand, binding bound; extra holds more top-level fields.
func composedFormulaJSON(t *testing.T, name, path, with string, bound map[string]string, steps []map[string]interface{}, extra map[string]interface{}) string {
	t.Helper()
	work := map[string]interface{}{"id": "work", "title": "Work"}
	f := map[string]interface{}{"formula": name, "type": "workflow", "version": 1}
	switch path {
	case "compose_expand":
		f["compose"] = map[string]interface{}{"expand": []interface{}{map[string]interface{}{"target": "work", "with": with, "vars": bound}}}
	case "compose_map":
		f["compose"] = map[string]interface{}{"map": []interface{}{map[string]interface{}{"select": "work", "with": with, "vars": bound}}}
	case "inline":
		work["expand"] = with
		work["expand_vars"] = bound
	default:
		t.Fatalf("unknown expansion path %q", path)
	}
	all := []interface{}{work}
	for _, s := range steps {
		all = append(all, s)
	}
	f["steps"] = all
	for k, v := range extra {
		f[k] = v
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var expansionPaths = []string{"compose_expand", "compose_map", "inline"}

// testsExpansionJSON is an expansion formula whose second template step runs
// only when its own with_tests, true by default, is.
const testsExpansionJSON = `{
	"formula": "tests-exp", "type": "expansion", "version": 1,
	"vars": {"with_tests": {"default": "true"}},
	"template": [
		{"id": "{target}.impl", "title": "Implement {target.title}"},
		{"id": "{target}.tests", "title": "Test {target.title}", "needs": ["{target}.impl"], "condition": "{{with_tests}}"}
	]
}`

// TestCookTemplateConditionReadsTheExpansionsVars: through compose.expand,
// compose.map and an inline expand, a template step's condition reads the
// value the expansion gives its variable - its [vars] default, the rule's vars
// or the step's expand_vars - so composing the expansion keeps the steps it
// keeps on its own, the pour refuses a --var for that variable as one nothing
// can read, bd cook does not ask for it, and "{{name}}" passes the poured
// formula's variable through (bd-w153uq / #6433).
func TestCookTemplateConditionReadsTheExpansionsVars(t *testing.T) {
	for _, path := range expansionPaths {
		t.Run(path, func(t *testing.T) {
			cases := []struct {
				name      string
				bound     map[string]string
				extra     map[string]interface{}
				vars      map[string]string
				wantTests bool
			}{
				{"expansion_default", nil, nil, map[string]string{}, true},
				{"bound_false", map[string]string{"with_tests": "false"}, nil, map[string]string{}, false},
				{"bound_true", map[string]string{"with_tests": "true"}, nil, map[string]string{}, true},
				{"passed_through_true", map[string]string{"with_tests": "{{run_tests}}"},
					map[string]interface{}{"vars": map[string]interface{}{"run_tests": map[string]interface{}{"default": "false"}}},
					map[string]string{"run_tests": "true"}, true},
				{"passed_through_default", map[string]string{"with_tests": "{{run_tests}}"},
					map[string]interface{}{"vars": map[string]interface{}{"run_tests": map[string]interface{}{"default": "false"}}},
					map[string]string{}, false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					dir := writePipelineFormulaSet(t, map[string]string{
						"tests-exp.formula.json": testsExpansionJSON,
						"composed.formula.json":  composedFormulaJSON(t, "composed", path, "tests-exp", tc.bound, nil, tc.extra),
					})
					subgraph, err := resolveAndCookFormulaWithVars("composed", []string{dir}, tc.vars)
					if err != nil {
						t.Fatalf("cook: %v", err)
					}
					byTitle := subgraphIssuesByTitle(subgraph)
					if byTitle["Implement Work"] == nil {
						t.Fatalf("missing the unconditional template step; issues %v", sortedTitles(byTitle))
					}
					if _, got := byTitle["Test Work"]; got != tc.wantTests {
						t.Errorf("Test Work present %v, want %v; issues %v", got, tc.wantTests, sortedTitles(byTitle))
					}
					if err := checkPourVars(subgraph, nil, applyVariableDefaults(tc.vars, subgraph)); err != nil {
						t.Errorf("pour refused its own vars %v: %v", tc.vars, err)
					}
					// with_tests is the expansion's: a --var for it reaches
					// nothing, so the pour refuses it as it did before the
					// template kept its condition.
					withTests := map[string]string{"with_tests": "true"}
					for k, v := range tc.vars {
						withTests[k] = v
					}
					err = checkPourVars(subgraph, nil, applyVariableDefaults(withTests, subgraph))
					if err == nil || !strings.Contains(err.Error(), "with_tests") {
						t.Errorf("pour accepted --var with_tests, which no step reads: err = %v", err)
					}

					// bd cook lists, and in runtime mode requires, the
					// variables ExtractVariables finds: not with_tests.
					resolved, err := loadAndResolveFormula("composed", []string{dir})
					if err != nil {
						t.Fatalf("loadAndResolveFormula: %v", err)
					}
					if got := formula.ExtractVariables(resolved); slices.Contains(got, "with_tests") {
						t.Errorf("ExtractVariables = %v, want no with_tests (the expansion binds it)", got)
					}
				})
			}
		})
	}
}

// newCookOutputTestCommand is a bd cook command over the formulas in dir: it
// prints the JSON in mode, or with dryRun the preview.
func newCookOutputTestCommand(dir string, dryRun bool, mode string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", dryRun, "")
	cmd.Flags().Bool("persist", false, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().StringSlice("search-path", []string{dir}, "")
	cmd.Flags().String("prefix", "", "")
	cmd.Flags().StringArray("var", nil, "")
	cmd.Flags().String("mode", mode, "")
	return cmd
}

// cookedStepsByID decodes bd cook's JSON output and returns its steps,
// children included, by ID.
func cookedStepsByID(t *testing.T, stdout string) map[string]*formula.Step {
	t.Helper()
	var cooked formula.Formula
	if err := json.Unmarshal([]byte(stdout), &cooked); err != nil {
		t.Fatalf("decode bd cook JSON: %v\n%s", err, stdout)
	}
	byID := make(map[string]*formula.Step)
	var walk func([]*formula.Step)
	walk = func(steps []*formula.Step) {
		for _, s := range steps {
			byID[s.ID] = s
			walk(s.Children)
		}
	}
	walk(cooked.Steps)
	return byID
}

// TestCookOutputLeavesOutAStepItsExpansionExcludes: through compose.expand,
// compose.map and an inline expand, bd cook's JSON in compile and runtime
// mode, its --dry-run preview and the proto --persist writes leave out a
// template step that the expansion's own value excludes, as every pour does,
// and a step that waited for it waits for the step it needed. Nothing they
// keep records the exclusion, so a step they showed would read as
// unconditional (bd-w153uq / #6433).
func TestCookOutputLeavesOutAStepItsExpansionExcludes(t *testing.T) {
	for _, path := range expansionPaths {
		t.Run(path, func(t *testing.T) {
			// An inline expand moves no edge onto its last step.
			var deploy []map[string]interface{}
			if path != "inline" {
				deploy = []map[string]interface{}{{"id": "deploy", "title": "Deploy", "needs": []string{"work"}}}
			}
			for _, tc := range []struct {
				name      string
				bound     map[string]string
				wantTests bool
			}{
				{"expansion_default", nil, true},
				{"bound_false", map[string]string{"with_tests": "false"}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := writePipelineFormulaSet(t, map[string]string{
						"tests-exp.formula.json": testsExpansionJSON,
						"composed.formula.json":  composedFormulaJSON(t, "composed", path, "tests-exp", tc.bound, deploy, nil),
					})
					waitsFor := "work.impl"
					if tc.wantTests {
						waitsFor = "work.tests"
					}

					for _, mode := range []string{"compile", "runtime"} {
						stdout, stderr, err := runCookCapturingOutput(t, newCookOutputTestCommand(dir, false, mode), "composed")
						if err != nil {
							t.Fatalf("bd cook --mode=%s: %v; stderr = %q", mode, err, stderr)
						}
						steps := cookedStepsByID(t, stdout)
						if steps["work.impl"] == nil {
							t.Fatalf("bd cook --mode=%s JSON lacks work.impl:\n%s", mode, stdout)
						}
						tests, got := steps["work.tests"]
						if got != tc.wantTests {
							t.Errorf("bd cook --mode=%s JSON has work.tests %v, want %v:\n%s", mode, got, tc.wantTests, stdout)
						}
						if got && tests.Condition != "" {
							t.Errorf("bd cook --mode=%s JSON: work.tests condition = %q, want the decided condition cleared", mode, tests.Condition)
						}
						if deploy != nil {
							if needs := steps["deploy"].Needs; !slices.Equal(needs, []string{waitsFor}) {
								t.Errorf("bd cook --mode=%s JSON: deploy needs %v, want [%s]", mode, needs, waitsFor)
							}
						}
					}

					stdout, stderr, err := runCookCapturingOutput(t, newCookOutputTestCommand(dir, true, ""), "composed")
					if err != nil {
						t.Fatalf("bd cook --dry-run: %v; stderr = %q", err, stderr)
					}
					if got := strings.Contains(stdout, "Test Work"); got != tc.wantTests {
						t.Errorf("bd cook --dry-run lists Test Work %v, want %v:\n%s", got, tc.wantTests, stdout)
					}

					// bd cook --persist writes the proto with collectSteps and
					// collectDependencies, which cookFormulaToSubgraph runs
					// in memory, from the same resolved formula: it must hold
					// the issues and edges of the pour.
					resolved, err := loadAndResolveFormula("composed", []string{dir})
					if err != nil {
						t.Fatalf("loadAndResolveFormula: %v", err)
					}
					proto, err := cookFormulaToSubgraph(resolved, "composed")
					if err != nil {
						t.Fatalf("cookFormulaToSubgraph: %v", err)
					}
					pour, err := resolveAndCookFormulaWithVars("composed", []string{dir}, map[string]string{})
					if err != nil {
						t.Fatalf("pour cook: %v", err)
					}
					protoTitles, pourTitles := subgraphIssuesByTitle(proto), subgraphIssuesByTitle(pour)
					if got, want := sortedTitles(protoTitles), sortedTitles(pourTitles); !slices.Equal(got, want) {
						t.Errorf("the proto holds %v, the pour %v", got, want)
					}
					if _, got := protoTitles["Test Work"]; got != tc.wantTests {
						t.Errorf("the proto holds Test Work %v, want %v", got, tc.wantTests)
					}
					if deploy != nil {
						if got, want := blocksEdgesFrom(proto, "composed.deploy"), []string{"composed." + waitsFor}; !slices.Equal(got, want) {
							t.Errorf("the proto's deploy is blocked by %v, want %v", got, want)
						}
					}
				})
			}
		})
	}
}

// tailExpansionJSON is an expansion formula whose last template step runs
// only when the poured formula's with_extra is true.
const tailExpansionJSON = `{
	"formula": "tail-exp", "type": "expansion", "version": 1,
	"template": [
		{"id": "{target}.draft", "title": "Draft {target.title}"},
		{"id": "{target}.extra", "title": "Extra {target.title}", "needs": ["{target}.draft"], "condition": "{{with_extra}}"}
	]
}`

// TestCookStepAfterAnExpansionWaitsForItsKeptTail: a step that needs or
// depends on an expanded step waits for the expansion's last step; when a
// condition drops that step, it waits for the steps the dropped one needs,
// instead of the pour failing on an edge the formula never wrote
// (bd-w153uq / #6433).
func TestCookStepAfterAnExpansionWaitsForItsKeptTail(t *testing.T) {
	for _, path := range []string{"compose_expand", "compose_map"} {
		t.Run(path, func(t *testing.T) {
			dir := writePipelineFormulaSet(t, map[string]string{
				"tail-exp.formula.json": tailExpansionJSON,
				"composed.formula.json": composedFormulaJSON(t, "composed", path, "tail-exp", nil,
					[]map[string]interface{}{
						{"id": "deploy", "title": "Deploy", "needs": []string{"work"}},
						{"id": "notify", "title": "Notify", "depends_on": []string{"work"}},
					},
					map[string]interface{}{"vars": map[string]interface{}{"with_extra": map[string]interface{}{"default": "false"}}}),
			})
			for _, tc := range []struct {
				withExtra, wantTitle string
			}{{"true", "Extra Work"}, {"false", "Draft Work"}} {
				subgraph, err := resolveAndCookFormulaWithVars("composed", []string{dir}, map[string]string{"with_extra": tc.withExtra})
				if err != nil {
					t.Fatalf("with_extra=%s: cook: %v", tc.withExtra, err)
				}
				byTitle := subgraphIssuesByTitle(subgraph)
				want := byTitle[tc.wantTitle]
				if want == nil || byTitle["Deploy"] == nil || byTitle["Notify"] == nil {
					t.Fatalf("with_extra=%s: missing issues; have %v", tc.withExtra, sortedTitles(byTitle))
				}
				for _, waiter := range []string{"Deploy", "Notify"} {
					if got := blocksEdgesFrom(subgraph, byTitle[waiter].ID); !slices.Equal(got, []string{want.ID}) {
						t.Errorf("with_extra=%s: %s is blocked by %v, want [%s]", tc.withExtra, waiter, got, want.ID)
					}
				}
			}
		})
	}
}

// TestCookExpansionRefusalsAreValidationErrors: the errors an expansion now
// raises wrap formula.ErrValidation, so pour and wisp report them instead of
// saying the formula was not found (bd-w153uq / #6433).
func TestCookExpansionRefusalsAreValidationErrors(t *testing.T) {
	loopExp := `{"formula": "loop-exp", "type": "expansion", "version": 1,
		"template": [{"id": "{target}.loop", "title": "L", "loop": {"count": 2, "body": [{"id": "iter", "title": "Iter"}]}}]}`
	badCondExp := `{"formula": "badcond-exp", "type": "expansion", "version": 1,
		"template": [{"id": "{target}.s", "title": "S", "condition": "{env} == prod"}]}`
	for _, tc := range []struct {
		name, formula string
		files         map[string]string
		want          string
	}{
		{"template_loop", "composed", map[string]string{
			"loop-exp.formula.json": loopExp,
			"composed.formula.json": composedFormulaJSON(t, "composed", "compose_expand", "loop-exp", nil, nil, nil),
		}, "declares a loop"},
		{"standalone_template_loop", "loop-exp", map[string]string{"loop-exp.formula.json": loopExp}, "declares a loop"},
		{"invalid_template_condition", "composed", map[string]string{
			"badcond-exp.formula.json": badCondExp,
			"composed.formula.json":    composedFormulaJSON(t, "composed", "compose_expand", "badcond-exp", nil, nil, nil),
		}, "invalid step condition format"},
		{"placeholder_bound_to_a_condition", "composed", map[string]string{
			"tests-exp.formula.json": testsExpansionJSON,
			"composed.formula.json":  composedFormulaJSON(t, "composed", "compose_expand", "tests-exp", map[string]string{"with_tests": "{run_tests}"}, nil, nil),
		}, `bind with_tests to "{{name}}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePipelineFormulaSet(t, tc.files)
			_, err := resolveAndCookFormulaWithVars(tc.formula, []string{dir}, map[string]string{})
			if err == nil {
				t.Fatal("cook succeeded")
			}
			if !errors.Is(err, formula.ErrValidation) {
				t.Errorf("cook error %v does not wrap formula.ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("cook error %q does not mention %q", err, tc.want)
			}
		})
	}
}
