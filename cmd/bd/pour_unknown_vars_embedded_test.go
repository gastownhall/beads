//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unknown-var check runs end to end through the real cook pipeline, which
// is where its known set is actually assembled.
//
// The unit tests hand-build subgraphs, so they cannot see the ordering that
// matters most here: formula.FilterStepsByCondition consumes step conditions
// and drops the steps carrying them BEFORE the cook, so a var used only in a
// condition leaves no trace in the cooked subgraph. cook records those names
// ahead of the filter for exactly this reason. Pouring with the condition var
// set to a falsey value is the case that regresses if it stops doing so: the
// step is gone, and the var that removed it must still count as consumable.
func TestBdMolPour_AcceptsAVarUsedOnlyByAStepCondition(t *testing.T) {
	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cvp")

	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}

	// has_spike appears ONLY in a step condition: it is not declared in
	// [vars] and no issue field references it.
	const formulaTOML = `formula = "condvar-pour"
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
	if err := os.WriteFile(filepath.Join(formulaDir, "condvar-pour.formula.toml"), []byte(formulaTOML), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	for _, spike := range []string{"true", "false"} {
		t.Run("has_spike_"+spike, func(t *testing.T) {
			out, err := bdRunWithFlockRetry(t, bd, dir, "mol", "pour", "condvar-pour",
				"--var", "story=s1", "--var", "has_spike="+spike, "--dry-run")
			if err != nil {
				t.Fatalf("pour rejected a var its own step condition consumes (has_spike=%s): %v\n%s", spike, err, out)
			}
			if strings.Contains(string(out), "unknown variables") {
				t.Errorf("pour reported has_spike as unknown (has_spike=%s):\n%s", spike, out)
			}
		})
	}

	// The condition var widens the known set; it does not disable the check.
	t.Run("typo_in_the_condition_var_is_still_rejected", func(t *testing.T) {
		out, err := bdRunWithFlockRetry(t, bd, dir, "mol", "pour", "condvar-pour",
			"--var", "story=s1", "--var", "has_spke=true", "--dry-run")
		if err == nil {
			t.Fatalf("pour accepted a typo'd var name:\n%s", out)
		}
		if !strings.Contains(string(out), "has_spke") {
			t.Errorf("error does not name the unusable var:\n%s", out)
		}
	})
}
