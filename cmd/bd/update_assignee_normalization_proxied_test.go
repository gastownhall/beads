//go:build cgo

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// bdProxiedRunStdin runs bd in a proxied-server workspace with the given
// stdin and returns stdout, stderr and the exit code (a non-zero exit is an
// observation, not a harness failure).
func bdProxiedRunStdin(t *testing.T, bd, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bd %s could not run: %v", strings.Join(args, " "), err)
	}
	return stdout.String(), stderr.String(), code
}

// TestProxiedServerAssigneeHiddenEmptyClears pins bd-wkcfuz on the
// proxied-server route, whose update gather (gatherUpdateInput), assign
// positional (runAssignProxiedServer) and batch dispatch are separate input
// sites from the direct route: a bare "-" or whitespace-only assignee must
// clear exactly like the explicit empty string on each of them, while the
// live-claim fence, the --if-assignee guard and non-clear values keep their
// existing behavior.
func TestProxiedServerAssigneeHiddenEmptyClears(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	// AC-1: the proxied update gather normalizes hidden-empty assignees.
	t.Run("update_hidden_empty_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "anu")
		for i, value := range []string{"-", " ", "\t"} {
			issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Proxied update clear %d", i))
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder")

			updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "other", "--assignee", value)
			if got := updated.Assignee; got != "" {
				t.Errorf("value %q: proxied update stored assignee %q, want cleared", value, got)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("value %q: persisted assignee = %q, want \"\"", value, got)
			}
			// Cleared means back in the pool: another actor can claim it.
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "taker", "--claim")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "taker" {
				t.Errorf("value %q: claim by another actor set assignee %q, want taker", value, got)
			}
		}
	})

	// AC-2: the proxied assign positional normalizes hidden-empty names and
	// keeps the Unassigned confirmation.
	t.Run("assign_hidden_empty_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ana")
		for i, value := range []string{"-", " "} {
			issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Proxied assign clear %d", i))
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder")

			out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, value)
			if err != nil {
				t.Fatalf("value %q: proxied assign failed: %v\n%s", value, err, out)
			}
			if !strings.Contains(string(out), "Unassigned") {
				t.Errorf("value %q: proxied assign output should confirm Unassigned, got:\n%s", value, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("value %q: proxied assign stored assignee %q, want cleared", value, got)
			}
		}
	})

	// AC-3: the proxied batch route shares parseUpdateKVs, so its update
	// lines clear on the hidden-empty spellings too.
	t.Run("batch_hidden_empty_clears", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "anb")
		for i, token := range []string{`assignee=-`, `"assignee= "`} {
			issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Proxied batch clear %d", i))
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder")

			stdout, stderr, code := bdProxiedRunStdin(t, bd, p.dir, fmt.Sprintf("update %s %s\n", issue.ID, token), "batch")
			if code != 0 {
				t.Fatalf("token %s: proxied batch exited %d\nstdout:\n%s\nstderr:\n%s", token, code, stdout, stderr)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("token %s: proxied batch stored assignee %q, want cleared", token, got)
			}
		}
	})
}
