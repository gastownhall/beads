//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerLeaseDisarmRefused: the proxied-server path has no store to
// run the flip-and-sweep transaction on, so `bd lease disarm` is refused
// loudly rather than half-done. The refusal points at the config key, which
// does the flip on its own — but not the sweep, so it says so.
func TestProxiedServerLeaseDisarmRefused(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pld")

	issue := bdProxiedCreate(t, bd, p.dir, "Claimed over the proxy")
	if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--assignee", "worker", "--status", "in_progress"); err != nil {
		t.Fatalf("seed assign failed: %v\n%s", err, out)
	}

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "lease", "disarm")
	if err == nil {
		t.Fatalf("bd lease disarm should be refused in proxied-server mode; got:\n%s%s", stdout, stderr)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("bd lease disarm failed without an exit code: %v", err)
	}
	if ee.ExitCode() != 1 {
		t.Errorf("bd lease disarm exit code = %d, want 1", ee.ExitCode())
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "not supported in proxied-server mode") {
		t.Errorf("refusal should name proxied-server mode, got:\n%s", combined)
	}
	if !strings.Contains(combined, "bd config set lease.auto off") {
		t.Errorf("refusal should point at the config-key alternative, got:\n%s", combined)
	}

	// The refusal wrote nothing: the claim is untouched.
	got := bdProxiedShow(t, bd, p.dir, issue.ID)
	if got.Assignee != "worker" || got.Status != types.StatusInProgress {
		t.Errorf("refused disarm mutated the claim: assignee=%q status=%s", got.Assignee, got.Status)
	}
}
