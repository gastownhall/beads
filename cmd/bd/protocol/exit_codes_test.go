package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_CloseBlockedExitsNonZero verifies that closing an issue blocked
// by open dependencies returns exit code 1.
func TestProtocol_CloseBlockedExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	blocker := w.create("Blocker issue")
	blocked := w.create("Blocked issue")
	w.run("dep", "add", blocked, blocker, "--type=blocks")

	_, code := w.runExpectError("close", blocked)
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

// TestProtocol_CloseUnblockedExitsZero verifies that closing an unblocked
// issue returns exit code 0 (no regression).
func TestProtocol_CloseUnblockedExitsZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Simple issue")
	w.run("close", id)
}

// TestProtocol_UpdateNonexistentExitsNonZero verifies that updating a
// nonexistent issue returns exit code 1.
func TestProtocol_UpdateNonexistentExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	_, code := w.runExpectError("update", "nonexistent-xyz", "--status", "in_progress")
	if code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}
}

// TestProtocol_ClosePartialFailureExitsNonZero verifies that when closing
// multiple issues where some succeed and some fail (e.g., blocked), the
// command exits non-zero and still closes the closeable ones.
func TestProtocol_ClosePartialFailureExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	closeable := w.create("Closeable issue")
	blocker := w.create("Blocker issue")
	blocked := w.create("Blocked issue")
	w.run("dep", "add", blocked, blocker, "--type=blocks")

	// Close both: closeable should succeed, blocked should fail, and the
	// refusal makes the batch exit 1.
	if _, code := w.runExpectError("close", closeable, blocked); code != 1 {
		t.Errorf("expected exit code 1, got %d", code)
	}

	// Verify the closeable one was actually closed despite partial failure
	out := w.run("show", closeable, "--json")
	issues := parseJSONOutput(t, out)
	if len(issues) == 0 {
		t.Fatal("show returned no issues")
	}
	status, _ := issues[0]["status"].(string)
	if status != "closed" {
		t.Errorf("closeable issue should be closed despite partial failure, got status=%q", status)
	}

	// Verify the blocked one is still open
	out2 := w.run("show", blocked, "--json")
	issues2 := parseJSONOutput(t, out2)
	if len(issues2) > 0 {
		status2, _ := issues2[0]["status"].(string)
		if status2 == "closed" {
			t.Error("blocked issue should NOT be closed")
		}
	}
}

// TestProtocol_CloseNonexistentExitsNonZero verifies that closing a
// nonexistent issue returns a non-zero exit code.
func TestProtocol_CloseNonexistentExitsNonZero(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	out, _ := w.runExpectError("close", "nonexistent-xyz")
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Logf("output: %s", out)
	}
}

// undeferPerIDFailureExitCode is bd undefer's exit code TODAY when an id fails
// and the command keeps going (an id that is not deferred). Native fix C1
// (bdp-cli-conflict-20261107-fixes-c-exit-flags) makes it 1; until that
// ruling it is a pin, and the PR that lands C1 changes this line. The message
// half is TestProtocol_UndeferNonDeferredReportsStatus's (lifecycle_test.go).
const undeferPerIDFailureExitCode = 0

// TestProtocol_UndeferPerIDFailureExitCode pins bd undefer's exit code on the
// direct route when one id is not deferred, alone and beside an id that does
// undefer: the failure is reported, the other id still runs, and the exit
// code is undeferPerIDFailureExitCode. bd defer's write-failure twin is
// pinned at the command seam (TestDeferAndUndeferPerIDWriteFailureExitCode
// in cmd/bd), because nothing a real store does on demand fails one id's write.
func TestProtocol_UndeferPerIDFailureExitCode(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	// No t.Run: w's helpers fail through the test that made the workspace.
	for _, tc := range []struct {
		name     string
		deferred bool // whether a second, deferred id rides along
	}{
		{name: "the only id"},
		{name: "beside an id that undefers", deferred: true},
	} {
		open := w.create("--title", "Not deferred ("+tc.name+")", "--type", "task")
		args := []string{"undefer", open}
		var other string
		if tc.deferred {
			other = w.create("--title", "Deferred ("+tc.name+")", "--type", "task")
			w.run("defer", other)
			args = append(args, other)
		}
		out, err := w.tryRun(args...)
		code := 0
		if err != nil {
			code = exitCodeOf(t, err, args)
		}
		if code != undeferPerIDFailureExitCode {
			t.Errorf("%s: bd %s: exit %d, want %d\n%s", tc.name, strings.Join(args, " "), code, undeferPerIDFailureExitCode, out)
		}
		if want := open + " is not deferred (status: open)"; !strings.Contains(out, want) {
			t.Errorf("%s: bd %s: output lacks %q\n%s", tc.name, strings.Join(args, " "), want, out)
		}
		assertField(t, w.showJSON(open), "status", "open")
		if tc.deferred {
			assertField(t, w.showJSON(other), "status", "open")
		}
	}
}

// TestProtocol_DeferUnresolvedIDAbortsBeforeAnyWrite pins bd defer's per-id
// case the real binary can reach: an id that does not resolve fails the whole
// command (exit 1) before any id is written. C1 leaves this alone; a change
// that resolved ids one at a time inside the write loop would not.
func TestProtocol_DeferUnresolvedIDAbortsBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("--title", "Defer beside a missing id", "--type", "task")

	out, code := w.runExpectError("defer", id, w.prefix+"-nonexistent999")
	if code != 1 {
		t.Errorf("bd defer with an unresolvable id: exit %d, want 1\n%s", code, out)
	}
	if strings.Contains(out, "Deferred") {
		t.Errorf("bd defer reported a write before aborting:\n%s", out)
	}
	assertField(t, w.showJSON(id), "status", "open")
}
