package formula

import (
	"errors"
	"testing"
)

// A caller that falls back to a proto/issue lookup when a name is not a formula
// needs to tell "not a formula" from "is a formula, but invalid"; otherwise an
// invalid formula is reported as not found and its real error never surfaces.
func TestValidateErrorIsErrValidation(t *testing.T) {
	f := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", WaitsFor: "totally-bogus"},
		},
	}

	err := f.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("errors.Is(err, ErrValidation) = false for %v", err)
	}
}

func TestValidateErrorKeepsItsMessage(t *testing.T) {
	f := &Formula{
		Formula: "mol-invalid",
		Version: 1,
		Type:    TypeWorkflow,
		Steps: []*Step{
			{ID: "step1", Title: "Step 1", WaitsFor: "totally-bogus"},
		},
	}

	err := f.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	if got := err.Error(); got[:len("formula validation failed")] != "formula validation failed" {
		t.Errorf("Validate() error = %q, want it to still open with the original text", got)
	}
}
