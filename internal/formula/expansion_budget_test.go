package formula

import (
	"errors"
	"fmt"
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
