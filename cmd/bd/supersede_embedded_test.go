package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdSupersede runs "bd supersede" with the given args and returns raw stdout.
func bdSupersede(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"supersede"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd supersede %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdSupersedeFail runs "bd supersede" expecting failure.
func bdSupersedeFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"supersede"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd supersede %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedSupersede(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ss")

	// ===== Mark as superseded =====

	t.Run("mark_superseded", func(t *testing.T) {
		oldIssue := bdCreate(t, bd, dir, "Old spec v1", "--type", "task")
		newIssue := bdCreate(t, bd, dir, "New spec v2", "--type", "task")
		out := bdSupersede(t, bd, dir, oldIssue.ID, "--with", newIssue.ID)
		if !strings.Contains(out, "superseded") {
			t.Errorf("expected 'superseded' in output: %s", out)
		}
	})

	// ===== Verify closure =====

	t.Run("superseded_is_closed", func(t *testing.T) {
		oldIssue := bdCreate(t, bd, dir, "Closed old", "--type", "task")
		newIssue := bdCreate(t, bd, dir, "Closed new", "--type", "task")
		bdSupersede(t, bd, dir, oldIssue.ID, "--with", newIssue.ID)

		s := openStore(t, beadsDir, "ss")
		issue, err := s.GetIssue(t.Context(), oldIssue.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if issue.Status != "closed" {
			t.Errorf("expected status=closed, got %s", issue.Status)
		}
	})

	// ===== Creates supersedes link =====

	t.Run("creates_supersedes_link", func(t *testing.T) {
		oldIssue := bdCreate(t, bd, dir, "Link old", "--type", "task")
		newIssue := bdCreate(t, bd, dir, "Link new", "--type", "task")
		bdSupersede(t, bd, dir, oldIssue.ID, "--with", newIssue.ID)

		out := bdDep(t, bd, dir, "list", oldIssue.ID)
		if !strings.Contains(out, newIssue.ID) {
			t.Errorf("expected new issue in dep list: %s", out)
		}
	})

	// ===== JSON output =====

	t.Run("json_output", func(t *testing.T) {
		oldIssue := bdCreate(t, bd, dir, "JSON old", "--type", "task")
		newIssue := bdCreate(t, bd, dir, "JSON new", "--type", "task")
		fullArgs := []string{"supersede", oldIssue.ID, "--with", newIssue.ID, "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("supersede --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON: %s", s)
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if m["superseded"] != oldIssue.ID {
			t.Errorf("expected superseded=%s, got %v", oldIssue.ID, m["superseded"])
		}
		if m["replacement"] != newIssue.ID {
			t.Errorf("expected replacement=%s, got %v", newIssue.ID, m["replacement"])
		}
	})

	// ===== Error: same ID =====

	t.Run("error_same_id", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Same ID", "--type", "task")
		bdSupersedeFail(t, bd, dir, issue.ID, "--with", issue.ID)
	})

	// ===== Error: nonexistent replacement =====

	t.Run("error_nonexistent_replacement", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No replacement", "--type", "task")
		bdSupersedeFail(t, bd, dir, issue.ID, "--with", "ss-nonexistent999")
	})
}

// TestEmbeddedSupersedeConcurrent exercises supersede operations concurrently.
func TestEmbeddedSupersedeConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ssc")

	newIssue := bdCreate(t, bd, dir, "Concurrent replacement", "--type", "task")
	var oldIDs []string
	for i := 0; i < 8; i++ {
		old := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-old-%d", i), "--type", "task")
		oldIDs = append(oldIDs, old.ID)
	}

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

			args := []string{"supersede", oldIDs[worker], "--with", newIssue.ID}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d: %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
