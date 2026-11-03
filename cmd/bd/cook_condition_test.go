package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cookConditionWorkflowTOML is the GH#7639 workflow: an unconditional build
// step plus a deploy step conditioned on {{deploy}}, whose default is false.
const cookConditionWorkflowTOML = `formula = "cond-cook"
version = 1
type = "workflow"

[vars.deploy]
description = "Whether to include the deploy step"
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "deploy"
title = "Deploy to production"
condition = "{{deploy}}"
`

// writeCookConditionFormula writes the GH#7639 workflow to its own temp
// directory and answers the formula file's path. The directory also serves as
// a registry search path, so the pour-parity subtest can load the formula by
// name through the same path pour uses.
func writeCookConditionFormula(t *testing.T) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "cond-cook.formula.toml")
	if err := os.WriteFile(path, []byte(cookConditionWorkflowTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

// newCookConditionCommand builds a cook command with explicit flag values:
// runtime spells --mode=runtime (so the default-value case, with no --var,
// still runs runtime mode), dryRun the --dry-run flag, vars the --var values.
func newCookConditionCommand(t *testing.T, runtime, dryRun bool, vars ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", dryRun, "")
	cmd.Flags().Bool("persist", false, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().StringSlice("search-path", nil, "")
	cmd.Flags().String("prefix", "", "")
	cmd.Flags().StringArray("var", vars, "")
	cmd.Flags().String("mode", "", "")
	if runtime {
		if err := cmd.Flags().Set("mode", "runtime"); err != nil {
			t.Fatal(err)
		}
	}
	return cmd
}

// cookedStep is one step of the JSON a runtime cook emits, children included.
type cookedStep struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Condition string       `json:"condition"`
	Children  []cookedStep `json:"children"`
}

// cookedFormulaJSON is the formula a cook emits, as parsed from stdout.
type cookedFormulaJSON struct {
	Steps []cookedStep `json:"steps"`
}

// runCookJSON runs cook and parses its JSON output.
func runCookJSON(t *testing.T, cmd *cobra.Command, formulaArg string) cookedFormulaJSON {
	t.Helper()
	stdout, stderr, err := runCookCapturingOutput(t, cmd, formulaArg)
	if err != nil {
		t.Fatalf("runCook: %v; stderr = %q", err, stderr)
	}
	var out cookedFormulaJSON
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parse cook JSON: %v\n%s", err, stdout)
	}
	return out
}

// cookedStepIDs answers every step id in the JSON, children included.
func cookedStepIDs(steps []cookedStep) []string {
	var ids []string
	var walk func([]cookedStep)
	walk = func(ss []cookedStep) {
		for _, s := range ss {
			ids = append(ids, s.ID)
			walk(s.Children)
		}
	}
	walk(steps)
	sort.Strings(ids)
	return ids
}

// TestRunCookAppliesStepConditionsInRuntimeMode pins GH#7639: runtime cook
// filters steps whose condition evaluates false — across dry-run, JSON and
// the persist path — exactly as `bd mol pour` does through
// formula.FilterStepsByCondition. Compile mode keeps every step and its
// condition, so a runtime cook and a pour of the same formula agree, and a
// compile-time cook still shows the whole template.
func TestRunCookAppliesStepConditionsInRuntimeMode(t *testing.T) {
	formulaPath, formulaDir := writeCookConditionFormula(t)

	// AC-1: the dry-run listing omits a step whose condition is false.
	t.Run("dry run lists only surviving steps", func(t *testing.T) {
		cmd := newCookConditionCommand(t, true, true, "deploy=false")
		stdout, stderr, err := runCookCapturingOutput(t, cmd, formulaPath)
		if err != nil {
			t.Fatalf("runCook: %v; stderr = %q", err, stderr)
		}
		if !strings.Contains(stdout, "Always build") {
			t.Errorf("stdout = %q, want the unconditional step listed", stdout)
		}
		if strings.Contains(stdout, "Deploy to production") {
			t.Errorf("runtime cook listed a step whose condition is false (GH#7639):\n%s", stdout)
		}
	})

	// Parity with pour: the same formula, the same variable values, the same
	// surviving steps (resolveAndCookFormulaWithVars is pour's own path).
	t.Run("pour parity", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			vars  map[string]string
			want  []string
			dropD bool
		}{
			{"deploy=false", map[string]string{"deploy": "false"}, []string{"cond-cook.build"}, true},
			{"deploy=true", map[string]string{"deploy": "true"}, []string{"cond-cook.build", "cond-cook.deploy"}, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sg, err := resolveAndCookFormulaWithVars("cond-cook", []string{formulaDir}, tc.vars)
				if err != nil {
					t.Fatalf("pour cook: %v", err)
				}
				if got := allIssueIDs(sg); !equalStringSets(got, append([]string{"cond-cook"}, tc.want...)) {
					t.Errorf("pour subgraph issues = %v, want %v", got, tc.want)
				}
				cmd := newCookConditionCommand(t, true, false, "deploy="+tc.vars["deploy"])
				out := runCookJSON(t, cmd, formulaPath)
				got := cookedStepIDs(out.Steps)
				if tc.dropD {
					if len(got) != 1 || got[0] != "build" {
						t.Errorf("cook steps = %v, want only build", got)
					}
				} else if len(got) != 2 {
					t.Errorf("cook steps = %v, want build and deploy", got)
				}
			})
		}
	})

	// Children of a dropped step drop with it: the filter never leaves a
	// child whose parent is gone.
	t.Run("children drop with a conditioned parent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nested.formula.toml")
		src := `formula = "nested"
version = 1
type = "workflow"

[vars.deploy]
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "rollout"
title = "Roll out"
condition = "{{deploy}}"

[[steps.children]]
id = "canary"
title = "Canary first"

[[steps.children]]
id = "promote"
title = "Promote"
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runCookJSON(t, newCookConditionCommand(t, true, false), path)
		if got := cookedStepIDs(out.Steps); len(got) != 1 || got[0] != "build" {
			t.Errorf("runtime cook steps = %v, want only build (rollout and its children dropped)", got)
		}
		out = runCookJSON(t, newCookConditionCommand(t, true, false, "deploy=true"), path)
		if got := cookedStepIDs(out.Steps); !equalStringSets(got, []string{"build", "canary", "promote", "rollout"}) {
			t.Errorf("runtime cook steps with deploy=true = %v, want rollout with its children", got)
		}
	})

	// Negation and comparison conditions evaluate against the merged values.
	t.Run("negated and comparison conditions", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "ops.formula.toml")
		src := `formula = "ops"
version = 1
type = "workflow"

[vars.env]
default = "staging"
[vars.canary]
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "prod-only"
title = "Prod only"
condition = "{{env}} == \"prod\""

[[steps]]
id = "not-prod"
title = "Not prod"
condition = "{{env}} != \"prod\""

[[steps]]
id = "no-canary"
title = "No canary"
condition = "!{{canary}}"
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runCookJSON(t, newCookConditionCommand(t, true, false), path)
		if got := cookedStepIDs(out.Steps); !equalStringSets(got, []string{"build", "no-canary", "not-prod"}) {
			t.Errorf("runtime cook steps = %v, want build, not-prod and no-canary", got)
		}
		out = runCookJSON(t, newCookConditionCommand(t, true, false, "env=prod", "canary=true"), path)
		if got := cookedStepIDs(out.Steps); !equalStringSets(got, []string{"build", "prod-only"}) {
			t.Errorf("runtime cook steps with env=prod canary=true = %v, want build and prod-only", got)
		}
	})

	// A condition that is not one of the supported formats is a cook error,
	// never a silently-ignored condition.
	t.Run("malformed condition is an error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "badcond.formula.toml")
		src := `formula = "badcond"
version = 1
type = "workflow"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "odd"
title = "Odd condition"
condition = "deploy == true"
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := runCookCapturingOutput(t, newCookConditionCommand(t, true, false), path)
		if err == nil {
			t.Fatal("runtime cook accepted a malformed step condition")
		}
		if !strings.Contains(stderr, "filtering steps by condition") {
			t.Errorf("stderr = %q, want the condition-filtering error", stderr)
		}
	})

	// A placeholder only a dropped step uses is no longer demanded: the
	// runtime's required-variable check sees only the surviving steps.
	t.Run("a placeholder only a dropped step uses is not demanded", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "droppedvar.formula.toml")
		src := `formula = "droppedvar"
version = 1
type = "workflow"

[vars.deploy]
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "docs"
title = "Write docs"
description = "See {{extra_notes}} first"
condition = "{{deploy}}"
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runCookJSON(t, newCookConditionCommand(t, true, false), path)
		if got := cookedStepIDs(out.Steps); len(got) != 1 || got[0] != "build" {
			t.Errorf("runtime cook steps = %v, want only build (docs dropped, so {{extra_notes}} is not demanded)", got)
		}
	})

	// A condition variable with no value anywhere evaluates as empty — the
	// step drops, and the cook does not demand a value for it.
	t.Run("an absent condition variable evaluates empty, not an error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "absentvar.formula.toml")
		src := `formula = "absentvar"
version = 1
type = "workflow"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "maybe"
title = "Maybe step"
condition = "{{maybe}}"
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runCookJSON(t, newCookConditionCommand(t, true, false), path)
		if got := cookedStepIDs(out.Steps); len(got) != 1 || got[0] != "build" {
			t.Errorf("runtime cook steps = %v, want only build ({{maybe}} is empty, so falsy)", got)
		}
	})

	// Whole-formula validation is unchanged: a required variable is still
	// demanded even when every conditioned step would survive.
	t.Run("required variables are still validated", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "reqvar.formula.toml")
		src := `formula = "reqvar"
version = 1
type = "workflow"

[vars.env]
required = true

[[steps]]
id = "build"
title = "Build for {{env}}"

[[steps]]
id = "deploy"
title = "Deploy"
condition = "{{env}} == \"prod\""
`
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, err := runCookCapturingOutput(t, newCookConditionCommand(t, true, false), path)
		if err == nil {
			t.Fatal("runtime cook accepted a missing required variable")
		}
		if !strings.Contains(stderr, `variable "env" is required`) {
			t.Errorf("stderr = %q, want the required-variable error", stderr)
		}
	})
}

func equalStringSets(got, want []string) bool {
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedGot)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if len(sortedGot) != len(sortedWant) {
		return false
	}
	for i := range sortedGot {
		if sortedGot[i] != sortedWant[i] {
			return false
		}
	}
	return true
}