//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdHuman runs "bd human" with the given args and returns stdout.
func bdHuman(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"human"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd human %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedHuman(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "th")

	// ===== Default Help Output =====

	t.Run("human_default", func(t *testing.T) {
		out := bdHuman(t, bd, dir)
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty human output")
		}
	})

	// ===== List =====

	t.Run("human_list_empty", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "list")
		// No human-labeled issues yet — should succeed without error
		_ = out
	})

	// ===== Stats =====

	t.Run("human_stats", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "stats")
		// Should succeed and produce output
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty stats output")
		}
	})

	// ===== Respond and Dismiss =====

	t.Run("human_respond_and_dismiss", func(t *testing.T) {
		// Create a bead
		cmd := exec.Command(bd, "create", "Human test issue", "--type", "task", "--silent")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
		id := strings.TrimSpace(string(out))
		if id == "" {
			t.Fatalf("could not find issue ID in output: %s", out)
		}

		// Humanize it
		cmd = exec.Command(bd, "label", "add", id, "human")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("label add failed: %v\n%s", err, out)
		}

		// Verify it shows up in human list
		listOut := bdHuman(t, bd, dir, "list")
		if !strings.Contains(listOut, id) {
			t.Errorf("expected issue %s in human list output:\n%s", id, listOut)
		}

		// Test Respond
		bdHuman(t, bd, dir, "respond", id, "--response", "Approved")

		// Verify closed
		cmd = exec.Command(bd, "show", id)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		showOut, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("show failed: %v\n%s", err, showOut)
		}
		if !strings.Contains(string(showOut), "CLOSED") {
			t.Errorf("expected issue %s to be closed after respond:\n%s", id, showOut)
		}

		// Create another for Dismiss
		cmd = exec.Command(bd, "create", "Dismiss test issue", "--silent")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
		id2 := strings.TrimSpace(string(out))
		if id2 == "" {
			t.Fatalf("could not find issue ID in output: %s", out)
		}

		cmd = exec.Command(bd, "label", "add", id2, "human")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err = cmd.CombinedOutput(); err != nil {
			t.Fatalf("label add failed: %v\n%s", err, out)
		}

		// Test Dismiss
		bdHuman(t, bd, dir, "dismiss", id2, "--reason", "Not needed")

		// Verify closed
		cmd = exec.Command(bd, "show", id2)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		showOut2, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("show failed: %v\n%s", err, showOut2)
		}
		if !strings.Contains(string(showOut2), "CLOSED") {
			t.Errorf("expected issue %s to be closed after dismiss:\n%s", id2, showOut2)
		}
		if !strings.Contains(string(showOut2), "Dismissed: Not needed") {
			t.Errorf("expected dismiss reason in output:\n%s", showOut2)
		}
	})
}

// TestEmbeddedHumanConcurrent exercises human operations concurrently.
func TestEmbeddedHumanConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hx")

	const numWorkers = 8

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			var args []string
			switch worker % 2 {
			case 0:
				args = []string{"human", "list"}
			case 1:
				args = []string{"human", "stats"}
			}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("human (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
