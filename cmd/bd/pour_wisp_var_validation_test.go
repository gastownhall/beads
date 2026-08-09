//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Regression coverage for mybd-u2r6: bd mol pour/wisp previously validated
// only variable *presence* (extractRequiredVariables), never the enum/pattern
// constraints cook enforces. Once resolveAndCookFormulaWithVars started
// calling formula.ValidateProvidedVars (mybd-u2r6 fix), pour/wisp needed to
// distinguish "the arg isn't a formula at all" (fall through to legacy
// proto-ID resolution) from "it IS a formula, but the given --var values
// violate a constraint" (report directly) without disturbing the missing-var
// hint path, which has its own better UX and must keep working unchanged.

const varValidationFormulaTOML = `formula = "pour-wisp-var-validation-test"
version = 1
type = "workflow"

[vars.policy]
required = true
enum = ["merge-completes", "tracking-only"]

[vars.slug]
pattern = "^[a-z]+$"

[[steps]]
id = "publish"
title = "Publish with {{policy}} / {{slug}}"
`

// writeVarValidationFormula writes the shared fixture under a temp GT_ROOT so
// pour/wisp's DefaultSearchPaths() resolution (which always passes a nil
// search-path list) discovers it by name without perturbing the real
// project's formula registry.
func writeVarValidationFormula(t *testing.T) {
	t.Helper()
	gtRoot := t.TempDir()
	formulaDir := filepath.Join(gtRoot, ".beads", "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("mkdir formula dir: %v", err)
	}
	formulaPath := filepath.Join(formulaDir, "pour-wisp-var-validation-test.formula.toml")
	if err := os.WriteFile(formulaPath, []byte(varValidationFormulaTOML), 0o600); err != nil {
		t.Fatalf("write formula fixture: %v", err)
	}
	t.Setenv("GT_ROOT", gtRoot)
}

func makePourVarTestCmd(varFlags []string) *cobra.Command {
	c := &cobra.Command{Use: "pour"}
	c.Flags().Bool("dry-run", true, "")
	c.Flags().StringArray("var", varFlags, "")
	c.Flags().String("assignee", "", "")
	c.Flags().StringSlice("attach", []string{}, "")
	c.Flags().String("attach-type", "", "")
	return c
}

func makeWispVarTestCmd(varFlags []string) *cobra.Command {
	c := &cobra.Command{Use: "wisp"}
	c.Flags().StringArray("var", varFlags, "")
	c.Flags().Bool("dry-run", true, "")
	c.Flags().Bool("root-only", false, "")
	return c
}

func TestPourRejectsEnumViolatingVar(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPour(makePourVarTestCmd([]string{"policy=bogus", "slug=abc"}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runPour accepted a value outside the declared enum")
	}
	if !strings.Contains(stderr, `not in allowed values`) {
		t.Fatalf("runPour stderr = %q, want an enum-violation message", stderr)
	}
}

func TestPourRejectsPatternViolatingVar(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPour(makePourVarTestCmd([]string{"policy=merge-completes", "slug=NOT-LOWERCASE"}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runPour accepted a value violating the declared pattern")
	}
	if !strings.Contains(stderr, `does not match pattern`) {
		t.Fatalf("runPour stderr = %q, want a pattern-violation message", stderr)
	}
}

// TestPourMissingVarHintUnchanged asserts that a genuinely absent required
// var still surfaces through checkPourVars/missingVarHint's existing
// HandleErrorWithHint path, not the new enum/pattern validation error path.
func TestPourMissingVarHintUnchanged(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPour(makePourVarTestCmd([]string{}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runPour accepted a formula with a missing required variable")
	}
	if !strings.Contains(stderr, "missing required variables") {
		t.Fatalf("runPour stderr = %q, want the missing-required-variables message", stderr)
	}
	if !strings.Contains(stderr, "Hint: Provide them with: --var policy=") {
		t.Fatalf("runPour stderr = %q, want the pour-specific --var hint text", stderr)
	}
}

// TestPourRejectsExplicitlyEmptyRequiredVar covers the bead's headline repro:
// an unset shell variable interpolated into --var (e.g. --var
// policy=$UNSET_SHELL_VAR) arrives as an explicitly-*provided*, empty-string
// value rather than an absent flag, so it must hit ValidateProvidedVars'
// required-and-empty check rather than the missing-var hint path.
func TestPourRejectsExplicitlyEmptyRequiredVar(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runPour(makePourVarTestCmd([]string{"policy=", "slug=abc"}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runPour accepted an explicitly-empty value for a required variable")
	}
	if !strings.Contains(stderr, "is required and cannot be empty") {
		t.Fatalf("runPour stderr = %q, want the required-and-empty message", stderr)
	}
}

func TestWispRejectsEnumViolatingVar(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runWispCreate(makeWispVarTestCmd([]string{"policy=bogus", "slug=abc"}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runWispCreate accepted a value outside the declared enum")
	}
	if !strings.Contains(stderr, `not in allowed values`) {
		t.Fatalf("runWispCreate stderr = %q, want an enum-violation message", stderr)
	}
}

func TestWispRejectsPatternViolatingVar(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runWispCreate(makeWispVarTestCmd([]string{"policy=merge-completes", "slug=NOT-LOWERCASE"}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runWispCreate accepted a value violating the declared pattern")
	}
	if !strings.Contains(stderr, `does not match pattern`) {
		t.Fatalf("runWispCreate stderr = %q, want a pattern-violation message", stderr)
	}
}

// TestWispMissingVarHintUnchanged mirrors TestPourMissingVarHintUnchanged for
// wisp's own checkRequiredVars/firstMissingVar hint path.
func TestWispMissingVarHintUnchanged(t *testing.T) {
	writeVarValidationFormula(t)
	s := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	withWispTestGlobals(t, s, context.Background())

	var runErr error
	stderr := captureStderr(t, func() {
		runErr = runWispCreate(makeWispVarTestCmd([]string{}), []string{"pour-wisp-var-validation-test"})
	})

	if runErr == nil {
		t.Fatal("runWispCreate accepted a formula with a missing required variable")
	}
	if !strings.Contains(stderr, "missing required variables") {
		t.Fatalf("runWispCreate stderr = %q, want the missing-required-variables message", stderr)
	}
	if !strings.Contains(stderr, "Hint: Provide them with: --var policy=") {
		t.Fatalf("runWispCreate stderr = %q, want the wisp-specific --var hint text", stderr)
	}
}
