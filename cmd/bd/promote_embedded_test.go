//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// bdPromote runs "bd promote" with the given args and returns stdout.
func bdPromote(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"promote"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd promote %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdPromoteFail runs "bd promote" expecting failure.
func bdPromoteFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"promote"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd promote %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// plantCountingHook installs the workspace hook name as a script that appends
// its issue id ($1) to a log, one line per firing, and returns the log's path.
func plantCountingHook(t *testing.T, beadsDir, name string) string {
	t.Helper()
	hookLog := filepath.Join(t.TempDir(), name+".log")
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/bin/sh\necho \"$1\" >> " + shellQuote(hookLog) + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant %s: %v", name, err)
	}
	return hookLog
}

// waitForHookLines polls a plantCountingHook log until it names id want times.
// The command has returned by then, and a command waits for the hooks it
// fired before it exits, so a count past want is a hook that fired too often.
func waitForHookLines(t *testing.T, hookLog, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, _ := os.ReadFile(hookLog) // #nosec G304 -- this test's own temp file
		got := 0
		for _, line := range strings.Fields(string(data)) {
			if line == id {
				got++
			}
		}
		if got > want {
			t.Fatalf("hook fired %d times for %s, want %d; log:\n%s", got, id, want, data)
		}
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook fired %d times for %s, want %d; log:\n%s", got, id, want, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEmbeddedPromoteCLI(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pr")

	t.Run("promote_basic", func(t *testing.T) {
		// Create ephemeral issue
		issue := bdCreate(t, bd, dir, "CLI promote basic", "--ephemeral")

		// Verify it's ephemeral
		got := bdShow(t, bd, dir, issue.ID)
		if !got.Ephemeral {
			t.Skip("issue not ephemeral; cannot test promote")
		}

		out := bdPromote(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Promoted") {
			t.Errorf("expected 'Promoted' in output: %s", out)
		}

		// Verify no longer ephemeral
		got = bdShow(t, bd, dir, issue.ID)
		if got.Ephemeral {
			t.Error("expected non-ephemeral after promote")
		}
	})

	t.Run("promote_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "CLI promote reason", "--ephemeral")

		out := bdPromote(t, bd, dir, issue.ID, "--reason", "Important enough to persist")
		if !strings.Contains(out, "Promoted") {
			t.Errorf("expected 'Promoted' in output: %s", out)
		}
	})

	t.Run("promote_with_reason_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "CLI promote -r", "--ephemeral")

		out := bdPromote(t, bd, dir, issue.ID, "-r", "Short reason")
		if !strings.Contains(out, "Promoted") {
			t.Errorf("expected 'Promoted' in output: %s", out)
		}
	})

	t.Run("promote_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "CLI promote JSON", "--ephemeral")

		cmd := exec.Command(bd, "promote", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd promote --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON object in output: %s", s)
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse promote JSON: %v\n%s", err, s)
		}
		if m["id"] != issue.ID {
			t.Errorf("expected id=%s, got %v", issue.ID, m["id"])
		}
	})

	t.Run("promote_preserves_labels", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Promote labels", "--ephemeral", "--label", "keep-me")
		bdPromote(t, bd, dir, issue.ID)

		store := openStore(t, beadsDir, "pr")
		labels, err := store.GetLabels(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		found := false
		for _, l := range labels {
			if l == "keep-me" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected label 'keep-me' preserved after promote, got %v", labels)
		}
	})

	t.Run("promote_nonexistent_fails", func(t *testing.T) {
		bdPromoteFail(t, bd, dir, "pr-nonexistent999")
	})

	t.Run("promote_already_permanent_fails", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Already permanent", "--type", "task")
		bdPromoteFail(t, bd, dir, issue.ID)
	})

	t.Run("promote_no_args_fails", func(t *testing.T) {
		cmd := exec.Command(bd, "promote")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected promote with no args to fail, got: %s", out)
		}
	})
}

// TestEmbeddedPromoteMovesAWispParent pins what the direct route's
// persistence move carries for a wisp with children, a dependency and a
// comment. The dependency and the comment reach the durable tables, the
// children keep their parent, and the child counter carries, so the next
// `bd create --parent` mints .3 rather than colliding with the wisp child .1.
// `bd comments` lists the promotion reason, and on_update fires twice: once
// for the move and once for the promotion comment.
func TestEmbeddedPromoteMovesAWispParent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pw")

	target := bdCreate(t, bd, dir, "Promote dep target", "--type", "task")
	parent := bdCreate(t, bd, dir, "Promoted wisp parent", "--ephemeral")
	var children []string
	for i := 1; i <= 2; i++ {
		child := bdCreate(t, bd, dir, fmt.Sprintf("Wisp child %d", i), "--ephemeral", "--parent", parent.ID)
		if want := fmt.Sprintf("%s.%d", parent.ID, i); child.ID != want {
			t.Fatalf("wisp child %d is %s, want %s", i, child.ID, want)
		}
		children = append(children, child.ID)
	}
	bdDepAdd(t, bd, dir, parent.ID, target.ID)
	bdComment(t, bd, dir, parent.ID, "pre-promote note")

	// Planted after the setup, so every line it logs is the promote's.
	hookLog := plantCountingHook(t, beadsDir, "on_update")
	bdPromote(t, bd, dir, parent.ID, "--reason", "worth keeping")
	waitForHookLines(t, hookLog, parent.ID, 2)

	if got := bdShow(t, bd, dir, parent.ID); got.Ephemeral {
		t.Errorf("%s still ephemeral after promote", parent.ID)
	}
	assertDepExists(t, beadsDir, "pw", parent.ID, target.ID)
	for _, id := range children {
		var shown struct {
			Parent *string `json:"parent"`
		}
		if err := json.Unmarshal(parseShowJSON(t, bdShowJSON(t, bd, dir, id)), &shown); err != nil {
			t.Fatalf("decode bd show %s: %v", id, err)
		}
		if shown.Parent == nil || *shown.Parent != parent.ID {
			t.Errorf("%s parent after promote = %v, want %s", id, shown.Parent, parent.ID)
		}
	}
	comments := bdComments(t, bd, dir, parent.ID)
	for _, text := range []string{"pre-promote note", "Promoted from Level 0: worth keeping"} {
		if !strings.Contains(comments, text) {
			t.Errorf("bd comments %s does not list %q:\n%s", parent.ID, text, comments)
		}
	}
	if next := bdCreate(t, bd, dir, "Child after promote", "--parent", parent.ID); next.ID != parent.ID+".3" {
		t.Errorf("next child of the promoted parent is %s, want %s", next.ID, parent.ID+".3")
	}
}

// TestEmbeddedPromoteCLIConcurrent exercises promote concurrently.
func TestEmbeddedPromoteCLIConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "px")

	const numWorkers = 6

	// Pre-create ephemeral issues
	var issueIDs []string
	for i := 0; i < numWorkers; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-promote-%d", i), "--ephemeral")
		issueIDs = append(issueIDs, issue.ID)
	}

	type workerResult struct {
		worker int
		out    string
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			cmd := exec.Command(bd, "promote", issueIDs[worker])
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			r.out = string(out)
			if err != nil {
				r.err = fmt.Errorf("promote %s: %v", issueIDs[worker], err)
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	// The driver handles concurrency internally — all workers should succeed.
	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}

	// Verify all issues are permanent.
	for i, id := range issueIDs {
		got := bdShow(t, bd, dir, id)
		if got.Ephemeral {
			t.Errorf("expected %s (worker %d) to be permanent after promote", id, i)
		}
	}
}
