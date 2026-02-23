package protocol

import (
	"strings"
	"testing"
)

// TestProtocol_ReopenAlreadyOpenReportsStatus asserts that bd reopen on an
// already-open issue does NOT print a false "Reopened" message. The stderr
// output should indicate the issue is already open.
func TestProtocol_ReopenAlreadyOpenReportsStatus(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Already open", "--type", "task")

	out, err := w.tryRun("reopen", id)
	// Command may succeed (exit 0) or fail — either way, the output should
	// NOT contain the false positive "Reopened" message.
	_ = err
	if strings.Contains(out, "Reopened") {
		t.Errorf("bd reopen on already-open issue printed false 'Reopened' message:\n%s", out)
	}
	if !strings.Contains(out, "already open") {
		t.Errorf("bd reopen on already-open issue should mention 'already open':\n%s", out)
	}
}

// TestProtocol_UndeferNonDeferredReportsStatus asserts that bd undefer on a
// non-deferred issue does NOT print a false "Undeferred" message. The stderr
// output should indicate the issue is not deferred.
func TestProtocol_UndeferNonDeferredReportsStatus(t *testing.T) {
	w := newWorkspace(t)
	id := w.create("--title", "Not deferred", "--type", "task")

	out, err := w.tryRun("undefer", id)
	_ = err
	if strings.Contains(out, "Undeferred") {
		t.Errorf("bd undefer on non-deferred issue printed false 'Undeferred' message:\n%s", out)
	}
	if !strings.Contains(out, "not deferred") {
		t.Errorf("bd undefer on non-deferred issue should mention 'not deferred':\n%s", out)
	}
}
