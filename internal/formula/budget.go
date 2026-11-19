package formula

import (
	"errors"
	"fmt"
)

// MaxExpandedSteps is the maximum number of logical steps one formula
// resolution may expand to across EVERY cook transformation together: loop
// iterations, advice insertions, and compose expand/map rules share one
// budget, so none of them can multiply the graph past the limit on its own.
//
// It bounds the graph's logical WIDTH, not every cost of cooking: a step's
// title and description may still be arbitrarily large, and the limit is not
// configurable because a knob a repository formula could set is not a bound.
// A formula that legitimately needs more steps has to be split into several
// formulas (see the bd cook help).
const MaxExpandedSteps = 10000

// ErrExpansionLimit wraps every refusal raised by the step budget, so callers
// can tell an over-limit formula from a malformed one with errors.Is. The
// message names what was expanding and the limit it hit.
var ErrExpansionLimit = errors.New("expansion limit exceeded")

// StepBudget meters the number of live logical steps one formula resolution
// stands for. One budget is threaded through every cook transformation
// (loops, advice, inline and compose expansions); the error-returning entry
// points ApplyLoops, ApplyControlFlow, ApplyExpansions, ApplyInlineExpansions,
// ApplyAdvice and MaterializeExpansion are fresh-budget wrappers around the
// WithBudget variants this threading uses.
//
// WHAT COUNTS is the graph's logical shape: every Step a resolution keeps,
// including Children and the not-yet-expanded bodies of nested loops, costs
// one step plus its subtree. The input graph is charged once by Seed, and
// each transform then RELEASES what it removes and RESERVES what it puts in
// its place BEFORE building it, so an over-limit replacement is refused before
// the clone that would have proven the point, and intermediate graphs are
// bounded even when a later transform shrinks the result under the limit
// again. Same-size copying — cloning a step that replaces it one for one — is
// not new logical growth and costs nothing.
type StepBudget struct {
	limit   int
	current int
}

// NewStepBudget returns a budget with the default MaxExpandedSteps limit.
func NewStepBudget() *StepBudget {
	return NewStepBudgetWithLimit(MaxExpandedSteps)
}

// NewStepBudgetWithLimit returns a budget with a caller-chosen limit. It
// exists for tests, which inject a small limit rather than building graphs
// thousands of steps wide to prove a refusal.
func NewStepBudgetWithLimit(limit int) *StepBudget {
	return &StepBudget{limit: limit}
}

// Limit is the step ceiling this budget enforces.
func (b *StepBudget) Limit() int { return b.limit }

// Remaining is how many more steps this resolution may still grow by.
func (b *StepBudget) Remaining() int { return b.limit - b.current }

// Seed charges the input graph once. A formula that already declares more
// steps than the limit cannot cook at all, whatever its transforms would
// have done.
func (b *StepBudget) Seed(steps []*Step) error {
	return b.reserve(CountGraphSteps(steps), "the formula's own steps")
}

// Reserve charges n more steps. It is called before the steps it pays for are
// built; what names what is being expanded for the refusal's message.
func (b *StepBudget) Reserve(n int, what string) error {
	return b.reserve(n, what)
}

// Release returns the charge of steps that left the graph, so what replaces
// them is only charged for the growth it actually adds.
func (b *StepBudget) Release(n int) {
	b.current -= n
	if b.current < 0 {
		// A transform that releases more than it reserved is a bookkeeping
		// bug, not a license to grow: clamp at zero and stay a bound.
		b.current = 0
	}
}

func (b *StepBudget) reserve(n int, what string) error {
	if n > b.limit-b.current {
		return fmt.Errorf("%w: %s would take the formula past %d expanded steps", ErrExpansionLimit, what, b.limit)
	}
	b.current += n
	return nil
}

// CountGraphSteps counts the logical steps a graph stands for: each step,
// its Children, and the unexpanded bodies of its loops, recursively. It is
// the same count the budget books, published so a caller that removes steps
// outside a transform (the condition filter during a pour) can release what
// left.
func CountGraphSteps(steps []*Step) int {
	return countGraphSteps(steps)
}

func countGraphSteps(steps []*Step) int {
	n := 0
	for _, s := range steps {
		n += countStepGraph(s)
	}
	return n
}

// countStepGraph is one step plus everything its expansion still owes: its
// children and, for an unexpanded loop, its body. A cloned loop body costs the
// same here as in the clone that carries it, so releasing the charge when the
// loop expands is exact.
func countStepGraph(s *Step) int {
	if s == nil {
		return 0
	}
	n := 1
	for _, child := range s.Children {
		n += countStepGraph(child)
	}
	if s.Loop != nil {
		for _, body := range s.Loop.Body {
			n += countStepGraph(body)
		}
	}
	return n
}
