//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/formula"
)

// Regression coverage for the #5253 guarantees on the routed bond path:
// bd mol bond resolves operands through discoverMolBondOperand (read-only
// routing discovery) rather than resolveOrDescribe, so the discovery fallback
// must enforce the same --var enum/pattern/provided-empty validation — for
// --dry-run and execution alike — and a var-validation failure on a real
// formula must surface directly instead of being masked as
// "'X' not found as issue or formula".

func makeMolBondVarTestCmd(dryRun bool, varFlags []string) *cobra.Command {
	c := &cobra.Command{Use: "bond"}
	c.Flags().String("type", "sequential", "")
	c.Flags().String("as", "", "")
	c.Flags().Bool("dry-run", dryRun, "")
	c.Flags().Bool("ephemeral", false, "")
	c.Flags().Bool("pour", false, "")
	c.Flags().String("ref", "", "")
	c.Flags().StringArray("var", varFlags, "")
	return c
}

func runMolBondVarViolation(t *testing.T, dryRun bool) (error, string) {
	t.Helper()
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runMolBond(makeMolBondVarTestCmd(dryRun, []string{"policy=bogus", "slug=abc"}),
			[]string{"pour-wisp-var-validation-test", "pour-wisp-var-validation-test"})
	})
	return runErr, stderr
}

// TestMolBondDryRunRejectsEnumViolatingVar pins #4714's framing: the dry-run
// must fail the same way the real bond would, not preview success for a bond
// execution refuses.
func TestMolBondDryRunRejectsEnumViolatingVar(t *testing.T) {
	runErr, stderr := runMolBondVarViolation(t, true)

	if runErr == nil {
		t.Fatal("mol bond --dry-run accepted a --var value outside the declared enum")
	}
	if !strings.Contains(stderr, "not in allowed values") {
		t.Fatalf("stderr = %q, want an enum-violation message", stderr)
	}
	if strings.Contains(stderr, "not found as issue or formula") {
		t.Fatalf("stderr = %q: the var-validation failure was masked as not-found", stderr)
	}
}

func TestMolBondExecutionRejectsEnumViolatingVar(t *testing.T) {
	runErr, stderr := runMolBondVarViolation(t, false)

	if runErr == nil {
		t.Fatal("mol bond accepted a --var value outside the declared enum")
	}
	if !strings.Contains(stderr, "not in allowed values") {
		t.Fatalf("stderr = %q, want an enum-violation message", stderr)
	}
	if strings.Contains(stderr, "not found as issue or formula") {
		t.Fatalf("stderr = %q: the var-validation failure was masked as not-found", stderr)
	}
}

// TestMaterializeMolBondOperandPassesVarValidationThrough covers the writable
// phase directly: even if a violating value reaches the cook (discovery only
// checks vars that are provided against the formula it resolved), the sentinel
// must pass through unwrapped so callers and users see the constraint
// violation, not a bogus not-found.
func TestMaterializeMolBondOperandPassesVarValidationThrough(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	_, err := materializeMolBondOperand(context.Background(), s,
		&molBondDiscovery{operand: "pour-wisp-var-validation-test"},
		map[string]string{"policy": "bogus", "slug": "abc"})
	if err == nil {
		t.Fatal("materializeMolBondOperand cooked a formula whose --var values violate its enum")
	}
	if !errors.Is(err, formula.ErrVarValidation) {
		t.Fatalf("err = %v, want errors.Is(err, formula.ErrVarValidation)", err)
	}
	if strings.Contains(err.Error(), "not found as issue or formula") {
		t.Fatalf("err = %q: the var-validation failure was double-wrapped as not-found", err)
	}
}

// The direct (non-proxied) bond previews and executes through
// discoverMolBondOperand and materializeMolBondOperand, not resolveOrDescribe
// and resolveOrCookToSubgraph, so #5761's dry-run guarantee has to hold for
// this pair too: discovery is all a --dry-run runs, so whatever it accepts is
// previewed as a successful bond. As in mol_bond_dryrun_validation_test.go, the
// two phases are asserted against EACH OTHER, so a validation rule added later
// cannot re-open the gap.
func TestDiscoverMolBondOperandRejectsWhatMaterializeRejects(t *testing.T) {
	writeSearchableFormula(t, "dryrun-invalid-gate", invalidGateFormula)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	ctx := context.Background()

	_, discoverErr := discoverMolBondOperand(ctx, s, "dryrun-invalid-gate", nil)
	_, materializeErr := materializeMolBondOperand(ctx, s,
		&molBondDiscovery{operand: "dryrun-invalid-gate"}, nil)

	if materializeErr == nil {
		t.Fatal("materialization accepted an invalid formula, so this test is no longer testing the gap")
	}
	if discoverErr == nil {
		t.Fatalf("discovery accepted a formula materialization rejects (materialization said: %v)", materializeErr)
	}
	for name, err := range map[string]error{"discovery": discoverErr, "materialization": materializeErr} {
		if !errors.Is(err, formula.ErrValidation) {
			t.Errorf("%s error does not wrap formula.ErrValidation: %v", name, err)
		}
		if !strings.Contains(err.Error(), "waits_for") {
			t.Errorf("%s error does not name the failing field: %v", name, err)
		}
		if strings.Contains(err.Error(), "not found as issue or formula") {
			t.Errorf("%s reported an invalid formula as not found: %v", name, err)
		}
	}
	if discoverErr.Error() != materializeErr.Error() {
		t.Errorf("discovery and materialization report different errors:\n discovery:       %v\n materialization: %v", discoverErr, materializeErr)
	}
}

// The cook validates --var values against the MERGED formula, so discovery
// must resolve inheritance first: a var declared only in a parent is otherwise
// unchecked, and the dry-run previews a bond execution refuses.
func TestDiscoverMolBondOperandValidatesVarsAgainstTheMergedFormula(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("discover-parent", `formula = "discover-parent"
version = 1
type = "workflow"

[vars.env]
enum = ["dev", "prod"]

[[steps]]
id = "deploy"
title = "Deploy to {{env}}"
type = "task"
`)
	write("discover-child", `formula = "discover-child"
version = 1
type = "workflow"
extends = ["discover-parent"]
`)
	t.Setenv("GT_ROOT", root)

	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	ctx := context.Background()
	vars := map[string]string{"env": "staging"} // not in the parent's enum

	_, discoverErr := discoverMolBondOperand(ctx, s, "discover-child", vars)
	_, materializeErr := materializeMolBondOperand(ctx, s,
		&molBondDiscovery{operand: "discover-child"}, vars)

	if materializeErr == nil {
		t.Fatal("materialization accepted a var value outside the inherited enum")
	}
	if discoverErr == nil {
		t.Fatalf("discovery accepted a var value materialization rejects; the enum is declared only in the parent (materialization said: %v)", materializeErr)
	}
	for name, err := range map[string]error{"discovery": discoverErr, "materialization": materializeErr} {
		if !errors.Is(err, formula.ErrVarValidation) {
			t.Errorf("%s error does not wrap formula.ErrVarValidation: %v", name, err)
		}
	}
	if discoverErr.Error() != materializeErr.Error() {
		t.Errorf("discovery and materialization report different errors:\n discovery:       %v\n materialization: %v", discoverErr, materializeErr)
	}
}
