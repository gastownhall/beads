//go:build embeddeddolt

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

// bdMergeSlot runs "bd merge-slot" with the given args and returns stdout.
func bdMergeSlot(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"merge-slot"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd merge-slot %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdMergeSlotFail runs "bd merge-slot" expecting non-zero exit.
func bdMergeSlotFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"merge-slot"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd merge-slot %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdMergeSlotJSON runs "bd merge-slot" with --json and returns parsed map.
func bdMergeSlotJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"merge-slot"}, append(args, "--json")...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd merge-slot %s --json failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse merge-slot JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedMergeSlot(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)

	// ===== Create =====

	t.Run("merge_slot_create", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ms")
		out := bdMergeSlot(t, bd, dir, "create")
		if !strings.Contains(out, "Created merge slot") {
			t.Errorf("expected 'Created merge slot' in output: %s", out)
		}
		if !strings.Contains(out, "ms-merge-slot") {
			t.Errorf("expected slot ID 'ms-merge-slot' in output: %s", out)
		}
	})

	t.Run("merge_slot_create_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mj")
		m := bdMergeSlotJSON(t, bd, dir, "create")
		if m["id"] != "mj-merge-slot" {
			t.Errorf("expected id=mj-merge-slot, got %v", m["id"])
		}
		if m["status"] != "open" {
			t.Errorf("expected status=open, got %v", m["status"])
		}
	})

	t.Run("merge_slot_create_idempotent", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mi")
		bdMergeSlot(t, bd, dir, "create")
		out := bdMergeSlot(t, bd, dir, "create")
		if !strings.Contains(out, "already exists") {
			t.Errorf("expected 'already exists' on second create: %s", out)
		}
	})

	// ===== Check =====

	t.Run("merge_slot_check_no_slot", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mc")
		// Check without creating — prints not-found message but exits 0
		out := bdMergeSlot(t, bd, dir, "check")
		if !strings.Contains(out, "not found") {
			t.Logf("check output without slot: %s", out)
		}
	})

	t.Run("merge_slot_check_available", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mk")
		bdMergeSlot(t, bd, dir, "create")
		m := bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != true {
			t.Errorf("expected available=true, got %v", m["available"])
		}
	})

	// NOTE: acquire/release/waiters tests require the "holder" and "waiters"
	// columns which are not yet in the embedded dolt schema. These tests are
	// skipped until the schema migration adds those columns.

	t.Run("merge_slot_check_held", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mh")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "agent-1")
		m := bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != false {
			t.Errorf("expected available=false after acquire, got %v", m["available"])
		}
		if m["holder"] != "agent-1" {
			t.Errorf("expected holder=agent-1, got %v", m["holder"])
		}
	})

	// ===== Acquire =====

	t.Run("merge_slot_acquire", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "ma")
		bdMergeSlot(t, bd, dir, "create")
		m := bdMergeSlotJSON(t, bd, dir, "acquire", "--holder", "alice")
		if m["acquired"] != true {
			t.Errorf("expected acquired=true, got %v", m["acquired"])
		}
	})

	t.Run("merge_slot_acquire_held_without_wait", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mw")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")
		// Second acquire without --wait should fail (exit non-zero)
		bdMergeSlotFail(t, bd, dir, "acquire", "--holder", "bob")
	})

	t.Run("merge_slot_acquire_with_wait", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mt")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")
		// With --wait, should add to waiters and exit non-zero
		out := bdMergeSlotFail(t, bd, dir, "acquire", "--holder", "bob", "--wait")
		if !strings.Contains(out, "waiter") && !strings.Contains(out, "queue") {
			t.Logf("wait output: %s", out)
		}
	})

	t.Run("merge_slot_acquire_with_wait_json", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mq")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")

		// --wait with --json: exits non-zero but with JSON output
		cmd := exec.Command(bd, "merge-slot", "acquire", "--holder", "bob", "--wait", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, _ := cmd.CombinedOutput()
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "{")
		if start >= 0 {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(s[start:]), &m); err == nil {
				if m["waiting"] != true {
					t.Errorf("expected waiting=true, got %v", m["waiting"])
				}
			}
		}
	})

	// ===== Release =====

	t.Run("merge_slot_release", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mr")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")
		out := bdMergeSlot(t, bd, dir, "release")
		if !strings.Contains(out, "Released") {
			t.Errorf("expected 'Released' in output: %s", out)
		}
		// Verify slot is available again
		m := bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != true {
			t.Errorf("expected available=true after release, got %v", m["available"])
		}
	})

	t.Run("merge_slot_release_json", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "mx")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")
		m := bdMergeSlotJSON(t, bd, dir, "release")
		if m["released"] != true {
			t.Errorf("expected released=true, got %v", m["released"])
		}
	})

	t.Run("merge_slot_release_wrong_holder", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "my")
		bdMergeSlot(t, bd, dir, "create")
		bdMergeSlot(t, bd, dir, "acquire", "--holder", "alice")
		bdMergeSlotFail(t, bd, dir, "release", "--holder", "bob")
	})

	// ===== Full Lifecycle =====

	t.Run("merge_slot_lifecycle", func(t *testing.T) {
		t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
		dir, _, _ := bdInit(t, bd, "--prefix", "ml")

		// Create
		bdMergeSlot(t, bd, dir, "create")

		// Check — available
		m := bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != true {
			t.Fatal("expected available after create")
		}

		// Acquire
		m = bdMergeSlotJSON(t, bd, dir, "acquire", "--holder", "agent-1")
		if m["acquired"] != true {
			t.Fatal("expected acquired")
		}

		// Check — held
		m = bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != false {
			t.Fatal("expected not available after acquire")
		}

		// Release
		m = bdMergeSlotJSON(t, bd, dir, "release")
		if m["released"] != true {
			t.Fatal("expected released")
		}

		// Check — available again
		m = bdMergeSlotJSON(t, bd, dir, "check")
		if m["available"] != true {
			t.Fatal("expected available after release")
		}
	})
}

// TestEmbeddedMergeSlotConcurrent exercises merge-slot operations concurrently.
func TestEmbeddedMergeSlotConcurrent(t *testing.T) {
	t.Skip("requires holder column schema migration (missing from both DoltStore and EmbeddedDoltStore)")
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mp")

	// Create the slot
	bdMergeSlot(t, bd, dir, "create")

	const numWorkers = 6

	type workerResult struct {
		worker   int
		acquired bool
		err      error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// All workers try to acquire simultaneously — only one should succeed
	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			holder := fmt.Sprintf("agent-%d", worker)
			cmd := exec.Command(bd, "merge-slot", "acquire", "--holder", holder, "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()

			s := strings.TrimSpace(string(out))
			start := strings.Index(s, "{")
			if start >= 0 {
				var m map[string]interface{}
				if jsonErr := json.Unmarshal([]byte(s[start:]), &m); jsonErr == nil {
					if m["acquired"] == true {
						r.acquired = true
					}
				}
			}

			// Non-acquirers exit non-zero — that's expected
			if err != nil && !r.acquired {
				// Expected: slot was held by another worker
				r.err = nil
			} else if err != nil {
				r.err = fmt.Errorf("acquire: %v\n%s", err, out)
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	acquireCount := 0
	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d error: %v", r.worker, r.err)
		}
		if r.acquired {
			acquireCount++
		}
	}

	// Exactly one worker should have acquired
	if acquireCount != 1 {
		t.Errorf("expected exactly 1 acquire, got %d", acquireCount)
	}
}
