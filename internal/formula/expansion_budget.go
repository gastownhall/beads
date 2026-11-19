package formula

import (
	"errors"
	"fmt"
)

// MaxExpandedSteps bounds how many steps one formula resolution may grow to
// while loops, advice, inline expansions, compose.expand/compose.map rules and
// aspects transform it (bd-xx3ma9, GH#7882). A formula over the limit fails
// with ErrExpansionLimit before the steps that would cross it are built, so a
// large count, a wide range, nested loops or chained rules cannot make cook,
// pour or wisp allocate without bound. Split a formula that needs more into
// smaller formulas.
//
// The limit bounds the logical width of the step graph, not every byte a
// resolution allocates: long titles or descriptions are not counted.
const MaxExpandedSteps = 10000

// ErrExpansionLimit is wrapped by every refusal of an ExpansionBudget.
var ErrExpansionLimit = errors.New("formula expansion limit exceeded")

// ExpansionBudget meters the steps of one formula resolution against
// MaxExpandedSteps. It counts the current logical graph: every step, its
// children, and the body steps of loops not yet expanded. A transform that
// replaces a step releases the replaced subtree and reserves its replacement
// BEFORE building it, so one budget threaded through every phase of a
// resolution tracks the steps the resolution actually holds, and copying a
// step without growing the graph costs nothing.
//
// The steps a budget is created with are counted but never refused: a
// formula is only refused for what its transforms would add.
//
// A budget is not safe for concurrent use.
type ExpansionBudget struct {
	limit int
	used  int
}

// NewExpansionBudget returns a budget of MaxExpandedSteps seeded with steps,
// the graph the resolution starts from.
func NewExpansionBudget(steps []*Step) *ExpansionBudget {
	return newExpansionBudget(MaxExpandedSteps, steps)
}

func newExpansionBudget(limit int, steps []*Step) *ExpansionBudget {
	return &ExpansionBudget{limit: limit, used: stepGraphSize(steps)}
}

// Used reports how many steps the metered graph holds.
func (b *ExpansionBudget) Used() int { return b.used }

// Limit reports the most steps the metered graph may grow to.
func (b *ExpansionBudget) Limit() int { return b.limit }

// remaining is how many steps may still be added; never negative.
func (b *ExpansionBudget) remaining() int {
	if b.used >= b.limit {
		return 0
	}
	return b.limit - b.used
}

// limitError names what would have crossed the limit.
func (b *ExpansionBudget) limitError(what string) error {
	return fmt.Errorf("%w: %s would grow the formula past the %d-step limit; split it into smaller formulas",
		ErrExpansionLimit, what, b.limit)
}

// reserve charges n steps, or refuses without charging when they do not fit.
func (b *ExpansionBudget) reserve(n int, what string) error {
	if n <= 0 {
		return nil
	}
	if n > b.remaining() {
		return b.limitError(what)
	}
	b.used += n
	return nil
}

// reserveCopies charges copies of a perCopy-step subgraph, refusing before
// anything is charged when they do not all fit. The comparison cannot
// overflow however large copies is.
func (b *ExpansionBudget) reserveCopies(copies uint64, perCopy int, what string) error {
	if copies == 0 || perCopy <= 0 {
		return nil
	}
	if copies > uint64(b.remaining()/perCopy) {
		return b.limitError(what)
	}
	b.used += int(copies) * perCopy
	return nil
}

// release returns n steps a transform removed from the graph.
func (b *ExpansionBudget) release(n int) {
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
}

// replace releases a removed subtree of removed steps and reserves added
// steps for what replaces it. On refusal the budget is left as it was.
func (b *ExpansionBudget) replace(removed, added int, what string) error {
	b.release(removed)
	if err := b.reserve(added, what); err != nil {
		b.used += removed
		return err
	}
	return nil
}

// stepGraphSize counts steps, their children and the bodies of their
// unexpanded loops, recursively: the logical graph a resolution holds.
func stepGraphSize(steps []*Step) int {
	n := 0
	for _, s := range steps {
		if s == nil {
			continue
		}
		n++
		n += stepGraphSize(s.Children)
		if s.Loop != nil {
			n += stepGraphSize(s.Loop.Body)
		}
	}
	return n
}

// templateSize counts the steps an expansion template builds: each template
// step and its children. A template step's loop is not carried into the
// expansion, so its body is not counted.
func templateSize(template []*Step) int {
	n := 0
	for _, s := range template {
		if s == nil {
			continue
		}
		n += 1 + templateSize(s.Children)
	}
	return n
}
