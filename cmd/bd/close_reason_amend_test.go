package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// A close reason is first-close-wins (issueops.CloseRequest.Reason), so a
// re-close cannot rewrite it. These tests pin what `bd close` DOES about that,
// which before be-ctr was nothing: rc=0, no stderr, and the caller's own
// discarded text echoed back on stdout as if it had landed.
//
// The line the whole group draws is between an ordinary idempotent retry — which
// must stay a silent success, because a crashed close has to be replayable — and
// an AMENDMENT the command cannot honor. Only the second is reported.

const (
	amendStoredReason   = "REASON-A-first-close"
	amendSuppliedReason = "REASON-B-second-close"
)

// seedClosed seeds an issue already closed and carrying a stored close reason,
// directly through the store, so the fixture never depends on the behavior
// under test.
func seedClosed(t *testing.T, env *parityEnv, id, title, reason string) {
	t.Helper()
	env.seed(id, title, func(i *types.Issue) {
		i.Status = types.StatusClosed
		i.CloseReason = reason
	})
}

// TestCloseAlreadyClosedRefusesDiscardedReason is the be-ctr case: an agent
// repairing a thin or wrong close_reason. The write cannot happen, so the
// command must not report that it did.
func TestCloseAlreadyClosedRefusesDiscardedReason(t *testing.T) {
	env := newParityEnv(t)
	seedClosed(t, env, "test-amd1", "Amend a closed reason", amendStoredReason)

	env.setFlags(closeCmd, map[string]string{"reason": amendSuppliedReason})
	res := env.run(closeCmd, "test-amd1")

	if res.exitCode == 0 {
		t.Errorf("exit = 0 for a discarded reason; rc=0 is what made this read as success")
	}
	if !strings.Contains(res.stderr, "close reason NOT recorded on test-amd1") {
		t.Errorf("stderr does not say the reason was dropped:\n%s", res.stderr)
	}
	// The refusal has to carry a way forward, or it only relocates the dead end.
	if !strings.Contains(res.stderr, "bd comment test-amd1") {
		t.Errorf("stderr does not name bd comment as the add-to-record path:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "bd reopen test-amd1") {
		t.Errorf("stderr does not name reopen+close as the replace path:\n%s", res.stderr)
	}
	// stdout must not echo the text that was discarded — the echo is what a
	// --reason-file caller reads as confirmation.
	if strings.Contains(res.stdout, amendSuppliedReason) {
		t.Errorf("stdout echoed the discarded reason back:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, amendStoredReason) {
		t.Errorf("stdout does not report the reason actually on the record:\n%s", res.stdout)
	}
	if got := env.get("test-amd1").CloseReason; got != amendStoredReason {
		t.Errorf("close_reason = %q, want %q unchanged (first-close-wins)", got, amendStoredReason)
	}
}

// TestCloseAlreadyClosedIdenticalReasonIsSilentSuccess is the retry. It asks
// for the state that already exists, so nothing was lost and nothing is said.
func TestCloseAlreadyClosedIdenticalReasonIsSilentSuccess(t *testing.T) {
	env := newParityEnv(t)
	seedClosed(t, env, "test-amd2", "Replay the same close", amendStoredReason)

	env.setFlags(closeCmd, map[string]string{"reason": amendStoredReason})
	res := env.run(closeCmd, "test-amd2")

	if res.exitCode != 0 {
		t.Errorf("exit = %d, want 0: replaying a close with its own reason is the retry-safe no-op\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty for an identical replay", res.stderr)
	}
	if got := env.lastTouched(); got != "test-amd2" {
		t.Errorf("last-touched = %q, want %q: the retry-safe post-close contracts still replay", got, "test-amd2")
	}
}

// TestCloseAlreadyClosedWithoutReasonIsSilentSuccess covers the bare `bd close
// <id>` replay. bd supplies "Closed" itself, and its own default disagreeing
// with a stored reason is not a caller asking for anything.
func TestCloseAlreadyClosedWithoutReasonIsSilentSuccess(t *testing.T) {
	env := newParityEnv(t)
	seedClosed(t, env, "test-amd3", "Replay with no reason", amendStoredReason)

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd, "test-amd3")

	if res.exitCode != 0 {
		t.Errorf("exit = %d, want 0 for a bare re-close\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty for a bare re-close", res.stderr)
	}
	// It still reports the record rather than bd's unused default.
	if !strings.Contains(res.stdout, amendStoredReason) {
		t.Errorf("stdout does not report the stored reason:\n%s", res.stdout)
	}
}

// TestCloseAlreadyClosedRefusesReasonOnEmptyStored covers the be-mh0 shape:
// an issue closed with NO reason at all. Filling the blank in is still a write
// that did not happen, and the refusal must not quote an empty reason as if
// the first close had written one.
func TestCloseAlreadyClosedRefusesReasonOnEmptyStored(t *testing.T) {
	env := newParityEnv(t)
	seedClosed(t, env, "test-amd4", "Closed with no reason", "")

	env.setFlags(closeCmd, map[string]string{"reason": amendSuppliedReason})
	res := env.run(closeCmd, "test-amd4")

	if res.exitCode == 0 {
		t.Errorf("exit = 0; filling in an empty close_reason did not happen either")
	}
	if strings.Contains(res.stderr, `reason ""`) {
		t.Errorf("refusal quotes an empty stored reason as if one were written:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "empty close reason") {
		t.Errorf("refusal does not say the stored reason is empty:\n%s", res.stderr)
	}
	// The gap codex caught on the first revision: an empty stored reason made
	// the success line fall back to the SUPPLIED text, echoing the discarded
	// reason back on exactly the shape this bead was filed about.
	if strings.Contains(res.stdout, amendSuppliedReason) {
		t.Errorf("stdout echoed the discarded reason back:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "(no close reason recorded)") {
		t.Errorf("stdout does not say the record carries no reason:\n%s", res.stdout)
	}
	if got := env.get("test-amd4").CloseReason; got != "" {
		t.Errorf("close_reason = %q, want it still empty", got)
	}
}

// TestCloseReasonDiscarded pins the predicate itself, including the two arms
// that keep an ordinary retry silent.
func TestCloseReasonDiscarded(t *testing.T) {
	tests := []struct {
		name     string
		changed  bool
		explicit bool
		supplied string
		stored   string
		want     bool
	}{
		{"real close writes what it was given", true, true, "new", "", false},
		{"bd's own default is not an amendment", false, false, "Closed", "stored", false},
		{"identical reason is a replay", false, true, "stored", "stored", false},
		{"blank explicit reason asks for nothing", false, true, "   ", "stored", false},
		{"explicit reason disagreeing with the record", false, true, "new", "stored", true},
		{"filling in an empty stored reason is still a write that did not happen", false, true, "new", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := closeReasonDiscarded(tt.changed, tt.explicit, tt.supplied, tt.stored); got != tt.want {
				t.Errorf("closeReasonDiscarded(%v, %v, %q, %q) = %v, want %v",
					tt.changed, tt.explicit, tt.supplied, tt.stored, got, tt.want)
			}
		})
	}
}

// TestCloseReportedReason pins what the ✓ line prints: the reason on the
// record, never the one the close threw away.
func TestCloseReportedReason(t *testing.T) {
	tests := []struct {
		name     string
		changed  bool
		supplied string
		stored   string
		want     string
	}{
		{"a real close reports what it wrote", true, "new", "", "new"},
		{"a no-op reports the stored reason", false, "new", "stored", "stored"},
		{"a no-op with nothing stored never borrows the supplied text", false, "new", "", noCloseReasonRecorded},
		{"whitespace is not a stored reason either", false, "new", "  \n ", noCloseReasonRecorded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := closeReportedReason(tt.changed, tt.supplied, tt.stored); got != tt.want {
				t.Errorf("closeReportedReason(%v, %q, %q) = %q, want %q",
					tt.changed, tt.supplied, tt.stored, got, tt.want)
			}
		})
	}
}
