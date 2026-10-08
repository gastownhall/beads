//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestUpdateCloseReasonValidation pins validation.on-close on the direct
// `bd update --status closed` route. bd close has always run the close-reason
// check; the update route skipped it, so a store set to "error" still took a
// close with no reason through bd update. bd update records an empty close
// reason, so "error" must refuse the close and write nothing, "warn" must warn
// and close, and "none" must close without a word. The proxied route is pinned
// in TestProxiedServerUpdateClosePolicy.
func TestUpdateCloseReasonValidation(t *testing.T) {
	t.Run("error_refuses_and_writes_nothing", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "error"})
		env.seed("test-ucr1", "Closed through update", nil)

		env.setFlags(updateCmd, map[string]string{"status": "closed"})
		res := env.run(updateCmd, "test-ucr1")
		if res.exitCode != 1 {
			t.Fatalf("exit = %d, want 1: on-close=error must refuse a reasonless close\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, "close reason is empty") {
			t.Errorf("refusal does not name the close reason; stderr:\n%s", res.stderr)
		}
		if !strings.Contains(res.stderr, "bd close <id> --reason") {
			t.Errorf("refusal does not point at bd close --reason; stderr:\n%s", res.stderr)
		}
		if got := env.get("test-ucr1").Status; got != types.StatusOpen {
			t.Errorf("status = %q after a refused close, want open", got)
		}
	})

	t.Run("error_keeps_an_already_closed_reason", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "error"})
		const reason = "Fixed the parser and added a regression test"
		closedAt := time.Now().UTC()
		env.seed("test-ucr5", "Already closed", func(i *types.Issue) {
			i.Status = types.StatusClosed
			i.ClosedAt = &closedAt
			i.CloseReason = reason
		})

		// A re-close through update would replace the recorded reason with
		// the empty one, so it is refused like any other reasonless close.
		env.setFlags(updateCmd, map[string]string{"status": "closed"})
		if res := env.run(updateCmd, "test-ucr5"); res.exitCode != 1 {
			t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
		}
		if got := env.get("test-ucr5").CloseReason; got != reason {
			t.Errorf("close_reason = %q after a refused re-close, want %q", got, reason)
		}
	})

	t.Run("error_json_reports_on_stdout_with_hint", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "error"})
		env.seed("test-ucr6", "Closed through update, JSON", nil)
		jsonOutput = true

		env.setFlags(updateCmd, map[string]string{"status": "closed"})
		res := env.run(updateCmd, "test-ucr6")
		if res.exitCode != 1 {
			t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(res.stdout), &payload); err != nil {
			t.Fatalf("stdout is not one JSON error object: %v\n%s", err, res.stdout)
		}
		if data, ok := payload["data"].(map[string]any); ok {
			payload = data
		}
		if msg, _ := payload["error"].(string); !strings.Contains(msg, "close reason is empty") {
			t.Errorf("error = %q, want the close-reason message", msg)
		}
		if hint, _ := payload["hint"].(string); !strings.Contains(hint, "bd close <id> --reason") {
			t.Errorf("hint = %q, want a pointer at bd close --reason", hint)
		}
		if got := env.get("test-ucr6").Status; got != types.StatusOpen {
			t.Errorf("status = %q after a refused close, want open", got)
		}
	})

	t.Run("error_leaves_other_updates_alone", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "error"})
		env.seed("test-ucr2", "Reprioritized", nil)

		env.setFlags(updateCmd, map[string]string{"priority": "1"})
		if res := env.run(updateCmd, "test-ucr2"); res.exitCode != 0 {
			t.Fatalf("exit = %d, want 0: the check applies only to a close\nstderr:\n%s", res.exitCode, res.stderr)
		}
		if got := env.get("test-ucr2").Priority; got != 1 {
			t.Errorf("priority = %d, want 1", got)
		}
	})

	t.Run("warn_closes_with_warning", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "warn"})
		env.seed("test-ucr3", "Closed with a warning", nil)

		env.setFlags(updateCmd, map[string]string{"status": "closed"})
		res := env.run(updateCmd, "test-ucr3")
		if res.exitCode != 0 {
			t.Fatalf("exit = %d, want 0: on-close=warn must not refuse\nstderr:\n%s", res.exitCode, res.stderr)
		}
		if !strings.Contains(res.stderr, "close reason is empty") {
			t.Errorf("on-close=warn printed no warning; stderr:\n%s", res.stderr)
		}
		if got := env.get("test-ucr3").Status; got != types.StatusClosed {
			t.Errorf("status = %q, want closed", got)
		}
	})

	t.Run("none_closes_silently", func(t *testing.T) {
		env := newParityEnv(t)
		setParityConfig(t, map[string]any{"validation.on-close": "none"})
		env.seed("test-ucr4", "Closed by default", nil)

		env.setFlags(updateCmd, map[string]string{"status": "closed"})
		res := env.run(updateCmd, "test-ucr4")
		if res.exitCode != 0 {
			t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
		}
		if strings.Contains(res.stderr, "close reason") {
			t.Errorf("on-close=none printed a close-reason message; stderr:\n%s", res.stderr)
		}
		if got := env.get("test-ucr4").Status; got != types.StatusClosed {
			t.Errorf("status = %q, want closed", got)
		}
	})
}
