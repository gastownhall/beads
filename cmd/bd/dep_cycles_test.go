package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The rendering half of `bd dep cycles` and of the post-add warning, driven
// directly with a report rather than through a database.
//
// These need no store and no server: they pin the one thing the conformance
// contract and the HTTP tests cannot see, which is what a human reads. The
// partial phrasing exists only here — a member with no row behind it used to be
// dropped, so there was nothing to print.

func TestDepCyclesNamesTheMembersItCannotDescribe(t *testing.T) {
	out := captureCycleStdout(t, func() {
		printCycleReportForTest(t, issueops.CycleReport{Cycles: []issueops.Cycle{
			{Members: []issueops.CycleMember{
				{ID: "bd-a", Issue: &types.Issue{ID: "bd-a", Title: "Alpha"}},
				{ID: "bd-b", Issue: &types.Issue{ID: "bd-b", Title: "Beta"}},
			}},
			{Partial: true, Members: []issueops.CycleMember{
				{ID: "bd-c", Issue: &types.Issue{ID: "bd-c", Title: "Gamma"}},
				{ID: "bd-ghost"},
			}},
		}})
	})

	if !strings.Contains(out, "Found 2 dependency cycles") {
		t.Errorf("output does not report both cycles:\n%s", out)
	}
	if !strings.Contains(out, "bd-a: Alpha") || !strings.Contains(out, "bd-b: Beta") {
		t.Errorf("a fully described cycle lost a title:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2 members have no record in this database") {
		t.Errorf("the partial cycle is not marked as partial:\n%s", out)
	}
	if !strings.Contains(out, "bd-ghost") {
		t.Errorf("the undescribable member is missing from the path; it used to be dropped:\n%s", out)
	}
	if !strings.Contains(out, "no record in this database") {
		t.Errorf("the undescribable member has no description at all, leaving a bare id and a blank:\n%s", out)
	}
}

func TestDepCyclesSaysNothingIsWrongForACleanWorkspace(t *testing.T) {
	out := captureCycleStdout(t, func() {
		printCycleReportForTest(t, issueops.CycleReport{Cycles: []issueops.Cycle{}})
	})
	if !strings.Contains(out, "No dependency cycles detected") {
		t.Errorf("a clean workspace did not say so:\n%s", out)
	}
}

// TestCycleWarningSpellsTheWholePathIncludingUndescribableMembers pins the
// post-add warning both dep-add routes and `bd link` print. It is spelled from
// the member IDS, so a node with no row behind it still appears.
func TestCycleWarningSpellsTheWholePathIncludingUndescribableMembers(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	printCycleWarnings([]issueops.Cycle{{Partial: true, Members: []issueops.CycleMember{
		{ID: "bd-a", Issue: &types.Issue{ID: "bd-a"}},
		{ID: "bd-ghost"},
		{ID: "bd-c", Issue: &types.Issue{ID: "bd-c"}},
	}}})

	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	out := buf.String()
	// The closing edge is drawn by repeating the first member, so the path
	// reads as a cycle rather than as a chain.
	if !strings.Contains(out, "bd-a → bd-ghost → bd-c → bd-a") {
		t.Errorf("cycle path is not the whole rotated path closed on itself:\n%s", out)
	}
	if !strings.Contains(out, "Run 'bd dep cycles' for detailed analysis.") {
		t.Errorf("the warning does not point at the detailed command:\n%s", out)
	}
}

// TestCycleWarningIsSilentWithoutCycles keeps the sweep from printing a header
// over an empty list on every successful `bd dep add`.
func TestCycleWarningIsSilentWithoutCycles(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	printCycleWarnings(nil)

	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	if buf.Len() != 0 {
		t.Errorf("an empty sweep wrote to stderr:\n%s", buf.String())
	}
}

// printCycleReportForTest drives `bd dep cycles`'s human rendering with a
// report, through the same role the command uses.
func printCycleReportForTest(t *testing.T, report issueops.CycleReport) {
	t.Helper()
	previousStore := store
	previousJSON := jsonOutput
	store = &fixedCycleStore{report: report}
	jsonOutput = false
	defer func() {
		store = previousStore
		jsonOutput = previousJSON
	}()

	if err := runDepCycles(); err != nil {
		t.Fatalf("runDepCycles: %v", err)
	}
}

// fixedCycleStore is a store whose only real method is the cycle accessor. It
// embeds the interface, so any other method the command reached for would panic
// rather than quietly answering zero — which is the assertion that `bd dep
// cycles` opens nothing but the role. The detector is a separate type because
// the store's LEGACY surface already carries a DetectCycles with a different
// signature.
type fixedCycleStore struct {
	storage.DoltStorage
	report issueops.CycleReport
}

func (s *fixedCycleStore) CycleDetector() (issueops.CycleDetector, error) {
	return fixedCycleDetector{report: s.report}, nil
}

type fixedCycleDetector struct{ report issueops.CycleReport }

func (d fixedCycleDetector) DetectCycles(context.Context, issueops.DetectCyclesRequest) (issueops.CycleReport, error) {
	return d.report, nil
}

func captureCycleStdout(t *testing.T, run func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	// Drain concurrently: a synchronous drain deadlocks once the command
	// writes more than the 64KiB pipe buffer, and it cannot run at all when
	// run() exits via Goexit.
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()

	os.Stdout = w
	restored := false
	restore := func() string {
		if restored {
			return ""
		}
		restored = true
		_ = w.Close()          // unblocks the drain goroutine
		os.Stdout = origStdout // ALWAYS runs, including on Goexit/panic
		out := <-done
		_ = r.Close()
		return out
	}
	defer restore()

	run()

	return restore()
}

// TestCaptureCycleStdoutRestoresOnFatal pins be-gh02: a callback that Goexits
// must still leave os.Stdout restored. Before the fix this left os.Stdout as an
// orphaned pipe whose read end the GC finalizer later closed, making every
// subsequent write in the binary fail with "write |1: broken pipe".
//
// The leaking callback runs on a manually created goroutine that calls
// runtime.Goexit() directly rather than a t.Run subtest calling t.Fatal: the
// unwind-and-run-deferred-calls mechanism is identical, but this keeps the test
// itself green instead of always reporting a deliberately-failed subtest.
func TestCaptureCycleStdoutRestoresOnFatal(t *testing.T) {
	real := os.Stdout
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = captureCycleStdout(t, func() {
			runtime.Goexit()
		})
	}()
	<-done

	if os.Stdout != real {
		os.Stdout = real
		t.Fatalf("captureCycleStdout leaked os.Stdout after a Goexit")
	}
	if _, err := os.Stdout.Write(nil); err != nil {
		t.Fatalf("os.Stdout unusable after capture: %v", err)
	}
}
