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

// bdVC runs "bd vc" with extra args. Returns combined output.
func bdVC(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"vc"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd vc %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdVCFail runs "bd vc" expecting failure.
func bdVCFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"vc"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd vc %s should have failed, got: %s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedVC(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("status", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcst")

		out := bdVC(t, bd, dir, "status")
		if !strings.Contains(out, "main") {
			t.Errorf("expected 'main' branch in status, got: %s", out)
		}
	})

	t.Run("status_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcstj")

		out := bdVC(t, bd, dir, "status", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if branch, _ := result["branch"].(string); branch != "main" {
			t.Errorf("expected branch='main', got %q", branch)
		}
		if commit, _ := result["commit"].(string); commit == "" {
			t.Error("expected non-empty commit hash")
		}
	})

	t.Run("commit_with_message", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vccm")
		// bd create auto-commits, so vc commit may see "nothing to commit".
		// Both outcomes are valid.
		bdCreateSilent(t, bd, dir, "commit test issue")

		cmd := exec.Command(bd, "vc", "commit", "-m", "test commit message")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "nothing to commit") {
			t.Fatalf("bd vc commit failed unexpectedly: %v\n%s", err, out)
		}
	})

	t.Run("commit_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vccj")
		bdCreateSilent(t, bd, dir, "commit json issue")

		cmd := exec.Command(bd, "vc", "commit", "-m", "json commit", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "nothing to commit") {
			t.Fatalf("bd vc commit --json failed unexpectedly: %v\n%s", err, out)
		}
		// If commit succeeded, verify JSON
		if err == nil {
			var result map[string]interface{}
			if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
				t.Fatalf("failed to parse JSON: %v\n%s", jsonErr, out)
			}
			if committed, _ := result["committed"].(bool); !committed {
				t.Error("expected committed=true")
			}
		}
	})

	t.Run("commit_stdin", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vccs")
		bdCreateSilent(t, bd, dir, "stdin commit issue")

		cmd := exec.Command(bd, "vc", "commit", "--stdin")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.Stdin = strings.NewReader("message from stdin")
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "nothing to commit") {
			t.Fatalf("bd vc commit --stdin failed unexpectedly: %v\n%s", err, out)
		}
	})

	t.Run("commit_no_message", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcnm")

		out := bdVCFail(t, bd, dir, "commit")
		if !strings.Contains(out, "commit message is required") {
			t.Errorf("expected 'commit message is required', got: %s", out)
		}
	})

	t.Run("merge_no_conflicts", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcmrg")

		// Create a branch, switch to it, create an issue, switch back, merge
		bdBranch(t, bd, dir, "feature-test")

		// Create an issue on main (auto-committed)
		bdCreateSilent(t, bd, dir, "main branch issue")

		// Merge the feature branch (which is at the same base commit)
		out := bdVC(t, bd, dir, "merge", "feature-test")
		if !strings.Contains(out, "merged") && !strings.Contains(out, "Merged") {
			t.Errorf("expected merge success message, got: %s", out)
		}
	})

	t.Run("merge_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "vcmj")
		bdBranch(t, bd, dir, "merge-json-branch")

		out := bdVC(t, bd, dir, "merge", "merge-json-branch", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if merged, _ := result["merged"].(string); merged != "merge-json-branch" {
			t.Errorf("expected merged='merge-json-branch', got %q", merged)
		}
	})
}

func TestEmbeddedVCConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vcconc")

	const numWorkers = 10

	type result struct {
		worker int
		out    string
		err    error
	}

	// Each worker creates an issue then commits
	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			// Create an issue
			createCmd := exec.Command(bd, "create", "--silent", fmt.Sprintf("vc-conc-issue-%d", worker))
			createCmd.Dir = dir
			createCmd.Env = bdEnv(dir)
			if out, err := createCmd.CombinedOutput(); err != nil {
				results[worker] = result{worker: worker, out: string(out), err: err}
				return
			}

			// Try to commit
			cmd := exec.Command(bd, "vc", "commit", "-m", fmt.Sprintf("worker %d commit", worker))
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
		} else if strings.Contains(r.out, "one writer at a time") ||
			strings.Contains(r.out, "nothing to commit") {
			// Expected: auto-commit means explicit commit often has nothing left
			successes++ // still counts as successful execution
		} else {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful commit, got %d", successes)
	}
	t.Logf("%d/%d vc commit workers succeeded", successes, numWorkers)
}
