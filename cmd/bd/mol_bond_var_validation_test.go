//go:build cgo

package main

import (
	"context"
	"errors"
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
// checks vars that are provided against the formula it loaded), the sentinel
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
