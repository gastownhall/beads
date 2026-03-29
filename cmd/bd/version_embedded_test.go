package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedVersion(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("version_output", func(t *testing.T) {
		cmd := exec.Command(bd, "version")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd version failed: %v\n%s", err, out)
		}
		s := string(out)
		if len(strings.TrimSpace(s)) == 0 {
			t.Error("expected non-empty version output")
		}
		// Should contain a version-like string
		if !strings.Contains(s, "v") && !strings.Contains(s, "0.") {
			t.Errorf("expected version string in output: %s", s)
		}
	})
}

func TestEmbeddedVersionConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

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
			cmd := exec.Command(bd, "version")
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("version (worker %d): %v\n%s", worker, err, out)
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
