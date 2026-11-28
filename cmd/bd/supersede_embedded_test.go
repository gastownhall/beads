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
)

// bdSupersede runs "bd supersede" with the given args and returns raw stdout.
func bdSupersede(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"supersede"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd supersede %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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
		if issue.ClosedAt == nil {
			t.Error("expected supersede close to set closed_at")
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("supersede --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
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

	// ===== --ack blocks open dependents until acknowledged =====

	t.Run("ack_blocks_open_dependents", func(t *testing.T) {
		oldDecision := bdCreate(t, bd, dir, "Ack decision v1", "--type", "decision")
		newDecision := bdCreate(t, bd, dir, "Ack decision v2", "--type", "decision")
		inProgress := bdCreate(t, bd, dir, "Ack step in progress", "--type", "task")
		next := bdCreate(t, bd, dir, "Ack next step", "--type", "task")
		finished := bdCreate(t, bd, dir, "Ack finished step", "--type", "task")
		unrelated := bdCreate(t, bd, dir, "Ack unrelated step", "--type", "task")
		bdDepAdd(t, bd, dir, inProgress.ID, oldDecision.ID, "--type", "related")
		bdDepAdd(t, bd, dir, next.ID, oldDecision.ID, "--type", "related")
		bdDepAdd(t, bd, dir, finished.ID, oldDecision.ID, "--type", "related")
		bdClose(t, bd, dir, finished.ID)
		bdUpdate(t, bd, dir, inProgress.ID, "--claim")

		out, err := bdRunWithFlockRetry(t, bd, dir, "supersede", oldDecision.ID, "--with", newDecision.ID, "--ack", "--json")
		if err != nil {
			t.Fatalf("supersede --ack --json failed: %v\n%s", err, out)
		}
		var result struct {
			Superseded      string   `json:"superseded"`
			Replacement     string   `json:"replacement"`
			Acknowledgement string   `json:"acknowledgement"`
			Blocked         []string `json:"blocked"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse supersede --ack --json: %v\n%s", err, out)
		}
		if result.Superseded != oldDecision.ID || result.Replacement != newDecision.ID {
			t.Fatalf("unexpected supersede result: %+v", result)
		}
		if result.Acknowledgement == "" {
			t.Fatalf("expected an acknowledgement issue, got %+v", result)
		}
		blocked := map[string]bool{}
		for _, id := range result.Blocked {
			blocked[id] = true
		}
		if len(blocked) != 2 || !blocked[inProgress.ID] || !blocked[next.ID] {
			t.Fatalf("expected exactly %s and %s blocked, got %v", inProgress.ID, next.ID, result.Blocked)
		}

		ready := readyIDs(t, bd, dir)
		if ready[next.ID] {
			t.Errorf("%s should leave bd ready until the acknowledgement closes", next.ID)
		}
		if !ready[unrelated.ID] {
			t.Errorf("%s has no edge to the superseded issue and should stay ready", unrelated.ID)
		}
		if !ready[result.Acknowledgement] {
			t.Errorf("acknowledgement %s should be ready", result.Acknowledgement)
		}

		closeOut := bdCloseFail(t, bd, dir, inProgress.ID)
		if !strings.Contains(closeOut, result.Acknowledgement) {
			t.Errorf("close refusal should name the acknowledgement %s: %s", result.Acknowledgement, closeOut)
		}

		ack := bdShow(t, bd, dir, result.Acknowledgement)
		if !strings.Contains(ack.Description, oldDecision.Title) || !strings.Contains(ack.Description, newDecision.Title) {
			t.Errorf("acknowledgement description should show both versions: %q", ack.Description)
		}

		bdClose(t, bd, dir, result.Acknowledgement, "--reason", "read v2")
		bdClose(t, bd, dir, inProgress.ID)
		if !readyIDs(t, bd, dir)[next.ID] {
			t.Errorf("%s should be ready once the acknowledgement and its blocker close", next.ID)
		}
	})

	t.Run("ack_without_open_dependents_creates_nothing", func(t *testing.T) {
		oldIssue := bdCreate(t, bd, dir, "Ack lonely v1", "--type", "decision")
		newIssue := bdCreate(t, bd, dir, "Ack lonely v2", "--type", "decision")
		out, err := bdRunWithFlockRetry(t, bd, dir, "supersede", oldIssue.ID, "--with", newIssue.ID, "--ack", "--json")
		if err != nil {
			t.Fatalf("supersede --ack --json failed: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("parse: %v\n%s", err, out)
		}
		if _, ok := m["acknowledgement"]; ok {
			t.Errorf("expected no acknowledgement without open dependents, got %v", m)
		}
		if blocked, ok := m["blocked"].([]interface{}); !ok || len(blocked) != 0 {
			t.Errorf("expected blocked=[], got %v", m["blocked"])
		}
		if got := bdShow(t, bd, dir, oldIssue.ID); got.Status != "closed" {
			t.Errorf("expected superseded issue closed, got %s", got.Status)
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
