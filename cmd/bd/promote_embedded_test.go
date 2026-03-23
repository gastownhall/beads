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

// bdPromote runs "bd promote" with the given args and returns stdout.
func bdPromote(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"promote"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd promote %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd promote --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
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
			if err != nil {
				r.err = fmt.Errorf("promote %s: %v\n%s", issueIDs[worker], err, out)
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

	// Verify all are now permanent
	for _, id := range issueIDs {
		got := bdShow(t, bd, dir, id)
		if got.Ephemeral {
			t.Errorf("expected %s to be permanent after promote", id)
		}
	}
}
