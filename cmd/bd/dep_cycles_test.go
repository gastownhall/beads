package main

import (
	"bytes"
	"context"
	"fmt"
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
		}}, 0)
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
		printCycleReportForTest(t, issueops.CycleReport{Cycles: []issueops.Cycle{}}, 0)
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
// report, through the same role the command uses. limit is the rendering cap
// runDepCycles takes; 0 prints the whole report.
func printCycleReportForTest(t *testing.T, report issueops.CycleReport, limit int) {
	t.Helper()
	previousStore := store
	previousJSON := jsonOutput
	store = &fixedCycleStore{report: report}
	jsonOutput = false
	defer func() {
		store = previousStore
		jsonOutput = previousJSON
	}()

	if err := runDepCycles(false, limit); err != nil {
		t.Fatalf("runDepCycles: %v", err)
	}
}

// TestDepCyclesFlagRegistration pins the --include-tracks flag itself: it
// exists on the command, is a bool, and defaults to false so an invocation
// with no flag leaves the base (blocks/conditional-blocks-only) walk
// unchanged.
func TestDepCyclesFlagRegistration(t *testing.T) {
	flag := depCyclesCmd.Flags().Lookup("include-tracks")
	if flag == nil {
		t.Fatal("depCyclesCmd has no --include-tracks flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--include-tracks is a %s, want bool", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--include-tracks defaults to %q, want \"false\"", flag.DefValue)
	}
}

// TestDepCyclesLimitFlagRegistration pins the rendering cap: an int flag whose
// default is not 0, so any workspace dense enough to report more cycles than a
// reader can use gets a readable diagnostic without the operator having to know
// the flag exists. This pins only the REGISTRATION; the "registered default"
// arm of TestDepCyclesTruncatesTheRenderedListButNotTheCount is what exercises
// the value through the renderer.
func TestDepCyclesLimitFlagRegistration(t *testing.T) {
	flag := depCyclesCmd.Flags().Lookup("limit")
	if flag == nil {
		t.Fatal("depCyclesCmd has no --limit flag")
	}
	if flag.Value.Type() != "int" {
		t.Errorf("--limit is a %s, want int", flag.Value.Type())
	}
	if flag.DefValue == "0" {
		t.Error("--limit defaults to 0 (no limit), so the default invocation is unbounded; it exists to bound the default rendering")
	}
}

// TestDepCyclesTruncatesTheRenderedListButNotTheCount pins the cap's contract.
// --include-tracks is bounded by the scheduling-edge count and not by the number
// of real deadlocks, so the printed LIST is capped — but the reported total stays
// the whole report's and the truncation announces itself, because a diagnostic
// that quietly dropped rows would understate the very problem it exists to show.
func TestDepCyclesTruncatesTheRenderedListButNotTheCount(t *testing.T) {
	var report issueops.CycleReport
	for i := range 5 {
		id := fmt.Sprintf("bd-%d", i)
		report.Cycles = append(report.Cycles, issueops.Cycle{Members: []issueops.CycleMember{
			{ID: id, Issue: &types.Issue{ID: id, Title: "Cycle " + id}},
		}})
	}

	t.Run("capped", func(t *testing.T) {
		out := captureCycleStdout(t, func() { printCycleReportForTest(t, report, 2) })
		if !strings.Contains(out, "Found 5 dependency cycles") {
			t.Errorf("the total is not the whole report's; a cap must not shrink the count:\n%s", out)
		}
		for _, shown := range []string{"bd-0", "bd-1"} {
			if !strings.Contains(out, shown) {
				t.Errorf("%s is inside the cap but was not printed:\n%s", shown, out)
			}
		}
		for _, held := range []string{"bd-2", "bd-3", "bd-4"} {
			if strings.Contains(out, held) {
				t.Errorf("%s is past the cap but was printed anyway:\n%s", held, out)
			}
		}
		if !strings.Contains(out, "and 3 more not shown") {
			t.Errorf("the truncation is silent, so a reader cannot tell the list is partial:\n%s", out)
		}
		if !strings.Contains(out, "--limit 0") {
			t.Errorf("the truncation does not name the way to print the rest:\n%s", out)
		}
	})

	// The uncapped arm is what proves the cap is doing the work above, rather
	// than the fixture or the renderer dropping cycles on its own.
	t.Run("no limit", func(t *testing.T) {
		out := captureCycleStdout(t, func() { printCycleReportForTest(t, report, 0) })
		for _, id := range []string{"bd-0", "bd-1", "bd-2", "bd-3", "bd-4"} {
			if !strings.Contains(out, id) {
				t.Errorf("--limit 0 held back %s:\n%s", id, out)
			}
		}
		if strings.Contains(out, "not shown") {
			t.Errorf("--limit 0 announced a truncation it did not make:\n%s", out)
		}
	})

	// Both arms above drive an explicit limit, so the value that actually SHIPS
	// would otherwise be pinned only by TestDepCyclesLimitFlagRegistration's
	// "not 0" assertion — a registration property, not a behavior. This arm
	// reads the default off the command and renders past it, so it keeps
	// exercising whatever the command registers. Ids are fixed-width so that
	// "past the cap" cannot match as a prefix of an id inside it.
	t.Run("registered default", func(t *testing.T) {
		registered, err := depCyclesCmd.Flags().GetInt("limit")
		if err != nil {
			t.Fatalf("reading the registered --limit default: %v", err)
		}
		if registered <= 0 {
			t.Fatalf("the registered --limit default is %d, so this arm would cap nothing", registered)
		}

		var big issueops.CycleReport
		for i := range registered + 3 {
			id := fmt.Sprintf("bd-big-%03d", i)
			big.Cycles = append(big.Cycles, issueops.Cycle{Members: []issueops.CycleMember{
				{ID: id, Issue: &types.Issue{ID: id, Title: "Cycle " + id}},
			}})
		}

		out := captureCycleStdout(t, func() { printCycleReportForTest(t, big, registered) })
		if want := fmt.Sprintf("Found %d dependency cycles", registered+3); !strings.Contains(out, want) {
			t.Errorf("the registered default shrank the count; want %q:\n%s", want, out)
		}
		if last := fmt.Sprintf("bd-big-%03d", registered-1); !strings.Contains(out, last) {
			t.Errorf("%s is the last cycle inside the registered default but was not printed:\n%s", last, out)
		}
		if first := fmt.Sprintf("bd-big-%03d", registered); strings.Contains(out, first) {
			t.Errorf("%s is past the registered default but was printed anyway:\n%s", first, out)
		}
		if !strings.Contains(out, "and 3 more not shown") {
			t.Errorf("the registered default truncated silently:\n%s", out)
		}
	})
}

// TestRunDepCyclesThreadsIncludeTracks pins dep_cycles.go's one line of real
// logic for this option: the includeTracks parameter lands on
// DetectCyclesRequest.IncludeTracks unchanged, in both directions.
func TestRunDepCyclesThreadsIncludeTracks(t *testing.T) {
	for _, includeTracks := range []bool{false, true} {
		recorder := &recordingCycleStore{}
		previousStore := store
		previousJSON := jsonOutput
		store = recorder
		jsonOutput = false
		func() {
			defer func() {
				store = previousStore
				jsonOutput = previousJSON
			}()
			if err := runDepCycles(includeTracks, 0); err != nil {
				t.Fatalf("runDepCycles(%v): %v", includeTracks, err)
			}
		}()

		if recorder.gotRequest.IncludeTracks != includeTracks {
			t.Errorf("runDepCycles(%v) called DetectCycles with IncludeTracks=%v",
				includeTracks, recorder.gotRequest.IncludeTracks)
		}
	}
}

// recordingCycleStore is a store whose cycle detector remembers the last
// request it was asked to detect, so the flag-threading test can look inside
// runDepCycles rather than only at its rendered output.
type recordingCycleStore struct {
	storage.DoltStorage
	gotRequest issueops.DetectCyclesRequest
}

func (s *recordingCycleStore) CycleDetector() (issueops.CycleDetector, error) {
	return &recordingCycleDetector{store: s}, nil
}

type recordingCycleDetector struct{ store *recordingCycleStore }

func (d *recordingCycleDetector) DetectCycles(_ context.Context, req issueops.DetectCyclesRequest) (issueops.CycleReport, error) {
	d.store.gotRequest = req
	return issueops.CycleReport{}, nil
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
