package formula

import "testing"

func TestPrimitiveByName_ExactBeatsSuffixStrip(t *testing.T) {
	// "gate" must resolve to Gate (the field-bearing struct), not GateRule.
	// Same logic for any other Foo / FooRule / FooSpec collision.
	cases := []struct {
		input string
		want  string
	}{
		{"Gate", "Gate"},
		{"gate", "Gate"},
		{"gaterule", "GateRule"},
		{"gate_rule", "GateRule"},
	}
	for _, c := range cases {
		got := PrimitiveByName(c.input)
		if got == nil {
			t.Errorf("PrimitiveByName(%q) = nil, want %s", c.input, c.want)
			continue
		}
		if got.Name != c.want {
			t.Errorf("PrimitiveByName(%q) = %s, want %s", c.input, got.Name, c.want)
		}
	}
}

func TestPrimitiveByName_SuffixStripFallback(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"loop", "LoopSpec"},
		{"Loop", "LoopSpec"},
		{"loop_spec", "LoopSpec"},
		{"on_complete", "OnCompleteSpec"},
		{"OnComplete", "OnCompleteSpec"},
		{"branch", "BranchRule"},
		{"compose", "ComposeRules"},
		{"map", "MapRule"},
		{"expand", "ExpandRule"},
		{"advice", "AdviceRule"},
		{"waits_for", "WaitsForSpec"},
	}
	for _, c := range cases {
		got := PrimitiveByName(c.input)
		if got == nil {
			t.Errorf("PrimitiveByName(%q) = nil, want %s", c.input, c.want)
			continue
		}
		if got.Name != c.want {
			t.Errorf("PrimitiveByName(%q) = %s, want %s", c.input, got.Name, c.want)
		}
	}
}

func TestPrimitiveByName_Unknown(t *testing.T) {
	for _, in := range []string{"", "bogus", "thing-that-does-not-exist", "step!!!"} {
		if got := PrimitiveByName(in); got != nil {
			t.Errorf("PrimitiveByName(%q) = %s, want nil", in, got.Name)
		}
	}
}

func TestPrimitivesIndex_NonEmpty(t *testing.T) {
	if len(Primitives) == 0 {
		t.Fatal("Primitives is empty; did go generate run?")
	}
	// Spot-check a few that should always be present.
	for _, name := range []string{"Formula", "Step", "LoopSpec", "OnCompleteSpec"} {
		if PrimitiveByName(name) == nil {
			t.Errorf("expected %s in Primitives index", name)
		}
	}
}

func TestPrimitivesIndex_StepExcludesSourceFields(t *testing.T) {
	// Step.SourceFormula and Step.SourceLocation are tagged json:"-"
	// and must never appear in the public schema.
	step := PrimitiveByName("Step")
	if step == nil {
		t.Fatal("Step missing from Primitives")
	}
	for _, f := range step.Fields {
		if f.Name == "SourceFormula" || f.Name == "SourceLocation" {
			t.Errorf("Step exposes internal field %s; json:\"-\" fields must be filtered", f.Name)
		}
	}
}
