//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")

	// ===== Default =====

	t.Run("ready_default", func(t *testing.T) {
		bdCreate(t, bd, dir, "Ready test issue", "--type", "task")
		cmd := exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Ready test issue") {
			t.Errorf("expected issue in ready output: %s", out)
		}
	})

	// ===== --json =====

	t.Run("ready_json", func(t *testing.T) {
		cmd := exec.Command(bd, "ready", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready --json failed: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.IndexAny(s, "[{")
		if start < 0 {
			t.Fatalf("no JSON in ready --json output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("invalid JSON in ready output: %s", s[:min(200, len(s))])
		}
	})

	t.Run("ready_json_truncation_hint", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			bdCreate(t, bd, dir, fmt.Sprintf("Ready capped issue %d", i), "--type", "task")
		}

		cmd := exec.Command(bd, "ready", "--json", "--limit", "2")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("bd ready --json --limit 2 failed: %v\nstderr: %s\nstdout: %s", err, stderr.String(), out)
		}
		if !json.Valid(bytes.TrimSpace(out)) {
			t.Fatalf("ready JSON stdout should remain parseable, got: %s", out)
		}
		if !strings.Contains(stderr.String(), "Use --limit 0 for all") {
			t.Fatalf("expected truncation hint on stderr, got: %q", stderr.String())
		}
	})

	// ===== With Blockers =====

	t.Run("ready_excludes_blocked", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker issue", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked by blocker", "--type", "task")

		// Add blocking dependency: blocked depends on blocker
		cmd := exec.Command(bd, "dep", "add", blocked.ID, blocker.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dep add failed: %v\n%s", err, out)
		}

		cmd = exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		// The blocked issue should not appear in ready output
		if strings.Contains(string(out), "Blocked by blocker") {
			t.Errorf("blocked issue should not appear in ready output: %s", out)
		}
	})

	// ===== Exclude Label =====

	t.Run("ready_exclude_label", func(t *testing.T) {
		bdCreate(t, bd, dir, "Triage pending item", "--type", "task", "--label", "triage:pending")
		bdCreate(t, bd, dir, "Normal ready item", "--type", "task")

		cmd := exec.Command(bd, "ready", "--exclude-label", "triage:pending")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready --exclude-label failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "Triage pending item") {
			t.Errorf("triage:pending issue should not appear with --exclude-label: %s", out)
		}
		if !strings.Contains(string(out), "Normal ready item") {
			t.Errorf("normal issue should still appear with --exclude-label: %s", out)
		}
	})
}

func TestEmbeddedReadyConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rx")

	bdCreate(t, bd, dir, "Ready concurrent issue", "--type", "task")

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
			cmd := exec.Command(bd, "ready")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("ready (worker %d): %v\n%s", worker, err, out)
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
