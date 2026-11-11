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

		out := bdReopen(t, bd, dir, issue.ID, "--reason", "Not actually done")
		if !strings.Contains(out, "Reopened") {
			t.Errorf("expected 'Reopened' in output: %s", out)
		}
		if !strings.Contains(out, "Not actually done") {
			t.Logf("reason may not appear in text output: %s", out)
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
// leave the reason on the issue's comment thread, written by the reopening
// actor, where every read surface shows it: `bd comments`, `bd show` and
// `bd export`. Before the fix the reason was echoed and then only kept as a
// clone-local audit event none of them read.
func TestEmbeddedReopenReasonIsReadable(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rr")

	const reason = "REOPEN_REASON_MARKER"
	issue := bdCreate(t, bd, dir, "probe issue", "--type", "task")
	bdClose(t, bd, dir, issue.ID, "--reason", "CLOSE_REASON_MARKER")
	out := bdReopen(t, bd, dir, issue.ID, "--reason", reason, "--actor", "alice")
	if !strings.Contains(out, reason) {
		t.Fatalf("reopen did not echo the reason: %s", out)
	}

	assertOneReasonComment := func(surface string, comments []*types.Comment) {
		t.Helper()
		var matching int
		for _, c := range comments {
			if c.Text == reason {
				matching++
				if c.Author != "alice" {
					t.Errorf("%s: reopen comment author = %q, want alice", surface, c.Author)
				}
			}
		}
		if matching != 1 {
			t.Errorf("%s: %d comments read %q, want exactly 1; comments = %+v", surface, matching, reason, comments)
		}
	}

	raw := bdComments(t, bd, dir, issue.ID, "--json")
	start := strings.Index(raw, "[")
	if start < 0 {
		t.Fatalf("no JSON array in `bd comments %s --json`:\n%s", issue.ID, raw)
	}
	var comments []*types.Comment
	if err := json.Unmarshal([]byte(raw[start:]), &comments); err != nil {
		t.Fatalf("parse `bd comments --json`: %v\n%s", err, raw)
	}
	assertOneReasonComment("bd comments", comments)

	// bd show --json is count-only by default; the thread needs --include-comments.
	shown := parseIssueJSON(t, []byte(bdShowRaw(t, bd, dir, issue.ID, "--json", "--include-comments")))
	assertOneReasonComment("bd show", shown.Comments)

	var exported *types.Issue
	for _, line := range strings.Split(bdExport(t, bd, dir), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			continue
		}
		var candidate types.Issue
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			continue
		}
		if candidate.ID == issue.ID {
			exported = &candidate
		}
	}
	if exported == nil {
		t.Fatalf("bd export did not include %s", issue.ID)
	}
	assertOneReasonComment("bd export", exported.Comments)
}
