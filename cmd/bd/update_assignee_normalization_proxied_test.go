//go:build cgo

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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

	// AC-4: the live-claim fence treats a hidden-empty clear like an
	// explicit one: refused without --force, applied with it.
	t.Run("live_claim_fence_on_hidden_empty_clear", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "anf")
		claimAs := func(t *testing.T, title string) *types.Issue {
			t.Helper()
			issue := bdProxiedCreate(t, bd, p.dir, title)
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "holder", "--assignee", "holder", "--status", "in_progress")
			return issue
		}

		t.Run("update_refused_then_force_clears", func(t *testing.T) {
			t.Parallel()
			issue := claimAs(t, "Proxied fence update")
			out, code := bdProxiedUpdateFailCode(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", "-")
			if code != 1 {
				t.Errorf("fence refusal exit code = %d, want 1:\n%s", code, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID); got.Assignee != "holder" || got.Status != types.StatusInProgress {
				t.Errorf("refused hidden-empty clear clobbered the row: assignee=%q status=%s", got.Assignee, got.Status)
			}

			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--actor", "thief", "--assignee", "-", "--force")
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("--force hidden-empty clear left assignee %q, want cleared", got)
			}
		})

		t.Run("assign_refused_then_force_clears", func(t *testing.T) {
			t.Parallel()
			issue := claimAs(t, "Proxied fence assign")
			out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, " ", "--actor", "thief")
			if err == nil {
				t.Fatalf("proxied hidden-empty assign over a live foreign claim should fail, got:\n%s", out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "holder" {
				t.Errorf("refused hidden-empty assign clobbered the row: assignee=%q", got)
			}

			if out, err := bdProxiedRun(t, bd, p.dir, "assign", issue.ID, " ", "--actor", "thief", "--force"); err != nil {
				t.Fatalf("proxied assign --force failed: %v\n%s", err, out)
			}
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != "" {
				t.Errorf("--force hidden-empty assign left assignee %q, want cleared", got)
			}
		})
	})

	// AC-5: non-clear values stay verbatim on the proxied routes and the
	// guard keeps comparing literally.
	t.Run("non_clear_values_and_guard_verbatim", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "anv")
		for _, value := range []string{"alice", " alice ", " - ", "--"} {
			issue := bdProxiedCreate(t, bd, p.dir, "Proxied verbatim "+value)
			bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--assignee", value)
			if got := bdProxiedShow(t, bd, p.dir, issue.ID).Assignee; got != value {
				t.Errorf("proxied update stored assignee %q, want verbatim %q", got, value)
			}
		}

		// The create path still stores a raw dash, the guard still matches
		// it literally, and a mismatched guard refuses with exit 13.
		legacy := bdProxiedCreate(t, bd, p.dir, "Proxied legacy dash", "--assignee", "-")
		if got := bdProxiedShow(t, bd, p.dir, legacy.ID).Assignee; got != "-" {
			t.Fatalf("proxied create should keep storing the raw dash, got %q", got)
		}
		bdProxiedUpdateOne(t, bd, p.dir, legacy.ID, "--assignee", "", "--if-assignee", "-")
		if got := bdProxiedShow(t, bd, p.dir, legacy.ID).Assignee; got != "" {
			t.Errorf("guard-matched explicit clear left assignee %q", got)
		}
		fresh := bdProxiedCreate(t, bd, p.dir, "Proxied guard mismatch")
		out, code := bdProxiedUpdateFailCode(t, bd, p.dir, fresh.ID, "--title", "Guarded", "--if-assignee", "-")
		if code != ExitGuardMismatch {
			t.Errorf("--if-assignee - on unassigned row exit code = %d, want %d:\n%s", code, ExitGuardMismatch, out)
		}
		if got := bdProxiedShow(t, bd, p.dir, fresh.ID).Title; got == "Guarded" {
			t.Errorf("guard-mismatched update wrote the title anyway")
		}
	})
}
