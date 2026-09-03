package formula

import (
	"errors"
	"testing"
)

func varDefault(s string) *string { return &s }

// TestValidateProvidedVarsChecksAbsentDefaultAgainstEnum is a born-failing
// reproduction for the first gap in #5695: ValidateProvidedVars skipped absent
// variables entirely, so a var whose own declared default violates its enum
// (an authoring typo) was silently applied by pour/wisp/mol bond, while cook's
// ValidateVars rejected the same malformed formula. The two entry points must
// agree — a bad default is a formula-authoring error regardless of who loads it.
func TestValidateProvidedVarsChecksAbsentDefaultAgainstEnum(t *testing.T) {
	f := &Formula{
		Vars: map[string]*VarDef{
			"policy": {Enum: []string{"strict", "lax"}, Default: varDefault("loose")},
		},
	}

	// Baseline: ValidateVars already rejects the bad default.
	if err := ValidateVars(f, map[string]string{}); err == nil {
		t.Fatal("ValidateVars: want error for default not in enum, got nil")
	}

	// The fix: ValidateProvidedVars must reject it too, even though "policy" is
	// absent from the provided values.
	err := ValidateProvidedVars(f, map[string]string{})
	if err == nil {
		t.Fatal("ValidateProvidedVars: want error for absent var's bad default, got nil")
	}
	if !errors.Is(err, ErrVarValidation) {
		t.Fatalf("ValidateProvidedVars: want ErrVarValidation, got %v", err)
	}
}

// TestValidateProvidedVarsStillSkipsAbsentNoDefault guards that the fix does not
// over-reach: an absent var with NO default is still skipped (that is the whole
// purpose of ValidateProvidedVars — callers surface their own missing-var
// message), and a valid default passes.
func TestValidateProvidedVarsStillSkipsAbsentNoDefault(t *testing.T) {
	f := &Formula{
		Vars: map[string]*VarDef{
			"needed": {Enum: []string{"a", "b"}}, // no default, absent from values
		},
	}
	if err := ValidateProvidedVars(f, map[string]string{}); err != nil {
		t.Fatalf("absent no-default var must be skipped, got %v", err)
	}

	f.Vars["needed"].Default = varDefault("a") // valid default now present
	if err := ValidateProvidedVars(f, map[string]string{}); err != nil {
		t.Fatalf("valid absent default must pass, got %v", err)
	}
}
