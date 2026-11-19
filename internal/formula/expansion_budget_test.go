package formula

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bd-xx3ma9 (#7882): a formula whose loop expands past the 10000-step budget
// must fail fast instead of materializing every iteration.
func TestApplyLoops_CountOverExpansionLimitFails(t *testing.T) {
	steps := []*Step{{
		ID:    "big",
		Title: "Big loop",
		Loop: &LoopSpec{
			Count: 10001,
			Body:  []*Step{{ID: "work", Title: "Work"}},
		},
	}}

	result, err := ApplyLoops(steps)
	if err == nil {
		t.Fatalf("ApplyLoops expanded a 10001-iteration loop into %d steps; want an expansion limit error", len(result))
	}
	if !strings.Contains(err.Error(), "expansion limit exceeded") {
		t.Fatalf("ApplyLoops error = %v; want an expansion limit error", err)
	}
}

// A range loop is charged like a count loop, and the refusal names the loop
// and the limit, so the author knows which loop to split.
func TestApplyLoops_RangeOverExpansionLimitNamesLoopAndLimit(t *testing.T) {
	steps := []*Step{{
		ID:   "sweep",
		Loop: &LoopSpec{Range: "1..10001", Var: "n", Body: []*Step{{ID: "work", Title: "Work {n}"}}},
	}}
	result, err := ApplyLoops(steps)
	if err == nil {
		t.Fatalf("ApplyLoops expanded a 10001-value range into %d steps; want an expansion limit error", len(result))
	}
	for _, want := range []string{"expansion limit exceeded", `loop "sweep"`, "10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ApplyLoops error = %q; want it to contain %q", err, want)
		}
	}
}

// A nested loop's body is part of what each outer iteration clones, so two
// loops that are each under the limit still refuse when their product is not.
func TestApplyLoops_NestedLoopsOverExpansionLimitFail(t *testing.T) {
	steps := []*Step{{
		ID: "outer",
		Loop: &LoopSpec{Count: 101, Body: []*Step{{
			ID:   "inner",
			Loop: &LoopSpec{Count: 100, Body: []*Step{{ID: "leaf", Title: "Leaf"}}},
		}}},
	}}
	result, err := ApplyLoops(steps)
	if err == nil {
		t.Fatalf("ApplyLoops expanded 101x100 nested loops into %d steps; want an expansion limit error", countAllSteps(result))
	}
	if !strings.Contains(err.Error(), "expansion limit exceeded") || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("ApplyLoops error = %v; want an expansion limit error naming the limit", err)
	}
}

// countAllSteps counts steps and their children.
func countAllSteps(steps []*Step) int {
	n := 0
	for _, s := range steps {
		n += 1 + countAllSteps(s.Children)
	}
	return n
}

// The refusal is the typed sentinel, so callers can tell it from a malformed
// formula.
func TestApplyLoops_ExpansionLimitIsTheSentinel(t *testing.T) {
	steps := []*Step{{ID: "big", Loop: &LoopSpec{Count: MaxExpandedSteps + 1, Body: []*Step{{ID: "work"}}}}}
	if _, err := ApplyLoops(steps); !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyLoops error = %v; want ErrExpansionLimit", err)
	}
	if _, err := ApplyControlFlow(steps, nil); !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyControlFlow error = %v; want ErrExpansionLimit", err)
	}
}

// Loops far past the limit refuse before materializing anything: these would
// allocate a billion steps, or never finish, if the check came after cloning.
func TestApplyLoops_HugeLoopsRefuseBeforeMaterializing(t *testing.T) {
	cases := map[string]*LoopSpec{
		"count":            {Count: 1_000_000_000, Body: []*Step{{ID: "work"}}},
		"range":            {Range: "1..1000000000", Body: []*Step{{ID: "work"}}},
		"power range":      {Range: "1..2^40", Body: []*Step{{ID: "work"}}},
		"negative to 2^62": {Range: "-(2^62)..2^62", Body: []*Step{{ID: "work"}}},
		"nested 1000x1000": {Count: 1000, Body: []*Step{{ID: "inner", Loop: &LoopSpec{Count: 1000, Body: []*Step{{ID: "leaf"}}}}}},
	}
	for name, loop := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyLoops([]*Step{{ID: "big", Loop: loop}})
			if !errors.Is(err, ErrExpansionLimit) {
				t.Fatalf("ApplyLoops error = %v; want ErrExpansionLimit", err)
			}
		})
	}
}

// With a small injected limit, every refusal comes before the budget is
// overdrawn: the meter never holds more steps than the limit, and a loop
// that would cross it is refused with the budget it was handed intact.
func TestApplyLoops_ChargesBeforeCloning(t *testing.T) {
	cases := []struct {
		name string
		loop *LoopSpec
	}{
		{"flat", &LoopSpec{Count: 11, Body: []*Step{{ID: "work"}}}},
		{"range", &LoopSpec{Range: "1..11", Body: []*Step{{ID: "work"}}}},
		{"until", &LoopSpec{Until: "step.status == 'complete'", Max: 3, Body: []*Step{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}, {ID: "f"}, {ID: "g"}, {ID: "h"}, {ID: "i"}, {ID: "j"}, {ID: "k"},
		}}},
		// The outer loop clones the inner loop's body with every iteration,
		// so it pays 4 x (1 + 2) = 12 before any inner loop expands.
		{"nested body", &LoopSpec{Count: 4, Body: []*Step{{ID: "inner", Loop: &LoopSpec{Count: 1, Body: []*Step{{ID: "x"}, {ID: "y"}}}}}}},
		{"children", &LoopSpec{Count: 4, Body: []*Step{{ID: "parent", Children: []*Step{{ID: "c1"}, {ID: "c2"}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := []*Step{{ID: "big", Loop: tc.loop}}
			budget := newExpansionBudget(10, steps)
			_, err := ApplyLoopsWithBudget(steps, budget)
			if !errors.Is(err, ErrExpansionLimit) {
				t.Fatalf("ApplyLoopsWithBudget error = %v; want ErrExpansionLimit", err)
			}
			if budget.Used() > budget.Limit() {
				t.Fatalf("budget holds %d steps after the refusal; the limit is %d", budget.Used(), budget.Limit())
			}
		})
	}
}

// A nested loop's clone stops counting once it expands: 3 outer iterations
// of a 4-iteration inner loop cost 12 steps, not 12 plus the 3 x 2 cloned
// inner markers and bodies they replaced.
func TestApplyLoops_NestedLoopBodyNotChargedTwice(t *testing.T) {
	steps := []*Step{{ID: "outer", Loop: &LoopSpec{Count: 3, Body: []*Step{
		{ID: "inner", Loop: &LoopSpec{Count: 4, Body: []*Step{{ID: "leaf"}}}},
	}}}}
	budget := newExpansionBudget(12, steps)
	result, err := ApplyLoopsWithBudget(steps, budget)
	if err != nil {
		t.Fatalf("ApplyLoopsWithBudget at exactly the limit: %v", err)
	}
	if got := countAllSteps(result); got != 12 {
		t.Fatalf("ApplyLoopsWithBudget = %d steps; want 12", got)
	}
	if budget.Used() != 12 {
		t.Fatalf("budget holds %d steps; want the 12 the expansion output", budget.Used())
	}
}

// A flat loop of exactly MaxExpandedSteps steps cooks, with every iteration
// chained to the one before it.
func TestApplyLoops_FlatLoopAtLimitCooks(t *testing.T) {
	steps := []*Step{{ID: "big", Loop: &LoopSpec{Count: MaxExpandedSteps, Body: []*Step{{ID: "work"}}}}}
	result, err := ApplyLoops(steps)
	if err != nil {
		t.Fatalf("ApplyLoops at exactly the limit: %v", err)
	}
	if len(result) != MaxExpandedSteps {
		t.Fatalf("ApplyLoops = %d steps; want %d", len(result), MaxExpandedSteps)
	}
	for i, s := range result {
		if want := fmt.Sprintf("big.iter%d.work", i+1); s.ID != want {
			t.Fatalf("step %d = %q; want %q", i, s.ID, want)
		}
		if i == 0 {
			if len(s.Needs) != 0 {
				t.Fatalf("first step needs %v; want nothing", s.Needs)
			}
			continue
		}
		if prev := result[i-1].ID; !slicesContains(s.Needs, prev) {
			t.Fatalf("step %q needs %v; want it chained to %q", s.ID, s.Needs, prev)
		}
	}
}

// Nested 100-by-100 one-leaf loops reach exactly MaxExpandedSteps steps and
// keep both chains: inner iterations within an outer iteration, and each
// outer iteration's first leaf after the previous one's last.
func TestApplyLoops_NestedLoopsAtLimitCook(t *testing.T) {
	steps := []*Step{{ID: "outer", Loop: &LoopSpec{Count: 100, Body: []*Step{
		{ID: "inner", Loop: &LoopSpec{Count: 100, Body: []*Step{{ID: "leaf"}}}},
	}}}}
	budget := NewExpansionBudget(steps)
	result, err := ApplyLoopsWithBudget(steps, budget)
	if err != nil {
		t.Fatalf("ApplyLoopsWithBudget at exactly the limit: %v", err)
	}
	if len(result) != MaxExpandedSteps || budget.Used() != MaxExpandedSteps {
		t.Fatalf("ApplyLoopsWithBudget = %d steps, budget %d; want %d of each", len(result), budget.Used(), MaxExpandedSteps)
	}
	byID := make(map[string]*Step, len(result))
	for _, s := range result {
		byID[s.ID] = s
	}
	leaf := func(o, i int) string { return fmt.Sprintf("outer.iter%d.inner.iter%d.leaf", o, i) }
	for o := 1; o <= 100; o++ {
		for i := 1; i <= 100; i++ {
			s := byID[leaf(o, i)]
			if s == nil {
				t.Fatalf("missing %s", leaf(o, i))
			}
			var want string
			switch {
			case i > 1:
				want = leaf(o, i-1)
			case o > 1:
				want = leaf(o-1, 100)
			default:
				continue
			}
			if !slicesContains(s.Needs, want) {
				t.Fatalf("%s needs %v; want %q", s.ID, s.Needs, want)
			}
		}
	}
}

func slicesContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// loopIteration finds exactly the iterations the old per-iteration prefix
// probe did: "{loop}.iter{N}." with N as %d writes it, within 1..count.
func TestLoopIterationMatchesTheIterationPrefix(t *testing.T) {
	const count = 20
	for _, id := range []string{
		"L.iter1.a", "L.iter12.a", "L.iter20.a.b", "L.iter01.a", "L.iter21.a", "L.iter0.a",
		"L.iter1", "L.iter.a", "L.iterx.a", "L.iter-1.a", "L.iter+1.a", "Lx.iter1.a", "M.iter1.a", "L.iter1x.a",
	} {
		want := 0
		for iter := 1; iter <= count; iter++ {
			if strings.HasPrefix(id, fmt.Sprintf("L.iter%d.", iter)) {
				want = iter
				break
			}
		}
		got, ok := loopIteration(id, "L", count)
		if !ok {
			got = 0
		}
		if got != want {
			t.Errorf("loopIteration(%q) = %d, %v; want %d", id, got, ok, want)
		}
	}
}

// writeExpansionFormula writes an expansion formula named name whose
// template has width top-level steps, each with the given children, into dir.
func writeExpansionFormula(t *testing.T, dir, name string, width, children int) {
	t.Helper()
	var tmpl []map[string]any
	for i := 0; i < width; i++ {
		step := map[string]any{"id": fmt.Sprintf("{target}.%s%d", name, i), "title": "t"}
		var kids []map[string]any
		for c := 0; c < children; c++ {
			kids = append(kids, map[string]any{"id": fmt.Sprintf("{target}.%s%d.c%d", name, i, c), "title": "c"})
		}
		if kids != nil {
			step["children"] = kids
		}
		tmpl = append(tmpl, step)
	}
	raw, err := json.Marshal(map[string]any{"formula": name, "type": "expansion", "version": 1, "template": tmpl})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".formula.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// flatSteps returns n plain steps named prefix0..prefix{n-1}.
func flatSteps(prefix string, n int) []*Step {
	steps := make([]*Step, n)
	for i := range steps {
		steps[i] = &Step{ID: fmt.Sprintf("%s%d", prefix, i), Title: "s"}
	}
	return steps
}

// A compose.expand rule that grows a 6000-step graph by a net 5000 steps is
// refused: the expansion is charged against the graph it grows.
func TestApplyExpansions_NetGrowthPastLimitFails(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "wide", 5001, 0)
	steps := flatSteps("s", 6000)
	compose := &ComposeRules{Expand: []*ExpandRule{{Target: "s0", With: "wide"}}}
	result, err := ApplyExpansions(steps, compose, NewParser(dir))
	if err == nil {
		t.Fatalf("ApplyExpansions grew 6000 steps to %d; want an expansion limit error", len(result))
	}
	for _, want := range []string{"expansion limit exceeded", `"s0"`, `"wide"`, "10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ApplyExpansions error = %q; want it to contain %q", err, want)
		}
	}
}

// Chained compose.map rules multiply a formula's steps; each is charged, so
// two doublings of 3000 steps are refused at the second.
func TestApplyExpansions_ChainedMapsPastLimitFail(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "twice", 2, 0)
	writeExpansionFormula(t, dir, "again", 2, 0)
	steps := flatSteps("s", 3000)
	compose := &ComposeRules{Map: []*MapRule{
		{Select: "s*", With: "twice"},
		{Select: "*", With: "again"},
	}}
	result, err := ApplyExpansions(steps, compose, NewParser(dir))
	if err == nil {
		t.Fatalf("ApplyExpansions grew 3000 steps to %d; want an expansion limit error", len(result))
	}
	if !strings.Contains(err.Error(), "expansion limit exceeded") || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("ApplyExpansions error = %v; want an expansion limit error naming the limit", err)
	}
}

// Loops and expansions in one resolution share a budget: 6000 loop steps
// leave room for less than the 5000 net steps an expansion then adds, though
// each phase alone is under the limit.
func TestApplyExpansionsWithBudget_SharesBudgetWithLoops(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "wide", 5001, 0)
	steps := []*Step{
		{ID: "big", Loop: &LoopSpec{Count: 6000, Body: []*Step{{ID: "work"}}}},
		{ID: "target"},
	}
	budget := NewExpansionBudget(steps)
	looped, err := ApplyControlFlowWithBudget(steps, nil, budget)
	if err != nil {
		t.Fatalf("ApplyControlFlowWithBudget: %v", err)
	}
	if budget.Used() != 6001 {
		t.Fatalf("budget holds %d steps after loops; want 6001", budget.Used())
	}
	compose := &ComposeRules{Expand: []*ExpandRule{{Target: "target", With: "wide"}}}
	if _, err := ApplyExpansionsWithBudget(looped, compose, NewParser(dir), budget); !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyExpansionsWithBudget error = %v; want ErrExpansionLimit", err)
	}
	if budget.Used() > budget.Limit() {
		t.Fatalf("budget holds %d steps after the refusal; the limit is %d", budget.Used(), budget.Limit())
	}

	// The same expansion fits a graph the loops left room for.
	small := []*Step{{ID: "big", Loop: &LoopSpec{Count: 4999, Body: []*Step{{ID: "work"}}}}, {ID: "target"}}
	budget = NewExpansionBudget(small)
	looped, err = ApplyControlFlowWithBudget(small, nil, budget)
	if err != nil {
		t.Fatalf("ApplyControlFlowWithBudget: %v", err)
	}
	result, err := ApplyExpansionsWithBudget(looped, compose, NewParser(dir), budget)
	if err != nil {
		t.Fatalf("ApplyExpansionsWithBudget at exactly the limit: %v", err)
	}
	if got := countAllSteps(result); got != MaxExpandedSteps || budget.Used() != MaxExpandedSteps {
		t.Fatalf("expansion = %d steps, budget %d; want %d of each", got, budget.Used(), MaxExpandedSteps)
	}
}

// The meter tracks the graph the expansions leave, including a map whose
// matches overlap: a child replaced along with its matching parent is not
// released twice, whichever of the two the map expands first.
func TestApplyExpansionsWithBudget_MeterMatchesTheGraph(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "one", 1, 0)
	writeExpansionFormula(t, dir, "tree", 2, 2)
	cases := map[string]*ComposeRules{
		"overlapping map": {Map: []*MapRule{{Select: "p*", With: "one"}}},
		"expand parent":   {Expand: []*ExpandRule{{Target: "p", With: "tree"}}},
		"expand then map": {
			Expand: []*ExpandRule{{Target: "q", With: "tree"}},
			Map:    []*MapRule{{Select: "p*", With: "tree"}, {Select: "q.*", With: "one"}},
		},
	}
	for name, compose := range cases {
		t.Run(name, func(t *testing.T) {
			for run := 0; run < 20; run++ { // map order is random; overlap both ways
				steps := []*Step{
					{ID: "p", Children: []*Step{{ID: "p.a"}, {ID: "p.b", Children: []*Step{{ID: "p.b.c"}}}}},
					{ID: "q"},
				}
				budget := NewExpansionBudget(steps)
				result, err := ApplyExpansionsWithBudget(steps, compose, NewParser(dir), budget)
				if err != nil {
					t.Fatalf("ApplyExpansionsWithBudget: %v", err)
				}
				if got := stepGraphSize(result); budget.Used() != got {
					t.Fatalf("budget holds %d steps; the expanded graph has %d", budget.Used(), got)
				}
			}
		})
	}
}

// With a small injected limit, a map is refused at the expansion that would
// cross it, before cloning its template, and the budget is never overdrawn.
func TestApplyExpansionsWithBudget_MapRefusesBeforeOverdrawing(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "triple", 3, 0)
	steps := flatSteps("s", 5)
	budget := newExpansionBudget(10, steps)
	compose := &ComposeRules{Map: []*MapRule{{Select: "s*", With: "triple"}}}
	_, err := ApplyExpansionsWithBudget(steps, compose, NewParser(dir), budget)
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyExpansionsWithBudget error = %v; want ErrExpansionLimit", err)
	}
	// 5 steps, then +2 per expansion: the third (5+2+2+2 = 11) is refused.
	if budget.Used() != 9 {
		t.Fatalf("budget holds %d steps after the refusal; want the 9 the first two expansions left", budget.Used())
	}
}

// Inline expansions are charged like compose rules: 4000 steps that each
// inline a 3-step template would grow to 12000.
func TestApplyInlineExpansions_PastLimitFails(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "three", 3, 0)
	steps := flatSteps("s", 4000)
	for _, s := range steps {
		s.Expand = "three"
	}
	result, err := ApplyInlineExpansions(steps, NewParser(dir))
	if err == nil {
		t.Fatalf("ApplyInlineExpansions grew 4000 steps to %d; want an expansion limit error", len(result))
	}
	for _, want := range []string{"expansion limit exceeded", `"three"`, "10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ApplyInlineExpansions error = %q; want it to contain %q", err, want)
		}
	}
}

// A standalone expansion formula whose template alone is over the limit is
// refused before it is materialized into steps.
func TestMaterializeExpansion_TemplatePastLimitFails(t *testing.T) {
	f := &Formula{Formula: "huge", Type: TypeExpansion, Template: flatSteps("{target}.t", 5001)}
	for _, s := range f.Template {
		s.Children = []*Step{{ID: s.ID + ".c"}}
	}
	err := MaterializeExpansion(f, "main", nil)
	if err == nil {
		t.Fatalf("MaterializeExpansion built %d steps; want an expansion limit error", countAllSteps(f.Steps))
	}
	if !strings.Contains(err.Error(), "expansion limit exceeded") || !strings.Contains(err.Error(), `"huge"`) {
		t.Fatalf("MaterializeExpansion error = %v; want an expansion limit error naming the formula", err)
	}
	if len(f.Steps) != 0 {
		t.Fatalf("MaterializeExpansion left %d steps on the formula after refusing", len(f.Steps))
	}
}

// Advice that wraps every step of a 6000-step formula would triple it.
func TestApplyAdvice_PastLimitRefuses(t *testing.T) {
	steps := flatSteps("s", 6000)
	advice := []*AdviceRule{{
		Target: "*",
		Before: &AdviceStep{ID: "{step.id}.pre"},
		After:  &AdviceStep{ID: "{step.id}.post"},
	}}
	result, err := ApplyAdvice(steps, advice)
	if err == nil {
		t.Fatalf("ApplyAdvice grew 6000 steps to %d; want an expansion limit error", len(result))
	}
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyAdvice error = %v; want ErrExpansionLimit", err)
	}
	for _, want := range []string{"advice on step", "10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ApplyAdvice error = %q; want it to contain %q", err, want)
		}
	}
}

// Advice that lands exactly on the limit still applies.
func TestApplyAdvice_AtLimitApplies(t *testing.T) {
	steps := flatSteps("s", 5000)
	advice := []*AdviceRule{{Target: "*", After: &AdviceStep{ID: "{step.id}.post"}}}
	result, err := ApplyAdvice(steps, advice)
	if err != nil {
		t.Fatalf("ApplyAdvice at the limit: %v", err)
	}
	if len(result) != MaxExpandedSteps {
		t.Fatalf("ApplyAdvice produced %d steps, want %d", len(result), MaxExpandedSteps)
	}
}

// Advice is charged before its steps are built, including advice on
// children, and aspects applied one after another share the budget.
func TestApplyAdviceWithBudget_ChargesBeforeBuilding(t *testing.T) {
	advice := []*AdviceRule{{
		Target: "*",
		Around: &AroundAdvice{
			Before: []*AdviceStep{{ID: "{step.id}.a"}, {ID: "{step.id}.b"}},
			After:  []*AdviceStep{{ID: "{step.id}.z"}},
		},
	}}

	t.Run("children", func(t *testing.T) {
		steps := []*Step{{ID: "p", Children: []*Step{{ID: "c1"}, {ID: "c2"}}}}
		budget := newExpansionBudget(10, steps) // 3 used; p fits (+3), c1 does not
		if _, err := ApplyAdviceWithBudget(steps, advice, budget); !errors.Is(err, ErrExpansionLimit) {
			t.Fatalf("ApplyAdviceWithBudget error = %v; want ErrExpansionLimit", err)
		}
		if budget.Used() != 9 {
			t.Fatalf("budget used = %d after refusal; want 9 (advice on p and c1, not c2)", budget.Used())
		}
	})

	t.Run("aspects share the budget", func(t *testing.T) {
		steps := flatSteps("s", 2)
		budget := newExpansionBudget(10, steps)
		first, err := ApplyAdviceWithBudget(steps, advice, budget) // 2 + 6 = 8
		if err != nil {
			t.Fatalf("first aspect: %v", err)
		}
		if budget.Used() != stepGraphSize(first) {
			t.Fatalf("budget used = %d; want the graph's %d steps", budget.Used(), stepGraphSize(first))
		}
		if _, err := ApplyAdviceWithBudget(first, advice, budget); !errors.Is(err, ErrExpansionLimit) {
			t.Fatalf("second aspect error = %v; want ErrExpansionLimit", err)
		}
	})
}

// Inline expansions charged to a shared budget: the budget tracks the graph
// they produce, and an injected limit refuses before the template is built.
func TestApplyInlineExpansionsWithBudget_TracksTheGraph(t *testing.T) {
	dir := t.TempDir()
	writeExpansionFormula(t, dir, "pair", 2, 1) // 2 steps with 1 child each
	steps := flatSteps("s", 3)
	steps[0].Expand = "pair"
	steps[2].Children = []*Step{{ID: "s2.kid", Expand: "pair"}}

	budget := NewExpansionBudget(steps)
	result, err := ApplyInlineExpansionsWithBudget(steps, NewParser(dir), budget)
	if err != nil {
		t.Fatalf("ApplyInlineExpansionsWithBudget: %v", err)
	}
	if budget.Used() != stepGraphSize(result) {
		t.Fatalf("budget used = %d; want the graph's %d steps", budget.Used(), stepGraphSize(result))
	}

	small := newExpansionBudget(6, steps) // 4 used; s0 grows to 7
	if _, err := ApplyInlineExpansionsWithBudget(steps, NewParser(dir), small); !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("ApplyInlineExpansionsWithBudget error = %v; want ErrExpansionLimit", err)
	}
	if small.Used() != 4 {
		t.Fatalf("budget used = %d after refusal; want it unchanged at 4", small.Used())
	}
}

// ExpansionVarRefs does not walk a template MaterializeExpansion refuses.
func TestExpansionVarRefs_TemplatePastLimitIsNil(t *testing.T) {
	f := &Formula{Formula: "huge", Type: TypeExpansion, Template: flatSteps("{target}.t{x}", MaxExpandedSteps+1)}
	if got := ExpansionVarRefs(f, "main"); got != nil {
		t.Fatalf("ExpansionVarRefs = %v; want nil for an over-limit template", got)
	}
	f.Template = f.Template[:MaxExpandedSteps]
	if got := ExpansionVarRefs(f, "main"); len(got) != 1 || got[0] != "x" {
		t.Fatalf("ExpansionVarRefs at the limit = %v; want [x]", got)
	}
}
