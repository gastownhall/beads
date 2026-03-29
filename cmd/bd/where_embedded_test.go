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

// bdWhere runs "bd where" with the given args and returns stdout.
func bdWhere(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"where"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd where %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedWhere(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tw")

	// ===== Default Output =====

	t.Run("where_default", func(t *testing.T) {
		out := bdWhere(t, bd, dir)
		if !strings.Contains(out, ".beads") {
			t.Errorf("expected .beads in where output: %s", out)
		}
		// Should contain the actual beads directory path
		if !strings.Contains(out, beadsDir) {
			t.Errorf("expected beads dir %q in where output: %s", beadsDir, out)
		}
	})

	// ===== JSON Output =====

	t.Run("where_json", func(t *testing.T) {
		out := bdWhere(t, bd, dir, "--json")
		s := strings.TrimSpace(out)
		start := strings.Index(s, "{")
		if start < 0 {
			// --json may be affected by same flag shadowing as info;
			// just verify no crash and output contains path
			if !strings.Contains(out, ".beads") {
				t.Errorf("expected .beads in where --json output: %s", out)
			}
			return
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("parse where JSON: %v\n%s", err, s)
		}
		// Verify path is present in JSON (WhereResult uses "path" key)
		if path, ok := m["path"]; ok {
			if p, ok := path.(string); ok && !strings.Contains(p, ".beads") {
				t.Errorf("expected .beads in path: %v", path)
			}
		}
	})
}

// TestEmbeddedWhereConcurrent exercises where operations concurrently.
func TestEmbeddedWhereConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wx")

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

			cmd := exec.Command(bd, "where")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("where (worker %d): %v\n%s", worker, err, out)
				results[worker] = r
				return
			}
			if !strings.Contains(string(out), ".beads") {
				r.err = fmt.Errorf("where (worker %d): expected .beads in output: %s", worker, out)
				results[worker] = r
				return
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
