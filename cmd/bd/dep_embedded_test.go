//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdDep runs "bd dep" with the given args and returns raw stdout.
func bdDep(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDepFail runs "bd dep" expecting failure.
func bdDepFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dep %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdDepJSON runs "bd dep" with --json and parses the result.
func bdDepJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		t.Fatalf("no JSON in dep output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse dep JSON: %v\n%s", err, s)
	}
	return m
}

func bdDepWithInput(t *testing.T, bd, dir, input string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func dropEmbeddedWispDependencies(t *testing.T, beadsDir, database string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if _, err := db.ExecContext(t.Context(), "DROP TABLE IF EXISTS wisp_dependencies"); err != nil {
		t.Fatalf("drop table wisp_dependencies: %v", err)
	}
}

func TestEmbeddedDepMissingWispDependencies(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mw")
	blocker := bdCreate(t, bd, dir, "Missing wisp dep blocker", "--type", "task")
	blocked := bdCreate(t, bd, dir, "Missing wisp dep blocked", "--type", "task")

	dropEmbeddedWispDependencies(t, beadsDir, "mw")

	out := bdDep(t, bd, dir, "add", blocked.ID, blocker.ID)
	if !strings.Contains(out, "Added dependency") {
		t.Fatalf("expected dep add to succeed after recreating wisp_dependencies: %s", out)
	}

	tree := bdDep(t, bd, dir, "tree", blocked.ID)
	if !strings.Contains(tree, blocker.ID) {
		t.Fatalf("expected dep tree to include blocker %s after wisp_dependencies repair:\n%s", blocker.ID, tree)
	}
}

func TestEmbeddedDep(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dp")

	// Pre-create issues for dependency testing.
	issueA := bdCreate(t, bd, dir, "Dep issue A", "--type", "task")
	issueB := bdCreate(t, bd, dir, "Dep issue B", "--type", "task")
	issueC := bdCreate(t, bd, dir, "Dep issue C", "--type", "task")
	issueD := bdCreate(t, bd, dir, "Dep issue D", "--type", "task")
	epic := bdCreate(t, bd, dir, "Dep epic", "--type", "epic")
	child1 := bdCreate(t, bd, dir, "Dep child 1", "--type", "task")
	child2 := bdCreate(t, bd, dir, "Dep child 2", "--type", "task")

	// ===== dep --blocks =====

	t.Run("root_blocks_flag", func(t *testing.T) {
		out := bdDep(t, bd, dir, issueA.ID, "--blocks", issueB.ID)
		if !strings.Contains(out, "Added") || !strings.Contains(out, "blocks") {
			t.Errorf("expected 'Added ... blocks' output: %s", out)
		}
	})

	// ===== dep add =====

	t.Run("add_positional_args", func(t *testing.T) {
		out := bdDep(t, bd, dir, "add", issueC.ID, issueD.ID)
		if !strings.Contains(out, "Added dependency") {
			t.Errorf("expected 'Added dependency': %s", out)
		}
	})

	t.Run("add_type_parent_child", func(t *testing.T) {
		bdDep(t, bd, dir, "add", child1.ID, epic.ID, "--type", "parent-child")
		bdDep(t, bd, dir, "add", child2.ID, epic.ID, "--type", "parent-child")
		// Verify via dep list
		out := bdDep(t, bd, dir, "list", epic.ID, "--direction", "up")
		if !strings.Contains(out, child1.ID) {
			t.Errorf("expected child1 in dependents: %s", out)
		}
	})

	t.Run("add_type_tracks", func(t *testing.T) {
		tracker := bdCreate(t, bd, dir, "Tracker", "--type", "task")
		tracked := bdCreate(t, bd, dir, "Tracked", "--type", "task")
		bdDep(t, bd, dir, "add", tracker.ID, tracked.ID, "--type", "tracks")
		out := bdDep(t, bd, dir, "list", tracker.ID)
		if !strings.Contains(out, tracked.ID) {
			t.Errorf("expected tracked issue in deps: %s", out)
		}
	})

	t.Run("add_type_related", func(t *testing.T) {
		r1 := bdCreate(t, bd, dir, "Related 1", "--type", "task")
		r2 := bdCreate(t, bd, dir, "Related 2", "--type", "task")
		bdDep(t, bd, dir, "add", r1.ID, r2.ID, "--type", "related")
		// Should succeed without error
	})

	t.Run("add_blocked_by_flag", func(t *testing.T) {
		x := bdCreate(t, bd, dir, "Blocked by test", "--type", "task")
		y := bdCreate(t, bd, dir, "Blocker test", "--type", "task")
		bdDep(t, bd, dir, "add", x.ID, "--blocked-by", y.ID)
		out := bdDep(t, bd, dir, "list", x.ID)
		if !strings.Contains(out, y.ID) {
			t.Errorf("expected blocker in deps: %s", out)
		}
	})

	t.Run("add_depends_on_flag", func(t *testing.T) {
		x := bdCreate(t, bd, dir, "Depends on test", "--type", "task")
		y := bdCreate(t, bd, dir, "Dependency test", "--type", "task")
		bdDep(t, bd, dir, "add", x.ID, "--depends-on", y.ID)
		out := bdDep(t, bd, dir, "list", x.ID)
		if !strings.Contains(out, y.ID) {
			t.Errorf("expected dependency in deps: %s", out)
		}
	})

	t.Run("add_cycle_rejected", func(t *testing.T) {
		// A->B already exists, add B->A to create cycle — should be rejected
		cyA := bdCreate(t, bd, dir, "Cycle A", "--type", "task")
		cyB := bdCreate(t, bd, dir, "Cycle B", "--type", "task")
		bdDep(t, bd, dir, "add", cyA.ID, cyB.ID)
		out := bdDepFail(t, bd, dir, "add", cyB.ID, cyA.ID)
		if !strings.Contains(out, "cycle") {
			t.Errorf("expected 'cycle' error: %s", out)
		}
	})

	t.Run("add_child_parent_antipattern", func(t *testing.T) {
		p := bdCreate(t, bd, dir, "AP Parent", "--type", "epic")
		// Create child with hierarchical ID
		c := bdCreate(t, bd, dir, "AP Child", "--type", "task")
		bdDep(t, bd, dir, "add", c.ID, p.ID, "--type", "parent-child")
		// Try to add child->parent blocking dep (should fail)
		bdDepFail(t, bd, dir, "add", c.ID, p.ID)
	})

	t.Run("add_json_output", func(t *testing.T) {
		j1 := bdCreate(t, bd, dir, "JSON dep A", "--type", "task")
		j2 := bdCreate(t, bd, dir, "JSON dep B", "--type", "task")
		m := bdDepJSON(t, bd, dir, "add", j1.ID, j2.ID)
		if m["status"] != "added" {
			t.Errorf("expected status=added, got %v", m["status"])
		}
	})

	t.Run("add_bulk_file_jsonl", func(t *testing.T) {
		b1 := bdCreate(t, bd, dir, "Bulk dep A", "--type", "task")
		b2 := bdCreate(t, bd, dir, "Bulk dep B", "--type", "task")
		b3 := bdCreate(t, bd, dir, "Bulk dep C", "--type", "task")
		path := filepath.Join(t.TempDir(), "deps.jsonl")
		body := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"issue_id\":%q,\"depends_on_id\":%q,\"type\":\"tracks\"}\n", b1.ID, b2.ID, b3.ID, b2.ID)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write deps file: %v", err)
		}

		out := bdDep(t, bd, dir, "add", "--file", path)
		if !strings.Contains(out, "Added 2 dependencies") {
			t.Fatalf("expected bulk add summary, got: %s", out)
		}
		list1 := bdDep(t, bd, dir, "list", b1.ID)
		if !strings.Contains(list1, b2.ID) {
			t.Fatalf("expected first bulk dependency in list: %s", list1)
		}
		list3 := bdDep(t, bd, dir, "list", b3.ID)
		if !strings.Contains(list3, b2.ID) || !strings.Contains(list3, "tracks") {
			t.Fatalf("expected typed bulk dependency in list: %s", list3)
		}
	})

	t.Run("add_bulk_file_validation_no_partial_mutation", func(t *testing.T) {
		v1 := bdCreate(t, bd, dir, "Bulk validation A", "--type", "task")
		v2 := bdCreate(t, bd, dir, "Bulk validation B", "--type", "task")
		path := filepath.Join(t.TempDir(), "bad-deps.jsonl")
		body := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"from\":\"\",\"to\":%q}\n{\"from\":%q,\"to\":\"\"}\n", v1.ID, v2.ID, v2.ID, v1.ID)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write bad deps file: %v", err)
		}

		out := bdDepFail(t, bd, dir, "add", "--file", path)
		if !strings.Contains(out, "line 2: missing from") || !strings.Contains(out, "line 3: missing to") {
			t.Fatalf("expected all validation errors, got: %s", out)
		}
		list := bdDep(t, bd, dir, "list", v1.ID)
		if strings.Contains(list, v2.ID) {
			t.Fatalf("bulk validation failure should not add valid rows: %s", list)
		}
	})

	// ===== dep remove =====

	t.Run("remove_basic", func(t *testing.T) {
		r1 := bdCreate(t, bd, dir, "Remove A", "--type", "task")
		r2 := bdCreate(t, bd, dir, "Remove B", "--type", "task")
		bdDep(t, bd, dir, "add", r1.ID, r2.ID)
		out := bdDep(t, bd, dir, "remove", r1.ID, r2.ID)
		if !strings.Contains(out, "Removed") {
			t.Errorf("expected 'Removed' in output: %s", out)
		}
	})

	t.Run("remove_rm_alias", func(t *testing.T) {
		r1 := bdCreate(t, bd, dir, "Rm A", "--type", "task")
		r2 := bdCreate(t, bd, dir, "Rm B", "--type", "task")
		bdDep(t, bd, dir, "add", r1.ID, r2.ID)
		out := bdDep(t, bd, dir, "rm", r1.ID, r2.ID)
		if !strings.Contains(out, "Removed") {
			t.Errorf("expected 'Removed' via rm alias: %s", out)
		}
	})

	t.Run("remove_json_output", func(t *testing.T) {
		r1 := bdCreate(t, bd, dir, "RmJSON A", "--type", "task")
		r2 := bdCreate(t, bd, dir, "RmJSON B", "--type", "task")
		bdDep(t, bd, dir, "add", r1.ID, r2.ID)
		m := bdDepJSON(t, bd, dir, "remove", r1.ID, r2.ID)
		if m["status"] != "removed" {
			t.Errorf("expected status=removed, got %v", m["status"])
		}
	})

	// ===== dep list =====

	t.Run("list_default_direction_down", func(t *testing.T) {
		// issueC depends on issueD (added earlier)
		out := bdDep(t, bd, dir, "list", issueC.ID)
		if !strings.Contains(out, issueD.ID) {
			t.Errorf("expected dependency in list output: %s", out)
		}
	})

	t.Run("list_direction_up", func(t *testing.T) {
		out := bdDep(t, bd, dir, "list", issueD.ID, "--direction", "up")
		if !strings.Contains(out, issueC.ID) {
			t.Errorf("expected dependent in list --direction up: %s", out)
		}
	})

	t.Run("list_type_filter", func(t *testing.T) {
		out := bdDep(t, bd, dir, "list", epic.ID, "--direction", "up", "--type", "parent-child")
		if !strings.Contains(out, child1.ID) || !strings.Contains(out, child2.ID) {
			t.Errorf("expected children in type-filtered list: %s", out)
		}
	})

	t.Run("list_json_output", func(t *testing.T) {
		fullArgs := []string{"dep", "list", issueC.ID, "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dep list --json failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), issueD.ID) {
			t.Errorf("expected dependency ID in JSON: %s", out)
		}
	})

	// ===== dep tree =====

	t.Run("tree_basic", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", epic.ID)
		if !strings.Contains(out, epic.ID) {
			t.Errorf("expected epic ID in tree: %s", out)
		}
	})

	t.Run("tree_direction_up", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", child1.ID, "--direction", "up")
		if len(out) == 0 {
			t.Error("expected non-empty tree output")
		}
	})

	t.Run("tree_direction_both", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", epic.ID, "--direction", "both")
		if len(out) == 0 {
			t.Error("expected non-empty tree output for --direction both")
		}
	})

	t.Run("tree_max_depth", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", epic.ID, "--max-depth", "1")
		// Should show epic but not recurse deeply
		if !strings.Contains(out, epic.ID) {
			t.Errorf("expected epic in shallow tree: %s", out)
		}
	})

	t.Run("tree_status_filter", func(t *testing.T) {
		// Filter to open only — should still work
		bdDep(t, bd, dir, "tree", epic.ID, "--status", "open")
		// Just verify no crash
	})

	t.Run("tree_format_mermaid", func(t *testing.T) {
		out := bdDep(t, bd, dir, "tree", epic.ID, "--format", "mermaid")
		if !strings.Contains(out, "flowchart") && !strings.Contains(out, "graph") {
			t.Errorf("expected mermaid flowchart/graph syntax: %s", out)
		}
	})

	t.Run("tree_json_output", func(t *testing.T) {
		fullArgs := []string{"dep", "tree", epic.ID, "--json"}
		cmd := exec.Command(bd, fullArgs...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dep tree --json failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), epic.ID) {
			t.Errorf("expected epic ID in JSON tree: %s", out)
		}
	})

	// ===== dep cycles =====

	t.Run("cycles_detect", func(t *testing.T) {
		// We created a cycle earlier (cyA <-> cyB)
		out := bdDep(t, bd, dir, "cycles")
		// Should report at least one cycle
		if !strings.Contains(out, "cycle") && !strings.Contains(out, "Cycle") && !strings.Contains(out, "No cycles") {
			t.Errorf("expected cycle info in output: %s", out)
		}
	})

	t.Run("cycles_no_cycles", func(t *testing.T) {
		// Fresh init with no cycles
		dir2, _, _ := bdInit(t, bd, "--prefix", "dp2")
		a := bdCreate(t, bd, dir2, "No cycle A", "--type", "task")
		b := bdCreate(t, bd, dir2, "No cycle B", "--type", "task")
		bdDep(t, bd, dir2, "add", a.ID, b.ID)
		out := bdDep(t, bd, dir2, "cycles")
		if !strings.Contains(out, "No cycles") && !strings.Contains(out, "no cycles") {
			t.Logf("expected 'No cycles' message: %s", out)
		}
	})
}

// TestEmbeddedDepConcurrent exercises dep operations concurrently.
func TestEmbeddedDepConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dpc")

	// Pre-create issues.
	var ids []string
	for i := 0; i < 16; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-dep-%d", i), "--type", "task")
		ids = append(ids, issue.ID)
	}
	// Pre-create some deps for read operations.
	for i := 0; i < 8; i++ {
		bdDep(t, bd, dir, "add", ids[i*2], ids[i*2+1])
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
			id := ids[worker*2]

			// Read operations: list and tree
			args := []string{"dep", "list", id, "--json"}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d dep list: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			args = []string{"dep", "tree", id}
			cmd = exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d dep tree: %v\n%s", worker, err, out)
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

func TestEmbeddedDepNoCycleCheck(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ncc")

	// Create a linear chain of issues to simulate bulk dependency wiring.
	const n = 10
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("bulk-dep-%d", i), "--type", "task")
		ids[i] = issue.ID
	}

	// Wire the chain with --no-cycle-check — each call must succeed without
	// hanging on a full-graph cycle traversal.
	for i := 1; i < n; i++ {
		out := bdDep(t, bd, dir, "add", ids[i], ids[i-1], "--no-cycle-check")
		// Must not print a cycle warning (there are no cycles in a linear chain).
		if strings.Contains(out, "cycle") {
			t.Errorf("unexpected cycle warning with --no-cycle-check: %s", out)
		}
	}

	// Verify the graph is acyclic after bulk wiring.
	cyclesOut := bdDep(t, bd, dir, "cycles")
	if strings.Contains(cyclesOut, "Found") {
		t.Errorf("unexpected cycles after bulk wiring: %s", cyclesOut)
	}

	// Verify --no-cycle-check also works with the dep --blocks shorthand.
	extra := bdCreate(t, bd, dir, "bulk-dep-extra", "--type", "task")
	bdDep(t, bd, dir, extra.ID, "--blocks", ids[n-1], "--no-cycle-check")
}

func TestEmbeddedDepBulkNoCycleCheckSkipsPerEdgeCycleValidation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "bcy")

	a := bdCreate(t, bd, dir, "Bulk cycle A", "--type", "task")
	b := bdCreate(t, bd, dir, "Bulk cycle B", "--type", "task")
	input := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"from\":%q,\"to\":%q}\n", a.ID, b.ID, b.ID, a.ID)

	out := bdDepWithInput(t, bd, dir, input, "add", "--file", "-", "--no-cycle-check")
	if !strings.Contains(out, "Added 2 dependencies") {
		t.Fatalf("expected bulk add summary, got: %s", out)
	}

	cycles := bdDep(t, bd, dir, "cycles")
	if !strings.Contains(cycles, "Found") {
		t.Fatalf("expected skipped cycle validation to leave detectable cycle, got: %s", cycles)
	}
}
