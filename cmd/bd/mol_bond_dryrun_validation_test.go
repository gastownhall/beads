//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `bd mol bond --dry-run` must fail the same way the real bond does.
//
// The dry-run path (resolveOrDescribe) resolved a formula with
// parser.LoadByName, which never calls Formula.Validate - only parser.Resolve
// does. So a formula the real bond rejects previewed as a successful bond,
// including for the waits_for-without-a-spawner rule this branch adds: the
// dry-run said "will be cooked", and the bond the user then ran errored.
func TestBdMolBondDryRun_RejectsInvalidFormulaLikeTheRealBond(t *testing.T) {
	bd := buildEmbeddedBD(t)

	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dvr")

	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}

	// A waits_for gate with no needs: nothing to infer a spawner from, so the
	// cooked gate would carry its gate:<value> label and no dependency edge.
	const invalidTOML = `formula = "dryrun-invalid-gate"
version = 1
type = "workflow"

[[steps]]
id = "fanout"
title = "Fan out"
type = "task"

[[steps]]
id = "gate"
title = "Wait for the children"
type = "task"
waits_for = "all-children"
`
	if err := os.WriteFile(filepath.Join(formulaDir, "dryrun-invalid-gate.formula.toml"), []byte(invalidTOML), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	out, err := bdRunWithFlockRetry(t, bd, dir, "create", "target", "-t", "task", "-p", "4", "--json")
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}
	targetID := extractFirstJSONStringField(string(out), "id")
	if targetID == "" {
		t.Fatalf("could not parse issue id from bd create output: %s", out)
	}

	dryOut, dryErr := bdRunWithFlockRetry(t, bd, dir, "mol", "bond", "dryrun-invalid-gate", targetID, "--dry-run")
	realOut, realErr := bdRunWithFlockRetry(t, bd, dir, "mol", "bond", "dryrun-invalid-gate", targetID)

	if realErr == nil {
		t.Fatalf("the real bond accepted an invalid formula, so this test is no longer testing the gap:\n%s", realOut)
	}
	if dryErr == nil {
		t.Fatalf("dry-run previewed a bond the real bond rejects:\ndry-run output:\n%s\nreal bond error: %v\n%s", dryOut, realErr, realOut)
	}

	// Both routes must name the actual problem, not "not found as issue or
	// formula" - the formula resolved fine, it just does not validate.
	for name, out := range map[string]string{"dry-run": string(dryOut), "real bond": string(realOut)} {
		if !strings.Contains(out, "waits_for") {
			t.Errorf("%s error does not name the failing field:\n%s", name, out)
		}
		if strings.Contains(out, "not found as issue or formula") {
			t.Errorf("%s reported an invalid formula as not found:\n%s", name, out)
		}
	}
}
