package main

import (
	"sync"
	"time"
)

// Debouncer batches rapid events into a single action after a quiet period.
// Thread-safe for concurrent triggers.
type Debouncer struct {
	mu       sync.Mutex
	timer    *time.Timer
	duration time.Duration
	action   func()
	seq      uint64 // Sequence number to prevent stale timer fires
}

// NewDebouncer creates a new debouncer with the given duration and action.
// The action will be called once after the duration has passed since the last trigger.
func NewDebouncer(duration time.Duration, action func()) *Debouncer {
	return &Debouncer{
		duration: duration,
		action:   action,
	}
}

// Trigger schedules the action to run after the debounce duration.
// If called multiple times, the timer is reset each time, ensuring
// the action only fires once after the last trigger.
func (d *Debouncer) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
	}

	// Increment sequence number to invalidate any pending timers
	d.seq++
	currentSeq := d.seq

	d.timer = time.AfterFunc(d.duration, func() {
		d.mu.Lock()
		// Only fire if this is still the latest trigger
		if d.seq != currentSeq {
			d.mu.Unlock()
			return
		}
		d.timer = nil
		d.mu.Unlock() // Unlock before calling action to avoid holding lock during callback

		// Action runs without lock held. If action panics, the lock is already
		// released, avoiding a double-unlock that would occur with the previous
		// defer-based pattern.
		d.action()
	})
}

// Cancel stops any pending debounced action.
// Safe to call even if no action is pending.
func (d *Debouncer) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
