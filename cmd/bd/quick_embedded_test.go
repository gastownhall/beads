//go:build embeddeddolt

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdQuick runs "bd q" with the given args and returns the trimmed ID output.
func bdQuick(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"q"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd q %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestEmbeddedQuick(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "qq")

	t.Run("basic_quick_create", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Quick task")
		if !strings.HasPrefix(id, "qq-") {
			t.Errorf("expected ID with prefix qq-, got %q", id)
		}
		// Verify the issue exists with correct title
		got := bdShow(t, bd, dir, id)
		if got.Title != "Quick task" {
			t.Errorf("expected title 'Quick task', got %q", got.Title)
		}
		if got.Status != "open" {
			t.Errorf("expected status open, got %s", got.Status)
		}
	})

	t.Run("quick_output_is_id_only", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "ID only check")
		// Output should be a single line with just the ID
		lines := strings.Split(id, "\n")
		if len(lines) != 1 {
			t.Errorf("expected single line output, got %d lines: %q", len(lines), id)
		}
		if strings.Contains(id, " ") {
			t.Errorf("expected ID-only output (no spaces), got %q", id)
		}
	})

	t.Run("quick_priority", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Priority test", "-p", "1")
		got := bdShow(t, bd, dir, id)
		if got.Priority != 1 {
			t.Errorf("expected priority 1, got %d", got.Priority)
		}
	})

	t.Run("quick_priority_pn_format", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "P0 test", "-p", "P0")
		got := bdShow(t, bd, dir, id)
		if got.Priority != 0 {
			t.Errorf("expected priority 0 for P0, got %d", got.Priority)
		}
	})

	t.Run("quick_type", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Bug quick", "-t", "bug")
		got := bdShow(t, bd, dir, id)
		if got.IssueType != "bug" {
			t.Errorf("expected type bug, got %s", got.IssueType)
		}
	})

	t.Run("quick_default_type", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Default type")
		got := bdShow(t, bd, dir, id)
		if got.IssueType != "task" {
			t.Errorf("expected default type task, got %s", got.IssueType)
		}
	})

	t.Run("quick_default_priority", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Default priority")
		got := bdShow(t, bd, dir, id)
		if got.Priority != 2 {
			t.Errorf("expected default priority 2, got %d", got.Priority)
		}
	})

	t.Run("quick_labels", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "Labeled quick", "-l", "urgent", "-l", "backend")
		// Verify labels via show --json
		m := bdShowDetails(t, bd, dir, id)
		labels, _ := m["labels"].([]interface{})
		labelSet := map[string]bool{}
		for _, l := range labels {
			labelSet[l.(string)] = true
		}
		if !labelSet["urgent"] || !labelSet["backend"] {
			t.Errorf("expected labels urgent and backend, got %v", labels)
		}
	})

	t.Run("quick_multi_word_title", func(t *testing.T) {
		id := bdQuick(t, bd, dir, "This", "is", "multi", "word")
		got := bdShow(t, bd, dir, id)
		if got.Title != "This is multi word" {
			t.Errorf("expected multi-word title, got %q", got.Title)
		}
	})

	t.Run("quick_no_title_fails", func(t *testing.T) {
		cmd := exec.Command(bd, "q")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected bd q with no title to fail, got: %s", out)
		}
	})
}

// TestEmbeddedQuickConcurrent exercises quick-create concurrently.
func TestEmbeddedQuickConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "qc")

	const (
		numWorkers      = 8
		issuesPerWorker = 3
	)

	type workerResult struct {
		worker int
		ids    []string
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			for i := 0; i < issuesPerWorker; i++ {
				title := fmt.Sprintf("w%d-quick-%d", worker, i)
				cmd := exec.Command(bd, "q", title)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("q %s: %v\n%s", title, err, out)
					results[worker] = r
					return
				}
				id := strings.TrimSpace(string(out))
				if id == "" {
					r.err = fmt.Errorf("q %s: empty ID", title)
					results[worker] = r
					return
				}
				r.ids = append(r.ids, id)
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	allIDs := map[string]bool{}
	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
			continue
		}
		for _, id := range r.ids {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	expected := numWorkers * issuesPerWorker
	if len(allIDs) != expected {
		t.Errorf("expected %d unique IDs, got %d", expected, len(allIDs))
	}
}
