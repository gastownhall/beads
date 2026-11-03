package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCookRuntimeModeFiltersStepsByCondition pins GH#7639: `bd cook
// --mode=runtime` must omit a step whose condition is false, as `bd mol pour`
// does through formula.FilterStepsByCondition.
func TestRunCookRuntimeModeFiltersStepsByCondition(t *testing.T) {
	formulaDir := t.TempDir()
	formulaPath := filepath.Join(formulaDir, "cond-filter.formula.toml")
	formulaTOML := `formula = "cond-filter"
version = 1
type = "workflow"

[vars.with_docs]
default = "false"

[[steps]]
id = "build"
title = "Always build"

[[steps]]
id = "docs"
title = "Conditional docs step"
condition = "{{with_docs}}"
`
	if err := os.WriteFile(formulaPath, []byte(formulaTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newCookValidationTestCommand(nil, "with_docs=false")
	stdout, stderr, err := runCookCapturingOutput(t, cmd, formulaPath)
	if err != nil {
		t.Fatalf("runCook: %v; stderr = %q", err, stderr)
	}
	if !strings.Contains(stdout, "Always build") {
		t.Fatalf("stdout = %q, want the unconditional step", stdout)
	}
	if strings.Contains(stdout, "Conditional docs step") {
		t.Fatalf("runtime cook listed a step whose condition is false (GH#7639):\n%s", stdout)
	}
}