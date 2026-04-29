//go:build cgo && integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDepLinkRoutingDeadlock_GH3586 is a regression test for the file-lock
// deadlock that hung dep/link commands in contributor-mode workspaces when
// both IDs auto-routed to the same planning repo. See GH#3586 / beads-i4s.
//
// Before the fix, two back-to-back resolveIDWithRouting calls each opened
// the same embeddeddolt store, and the second blocked on the .lock file
// until the cenkalti/backoff retry exhausted (~5 minutes). The test asserts
// each affected command completes well inside that window.
func TestDepLinkRoutingDeadlock_GH3586(t *testing.T) {
	root := t.TempDir()
	planning := filepath.Join(root, "planning")
	contrib := filepath.Join(root, "contrib")

	for _, dir := range []string{planning, contrib} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		runHostOrFatal(t, dir, "git", "init", "-q")
	}

	runBDOrFatal(t, planning, "init", "--prefix", "planning", "--non-interactive", "--quiet")
	runBDOrFatal(t, contrib, "init", "--prefix", "contrib", "--non-interactive", "--quiet")

	runBDOrFatal(t, contrib, "config", "set", "routing.mode", "auto")
	runBDOrFatal(t, contrib, "config", "set", "routing.contributor", planning)
	runHostOrFatal(t, contrib, "git", "config", "beads.role", "contributor")

	a := createIssueInDir(t, planning, "A")
	b := createIssueInDir(t, planning, "B")

	cases := []struct {
		name string
		args []string
	}{
		{"dep_add", []string{"dep", "add", a, b}},
		{"dep_remove", []string{"dep", "rm", a, b}},
		{"dep_blocks_flag", []string{"dep", a, "--blocks", b}},
		{"link", []string{"link", a, b}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 30s is well under the ~5min backoff exhaustion the bug exhibits,
			// and well over a healthy command's wall-clock (<2s in practice).
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, testBD, tc.args...)
			cmd.Dir = contrib
			cmd.Env = os.Environ()

			start := time.Now()
			out, err := cmd.CombinedOutput()
			elapsed := time.Since(start)

			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("bd %s deadlocked (>30s; reproduces GH#3586)\noutput:\n%s",
					strings.Join(tc.args, " "), out)
			}
			if err != nil {
				// The dep_blocks_flag case may fail with "would create a cycle"
				// after a previous subtest added the dep; that's acceptable
				// because the resolution succeeded (which is what the bug
				// blocked on). A pre-fix run hits the timeout above, not this.
				if !strings.Contains(string(out), "create a cycle") &&
					!strings.Contains(string(out), "already exists") {
					t.Fatalf("bd %s failed (elapsed %s): %v\noutput:\n%s",
						strings.Join(tc.args, " "), elapsed, err, out)
				}
			}
			if elapsed > 10*time.Second {
				t.Errorf("bd %s took %s (suspiciously long; possible partial deadlock)",
					strings.Join(tc.args, " "), elapsed)
			}
		})
	}
}

func runBDOrFatal(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(testBD, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed in %s: %v\noutput:\n%s",
			strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

func runHostOrFatal(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s failed in %s: %v\noutput:\n%s",
			name, strings.Join(args, " "), dir, err, out)
	}
}

func createIssueInDir(t *testing.T, dir, title string) string {
	t.Helper()
	out := runBDOrFatal(t, dir, "create", "--title="+title, "--type=task", "-p", "2", "--json")
	var resp struct {
		ID    string `json:"id"`
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	// bd create --json may emit either {"id": "..."} or {"issue": {"id": "..."}};
	// both are accepted to keep the test resilient to format tweaks.
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("parse bd create json: %v\noutput:\n%s", err, out)
	}
	id := resp.ID
	if id == "" {
		id = resp.Issue.ID
	}
	if id == "" {
		t.Fatalf("could not extract issue ID from bd create output:\n%s", out)
	}
	return id
}
