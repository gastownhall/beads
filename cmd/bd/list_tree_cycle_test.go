package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// captureBoundedStdout runs fn with os.Stdout redirected and returns what it
// wrote and whether it returned before the deadline.
//
// It exists for tests whose failure mode is "never returns": the reader stops
// after captureLimit bytes, so a runaway writer blocks on the full pipe instead of
// growing a buffer until the host swaps, and os.Stdout is restored before the
// function returns, so a failing test does not leave the package's stdout
// redirected for every later test (the leak that poisoned earlier attempts at
// this guard). On deadline the writer goroutine is left parked on the pipe.
// parkedCaptures keeps the pipe ends of every timed-out capture reachable for
// the life of the test binary. The runaway writer is parked on the full pipe;
// if the read end were garbage-collected its finalizer would close it, the
// parked write would fail with EPIPE, and the writer's next Printf would
// resolve os.Stdout afresh and resume against the real stdout or the next
// test's capture.
var parkedCaptures []*os.File

const (
	// captureLimit bounds what a capture keeps; past it the writer blocks on
	// the full pipe instead of growing a buffer.
	captureLimit = 1 << 20
	// captureDeadline is how long a walk may run before it is declared hung.
	captureDeadline = 10 * time.Second
)

func captureBoundedStdout(t *testing.T, fn func()) (out string, terminated bool) {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	read := make(chan string, 1)
	go func() {
		buf := make([]byte, captureLimit)
		n := 0
		for n < captureLimit {
			m, err := r.Read(buf[n:])
			n += m
			if err != nil {
				break
			}
		}
		read <- string(buf[:n])
	}()

	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()

	select {
	case <-done:
		terminated = true
		os.Stdout = oldStdout
		_ = w.Close()
		out = <-read
		_ = r.Close()
	case <-time.After(captureDeadline):
		os.Stdout = oldStdout
		// Leave both pipe ends open, and referenced, on purpose: parked on
		// the full pipe the runaway writer costs nothing and cannot escape.
		parkedCaptures = append(parkedCaptures, r, w)
		select {
		case out = <-read:
		case <-time.After(time.Second):
		}
	}
	return out, terminated
}

func cycleTestIssue(id, title, issueType string) *types.Issue {
	return &types.Issue{ID: id, Title: title, IssueType: types.IssueType(issueType), Status: types.StatusOpen, Priority: 1}
}

// The renderer must terminate on a cyclic childrenMap and show the repeated
// ancestor exactly once, marked. Titles are distinct from IDs so an ID count
// counts rendered lines, not title echoes.
func TestPrintPrettyTree_CycleRendersAncestorOnce(t *testing.T) {
	a := cycleTestIssue("bd-a", "alpha", "epic")
	b := cycleTestIssue("bd-b", "beta", "epic")
	c := cycleTestIssue("bd-c", "gamma", "task")
	childrenMap := map[string][]*types.Issue{
		"bd-a": {b},
		"bd-b": {a, c}, // b -> a closes the cycle; c is an ordinary leaf
	}

	out, terminated := captureBoundedStdout(t, func() {
		printPrettyTree(childrenMap, "bd-a", "", nil)
	})
	if !terminated {
		t.Fatalf("printPrettyTree did not return within %v on a cyclic tree; first output:\n%s", captureDeadline, head(out, 20))
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected exactly 3 lines (b, a marked as cycle, c), got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "bd-b") || strings.Contains(lines[0], treeCycleMarker) {
		t.Fatalf("line 1 should be b without a marker:\n%s", out)
	}
	if !strings.Contains(lines[1], "bd-a") || !strings.Contains(lines[1], treeCycleMarker) {
		t.Fatalf("line 2 should be the ancestor a carrying the cycle marker:\n%s", out)
	}
	if !strings.Contains(lines[2], "bd-c") || strings.Contains(lines[2], treeCycleMarker) {
		t.Fatalf("line 3 should be the sibling leaf c without a marker:\n%s", out)
	}
	if n := strings.Count(out, "bd-a"); n != 1 {
		t.Fatalf("ancestor bd-a must render exactly once, got %d:\n%s", n, out)
	}
}

// A node reachable through two parents is a diamond, not a cycle: it must
// render under both and carry no marker. This pins the guard to the ancestor
// path so it cannot drift into a whole-walk visited set.
func TestPrintPrettyTree_DiamondRendersUnderBothParents(t *testing.T) {
	a := cycleTestIssue("bd-a", "alpha", "epic")
	b := cycleTestIssue("bd-b", "beta", "epic")
	c := cycleTestIssue("bd-c", "shared leaf", "task")
	childrenMap := map[string][]*types.Issue{
		"bd-a": {b, c},
		"bd-b": {c},
	}

	out, terminated := captureBoundedStdout(t, func() {
		printPrettyTree(childrenMap, "bd-a", "", nil)
	})
	if !terminated {
		t.Fatalf("printPrettyTree did not return on an acyclic diamond:\n%s", head(out, 20))
	}
	if n := strings.Count(out, "bd-c"); n != 2 {
		t.Fatalf("shared child must render under both parents (2), got %d:\n%s", n, out)
	}
	if strings.Contains(out, treeCycleMarker) {
		t.Fatalf("diamond wrongly reported as a cycle:\n%s", out)
	}
	_ = a
}

// End to end through the public path: a rooted parent-child cycle in real
// dependency rows (R <- A <- B <- A) reaches the renderer via
// buildIssueTreeWithDeps, so this covers rows that bypassed dep-add
// validation (JSONL import, older builds). Without the guard this hangs.
func TestDisplayPrettyList_RootedParentChildCycleTerminates(t *testing.T) {
	r := cycleTestIssue("bd-r", "root", "epic")
	a := cycleTestIssue("bd-a", "alpha", "epic")
	b := cycleTestIssue("bd-b", "beta", "epic")
	issues := []*types.Issue{r, a, b}
	allDeps := map[string][]*types.Dependency{
		"bd-a": {
			{IssueID: "bd-a", DependsOnID: "bd-r", Type: types.DepParentChild},
			{IssueID: "bd-a", DependsOnID: "bd-b", Type: types.DepParentChild},
		},
		"bd-b": {
			{IssueID: "bd-b", DependsOnID: "bd-a", Type: types.DepParentChild},
		},
	}

	roots, childrenMap := buildIssueTreeWithDeps(issues, allDeps)
	if len(roots) != 1 || roots[0].ID != "bd-r" {
		t.Fatalf("fixture must have the single root bd-r, got %v", roots)
	}
	if len(childrenMap["bd-a"]) != 1 || len(childrenMap["bd-b"]) != 1 {
		t.Fatalf("fixture must carry the a <-> b hierarchy cycle, got %v", childrenMap)
	}

	out, terminated := captureBoundedStdout(t, func() {
		displayPrettyListWithDeps(issues, false, allDeps, false, false, "")
	})
	if !terminated {
		t.Fatalf("bd list tree rendering did not return within %v on a parent-child cycle; first output:\n%s", captureDeadline, head(out, 20))
	}
	if n := strings.Count(out, treeCycleMarker); n != 1 {
		t.Fatalf("expected exactly one cycle marker, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, "bd-a"); n != 2 {
		t.Fatalf("bd-a must render once as a node and once as the marked ancestor (2), got %d:\n%s", n, out)
	}
	for _, id := range []string{"bd-r", "bd-b"} {
		if n := strings.Count(out, id); n != 1 {
			t.Fatalf("%s must render exactly once, got %d:\n%s", id, n, out)
		}
	}
}

func head(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
