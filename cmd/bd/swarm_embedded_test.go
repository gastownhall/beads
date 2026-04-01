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

// bdSwarm runs "bd swarm" with the given args and returns raw stdout.
func bdSwarm(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"swarm"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd swarm %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdSwarmFail runs "bd swarm" expecting failure.
func bdSwarmFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"swarm"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd swarm %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdSwarmJSON runs "bd swarm" with --json and parses the result as a map.
func bdSwarmJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"swarm"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd swarm --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		t.Fatalf("no JSON in swarm output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse swarm JSON: %v\n%s", err, s)
	}
	return m
}

// createSwarmableEpic creates an epic with 3 children forming a DAG suitable for swarming.
func createSwarmableEpic(t *testing.T, bd, dir, prefix string) (epicID string, childIDs []string) {
	t.Helper()
	epic := bdCreate(t, bd, dir, prefix+" epic", "--type", "epic")
	c1 := bdCreate(t, bd, dir, prefix+" child 1", "--type", "task")
	c2 := bdCreate(t, bd, dir, prefix+" child 2", "--type", "task")
	c3 := bdCreate(t, bd, dir, prefix+" child 3", "--type", "task")
	bdDep(t, bd, dir, "add", c1.ID, epic.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", c2.ID, epic.ID, "--type", "parent-child")
	bdDep(t, bd, dir, "add", c3.ID, epic.ID, "--type", "parent-child")
	// c3 depends on c1 (serial constraint)
	bdDep(t, bd, dir, "add", c3.ID, c1.ID)
	return epic.ID, []string{c1.ID, c2.ID, c3.ID}
}

func TestEmbeddedSwarm(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sw")

	epicID, _ := createSwarmableEpic(t, bd, dir, "Swarm")

	// ===== swarm validate =====

	t.Run("validate_epic", func(t *testing.T) {
		out := bdSwarm(t, bd, dir, "validate", epicID)
		if !strings.Contains(out, epicID) {
			t.Errorf("expected epic ID in validate output: %s", out)
		}
	})

	t.Run("validate_verbose", func(t *testing.T) {
		m := bdSwarmJSON(t, bd, dir, "validate", epicID, "--verbose")
		if _, ok := m["issues"]; !ok {
			t.Error("expected 'issues' key with --verbose")
		}
	})

	t.Run("validate_json", func(t *testing.T) {
		m := bdSwarmJSON(t, bd, dir, "validate", epicID)
		if _, ok := m["swarmable"]; !ok {
			t.Error("expected 'swarmable' key in JSON")
		}
		if _, ok := m["total_issues"]; !ok {
			t.Error("expected 'total_issues' key in JSON")
		}
	})

	t.Run("validate_non_swarmable", func(t *testing.T) {
		// Epic with no children — validate succeeds but warns
		emptyEpic := bdCreate(t, bd, dir, "Empty epic", "--type", "epic")
		out := bdSwarm(t, bd, dir, "validate", emptyEpic.ID)
		if !strings.Contains(out, "no children") && !strings.Contains(out, "0") {
			t.Errorf("expected warning about no children: %s", out)
		}
	})

	// ===== swarm create =====

	t.Run("create_swarm", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "Create")
		out := bdSwarm(t, bd, dir, "create", eID)
		if !strings.Contains(out, "Created swarm molecule") {
			t.Errorf("expected 'Created swarm molecule': %s", out)
		}
	})

	t.Run("create_auto_wrap_single", func(t *testing.T) {
		// Creating swarm on a non-epic should auto-wrap
		task := bdCreate(t, bd, dir, "Auto wrap task", "--type", "task")
		out := bdSwarm(t, bd, dir, "create", task.ID)
		if !strings.Contains(out, "Created swarm molecule") && !strings.Contains(out, "Auto-wrapping") {
			t.Errorf("expected auto-wrap or creation: %s", out)
		}
	})

	t.Run("create_coordinator", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "Coord")
		m := bdSwarmJSON(t, bd, dir, "create", eID, "--coordinator", "alice")
		if m["coordinator"] != "alice" {
			t.Errorf("expected coordinator=alice, got %v", m["coordinator"])
		}
	})

	t.Run("create_force", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "Force")
		bdSwarm(t, bd, dir, "create", eID)
		// Second create without --force should fail
		bdSwarmFail(t, bd, dir, "create", eID)
		// With --force should succeed
		bdSwarm(t, bd, dir, "create", eID, "--force")
	})

	t.Run("create_json_output", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "JSON")
		m := bdSwarmJSON(t, bd, dir, "create", eID)
		if _, ok := m["swarm_id"]; !ok {
			t.Error("expected 'swarm_id' in JSON")
		}
		if _, ok := m["epic_id"]; !ok {
			t.Error("expected 'epic_id' in JSON")
		}
	})

	t.Run("create_error_existing", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "Exist")
		bdSwarm(t, bd, dir, "create", eID)
		bdSwarmFail(t, bd, dir, "create", eID)
	})

	// ===== swarm status =====

	t.Run("status_shows_progress", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "Status")
		bdSwarm(t, bd, dir, "create", eID)
		out := bdSwarm(t, bd, dir, "status", eID)
		if len(out) == 0 {
			t.Error("expected non-empty status output")
		}
	})

	t.Run("status_json", func(t *testing.T) {
		eID, _ := createSwarmableEpic(t, bd, dir, "StatJSON")
		bdSwarm(t, bd, dir, "create", eID)
		m := bdSwarmJSON(t, bd, dir, "status", eID)
		if len(m) == 0 {
			t.Error("expected non-empty JSON status")
		}
	})

	// ===== swarm list =====

	t.Run("list_shows_swarms", func(t *testing.T) {
		out := bdSwarm(t, bd, dir, "list")
		// We've created several swarms above
		if len(out) == 0 {
			t.Error("expected non-empty list output")
		}
	})

	t.Run("list_json", func(t *testing.T) {
		fullArgs := []string{"swarm", "list", "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("swarm list --json failed: %v\n%s", err, out)
		}
		// Should be valid JSON
		s := strings.TrimSpace(string(out))
		if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
			t.Errorf("expected JSON output: %s", s)
		}
	})

	t.Run("list_empty", func(t *testing.T) {
		dir2, _, _ := bdInit(t, bd, "--prefix", "sw2")
		out := bdSwarm(t, bd, dir2, "list")
		if !strings.Contains(out, "No swarm") && !strings.Contains(out, "no swarm") && len(strings.TrimSpace(out)) > 0 {
			// Just verify no crash — empty list message varies
		}
	})
}

// TestEmbeddedSwarmConcurrent exercises swarm operations concurrently.
func TestEmbeddedSwarmConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "swc")

	// Create several swarmable epics.
	var epicIDs []string
	for i := 0; i < 4; i++ {
		eID, _ := createSwarmableEpic(t, bd, dir, fmt.Sprintf("Conc%d", i))
		epicIDs = append(epicIDs, eID)
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

			eID := epicIDs[worker%len(epicIDs)]
			queries := [][]string{
				{"validate", eID, "--json"},
				{"validate", eID, "--verbose", "--json"},
				{"validate", eID, "--json"},
				{"list", "--json"},
				{"validate", eID, "--json"},
				{"list", "--json"},
				{"validate", eID, "--json"},
				{"validate", eID, "--verbose", "--json"},
			}
			q := queries[worker%len(queries)]

			args := append([]string{"swarm"}, q...)
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
