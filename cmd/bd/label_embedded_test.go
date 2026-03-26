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

// bdLabel runs "bd label" with the given args and returns stdout.
func bdLabel(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd label %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdLabelFail runs "bd label" expecting failure.
func bdLabelFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"label"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd label %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdLabelListJSON runs "bd label list --json" and returns parsed labels.
func bdLabelListJSON(t *testing.T, bd, dir, issueID string) []string {
	t.Helper()
	cmd := exec.Command(bd, "label", "list", issueID, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd label list %s --json failed: %v\n%s", issueID, err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(s[start:]), &labels); err != nil {
		t.Fatalf("parse label list JSON: %v\n%s", err, s)
	}
	return labels
}

// bdLabelListAllJSON runs "bd label list-all --json" and returns parsed results.
func bdLabelListAllJSON(t *testing.T, bd, dir string) []map[string]interface{} {
	t.Helper()
	cmd := exec.Command(bd, "label", "list-all", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd label list-all --json failed: %v\n%s", err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &results); err != nil {
		t.Fatalf("parse label list-all JSON: %v\n%s", err, s)
	}
	return results
}

func TestEmbeddedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tl")

	// ===== Label Add =====

	t.Run("label_add_single", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label add test", "--type", "task")
		out := bdLabel(t, bd, dir, "add", issue.ID, "urgent")
		if !strings.Contains(out, "Added") {
			t.Errorf("expected 'Added' in output: %s", out)
		}
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		found := false
		for _, l := range labels {
			if l == "urgent" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'urgent' in labels: %v", labels)
		}
	})

	t.Run("label_add_batch", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Batch label 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Batch label 2", "--type", "task")
		bdLabel(t, bd, dir, "add", issue1.ID, issue2.ID, "batch-label")

		for _, id := range []string{issue1.ID, issue2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			found := false
			for _, l := range labels {
				if l == "batch-label" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected 'batch-label' on %s: %v", id, labels)
			}
		}
	})

	t.Run("label_add_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label JSON add", "--type", "task")
		cmd := exec.Command(bd, "label", "add", issue.ID, "json-label", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd label add --json failed: %v\n%s", err, out)
		}
		if !json.Valid([]byte(strings.TrimSpace(string(out))[strings.Index(strings.TrimSpace(string(out)), "["):])) {
			t.Errorf("expected valid JSON: %s", out)
		}
	})

	t.Run("label_add_duplicate_idempotent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Dup label", "--type", "task")
		bdLabel(t, bd, dir, "add", issue.ID, "dup")
		bdLabel(t, bd, dir, "add", issue.ID, "dup")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		count := 0
		for _, l := range labels {
			if l == "dup" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 'dup' label, got %d in %v", count, labels)
		}
	})

	// ===== Label Remove =====

	t.Run("label_remove", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Remove label", "--type", "task", "--label", "removeme")
		bdLabel(t, bd, dir, "remove", issue.ID, "removeme")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		for _, l := range labels {
			if l == "removeme" {
				t.Error("label should have been removed")
			}
		}
	})

	t.Run("label_remove_batch", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Batch rm 1", "--type", "task", "--label", "batch-rm")
		issue2 := bdCreate(t, bd, dir, "Batch rm 2", "--type", "task", "--label", "batch-rm")
		bdLabel(t, bd, dir, "remove", issue1.ID, issue2.ID, "batch-rm")
		for _, id := range []string{issue1.ID, issue2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			for _, l := range labels {
				if l == "batch-rm" {
					t.Errorf("label should have been removed from %s", id)
				}
			}
		}
	})

	t.Run("label_remove_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON rm label", "--type", "task", "--label", "jsonrm")
		cmd := exec.Command(bd, "label", "remove", issue.ID, "jsonrm", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd label remove --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON: %s", s)
		}
	})

	// ===== Label List =====

	t.Run("label_list", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "List labels", "--type", "task", "--label", "alpha", "--label", "beta")
		out := bdLabel(t, bd, dir, "list", issue.ID)
		if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
			t.Errorf("expected both labels in list output: %s", out)
		}
	})

	t.Run("label_list_empty", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No labels", "--type", "task")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		if len(labels) != 0 {
			t.Errorf("expected empty labels, got %v", labels)
		}
	})

	t.Run("label_list_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON list", "--type", "task", "--label", "x", "--label", "y")
		labels := bdLabelListJSON(t, bd, dir, issue.ID)
		labelSet := map[string]bool{}
		for _, l := range labels {
			labelSet[l] = true
		}
		if !labelSet["x"] || !labelSet["y"] {
			t.Errorf("expected labels x and y, got %v", labels)
		}
	})

	// ===== Label List-All =====

	t.Run("label_list_all", func(t *testing.T) {
		out := bdLabel(t, bd, dir, "list-all")
		// Should show some labels from earlier tests
		if !strings.Contains(out, "urgent") {
			t.Logf("list-all output may not contain 'urgent': %s", out)
		}
	})

	t.Run("label_list_all_json", func(t *testing.T) {
		results := bdLabelListAllJSON(t, bd, dir)
		if len(results) == 0 {
			t.Error("expected labels in list-all")
		}
		// Each result should have label and count
		for _, r := range results {
			if _, ok := r["label"]; !ok {
				t.Error("expected 'label' key in list-all result")
			}
			if _, ok := r["count"]; !ok {
				t.Error("expected 'count' key in list-all result")
			}
		}
	})

	// ===== Label Propagate =====

	t.Run("label_propagate", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Propagate parent", "--type", "epic")
		child1 := bdCreate(t, bd, dir, "Propagate child 1", "--type", "task")
		child2 := bdCreate(t, bd, dir, "Propagate child 2", "--type", "task")
		bdDepAdd(t, bd, dir, child1.ID, parent.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, child2.ID, parent.ID, "--type", "parent-child")

		out := bdLabel(t, bd, dir, "propagate", parent.ID, "team:platform")
		if !strings.Contains(out, "Propagated") {
			t.Errorf("expected 'Propagated' in output: %s", out)
		}

		// Verify children got the label
		for _, id := range []string{child1.ID, child2.ID} {
			labels := bdLabelListJSON(t, bd, dir, id)
			found := false
			for _, l := range labels {
				if l == "team:platform" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected 'team:platform' on child %s: %v", id, labels)
			}
		}
	})

	t.Run("label_propagate_json", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "JSON propagate", "--type", "epic")
		child := bdCreate(t, bd, dir, "JSON prop child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		cmd := exec.Command(bd, "label", "propagate", parent.ID, "prop-json", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd label propagate --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start >= 0 && !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON: %s", s)
		}
	})

	t.Run("label_propagate_no_children", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "No children parent", "--type", "task")
		out := bdLabel(t, bd, dir, "propagate", parent.ID, "orphan-label")
		if !strings.Contains(out, "No children") {
			t.Logf("propagate with no children: %s", out)
		}
	})

	// ===== Error Cases =====

	t.Run("label_add_empty_label", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Empty label", "--type", "task")
		bdLabelFail(t, bd, dir, "add", issue.ID, "")
	})

	t.Run("label_add_reserved_provides", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reserved label", "--type", "task")
		bdLabelFail(t, bd, dir, "add", issue.ID, "provides:auth")
	})
}

// TestEmbeddedLabelConcurrent exercises label operations concurrently.
func TestEmbeddedLabelConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lx")

	const numWorkers = 8

	// Pre-create issues
	var issueIDs []string
	for i := 0; i < numWorkers; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("label-concurrent-%d", i), "--type", "task")
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
			id := issueIDs[worker]

			// Add labels
			for i := 0; i < 3; i++ {
				label := fmt.Sprintf("w%d-label-%d", worker, i)
				cmd := exec.Command(bd, "label", "add", id, label)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("add label %s to %s: %v\n%s", label, id, err, out)
					results[worker] = r
					return
				}
			}

			// List labels
			cmd := exec.Command(bd, "label", "list", id, "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("list labels for %s: %v\n%s", id, err, out)
				results[worker] = r
				return
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
