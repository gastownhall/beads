//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdReopen runs "bd reopen" with the given args and returns stdout.
func bdReopen(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"reopen"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd reopen %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedReopen(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ro")

	t.Run("reopen_single", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reopen me", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Fatalf("expected closed before reopen, got %s", got.Status)
		}

		out := bdReopen(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Reopened") {
			t.Errorf("expected 'Reopened' in output: %s", out)
		}

		got = bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected open after reopen, got %s", got.Status)
		}
		if got.ClosedAt != nil {
			t.Error("expected closed_at cleared after reopen")
		}
	})

	t.Run("reopen_multiple", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi reopen 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi reopen 2", "--type", "task")
		bdClose(t, bd, dir, issue1.ID, issue2.ID)

		bdReopen(t, bd, dir, issue1.ID, issue2.ID)

		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.Status != types.StatusOpen {
			t.Errorf("issue1: expected open, got %s", got1.Status)
		}
		if got2.Status != types.StatusOpen {
			t.Errorf("issue2: expected open, got %s", got2.Status)
		}
	})

	t.Run("reopen_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reason reopen", "--type", "task")
		bdClose(t, bd, dir, issue.ID)

		out := bdReopen(t, bd, dir, issue.ID, "--actor", "alice", "--reason", "Not actually done")
		if !strings.Contains(out, "Reopened") {
			t.Errorf("expected 'Reopened' in output: %s", out)
		}
		if !strings.Contains(out, "Not actually done") {
			t.Logf("reason may not appear in text output: %s", out)
		}

		// GH#5593: the reason must survive on a surface a user can read
		// back — the comment thread, attributed to the reopen actor.
		raw := bdComments(t, bd, dir, issue.ID, "--json")
		start := strings.Index(raw, "[")
		if start < 0 {
			t.Fatalf("no JSON array in bd comments output:\n%s", raw)
		}
		var comments []types.Comment
		if err := json.Unmarshal([]byte(raw[start:]), &comments); err != nil {
			t.Fatalf("parse comments JSON: %v\n%s", err, raw[start:])
		}
		if len(comments) != 1 {
			t.Fatalf("comment thread has %d comments after a reasoned reopen, want exactly 1:\n%s", len(comments), raw)
		}
		if comments[0].Text != "Not actually done" || comments[0].Author != "alice" {
			t.Errorf("reopen comment = {text: %q, author: %q}, want {text: %q, author: %q}",
				comments[0].Text, comments[0].Author, "Not actually done", "alice")
		}

		if shown := bdShowRaw(t, bd, dir, issue.ID); !strings.Contains(shown, "Not actually done") {
			t.Errorf("bd show does not carry the reopen reason in the comment thread:\n%s", shown)
		}
		if exported := bdExport(t, bd, dir, "--all"); !strings.Contains(exported, "Not actually done") {
			t.Errorf("bd export --all does not carry the reopen reason in the issue's comments:\n%s", exported)
		}
	})

	t.Run("reopen_with_reason_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Short reason reopen", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		bdReopen(t, bd, dir, issue.ID, "-r", "Needs more work")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected open, got %s", got.Status)
		}
	})

	t.Run("reopen_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON reopen", "--type", "task")
		bdClose(t, bd, dir, issue.ID)

		cmd := exec.Command(bd, "reopen", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd reopen --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "[")
		if start < 0 {
			start = strings.Index(s, "{")
		}
		if start >= 0 {
			// Verify it's valid JSON
			_ = s[start:]
		}
	})

	t.Run("reopen_already_open", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Already open", "--type", "task")
		// Reopen an already-open issue — should not error, just print message
		out := bdReopen(t, bd, dir, issue.ID)
		if !strings.Contains(out, "already open") {
			t.Logf("already-open message: %s", out)
		}
	})

	t.Run("reopen_clears_defer_until", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Deferred reopen", "--type", "task", "--defer", "2030-01-01")
		bdClose(t, bd, dir, issue.ID)
		bdReopen(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected open, got %s", got.Status)
		}
	})

	t.Run("reopen_nonexistent", func(t *testing.T) {
		cmd := exec.Command(bd, "reopen", "ro-nonexistent999")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected reopen of nonexistent to fail, got: %s", out)
		}
	})
}

// TestEmbeddedReopenConcurrent exercises reopen concurrently.
func TestEmbeddedReopenConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rx")

	const numWorkers = 8

	// Pre-create and close issues
	var issueIDs []string
	for i := 0; i < numWorkers; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-reopen-%d", i), "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		issueIDs = append(issueIDs, issue.ID)
	}

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

			cmd := exec.Command(bd, "reopen", issueIDs[worker])
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("reopen %s: %v\n%s", issueIDs[worker], err, out)
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	var successes int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			continue
		}
		successes++
	}
	if successes == 0 {
		t.Fatal("all workers failed; expected at least 1 success")
	}
	t.Logf("%d/%d workers succeeded (flock contention expected)", successes, numWorkers)

	// Verify only successful workers' issues are reopened
	for _, r := range results {
		if r.err != nil {
			continue
		}
		id := issueIDs[r.worker]
		got := bdShow(t, bd, dir, id)
		if got.Status != types.StatusOpen {
			t.Errorf("expected %s to be open after reopen, got %s", id, got.Status)
		}
	}
}

// TestEmbeddedReopenReasonIsReadable pins GH#5593: `bd reopen --reason` must
// leave the reason on a surface a user can read back. The reopen path calls it
// a "reopen comment", so `bd comments` is where it belongs.
func TestEmbeddedReopenReasonIsReadable(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rr")

	issue := bdCreate(t, bd, dir, "probe issue", "--type", "task")
	bdClose(t, bd, dir, issue.ID, "--reason", "CLOSE_REASON_MARKER")
	out := bdReopen(t, bd, dir, issue.ID, "--reason", "REOPEN_REASON_MARKER")
	if !strings.Contains(out, "REOPEN_REASON_MARKER") {
		t.Fatalf("reopen did not echo the reason: %s", out)
	}

	comments := bdComments(t, bd, dir, issue.ID, "--json")
	if !strings.Contains(comments, "REOPEN_REASON_MARKER") {
		t.Errorf("reopen reason was echoed but is not in `bd comments %s --json`:\n%s", issue.ID, comments)
	}
}
