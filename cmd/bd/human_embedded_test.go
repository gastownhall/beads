//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdHuman runs "bd human" with the given args and returns stdout.
func bdHuman(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"human"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd human %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// createHumanBead creates a bead carrying the 'human' label and returns its ID.
func createHumanBead(t *testing.T, bd, dir, title string) string {
	t.Helper()
	id := bdCreateSilent(t, bd, dir, title, "--labels", "human")
	if id == "" {
		t.Fatalf("could not find issue ID in create output for %q", title)
	}
	return id
}

// humanShowClosed asserts the bead is closed and returns its parsed issue.
func humanShowClosed(t *testing.T, bd, dir, id string) *types.Issue {
	t.Helper()
	issue := bdShow(t, bd, dir, id)
	if issue.Status != types.StatusClosed {
		t.Errorf("expected issue %s to be closed, got status %q", id, issue.Status)
	}
	return issue
}

func TestEmbeddedHuman(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "th")

	// ===== Default Help Output =====

	t.Run("human_default", func(t *testing.T) {
		out := bdHuman(t, bd, dir)
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty human output")
		}
	})

	// ===== List =====

	t.Run("human_list_empty", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "list")
		// No human-labeled issues yet — should succeed without error
		_ = out
	})

	// ===== Stats =====

	t.Run("human_stats", func(t *testing.T) {
		out := bdHuman(t, bd, dir, "stats")
		// Should succeed and produce output
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty stats output")
		}
	})

	// ===== Respond and Dismiss =====

	t.Run("human_respond_and_dismiss", func(t *testing.T) {
		id := createHumanBead(t, bd, dir, "Human test issue")

		// Verify it shows up in human list
		listOut := bdHuman(t, bd, dir, "list")
		if !strings.Contains(listOut, id) {
			t.Errorf("expected issue %s in human list output:\n%s", id, listOut)
		}

		// Test Respond
		bdHuman(t, bd, dir, "respond", id, "--response", "Approved")
		humanShowClosed(t, bd, dir, id)

		// Closed beads drop out of the default list but show with
		// --status=closed and --status=all.
		listOut = bdHuman(t, bd, dir, "list")
		if strings.Contains(listOut, id) {
			t.Errorf("closed issue %s should be hidden from default human list:\n%s", id, listOut)
		}
		closedOut := bdHuman(t, bd, dir, "list", "--status=closed")
		if !strings.Contains(closedOut, id) {
			t.Errorf("expected closed issue %s in human list --status=closed:\n%s", id, closedOut)
		}
		allOut := bdHuman(t, bd, dir, "list", "--status=all")
		if !strings.Contains(allOut, id) {
			t.Errorf("expected closed issue %s in human list --status=all:\n%s", id, allOut)
		}

		// An invalid status is an error, not a silent empty list.
		if out, _ := bdRunFailCode(t, bd, dir, "human", "list", "--status=colsed"); !strings.Contains(out, "invalid status") {
			t.Errorf("expected invalid-status error, got:\n%s", out)
		}

		// Test Dismiss
		id2 := createHumanBead(t, bd, dir, "Dismiss test issue")
		bdHuman(t, bd, dir, "dismiss", id2, "--reason", "Not needed")
		issue2 := humanShowClosed(t, bd, dir, id2)
		if issue2.CloseReason != "Dismissed: Not needed" {
			t.Errorf("expected dismiss reason %q, got %q", "Dismissed: Not needed", issue2.CloseReason)
		}
	})

	// ===== Respond via positional text and --file =====

	t.Run("human_respond_positional_and_file", func(t *testing.T) {
		// Positional response text, multiple words without a flag.
		id := createHumanBead(t, bd, dir, "Positional respond test")
		bdHuman(t, bd, dir, "respond", id, "Approved,", "proceed", "with", "implementation")
		humanShowClosed(t, bd, dir, id)

		// Response text from a file.
		id2 := createHumanBead(t, bd, dir, "File respond test")
		respFile := filepath.Join(t.TempDir(), "response.md")
		if err := os.WriteFile(respFile, []byte("Approved via file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bdHuman(t, bd, dir, "respond", id2, "--file", respFile)
		humanShowClosed(t, bd, dir, id2)

		// Positional dismiss reason.
		id3 := createHumanBead(t, bd, dir, "Positional dismiss test")
		bdHuman(t, bd, dir, "dismiss", id3, "No", "longer", "applicable")
		issue3 := humanShowClosed(t, bd, dir, id3)
		if issue3.CloseReason != "Dismissed: No longer applicable" {
			t.Errorf("expected dismiss reason %q, got %q", "Dismissed: No longer applicable", issue3.CloseReason)
		}
	})

	// ===== Guard rails: conflicting sources and empty text =====

	t.Run("human_respond_dismiss_guards", func(t *testing.T) {
		id := createHumanBead(t, bd, dir, "Guard test bead")

		// Positional text combined with a flag source is a conflict, not a
		// silent drop of the positional text.
		if out, _ := bdRunFailCode(t, bd, dir, "human", "respond", id, "typed", "text", "--response", "flag text"); !strings.Contains(out, "cannot combine positional text") {
			t.Errorf("expected positional/flag conflict error, got:\n%s", out)
		}
		if out, _ := bdRunFailCode(t, bd, dir, "human", "dismiss", id, "typed", "text", "--reason", "flag text"); !strings.Contains(out, "cannot combine positional text") {
			t.Errorf("expected positional/flag conflict error, got:\n%s", out)
		}

		// Whitespace-only response must not close the bead.
		if _, code := bdRunFailCode(t, bd, dir, "human", "respond", id, "   "); code == 0 {
			t.Error("whitespace-only response should fail")
		}

		// ID-shaped free text is accepted as a dismiss reason like any other
		// text: respond/dismiss act on exactly one bead, args[0].
		idText := createHumanBead(t, bd, dir, "Shaped-text bead")
		bdHuman(t, bd, dir, "dismiss", idText, "bd-dev")
		issueText := humanShowClosed(t, bd, dir, idText)
		if issueText.CloseReason != "Dismissed: bd-dev" {
			t.Errorf("expected ID-shaped free text kept as dismiss reason, got %q", issueText.CloseReason)
		}

		// The bead targeted by the rejected invocations is untouched.
		if issue := bdShow(t, bd, dir, id); issue.Status == types.StatusClosed {
			t.Errorf("rejected commands must not close the bead, got status %q", issue.Status)
		}
	})
}

// TestEmbeddedHumanConcurrent exercises human operations concurrently.
func TestEmbeddedHumanConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hx")

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

			var args []string
			switch worker % 2 {
			case 0:
				args = []string{"human", "list"}
			case 1:
				args = []string{"human", "stats"}
			}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("human (worker %d): %v\n%s", worker, err, out)
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
