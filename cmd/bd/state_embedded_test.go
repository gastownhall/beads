//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdState runs "bd state" with the given args and returns stdout.
func bdState(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"state"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd state %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdSetState runs "bd set-state" with the given args and returns stdout.
func bdSetState(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"set-state"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd set-state %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedState(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "st")

	issue := bdCreate(t, bd, dir, "State test issue", "--type", "task")

	// ===== set-state =====

	t.Run("set_state_basic", func(t *testing.T) {
		out := bdSetState(t, bd, dir, issue.ID, "phase=planning")
		if !strings.Contains(out, "planning") {
			t.Logf("set-state output: %s", out)
		}
	})

	t.Run("set_state_json", func(t *testing.T) {
		cmd := exec.Command(bd, "set-state", issue.ID, "env=staging", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd set-state --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		start := strings.Index(s, "{")
		if start >= 0 {
			var m map[string]interface{}
			if jsonErr := json.Unmarshal([]byte(s[start:]), &m); jsonErr != nil {
				t.Errorf("invalid JSON: %v\n%s", jsonErr, s)
			}
		}
	})

	t.Run("set_state_with_reason", func(t *testing.T) {
		out := bdSetState(t, bd, dir, issue.ID, "risk=high", "--reason", "New vulnerability found")
		_ = out
	})

	t.Run("set_state_overwrites", func(t *testing.T) {
		bdSetState(t, bd, dir, issue.ID, "phase=development")
		bdSetState(t, bd, dir, issue.ID, "phase=testing")

		out := bdState(t, bd, dir, issue.ID, "phase")
		if !strings.Contains(out, "testing") {
			t.Errorf("expected 'testing' after overwrite, got: %s", out)
		}
	})

	// ===== state query =====

	t.Run("state_query", func(t *testing.T) {
		out := bdState(t, bd, dir, issue.ID, "phase")
		if !strings.Contains(out, "testing") {
			t.Logf("state query output: %s", out)
		}
	})

	t.Run("state_query_json", func(t *testing.T) {
		cmd := exec.Command(bd, "state", issue.ID, "phase", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd state --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		_ = stdout.String()
	})

	t.Run("state_query_nonexistent_dimension", func(t *testing.T) {
		out := bdState(t, bd, dir, issue.ID, "nonexistent")
		// Should return empty/not-set, not error
		_ = out
	})

	// ===== state list =====

	t.Run("state_list", func(t *testing.T) {
		out := bdState(t, bd, dir, "list", issue.ID)
		// Should show the dimensions we set
		if !strings.Contains(out, "phase") {
			t.Logf("state list output: %s", out)
		}
	})

	t.Run("state_list_json", func(t *testing.T) {
		cmd := exec.Command(bd, "state", "list", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd state list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		_ = stdout.String()
	})

	t.Run("state_list_no_dimensions", func(t *testing.T) {
		fresh := bdCreate(t, bd, dir, "No state", "--type", "task")
		out := bdState(t, bd, dir, "list", fresh.ID)
		_ = out
	})
}

// TestEmbeddedStateConcurrent exercises set-state concurrently on different dimensions.
func TestEmbeddedStateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sx")

	issue := bdCreate(t, bd, dir, "Concurrent state", "--type", "task")

	const numWorkers = 6

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

			dim := fmt.Sprintf("dim%d=val%d", worker, worker)
			cmd := exec.Command(bd, "set-state", issue.ID, dim)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("set-state %s: %v\n%s", dim, err, out)
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

// bdSetStateJSON runs "bd set-state ... --json" and returns the parsed
// result object.
func bdSetStateJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	cmd := exec.Command(bd, append(append([]string{"set-state"}, args...), "--json")...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd set-state %s --json failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in set-state output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse set-state JSON: %v\n%s", err, s)
	}
	return m
}

// TestEmbeddedSetStateHealsADuplicatedDimension is GH#5657's AC-4: a
// dimension left holding two values (the union a lost update wrote) is
// healed to exactly the requested value by the first `bd set-state`, and the
// immediate repeat reports no change WITHOUT minting a second event bead for
// the same value.
func TestEmbeddedSetStateHealsADuplicatedDimension(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sr")

	issue := bdCreate(t, bd, dir, "Heal a duplicated dimension", "--type", "task")
	// The wedge: two values for one dimension, the state a lost update (or
	// the retired two-write set-state) left behind.
	for _, label := range []string{"state:b", "state:c", "keep"} {
		bdLabel(t, bd, dir, "add", issue.ID, label)
	}

	stateEventCount := func() int {
		count := 0
		for _, child := range bdListJSON(t, bd, dir, "--parent", issue.ID, "--all") {
			if child.IssueType == types.TypeEvent {
				count++
			}
		}
		return count
	}

	heal := bdSetStateJSON(t, bd, dir, issue.ID, "state=b")
	if heal["changed"] != true {
		t.Fatalf("healing run changed = %v, want true", heal["changed"])
	}
	labels := bdLabelListJSON(t, bd, dir, issue.ID)
	sort.Strings(labels)
	if want := []string{"keep", "state:b"}; !slices.Equal(labels, want) {
		t.Fatalf("labels after heal = %v, want %v: the duplicate value survived", labels, want)
	}
	eventsAfterHeal := stateEventCount()
	if eventsAfterHeal != 1 {
		t.Fatalf("event beads after heal = %d, want 1", eventsAfterHeal)
	}

	// The repeat is a no-op: changed=false and no second event bead.
	replay := bdSetStateJSON(t, bd, dir, issue.ID, "state=b")
	if replay["changed"] != false {
		t.Fatalf("replay changed = %v, want false", replay["changed"])
	}
	if _, present := replay["event_id"]; present {
		t.Fatalf("replay minted an event bead (%v) for a value the issue already holds", replay["event_id"])
	}
	if got := stateEventCount(); got != eventsAfterHeal {
		t.Fatalf("event beads after replay = %d, want %d", got, eventsAfterHeal)
	}
	labels = bdLabelListJSON(t, bd, dir, issue.ID)
	sort.Strings(labels)
	if want := []string{"keep", "state:b"}; !slices.Equal(labels, want) {
		t.Fatalf("labels after replay = %v, want %v", labels, want)
	}
}

// TestEmbeddedSetStateConcurrentSameDimensionKeepsOneValue is GH#5657's
// AC-3 at the command level: two overlapping `bd set-state` calls swapping
// the SAME dimension must leave exactly one requested value (plus every
// untouched label), never the union.
//
// On the embedded engine this passes at the BASE too, honestly: the engine
// admits one writer per process, so one command opens after the other has
// finished and reads the settled set — the route the base bug still bites is
// the server one, where two sessions truly interleave and the storage-level
// race is pinned red-at-base by
// internal/storage/uow.TestUOW_ConcurrentSetLabels_LastWriterWins. What this
// test adds is the command-level invariant on every route the interleave CAN
// happen on: the dimension swap (RemovePrefixes + Add) on one coordinated
// write means even a full overlap lands one value, never two.
func TestEmbeddedSetStateConcurrentSameDimensionKeepsOneValue(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ss")

	issue := bdCreate(t, bd, dir, "Concurrent same dimension", "--type", "task")
	for _, label := range []string{"state:a", "keep"} {
		bdLabel(t, bd, dir, "add", issue.ID, label)
	}

	values := []string{"b", "c"}
	errs := make([]error, len(values))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w, value := range values {
		wg.Add(1)
		go func(worker int, value string) {
			defer wg.Done()
			<-start
			cmd := exec.Command(bd, "set-state", issue.ID, "state="+value)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				errs[worker] = fmt.Errorf("set-state state=%s: %v\n%s", value, err, out)
			}
		}(w, value)
	}
	close(start)
	wg.Wait()

	for worker, err := range errs {
		// The engine's single-writer admission is environmental, not the
		// bug under test; any other failure is.
		if err != nil && !strings.Contains(err.Error(), "one writer at a time") {
			t.Errorf("worker %d (state=%s) failed: %v", worker, values[worker], err)
		}
	}

	labels := bdLabelListJSON(t, bd, dir, issue.ID)
	sort.Strings(labels)
	if len(labels) != 2 || labels[0] != "keep" || (labels[1] != "state:b" && labels[1] != "state:c") {
		t.Fatalf("labels after overlapping same-dimension set-state = %v, want exactly one of state:b/state:c beside keep", labels)
	}
}
