//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

// Regression coverage for mybd-u2r6 on the proxied-server pour/wisp paths
// (runPourProxiedServer / runWispCreateProxiedServer). The non-proxied
// runPour/runWispCreate paths already distinguish "arg IS a formula but the
// --var values violate an enum/pattern/required-empty constraint" (report
// formula.ErrVarValidation directly) from "arg is not a formula at all" (fall
// through to proto-ID resolution) — see pour_wisp_var_validation_test.go. The
// proxied-server variants swallowed that same error and fell through
// regardless, producing a misleading "not found as formula or proto ID"/"not
// found as formula or proto" message instead of the validation error.

func varValidationFixtureFormula(name string) *formula.Formula {
	return &formula.Formula{
		Formula: name,
		Version: 1,
		Type:    formula.TypeWorkflow,
		Vars: map[string]*formula.VarDef{
			"policy": {
				Required: true,
				Enum:     []string{"merge-completes", "tracking-only"},
			},
		},
		Steps: []*formula.Step{
			{ID: "publish", Title: "Publish with {{policy}}", Type: "task"},
		},
	}
}

func TestProxiedServerPourRejectsEnumViolatingVar(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "ppvv")
	writeFormulaFixture(t, p, varValidationFixtureFormula("proxied-pour-var-validation-test"))

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "pour", "proxied-pour-var-validation-test", "--var", "policy=bogus")
	if err == nil {
		t.Fatalf("expected an enum-violation error, got stdout:%s stderr:%s", stdout, stderr)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "not in allowed values") {
		t.Errorf("expected an enum-violation message, got: %s", combined)
	}
	if strings.Contains(combined, "not found as formula or proto ID") {
		t.Errorf("validation error was masked by the proto-ID fallback message: %s", combined)
	}
}

func TestProxiedServerWispCreateRejectsEnumViolatingVar(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "pwvv")
	writeFormulaFixture(t, p, varValidationFixtureFormula("proxied-wisp-var-validation-test"))

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "mol", "wisp", "create", "proxied-wisp-var-validation-test", "--var", "policy=bogus")
	if err == nil {
		t.Fatalf("expected an enum-violation error, got stdout:%s stderr:%s", stdout, stderr)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "not in allowed values") {
		t.Errorf("expected an enum-violation message, got: %s", combined)
	}
	if strings.Contains(combined, "not found as formula or proto") {
		t.Errorf("validation error was masked by the proto-ID fallback message: %s", combined)
	}
}
