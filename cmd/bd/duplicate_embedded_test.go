//go:build embeddeddolt

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

// bdDuplicate runs "bd duplicate" with the given args and returns raw stdout.
func bdDuplicate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"duplicate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd duplicate %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDuplicateFail runs "bd duplicate" expecting failure.
func bdDuplicateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"duplicate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd duplicate %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedDuplicate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "du")

	// ===== Mark as duplicate =====

	t.Run("mark_duplicate", func(t *testing.T) {
		canonical := bdCreate(t, bd, dir, "Canonical issue", "--type", "bug")
		dupe := bdCreate(t, bd, dir, "Duplicate issue", "--type", "bug")
		out := bdDuplicate(t, bd, dir, dupe.ID, "--of", canonical.ID)
		if !strings.Contains(out, "duplicate") {
			t.Errorf("expected 'duplicate' in output: %s", out)
		}
	})

	// ===== Verify closure =====

	t.Run("duplicate_is_closed", func(t *testing.T) {
		canonical := bdCreate(t, bd, dir, "Canon closed", "--type", "task")
		dupe := bdCreate(t, bd, dir, "Dupe closed", "--type", "task")
		bdDuplicate(t, bd, dir, dupe.ID, "--of", canonical.ID)

		s := openStore(t, beadsDir, "du")
		issue, err := s.GetIssue(t.Context(), dupe.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if issue.Status != "closed" {
			t.Errorf("expected status=closed, got %s", issue.Status)
		}
	})

	// ===== Creates dependency link =====

	t.Run("creates_dep_link", func(t *testing.T) {
		canonical := bdCreate(t, bd, dir, "Canon link", "--type", "task")
		dupe := bdCreate(t, bd, dir, "Dupe link", "--type", "task")
		bdDuplicate(t, bd, dir, dupe.ID, "--of", canonical.ID)

		// Check via dep list
		out := bdDep(t, bd, dir, "list", dupe.ID)
		if !strings.Contains(out, canonical.ID) {
			t.Errorf("expected canonical in dep list: %s", out)
		}
	})

	// ===== JSON output =====

	t.Run("json_output", func(t *testing.T) {
		canonical := bdCreate(t, bd, dir, "Canon JSON", "--type", "task")
		dupe := bdCreate(t, bd, dir, "Dupe JSON", "--type", "task")
		fullArgs := []string{"duplicate", dupe.ID, "--of", canonical.ID, "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("duplicate --json failed: %v\n%s", err, out)
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
		if m["duplicate"] != dupe.ID {
			t.Errorf("expected duplicate=%s, got %v", dupe.ID, m["duplicate"])
		}
		if m["canonical"] != canonical.ID {
			t.Errorf("expected canonical=%s, got %v", canonical.ID, m["canonical"])
		}
	})

	// ===== Error: same ID =====

	t.Run("error_same_id", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Same ID", "--type", "task")
		bdDuplicateFail(t, bd, dir, issue.ID, "--of", issue.ID)
	})

	// ===== Error: nonexistent canonical =====

	t.Run("error_nonexistent_canonical", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No canon", "--type", "task")
		bdDuplicateFail(t, bd, dir, issue.ID, "--of", "du-nonexistent999")
	})
}

// TestEmbeddedDuplicateConcurrent exercises duplicate operations concurrently.
func TestEmbeddedDuplicateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "duc")

	canonical := bdCreate(t, bd, dir, "Concurrent canonical", "--type", "task")
	var dupeIDs []string
	for i := 0; i < 8; i++ {
		d := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-dupe-%d", i), "--type", "task")
		dupeIDs = append(dupeIDs, d.ID)
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

			args := []string{"duplicate", dupeIDs[worker], "--of", canonical.ID}
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
