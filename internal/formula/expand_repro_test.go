package formula

import "testing"

// Repro for bd-o4gldt / GH#6433: expandStep must carry template ordering and
// gating fields (gate, waits_for, condition) into the expanded step.
func TestExpandStepCarriesGatingFields(t *testing.T) {
	target := &Step{ID: "implement", Title: "Implement"}
	template := []*Step{{
		ID:        "{target}.wait",
		Title:     "Wait",
		Needs:     []string{"{target}"},
		WaitsFor:  "all-children",
		Condition: "{{flag}}",
		Gate:      &Gate{Type: "human"},
	}}

	result, err := expandStep(target, template, nil)
	if err != nil {
		t.Fatalf("expandStep failed: %v", err)
	}
	got := result[0]
	if got.WaitsFor != "all-children" {
		t.Errorf("WaitsFor = %q, want %q", got.WaitsFor, "all-children")
	}
	if got.Condition != "{{flag}}" {
		t.Errorf("Condition = %q, want %q", got.Condition, "{{flag}}")
	}
	if got.Gate == nil || got.Gate.Type != "human" {
		t.Errorf("Gate = %+v, want Type human", got.Gate)
	}
}
