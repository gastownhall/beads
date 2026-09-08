package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// has_spike appears ONLY in a step condition: it is not declared in [vars] and
// no issue field references it.
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
title = "Spike first"
type = "task"
condition = "{{has_spike}}"
`

// writeCondVarFormula puts the formula where the cook pipeline will find it.
func writeCondVarFormula(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "condvar-pour.formula.toml"), []byte(conditionVarFormula), 0o644); err != nil {
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
	searchPaths := []string{writeCondVarFormula(t)}

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
