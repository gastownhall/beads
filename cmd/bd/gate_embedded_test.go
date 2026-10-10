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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd gate %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd gate list --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
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

// createBeadGate creates a bead gate awaiting awaitID on a new task, and
// returns the gate.
func createBeadGate(t *testing.T, bd, dir, awaitID string) *types.Issue {
	t.Helper()
	task := bdCreate(t, bd, dir, "Task waiting on "+awaitID, "--type", "task")
	cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID,
		"--type", "bead", "--await-id", awaitID, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd gate create --type=bead failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var gate types.Issue
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON in output: %s", s)
	}
	if err := json.Unmarshal([]byte(s[start:]), &gate); err != nil {
		t.Fatalf("parse gate JSON: %v\n%s", err, s)
	}
	return &gate
}

// setupEmbeddedGate is the TestEmbeddedGate* tests' workspace: prefix tg,
// with "gate" registered as a custom type.
func setupEmbeddedGate(t *testing.T) (string, string) {
	t.Helper()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tg")

	// Register "gate" as a custom type so bd create --type gate works.
	store := openStore(t, beadsDir, "tg")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()
	return bd, dir
}

func TestEmbeddedGateList(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedGate(t)

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
}

func TestEmbeddedGateShowResolve(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedGate(t)

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
}

func TestEmbeddedGateWaiterCheckLifecycle(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedGate(t)

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

// setupEmbeddedGateCreate is the shared workspace of the
// TestEmbeddedGateCreate* tests ("bd gate create"): prefix gc, with "gate"
// registered as a custom type so bd gate create works.
func setupEmbeddedGateCreate(t *testing.T) (string, string) {
	t.Helper()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gc")

	// Register "gate" as a custom type so bd gate create works.
	store := openStore(t, beadsDir, "gc")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()
	return bd, dir
}

func TestEmbeddedGateCreateFlags(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedGateCreate(t)

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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd gate create --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var gate types.Issue
		s := strings.TrimSpace(stdout.String())
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd gate create --type=timer failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var gate types.Issue
		s := strings.TrimSpace(stdout.String())
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd gate create --type=gh:pr failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var gate types.Issue
		s := strings.TrimSpace(stdout.String())
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
}

func TestEmbeddedGateCreateRepoErrorsList(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedGateCreate(t)

	t.Run("create_gate_with_repo", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for cross-repo PR gate", "--type", "task")

		cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID,
			"--type", "gh:pr", "--await-id", "7173", "--repo", "gastownhall/beads", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd gate create --repo failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var gate types.Issue
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if err := json.Unmarshal([]byte(s[start:]), &gate); err != nil {
			t.Fatalf("parse gate JSON: %v\n%s", err, s)
		}
		var m map[string]string
		if err := json.Unmarshal(gate.Metadata, &m); err != nil {
			t.Fatalf("gate metadata %s is not a string map: %v", gate.Metadata, err)
		}
		if m["repo"] != "gastownhall/beads" {
			t.Errorf("gate metadata repo = %q, want gastownhall/beads", m["repo"])
		}
	})

	t.Run("repo_flag_on_human_gate_is_refused", func(t *testing.T) {
		task := bdCreate(t, bd, dir, "Task for human gate", "--type", "task")

		cmd := exec.Command(bd, "gate", "create", "--blocks", task.ID,
			"--type", "human", "--repo", "gastownhall/beads")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatalf("--repo on a human gate must be refused; got:\n%s", stdout.String())
		}
		if combined := stdout.String() + stderr.String(); !strings.Contains(combined, "--repo applies only to gh:run and gh:pr gates") {
			t.Errorf("refusal text missing from output:\n%s", combined)
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

func TestEmbeddedGateCreateReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Ready test task") {
			t.Fatalf("task should appear in ready before gate: %s", stdout.String())
		}

		// Create gate blocking the task
		bdGate(t, bd, freshDir, "create", "--blocks", task.ID)

		// Verify task no longer in ready
		cmd = exec.Command(bd, "ready")
		cmd.Dir = freshDir
		cmd.Env = bdEnv(freshDir)
		stdout.Reset()
		stderr.Reset()
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// bd ready exits 0 even with no results
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Ready test task") {
			t.Errorf("task should NOT appear in ready while gate is open: %s", stdout.String())
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
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Unblock test task") {
			t.Errorf("task should reappear in ready after gate resolved: %s", stdout.String())
		}
	})
}

func TestEmbeddedGateCreateBeadCheck(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("check_bead_gate_resolves_only_a_missing_target_it_saw", func(t *testing.T) {
		// Use a fresh db so the check sees only these bead gates
		freshDir, freshBeads, _ := bdInit(t, bd, "--prefix", "gb")
		fs := openStore(t, freshBeads, "gb")
		if err := fs.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		fs.Close()

		target := bdCreate(t, bd, freshDir, "Awaited bead", "--type", "task")
		seenGate := createBeadGate(t, bd, freshDir, target.ID)
		neverSeenGate := createBeadGate(t, bd, freshDir, "gb-nosuchbead")

		// The first check finds the target open and records that on its gate.
		out := bdGate(t, bd, freshDir, "check", "--type", "bead")
		if !strings.Contains(out, "no earlier gate check saw it") {
			t.Errorf("expected a diagnostic for the never-seen await_id: %s", out)
		}
		if got := bdShow(t, bd, freshDir, seenGate.ID); got.Status != types.StatusOpen || !strings.Contains(string(got.Metadata), beadGateSeenKey) {
			t.Fatalf("gate on an open target: status=%s metadata=%s, want open with %s", got.Status, got.Metadata, beadGateSeenKey)
		}

		// A later check resolves the gate whose target it saw and that is now
		// deleted; the await_id no check ever saw stays pending.
		bdDelete(t, bd, freshDir, target.ID, "--force")
		out = bdGate(t, bd, freshDir, "check", "--type", "bead")
		if !strings.Contains(out, "1 resolved") || !strings.Contains(out, "no longer exists") {
			t.Errorf("expected the deleted target's gate to resolve: %s", out)
		}
		if got := bdShow(t, bd, freshDir, seenGate.ID); got.Status != types.StatusClosed {
			t.Errorf("gate on a deleted target: status=%s, want closed", got.Status)
		}
		if got := bdShow(t, bd, freshDir, neverSeenGate.ID); got.Status != types.StatusOpen {
			t.Errorf("gate on a never-seen await_id: status=%s, want open", got.Status)
		}
	})
}

func TestEmbeddedGateCreateBeadRename(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("rename_keeps_a_bead_gate_on_its_target", func(t *testing.T) {
		// Renaming the awaited bead points its gate at the new ID and moves
		// the sighting along, so the next check finds the bead instead of
		// resolving the gate as though the bead had been deleted.
		for _, tt := range []struct {
			name   string
			prefix string
			rename func(t *testing.T, dir, targetID string) (newID func(id string) string)
		}{
			{
				name:   "bd rename",
				prefix: "gn",
				rename: func(t *testing.T, dir, targetID string) func(string) string {
					bdCommand(t, bd, dir, "rename", targetID, "gn-renamed")
					return func(id string) string {
						if id == targetID {
							return "gn-renamed"
						}
						return id
					}
				},
			},
			{
				name:   "bd rename-prefix",
				prefix: "gp",
				rename: func(t *testing.T, dir, _ string) func(string) string {
					bdRenamePrefix(t, bd, dir, "gq")
					return func(id string) string { return strings.Replace(id, "gp-", "gq-", 1) }
				},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				freshDir, freshBeads, _ := bdInit(t, bd, "--prefix", tt.prefix)
				fs := openStore(t, freshBeads, tt.prefix)
				if err := fs.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
					t.Fatalf("SetConfig: %v", err)
				}
				fs.Close()

				target := bdCreate(t, bd, freshDir, "Awaited bead", "--type", "task")
				gate := createBeadGate(t, bd, freshDir, target.ID)
				bdGate(t, bd, freshDir, "check", "--type", "bead")
				if got := bdShow(t, bd, freshDir, gate.ID); !beadGateTargetSeen(got) {
					t.Fatalf("the first check did not record the open target: metadata=%s", got.Metadata)
				}

				newID := tt.rename(t, freshDir, target.ID)
				gateID, targetID := newID(gate.ID), newID(target.ID)
				got := bdShow(t, bd, freshDir, gateID)
				if got.AwaitID != targetID || !beadGateTargetSeen(got) {
					t.Fatalf("gate after the rename: await_id=%q metadata=%s, want %q recorded as seen", got.AwaitID, got.Metadata, targetID)
				}

				out := bdGate(t, bd, freshDir, "check", "--type", "bead")
				if got := bdShow(t, bd, freshDir, gateID); got.Status != types.StatusOpen {
					t.Fatalf("gate on a renamed open bead: status=%s, want open\n%s", got.Status, out)
				}

				bdClose(t, bd, freshDir, targetID)
				out = bdGate(t, bd, freshDir, "check", "--type", "bead")
				if got := bdShow(t, bd, freshDir, gateID); got.Status != types.StatusClosed {
					t.Errorf("gate on a renamed bead that closed: status=%s, want closed\n%s", got.Status, out)
				}
				if !strings.Contains(out, "bead "+targetID+" closed") {
					t.Errorf("expected the gate to resolve on the closed bead: %s", out)
				}
			})
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
			out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--silent", title, "--type", "gate")
			if err != nil {
				r.err = fmt.Errorf("create gate: %v\n%s", err, out)
				results[worker] = r
				return
			}
			gateID := strings.TrimSpace(string(out))

			// Add waiter
			cmd := exec.Command(bd, "gate", "add-waiter", gateID, fmt.Sprintf("agent-%d", worker))
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

// TestEmbeddedGateListScoped is the integration regression for the bug where
// `bd gate list <issue-id>` silently ignored the argument and returned the
// DB-wide gate list. It asserts the scoped list contains only the named issue's
// own gate and never leaks an unrelated issue's gate.
func TestEmbeddedGateListScoped(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gls")

	// Register "gate" as a custom type so bd gate create works.
	store := openStore(t, beadsDir, "gls")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()

	taskA := bdCreate(t, bd, dir, "Task A", "--type", "task")
	taskB := bdCreate(t, bd, dir, "Task B", "--type", "task")

	// No gates yet: both routes emit an empty JSON array, never `null`.
	if out := strings.TrimSpace(bdGate(t, bd, dir, "list", "--json")); out != "[]" {
		t.Fatalf("expected [] from DB-wide gate list with no gates, got: %s", out)
	}
	if out := strings.TrimSpace(bdGate(t, bd, dir, "list", taskA.ID, "--json")); out != "[]" {
		t.Fatalf("expected [] from gate list scoped to %s with no gates, got: %s", taskA.ID, out)
	}

	bdGate(t, bd, dir, "create", "--blocks", taskA.ID, "--type", "gh:pr", "--await-id", "111")
	bdGate(t, bd, dir, "create", "--blocks", taskB.ID, "--type", "gh:pr", "--await-id", "222")

	// DB-wide (no arg) still lists every gate.
	if all := bdGateListJSON(t, bd, dir); len(all) != 2 {
		t.Fatalf("expected 2 gates DB-wide, got %d: %v", len(all), all)
	}

	// Scoped to A: exactly A's gate, and never B's.
	scoped := bdGateListJSON(t, bd, dir, taskA.ID)
	if len(scoped) != 1 {
		t.Fatalf("expected exactly 1 gate scoped to %s, got %d: %v", taskA.ID, len(scoped), scoped)
	}
	if got := scoped[0]["await_id"]; got != "111" {
		t.Fatalf("expected await_id=111 scoped to %s, got %v", taskA.ID, got)
	}
	for _, g := range scoped {
		if g["await_id"] == "222" {
			t.Fatalf("gate list %s leaked unrelated gate 222: %v", taskA.ID, scoped)
		}
	}

	// Scoped to B: exactly B's gate.
	scopedB := bdGateListJSON(t, bd, dir, taskB.ID)
	if len(scopedB) != 1 || scopedB[0]["await_id"] != "222" {
		t.Fatalf("expected exactly gate 222 scoped to %s, got %v", taskB.ID, scopedB)
	}

	// Unknown issue id is a clean error, not a DB-wide dump.
	out := bdGateFail(t, bd, dir, "list", "gls-nope")
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected 'not found' error for unknown issue, got: %s", out)
	}
}
