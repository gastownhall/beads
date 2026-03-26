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

// bdBranch runs "bd branch" with extra args. Returns combined output.
func bdBranch(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"branch"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd branch %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt branch tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("list_default", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "brlist")

		out := bdBranch(t, bd, dir)
		if !strings.Contains(out, "main") {
			t.Errorf("expected 'main' in branch list, got: %s", out)
		}
	})

	t.Run("list_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "brjson")

		out := bdBranch(t, bd, dir, "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if current, _ := result["current"].(string); current != "main" {
			t.Errorf("expected current branch 'main', got %q", current)
		}
		branches, ok := result["branches"].([]interface{})
		if !ok || len(branches) == 0 {
			t.Errorf("expected non-empty branches list, got: %v", result["branches"])
		}
	})

	t.Run("create", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "brcreate")

		out := bdBranch(t, bd, dir, "feature-xyz")
		if !strings.Contains(out, "feature-xyz") {
			t.Errorf("expected 'feature-xyz' in output, got: %s", out)
		}

		// Verify branch appears in list
		listOut := bdBranch(t, bd, dir)
		if !strings.Contains(listOut, "feature-xyz") {
			t.Errorf("expected 'feature-xyz' in branch list, got: %s", listOut)
		}
	})

	t.Run("create_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "brcrj")

		out := bdBranch(t, bd, dir, "my-branch", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if created, _ := result["created"].(string); created != "my-branch" {
			t.Errorf("expected created='my-branch', got %q", created)
		}
	})

	t.Run("create_multiple", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "brmulti")

		bdBranch(t, bd, dir, "branch-a")
		bdBranch(t, bd, dir, "branch-b")
		bdBranch(t, bd, dir, "branch-c")

		out := bdBranch(t, bd, dir, "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		branches, _ := result["branches"].([]interface{})
		if len(branches) < 4 { // main + 3 created
			t.Errorf("expected at least 4 branches, got %d", len(branches))
		}
	})
}

func TestEmbeddedBranchConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt branch tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "brconc")

	const numWorkers = 10

	type result struct {
		worker int
		out    string
		err    error
	}

	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			branchName := fmt.Sprintf("conc-branch-%d", worker)
			cmd := exec.Command(bd, "branch", branchName)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			results[worker] = result{worker: worker, out: string(out), err: err}
		}(w)
	}
	wg.Wait()

	successes := 0
	for _, r := range results {
		if strings.Contains(r.out, "panic") {
			t.Errorf("worker %d panicked:\n%s", r.worker, r.out)
		}
		if r.err == nil {
			successes++
		} else if !strings.Contains(r.out, "one writer at a time") {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful branch create, got %d", successes)
	}
	t.Logf("%d/%d branch workers succeeded", successes, numWorkers)

	// Verify at least the successful branches exist
	listOut := bdBranch(t, bd, dir)
	if !strings.Contains(listOut, "main") {
		t.Error("expected 'main' in branch list after concurrent creates")
	}
}
