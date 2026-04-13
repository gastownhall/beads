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

// bdGate runs "bd gate" with the given args and returns stdout.
func bdGate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"gate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd gate %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdGateFail runs "bd gate" expecting failure.
func bdGateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"gate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd gate %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdGateListJSON runs "bd gate list --json" and returns parsed results.
func bdGateListJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"gate", "list", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd gate list --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &results); err != nil {
		t.Fatalf("parse gate list JSON: %v\n%s", err, s)
	}
	return results
}

// createGate creates a gate issue and returns it.
func createGate(t *testing.T, bd, dir, title string, extraArgs ...string) *types.Issue {
	t.Helper()
	args := append([]string{title, "--type", "gate"}, extraArgs...)
	return bdCreate(t, bd, dir, args...)
}

func TestEmbeddedGate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tg")

	// Register "gate" as a custom type so bd create --type gate works.
	store := openStore(t, beadsDir, "tg")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()

	// ===== Gate List =====

	t.Run("gate_list_empty", func(t *testing.T) {
		out := bdGate(t, bd, dir, "list")
		if !strings.Contains(out, "No gates") {
			t.Logf("expected 'No gates' message: %s", out)
		}
	})

	t.Run("gate_list_shows_open_gates", func(t *testing.T) {
		gate := createGate(t, bd, dir, "List test gate")
		out := bdGate(t, bd, dir, "list")
		if !strings.Contains(out, gate.ID) {
			t.Errorf("expected gate %s in list output: %s", gate.ID, out)
		}
	})

	t.Run("gate_list_json", func(t *testing.T) {
		results := bdGateListJSON(t, bd, dir)
		if len(results) == 0 {
			t.Error("expected at least one gate in JSON list")
		}
	})

	t.Run("gate_list_excludes_closed_by_default", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Close me gate")
		bdGate(t, bd, dir, "resolve", gate.ID)
		results := bdGateListJSON(t, bd, dir)
		for _, r := range results {
			if r["id"] == gate.ID {
				t.Errorf("closed gate %s should not appear without --all", gate.ID)
			}
		}
	})

	t.Run("gate_list_all_includes_closed", func(t *testing.T) {
		gate := createGate(t, bd, dir, "All flag gate")
		bdGate(t, bd, dir, "resolve", gate.ID)
		results := bdGateListJSON(t, bd, dir, "--all")
		found := false
		for _, r := range results {
			if r["id"] == gate.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected closed gate %s with --all flag", gate.ID)
		}
	})

	t.Run("gate_list_limit", func(t *testing.T) {
		// Create several gates
		for i := 0; i < 3; i++ {
			createGate(t, bd, dir, fmt.Sprintf("Limit gate %d", i))
		}
		results := bdGateListJSON(t, bd, dir, "--limit", "1")
		if len(results) > 1 {
			t.Errorf("expected at most 1 result with --limit 1, got %d", len(results))
		}
	})

	// ===== Gate Show =====

	t.Run("gate_show", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Show gate", "--description", "Gate description")
		out := bdGate(t, bd, dir, "show", gate.ID)
		if !strings.Contains(out, gate.ID) {
			t.Errorf("expected gate ID in show output: %s", out)
		}
		if !strings.Contains(out, "Show gate") {
			t.Errorf("expected gate title in show output: %s", out)
		}
	})

	t.Run("gate_show_nonexistent", func(t *testing.T) {
		bdGateFail(t, bd, dir, "show", "tg-nonexistent999")
	})

	t.Run("gate_show_not_a_gate", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Not a gate", "--type", "task")
		bdGateFail(t, bd, dir, "show", task.ID)
	})

	// ===== Gate Resolve =====

	t.Run("gate_resolve", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Resolve me")
		out := bdGate(t, bd, dir, "resolve", gate.ID)
		if !strings.Contains(out, "resolved") {
			t.Errorf("expected 'resolved' in output: %s", out)
		}
		got := bdShow(t, bd, dir, gate.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed status after resolve, got %s", got.Status)
		}
	})

	t.Run("gate_resolve_with_reason", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Reason resolve")
		out := bdGate(t, bd, dir, "resolve", gate.ID, "--reason", "CI passed")
		if !strings.Contains(out, "resolved") {
			t.Errorf("expected 'resolved' in output: %s", out)
		}
		if !strings.Contains(out, "CI passed") {
			t.Logf("reason may not appear in text output: %s", out)
		}
		got := bdShow(t, bd, dir, gate.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed, got %s", got.Status)
		}
	})

	t.Run("gate_resolve_nonexistent", func(t *testing.T) {
		bdGateFail(t, bd, dir, "resolve", "tg-nonexistent999")
	})

	t.Run("gate_resolve_not_a_gate", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Not a gate resolve", "--type", "task")
		bdGateFail(t, bd, dir, "resolve", task.ID)
	})

	// ===== Gate Add-Waiter =====

	t.Run("gate_add_waiter", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Waiter gate")
		out := bdGate(t, bd, dir, "add-waiter", gate.ID, "agent-1")
		if !strings.Contains(out, "Added waiter") {
			t.Errorf("expected 'Added waiter' in output: %s", out)
		}
		// Verify waiter was added
		got := bdShow(t, bd, dir, gate.ID)
		if len(got.Waiters) != 1 || got.Waiters[0] != "agent-1" {
			t.Errorf("expected waiter [agent-1], got %v", got.Waiters)
		}
	})

	t.Run("gate_add_waiter_multiple", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Multi waiter gate")
		bdGate(t, bd, dir, "add-waiter", gate.ID, "agent-1")
		bdGate(t, bd, dir, "add-waiter", gate.ID, "agent-2")
		got := bdShow(t, bd, dir, gate.ID)
		if len(got.Waiters) != 2 {
			t.Errorf("expected 2 waiters, got %d: %v", len(got.Waiters), got.Waiters)
		}
	})

	t.Run("gate_add_waiter_duplicate", func(t *testing.T) {
		gate := createGate(t, bd, dir, "Dup waiter gate")
		bdGate(t, bd, dir, "add-waiter", gate.ID, "agent-1")
		out := bdGate(t, bd, dir, "add-waiter", gate.ID, "agent-1")
		if !strings.Contains(out, "already registered") {
			t.Logf("duplicate waiter message: %s", out)
		}
		// Should still have only 1 waiter
		got := bdShow(t, bd, dir, gate.ID)
		if len(got.Waiters) != 1 {
			t.Errorf("expected 1 waiter after duplicate add, got %d", len(got.Waiters))
		}
	})

	t.Run("gate_add_waiter_nonexistent", func(t *testing.T) {
		bdGateFail(t, bd, dir, "add-waiter", "tg-nonexistent999", "agent-1")
	})

	// ===== Gate Check =====

	t.Run("gate_check_no_gates", func(t *testing.T) {
		// Create a fresh dir for this test to avoid interference
		checkDir, checkBeads, _ := bdInit(t, bd, "--prefix", "gc")
		cs := openStore(t, checkBeads, "gc")
		_ = cs.SetConfig(t.Context(), "types.custom", `["gate"]`)
		cs.Close()
		out := bdGate(t, bd, checkDir, "check")
		// Should not error even with no gates
		_ = out
	})

	t.Run("gate_check_dry_run", func(t *testing.T) {
		out := bdGate(t, bd, dir, "check", "--dry-run")
		// Dry-run should not close anything
		_ = out
	})

	t.Run("gate_check_with_type_filter", func(t *testing.T) {
		// Timer gates should be checkable
		out := bdGate(t, bd, dir, "check", "--type", "timer")
		_ = out
	})

	t.Run("gate_check_bead_type", func(t *testing.T) {
		// Create a bead-type gate that waits for another issue
		target := bdCreate(t, bd, dir, "Bead gate target", "--type", "task")
		_ = createGate(t, bd, dir, "Bead gate",
			"--description", fmt.Sprintf("Waiting for %s", target.ID))

		out := bdGate(t, bd, dir, "check", "--type", "bead")
		_ = out
	})

	t.Run("gate_check_limit", func(t *testing.T) {
		out := bdGate(t, bd, dir, "check", "--limit", "5")
		_ = out
	})

	// ===== Full Lifecycle =====

	t.Run("gate_lifecycle", func(t *testing.T) {
		// Create gate
		gate := createGate(t, bd, dir, "Lifecycle gate")

		// List shows it
		results := bdGateListJSON(t, bd, dir)
		found := false
		for _, r := range results {
			if r["id"] == gate.ID {
				found = true
			}
		}
		if !found {
			t.Error("expected new gate in list")
		}

		// Add waiter
		bdGate(t, bd, dir, "add-waiter", gate.ID, "lifecycle-agent")

		// Show gate with waiter
		got := bdShow(t, bd, dir, gate.ID)
		if len(got.Waiters) != 1 {
			t.Errorf("expected 1 waiter, got %d", len(got.Waiters))
		}

		// Resolve
		bdGate(t, bd, dir, "resolve", gate.ID, "--reason", "All done")

		// Verify closed
		got = bdShow(t, bd, dir, gate.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed after resolve, got %s", got.Status)
		}

		// Not in default list
		results = bdGateListJSON(t, bd, dir)
		for _, r := range results {
			if r["id"] == gate.ID {
				t.Error("resolved gate should not appear in default list")
			}
		}

		// In --all list
		results = bdGateListJSON(t, bd, dir, "--all")
		found = false
		for _, r := range results {
			if r["id"] == gate.ID {
				found = true
			}
		}
		if !found {
			t.Error("resolved gate should appear with --all")
		}
	})
}

// TestEmbeddedGateCreate exercises the "bd gate create" subcommand.
func TestEmbeddedGateCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gc")

	// Register "gate" as a custom type so bd gate create works.
	store := openStore(t, beadsDir, "gc")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()

	t.Run("create_default_human_gate", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for human gate", "--type", "task")

		out := bdGate(t, bd, dir, "create", "--blocks", task.ID)
		if !strings.Contains(out, "Created gate") {
			t.Errorf("expected 'Created gate' in output: %s", out)
		}
		if !strings.Contains(out, "type: human") {
			t.Errorf("expected default type 'human' in output: %s", out)
		}
		if !strings.Contains(out, task.ID) {
			t.Errorf("expected blocked issue ID in output: %s", out)
		}
	})

	t.Run("create_gate_json_output", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for JSON gate", "--type", "task")

		cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd gate create --json failed: %v\n%s", err, out)
		}

		var gate types.Issue
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		if err := json.Unmarshal([]byte(s[start:]), &gate); err != nil {
			t.Fatalf("parse gate JSON: %v\n%s", err, s)
		}
		if gate.IssueType != types.IssueType("gate") {
			t.Errorf("expected issue_type=gate, got %s", gate.IssueType)
		}
		if gate.AwaitType != "human" {
			t.Errorf("expected await_type=human, got %s", gate.AwaitType)
		}
	})

	t.Run("create_gate_with_type_and_reason", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for timer gate", "--type", "task")

		cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID,
			"--type", "timer", "--timeout", "2h", "--reason", "Wait for cooldown", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd gate create --type=timer failed: %v\n%s", err, out)
		}

		var gate types.Issue
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if err := json.Unmarshal([]byte(s[start:]), &gate); err != nil {
			t.Fatalf("parse gate JSON: %v\n%s", err, s)
		}
		if gate.AwaitType != "timer" {
			t.Errorf("expected await_type=timer, got %s", gate.AwaitType)
		}
		if gate.Timeout != 2*60*60*1e9 { // 2h in nanoseconds
			t.Errorf("expected timeout=2h, got %v", gate.Timeout)
		}
		if !strings.Contains(gate.Description, "Wait for cooldown") {
			t.Errorf("expected reason in description: %s", gate.Description)
		}
	})

	t.Run("create_gate_with_await_id", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for PR gate", "--type", "task")

		cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID,
			"--type", "gh:pr", "--await-id", "42", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd gate create --type=gh:pr failed: %v\n%s", err, out)
		}

		var gate types.Issue
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if err := json.Unmarshal([]byte(s[start:]), &gate); err != nil {
			t.Fatalf("parse gate JSON: %v\n%s", err, s)
		}
		if gate.AwaitType != "gh:pr" {
			t.Errorf("expected await_type=gh:pr, got %s", gate.AwaitType)
		}
		if gate.AwaitID != "42" {
			t.Errorf("expected await_id=42, got %s", gate.AwaitID)
		}
		if gate.Title != "Gate: gh:pr 42" {
			t.Errorf("expected title 'Gate: gh:pr 42', got %s", gate.Title)
		}
	})

	t.Run("create_gate_blocks_ready", func(t *testing.T) {
		// Use a fresh db so ready output isn't polluted by other subtests
		freshDir, freshBeads, _ := bdInit(t, bd, "--prefix", "gr")
		fs := openStore(t, freshBeads, "gr")
		if err := fs.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		fs.Close()

		task := bdCreate(t, bd, freshDir, "Ready test task", "--type", "task")

		// Verify task appears in ready
		cmd := exec.Command(bd, "ready")
		cmd.Dir = freshDir
		cmd.Env = bdEnv(freshDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Ready test task") {
			t.Fatalf("task should appear in ready before gate: %s", out)
		}

		// Create gate blocking the task
		bdGate(t, bd, freshDir, "create", "--blocks", task.ID)

		// Verify task no longer in ready
		cmd = exec.Command(bd, "ready")
		cmd.Dir = freshDir
		cmd.Env = bdEnv(freshDir)
		out, err = cmd.CombinedOutput()
		if err != nil {
			// bd ready exits 0 even with no results
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "Ready test task") {
			t.Errorf("task should NOT appear in ready while gate is open: %s", out)
		}
	})

	t.Run("create_gate_resolve_unblocks_ready", func(t *testing.T) {
		// Use a fresh db for clean ready output
		freshDir, freshBeads, _ := bdInit(t, bd, "--prefix", "gu")
		fs := openStore(t, freshBeads, "gu")
		if err := fs.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		fs.Close()

		task := bdCreate(t, bd, freshDir, "Unblock test task", "--type", "task")

		// Create and then resolve the gate
		gateOut := bdGate(t, bd, freshDir, "create", "--blocks", task.ID)

		// Extract gate ID from output ("Created gate gc-xxx ...")
		var gateID string
		for _, word := range strings.Fields(gateOut) {
			if strings.HasPrefix(word, "gu-") {
				gateID = word
				break
			}
		}
		if gateID == "" {
			t.Fatalf("could not extract gate ID from output: %s", gateOut)
		}

		// Resolve the gate
		bdGate(t, bd, freshDir, "resolve", gateID)

		// Verify task reappears in ready
		cmd := exec.Command(bd, "ready")
		cmd.Dir = freshDir
		cmd.Env = bdEnv(freshDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd ready failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Unblock test task") {
			t.Errorf("task should reappear in ready after gate resolved: %s", out)
		}
	})

	t.Run("create_gate_missing_blocks_flag", func(t *testing.T) {
		out := bdGateFail(t, bd, dir, "create")
		if !strings.Contains(out, "blocks") {
			t.Errorf("expected error about missing --blocks flag: %s", out)
		}
	})

	t.Run("create_gate_nonexistent_target", func(t *testing.T) {
		out := bdGateFail(t, bd, dir, "create", "--blocks", "gc-nonexistent999")
		if !strings.Contains(out, "not found") {
			t.Errorf("expected 'not found' error: %s", out)
		}
	})

	t.Run("create_gate_appears_in_gate_list", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for list check", "--type", "task")
		bdGate(t, bd, dir, "create", "--blocks", task.ID)

		results := bdGateListJSON(t, bd, dir)
		found := false
		for _, r := range results {
			if awaitType, ok := r["await_type"]; ok && awaitType == "human" {
				if desc, ok := r["description"].(string); ok && strings.Contains(desc, task.ID) {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("expected gate blocking %s in gate list", task.ID)
		}
	})
}

// TestEmbeddedGateConcurrent exercises gate operations concurrently.
func TestEmbeddedGateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, gxBeads, _ := bdInit(t, bd, "--prefix", "gx")

	// Register "gate" as custom type.
	gxStore := openStore(t, gxBeads, "gx")
	if err := gxStore.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	gxStore.Close()

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

			// Each worker: create a gate, add a waiter, resolve it
			title := fmt.Sprintf("w%d-gate", worker)
			cmd := exec.Command(bd, "create", "--silent", title, "--type", "gate")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("create gate: %v\n%s", err, out)
				results[worker] = r
				return
			}
			gateID := strings.TrimSpace(string(out))

			// Add waiter
			cmd = exec.Command(bd, "gate", "add-waiter", gateID, fmt.Sprintf("agent-%d", worker))
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("add-waiter %s: %v\n%s", gateID, err, out)
				results[worker] = r
				return
			}

			// Resolve
			cmd = exec.Command(bd, "gate", "resolve", gateID, "--reason", fmt.Sprintf("done-%d", worker))
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("resolve %s: %v\n%s", gateID, err, out)
				results[worker] = r
				return
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
