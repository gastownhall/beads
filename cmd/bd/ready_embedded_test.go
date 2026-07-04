//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")
	bdCreate(t, bd, dir, "Ready test issue", "--type", "task")

	// ===== Default =====

	t.Run("ready_includes_open_issue_with_zero_dependencies", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "GH3268 zero dependency ready issue", "--type", "task", "--label", "gh3268-zero-deps")

		cmd := exec.Command(bd, "ready", "--json", "--label", "gh3268-zero-deps")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var ready []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &ready); err != nil {
			t.Fatalf("parse ready JSON: %v\n%s", err, stdout.String())
		}
		if len(ready) != 1 {
			t.Fatalf("ready count = %d, want 1: %s", len(ready), stdout.String())
		}
		if ready[0].ID != issue.ID {
			t.Fatalf("ready ID = %s, want %s", ready[0].ID, issue.ID)
		}
		if ready[0].DependencyCount != 0 {
			t.Fatalf("dependency_count = %d, want 0", ready[0].DependencyCount)
		}
	})

	t.Run("ready_default", func(t *testing.T) {
		cmd := exec.Command(bd, "ready")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Ready test issue") {
			t.Errorf("expected issue in ready output: %s", stdout.String())
		}
	})

	// ===== --json =====

	t.Run("ready_json", func(t *testing.T) {
		cmd := exec.Command(bd, "ready", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
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

	t.Run("ready_claim_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Ready claim json", "--type", "task", "--label", "ready-claim-json")

		cmd := exec.Command(bd, "ready", "--claim", "--json", "--label", "missing-label")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready --claim --json with no matches failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		var empty []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &empty); err != nil {
			t.Fatalf("parse empty claim JSON: %v\n%s", err, stdout.String())
		}
		if len(empty) != 0 {
			t.Fatalf("expected no claimed issues for unmatched label, got %d", len(empty))
		}

		cmd = exec.Command(bd, "ready", "--claim", "--json", "--label", "ready-claim-json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout.Reset()
		stderr.Reset()
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("bd ready --claim --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		var claimed []types.IssueWithCounts
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &claimed); err != nil {
			t.Fatalf("parse claim JSON: %v\n%s", err, stdout.String())
		}
		if len(claimed) != 1 {
			t.Fatalf("expected one claimed issue, got %d: %s", len(claimed), stdout.String())
		}
		if claimed[0].ID != issue.ID {
			t.Fatalf("claimed ID = %s, want %s", claimed[0].ID, issue.ID)
		}
		if claimed[0].Status != types.StatusInProgress {
			t.Fatalf("claimed status = %s, want %s", claimed[0].Status, types.StatusInProgress)
		}
		if claimed[0].Assignee == "" {
			t.Fatal("expected claimed issue to have assignee")
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// The blocked issue should not appear in ready output
		if strings.Contains(stdout.String(), "Blocked by blocker") {
			t.Errorf("blocked issue should not appear in ready output: %s", stdout.String())
		}
	})

	// ===== Exclude Label =====

	t.Run("ready_exclude_label", func(t *testing.T) {
		bdCreate(t, bd, dir, "Triage pending item", "--type", "task", "--label", "triage:pending")
		bdCreate(t, bd, dir, "Normal ready item", "--type", "task")

		cmd := exec.Command(bd, "ready", "--exclude-label", "triage:pending")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready --exclude-label failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Triage pending item") {
			t.Errorf("triage:pending issue should not appear with --exclude-label: %s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Normal ready item") {
			t.Errorf("normal issue should still appear with --exclude-label: %s", stdout.String())
		}
	})

	// ===== -C flag =====

	t.Run("ready_with_C_flag", func(t *testing.T) {
		// Run bd ready from a different directory using -C to point at the beads project
		tmpDir := t.TempDir()
		cmd := exec.Command(bd, "-C", dir, "ready")
		cmd.Dir = tmpDir // Run from a directory with no .beads/
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd -C %s ready failed: %v\nstdout:\n%s\nstderr:\n%s", dir, err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Ready test issue") {
			t.Errorf("expected issue in ready -C output: %s", stdout.String())
		}
	})

	t.Run("ready_with_C_flag_invalid_path", func(t *testing.T) {
		tmpDir := t.TempDir()
		cmd := exec.Command(bd, "-C", filepath.Join(tmpDir, "missing"), "ready")
		cmd.Dir = tmpDir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("bd -C missing ready succeeded unexpectedly:\n%s", out)
		}
		if !strings.Contains(string(out), "cannot use -C directory") {
			t.Errorf("expected invalid -C path error, got: %s", out)
		}
	})

	t.Run("ready_with_C_flag_does_not_leak_cwd", func(t *testing.T) {
		// Verify that -C does not permanently mutate the process cwd.
		// Two sequential invocations from the same tmpDir: the first uses -C to
		// reach the project; the second omits -C and must fail (no .beads/ in tmpDir),
		// proving BEADS_DIR was not leaked into the test process environment.
		tmpDir := t.TempDir()
		env := bdEnv(dir) // strips all BEADS_* vars

		cmd1 := exec.Command(bd, "-C", dir, "ready")
		cmd1.Dir = tmpDir
		cmd1.Env = env
		if out, err := cmd1.CombinedOutput(); err != nil {
			t.Fatalf("first bd -C ready failed: %v\n%s", err, out)
		}

		cmd2 := exec.Command(bd, "ready")
		cmd2.Dir = tmpDir
		cmd2.Env = env // same env — BEADS_DIR must not have leaked
		out2, err2 := cmd2.CombinedOutput()
		if err2 == nil {
			t.Fatalf("second bd ready (no -C) should have failed in tmpDir, got: %s", out2)
		}
	})

	t.Run("offset_rejected_outside_proxied", func(t *testing.T) {
		cmd := exec.Command(bd, "ready", "--offset", "1")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("bd ready --offset 1 in embedded mode should have failed, got: %s", out)
		}
		if !strings.Contains(string(out), "--offset is only supported under --proxied-server") {
			t.Errorf("expected '--offset is only supported under --proxied-server' error, got: %s", out)
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

func TestEmbeddedReadyClaimConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rc")

	issue := bdCreate(t, bd, dir, "Ready claim concurrent issue", "--type", "task")

	const numWorkers = 8
	type workerResult struct {
		worker  int
		claimed []types.IssueWithCounts
		err     error
		out     string
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			out, err := bdRunWithFlockRetry(t, bd, dir, "ready", "--claim", "--json")
			r.out = string(out)
			if err != nil {
				r.err = fmt.Errorf("ready --claim (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
			}
			if err := json.Unmarshal(bytes.TrimSpace(out), &r.claimed); err != nil {
				r.err = fmt.Errorf("parse ready --claim JSON (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()

	claimCount := 0
	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
			continue
		}
		if len(r.claimed) > 1 {
			t.Errorf("worker %d claimed %d issues: %s", r.worker, len(r.claimed), r.out)
			continue
		}
		if len(r.claimed) == 1 {
			claimCount++
			if r.claimed[0].ID != issue.ID {
				t.Errorf("worker %d claimed %s, want %s", r.worker, r.claimed[0].ID, issue.ID)
			}
		}
	}
	if claimCount != 1 {
		t.Fatalf("expected exactly one successful claim, got %d", claimCount)
	}
	got := bdShow(t, bd, dir, issue.ID)
	if got.Status != types.StatusInProgress {
		t.Fatalf("final status = %s, want %s", got.Status, types.StatusInProgress)
	}
	if got.Assignee == "" {
		t.Fatal("expected final assignee to be set")
	}
}
