package types

import (
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/timeparsing"
)

// Repeat parses this issue's recurrence rule. A bead with no RepeatPattern
// yields the zero Repeat and no error.
func (i *Issue) Repeat() (timeparsing.Repeat, error) {
	return timeparsing.ParseRepeat(i.RepeatPattern)
}

// IsRecurring reports whether a repeat pattern is set. It says nothing about
// whether the series is exhausted — use NextOccurrence for that.
func (i *Issue) IsRecurring() bool { return i.RepeatPattern != "" }

// NextOccurrence computes the next due date in this bead's series strictly
// after `after`, honoring RepeatStart and RepeatEnd.
//
// ok is false — with a nil error — when the series has legitimately run out:
// the next occurrence falls past RepeatEnd, or the bead is not recurring. A
// non-nil error means the pattern itself is unusable.
func (i *Issue) NextOccurrence(after time.Time) (next time.Time, ok bool, err error) {
	if !i.IsRecurring() {
		return time.Time{}, false, nil
	}
	repeat, err := i.Repeat()
	if err != nil {
		return time.Time{}, false, err
	}
	// A series that has not started yet takes its first occurrence from the
	// start bound, not from whenever the bead happened to be closed — and for
	// an interval rule that first occurrence is the bound ITSELF, not one
	// interval past it. Once started, the start bound is also the series'
	// anchor: a monthly rule keeps its day-of-month from the day the series
	// was first scheduled, not from whatever a short month clamped it to.
	switch {
	case i.RepeatStart != nil && after.Before(*i.RepeatStart):
		next, err = repeat.FirstAtOrAfter(*i.RepeatStart)
	case i.RepeatStart != nil:
		next, err = repeat.NextFrom(after, *i.RepeatStart)
	default:
		next, err = repeat.Next(after)
	}
	if err != nil {
		return time.Time{}, false, err
	}
	next = next.UTC()
	if i.RepeatEnd != nil && next.After(*i.RepeatEnd) {
		return time.Time{}, false, nil
	}
	return next, true, nil
}

// ValidateRecurrence checks the recurrence fields are internally coherent.
// It is called from ValidateWithCustom, so every create and update path that
// validates an issue rejects an unparseable pattern or inverted bounds rather
// than storing a series that can never fire.
func (i *Issue) ValidateRecurrence() error {
	if i.RepeatPattern == "" {
		// Bounds without a pattern are a silent no-op, which reads as a typo
		// (a --repeat that did not make it) rather than an intent.
		if i.RepeatStart != nil || i.RepeatEnd != nil {
			return fmt.Errorf("repeat_start/repeat_end require repeat_pattern")
		}
		return nil
	}
	if err := CheckFieldLen("repeat_pattern", i.RepeatPattern); err != nil {
		return err
	}
	if _, err := timeparsing.ParseRepeat(i.RepeatPattern); err != nil {
		return fmt.Errorf("invalid repeat_pattern: %w", err)
	}
	if i.RepeatStart != nil && i.RepeatEnd != nil && i.RepeatEnd.Before(*i.RepeatStart) {
		return fmt.Errorf("repeat_end (%s) is before repeat_start (%s)",
			i.RepeatEnd.Format(time.RFC3339), i.RepeatStart.Format(time.RFC3339))
	}
	return nil
}
