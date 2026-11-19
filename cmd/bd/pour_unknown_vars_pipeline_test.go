package main

import (
	"os"
	"path/filepath"
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

// gateExpandingPour is a standalone expansion formula whose template steps
// carry the fields the expand path used to drop (#6433): a timer gate, a
// notes/metadata pair, a fanout collector with an explicit waits_for, and an
// optional step behind an undeclared condition-only flag whose title holds a
// double-brace content var.
const gateExpandingPourFormula = `formula = "expansion-gate-pour"
version = 1
type = "expansion"

[[template]]
id = "{target}.timed"
title = "Wait for the timer"
type = "task"
notes = "Notes for {target}"
metadata = { run = { channel = "{channel}" } }

[template.gate]
type = "timer"
timeout = "1h"

[[template]]
id = "{target}.spawn"
title = "Spawn follow-ups"
type = "task"

[[template]]
id = "{target}.collect"
title = "Collect results"
type = "task"
needs = ["{target}.spawn"]
waits_for = "children-of({target}.spawn)"

[[template]]
id = "{target}.optional"
title = "Only when {{flag}} is set ({{area}})"
type = "task"
condition = "{{flag}}"
`

// expansionDefaultsPourFormula filters a materialized step with a DECLARED
// var's default, not a supplied value.
const expansionDefaultsPourFormula = `formula = "expansion-defaults-pour"
version = 1
type = "expansion"

[vars.deploy]
default = "false"

[[template]]
id = "{target}.build"
title = "Build"
type = "task"

[[template]]
id = "{target}.deploy"
title = "Deploy"
type = "task"
needs = ["{target}.build"]
condition = "{{deploy}}"
`

// expansionAllConditionalPourFormula's every step is behind a condition, so
// a falsey pour filters them all out.
const expansionAllConditionalPourFormula = `formula = "expansion-all-conditional-pour"
version = 1
type = "expansion"

[[template]]
id = "{target}.one"
title = "One"
type = "task"
condition = "{{off}}"

[[template]]
id = "{target}.two"
title = "Two"
type = "task"
needs = ["{target}.one"]
condition = "{{off}}"
`

// expansionPreexistingPourFormula has Steps AND a Template: the template
// materializes only when the condition filter empties the steps.
const expansionPreexistingPourFormula = `formula = "expansion-preexisting-pour"
version = 1
type = "expansion"

[[steps]]
id = "design"
title = "Design"
type = "task"
condition = "{{build}}"

[[template]]
id = "{target}.later"
title = "Later"
type = "task"
`

// TestPourStandaloneExpansionKeepsTemplateGatesAndConditions: materializing
// a standalone expansion formula ran AFTER the condition filter and the var
// scan, so a template step's gate, waits_for edge and content reached the
// cook, but its condition was never evaluated and a condition-only var was
// rejected as unknown (#6433). The materialized steps now get both passes.
func TestPourStandaloneExpansionKeepsTemplateGatesAndConditions(t *testing.T) {
	searchPaths := []string{writePipelineFormula(t, "expansion-gate-pour", gateExpandingPourFormula)}

	issue := func(subgraph *TemplateSubgraph, id string) *types.Issue {
		t.Helper()
		for _, i := range subgraph.Issues {
			if i.ID == id {
				return i
			}
		}
		return nil
	}
	hasDep := func(subgraph *TemplateSubgraph, issueID, dependsOnID string, depType types.DependencyType) bool {
		t.Helper()
		for _, d := range subgraph.Dependencies {
			if d.IssueID == issueID && d.DependsOnID == dependsOnID && d.Type == depType {
				return true
			}
		}
		return false
	}

	for _, flag := range []string{"true", "false"} {
		t.Run("flag_"+flag, func(t *testing.T) {
			vars := map[string]string{"flag": flag, "area": "parser", "channel": "api"}
			subgraph, err := resolveAndCookFormulaWithVars("expansion-gate-pour", searchPaths, vars)
			if err != nil {
				t.Fatalf("cook: %v", err)
			}

			// The timer gate survived into its own issue and blocks its step.
			gate := issue(subgraph, "expansion-gate-pour.gate-main.timed")
			if gate == nil {
				t.Fatal("the template step's gate never cooked into a gate issue")
			}
			if gate.AwaitType != "timer" {
				t.Errorf("gate AwaitType = %q, want timer", gate.AwaitType)
			}
			if !hasDep(subgraph, "expansion-gate-pour.main.timed", "expansion-gate-pour.gate-main.timed", types.DepBlocks) {
				t.Error("no blocks edge from the gated step onto its gate issue")
			}

			// The waits_for collector edge survived, with the {target} in the
			// spawner reference resolved.
			if !hasDep(subgraph, "expansion-gate-pour.main.collect", "expansion-gate-pour.main.spawn", types.DepWaitsFor) {
				t.Error("no waits-for edge from the collector onto the spawner")
			}

			// Notes and metadata carried through, with the expansion's own
			// placeholders substituted.
			timed := issue(subgraph, "expansion-gate-pour.main.timed")
			if timed == nil {
				t.Fatal("main.timed never cooked")
			}
			if timed.Notes != "Notes for main" {
				t.Errorf("timed Notes = %q, want %q", timed.Notes, "Notes for main")
			}
			if !strings.Contains(string(timed.Metadata), `"channel":"api"`) {
				t.Errorf("timed Metadata = %s, want the substituted channel value", timed.Metadata)
			}

			// The optional step exists exactly while the condition holds.
			optional := issue(subgraph, "expansion-gate-pour.main.optional")
			if (optional != nil) != (flag == "true") {
				t.Errorf("flag=%s: optional step present = %v, want %v", flag, optional != nil, flag == "true")
			}

			// Both the condition-only flag and the content var a dropped
			// step carries stay consumable, whichever way the condition
			// went.
			if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
				t.Errorf("pour rejected vars of a template step the condition removed (flag=%s): %v", flag, err)
			}
		})
	}

	t.Run("nil_condition_vars_skips_the_filter", func(t *testing.T) {
		subgraph, err := resolveAndCookFormulaWithVars("expansion-gate-pour", searchPaths, nil)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		if issue(subgraph, "expansion-gate-pour.main.optional") == nil {
			t.Error("nil conditionVars dropped the optional step instead of skipping the filter")
		}
		if issue(subgraph, "expansion-gate-pour.gate-main.timed") == nil {
			t.Error("nil conditionVars lost the gate issue")
		}
	})

	t.Run("declared_default_filters_without_supplied_vars", func(t *testing.T) {
		paths := []string{writePipelineFormula(t, "expansion-defaults-pour", expansionDefaultsPourFormula)}
		subgraph, err := resolveAndCookFormulaWithVars("expansion-defaults-pour", paths, map[string]string{})
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		if issue(subgraph, "expansion-defaults-pour.main.deploy") != nil {
			t.Error("empty supplied vars left the deploy step in despite default=false")
		}
		if issue(subgraph, "expansion-defaults-pour.main.build") == nil {
			t.Error("the unconditional build step went missing")
		}
	})

	t.Run("filtering_every_step_materializes_nothing_left", func(t *testing.T) {
		paths := []string{writePipelineFormula(t, "expansion-all-conditional-pour", expansionAllConditionalPourFormula)}
		vars := map[string]string{"off": "false"}
		subgraph, err := resolveAndCookFormulaWithVars("expansion-all-conditional-pour", paths, vars)
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		for _, id := range []string{"expansion-all-conditional-pour.main.one", "expansion-all-conditional-pour.main.two"} {
			if issue(subgraph, id) != nil {
				t.Errorf("a falsey condition left %s in the subgraph", id)
			}
		}
		if err := checkPourVars(subgraph, nil, applyVariableDefaults(vars, subgraph)); err != nil {
			t.Errorf("pour rejected the condition var after filtering every step out: %v", err)
		}
	})

	t.Run("preexisting_steps_are_not_filtered_twice", func(t *testing.T) {
		paths := []string{writePipelineFormula(t, "expansion-preexisting-pour", expansionPreexistingPourFormula)}

		subgraph, err := resolveAndCookFormulaWithVars("expansion-preexisting-pour", paths, map[string]string{"build": "true"})
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		if issue(subgraph, "expansion-preexisting-pour.design") == nil {
			t.Error("the surviving pre-existing step went missing")
		}
		if issue(subgraph, "expansion-preexisting-pour.main.later") != nil {
			t.Error("the template materialized although pre-existing steps survived")
		}

		// All pre-existing steps filtered out: the template materializes
		// after all, and its steps are the ones the pour sees.
		subgraph, err = resolveAndCookFormulaWithVars("expansion-preexisting-pour", paths, map[string]string{"build": "false"})
		if err != nil {
			t.Fatalf("cook: %v", err)
		}
		if issue(subgraph, "expansion-preexisting-pour.main.later") == nil {
			t.Error("the template did not materialize after the filter emptied the steps")
		}
	})
}
