package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
)

// TestIfRevisionPrecondition checks the flag→*int64 mapping: unset is nil
// (unconditional), and an explicit value — including 0 (assert-pristine) — yields
// a non-nil pointer to that value.
func TestIfRevisionPrecondition(t *testing.T) {
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		registerIfRevisionFlag(c)
		return c
	}

	if got := ifRevisionPrecondition(newCmd()); got != nil {
		t.Fatalf("unset --if-revision: got %v, want nil", *got)
	}

	c := newCmd()
	if err := c.Flags().Set(ifRevisionFlag, "42"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if got := ifRevisionPrecondition(c); got == nil || *got != 42 {
		t.Fatalf("--if-revision 42: got %v, want 42", got)
	}

	// 0 is a real precondition (assert the row is unwritten), distinct from unset.
	c0 := newCmd()
	if err := c0.Flags().Set(ifRevisionFlag, "0"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if got := ifRevisionPrecondition(c0); got == nil || *got != 0 {
		t.Fatalf("--if-revision 0: got %v, want a non-nil *0", got)
	}
}

// TestMapConditionalWriteError locks the exit-code contract scripts and agents
// depend on: a revision mismatch exits 9, a gate refusal exits the distinct 13,
// and any other error is generic (1) — a gate refusal must never look like a
// retryable race.
func TestMapConditionalWriteError(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	if err := mapConditionalWriteError("x", nil); err != nil {
		t.Fatalf("nil error must map to nil, got %v", err)
	}

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"precondition", &storage.PreconditionFailedError{ID: "cas-1", ExpectedRevision: 1, CurrentRevision: 2}, ExitPreconditionFailed},
		{"gate", fmt.Errorf("gate: %w", storage.ErrConditionalWriteUnsupported), ExitConditionalWriteUnsupported},
		{"generic", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapConditionalWriteError("updating cas-1", tc.err)
			code, ok := exitCodeFromError(got)
			if !ok || code != tc.want {
				t.Fatalf("%s: exit code = %d (ok=%v), want %d", tc.name, code, ok, tc.want)
			}
		})
	}
}

// TestAsConditionalWriterRefusesNonCapableStore verifies a store lacking the
// capability yields the distinct gate exit code, never a silent unconditional
// fallback.
func TestAsConditionalWriterRefusesNonCapableStore(t *testing.T) {
	defer func(prev bool) { jsonOutput = prev }(jsonOutput)
	jsonOutput = false

	_, err := asConditionalWriter(struct{}{})
	code, ok := exitCodeFromError(err)
	if !ok || code != ExitConditionalWriteUnsupported {
		t.Fatalf("non-capable store: exit code = %d (ok=%v), want %d", code, ok, ExitConditionalWriteUnsupported)
	}
}
