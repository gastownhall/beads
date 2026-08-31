package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// captureBoundedStdout runs fn with os.Stdout redirected and returns what it
// printed, discarding anything past limit.
//
// The package already has captureStdout (test_helpers_pure_test.go), but it
// buffers without a cap: against the unguarded renderer, whose output on a
// cyclic graph is unbounded, that helper would exhaust memory instead of
// failing the test. This variant caps the read and keeps draining so the writer
// never blocks on a full pipe. It takes the same stdioMutex to stay race-free
// with the existing helper.
func captureBoundedStdout(t *testing.T, limit int64, fn func()) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, io.LimitReader(r, limit))
		// Drain any remainder so the writer never blocks on a full pipe.
		_, _ = io.Copy(io.Discard, r)
		done <- buf.String()
	}()

	fn()

	os.Stdout = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// epicBlockedByItsOwnChild builds the shape reported in GH#5887: an epic and
// its child, where the epic is also blocked by that child.
//
//	child --parent-child--> epic     (child belongs to the epic)
//	epic  --blocks-------->  child   (epic is blocked by its own child)
//
// "An epic is blocked until one of its children is done" is ordinary modelling,
// so no user error is required to reach the failure — which is what made this
// worse than the molecule-traversal cycle (GH#2719), where the cycle needed
// someone to bond two molecules in both directions.
func epicBlockedByItsOwnChild() ([]*types.Issue, map[string][]*types.Dependency) {
	now := time.Now()
	mk := func(id string) *types.Issue {
		return &types.Issue{
			ID:        id,
			Title:     "epic " + id,
			IssueType: "epic",
			Status:    "open",
			Priority:  1,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	parent, child := mk("bd-ep41"), mk("bd-ep40")

	deps := map[string][]*types.Dependency{
		child.ID:  {{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}},
		parent.ID: {{IssueID: parent.ID, DependsOnID: child.ID, Type: types.DepBlocks}},
	}
	return []*types.Issue{parent, child}, deps
}

// TestBuildIssueTreeWithDeps_BlocksIsNotHierarchy pins the structural half:
// nesting follows the parent-child edge only, so the blocks edge must not nest
// the parent back under its own child.
func TestBuildIssueTreeWithDeps_BlocksIsNotHierarchy(t *testing.T) {
	issues, deps := epicBlockedByItsOwnChild()
	parent, child := issues[0], issues[1]

	roots, childrenMap := buildIssueTreeWithDeps(issues, deps)

	if len(roots) != 1 || roots[0].ID != parent.ID {
		t.Errorf("roots = %v, want exactly [%s]", roots, parent.ID)
	}
	if got := childrenMap[parent.ID]; len(got) != 1 || got[0].ID != child.ID {
		t.Errorf("childrenMap[%s] = %v, want [%s]", parent.ID, got, child.ID)
	}
	if got := childrenMap[child.ID]; len(got) != 0 {
		t.Errorf("childrenMap[%s] = %v, want empty: a blocks edge is not containment",
			child.ID, got)
	}
}

// TestPrintPrettyTree_TerminatesOnCycle is the regression guard for GH#5887.
// It forces a cycle directly into childrenMap — bypassing the structural rule —
// so the renderer's own defenses are what is under test. Without them this
// never returns.
func TestPrintPrettyTree_TerminatesOnCycle(t *testing.T) {
	issues, _ := epicBlockedByItsOwnChild()
	a, b := issues[0], issues[1]

	childrenMap := map[string][]*types.Issue{
		a.ID: {b},
		b.ID: {a},
	}

	const limit = 1 << 20 // 1 MiB is orders of magnitude above any sane output

	finished := make(chan string, 1)
	go func() {
		finished <- captureBoundedStdout(t, limit, func() {
			printPrettyTree(childrenMap, a.ID, "", nil)
		})
	}()

	select {
	case out := <-finished:
		if n := strings.Count(out, b.ID); n > maxTreeDepth+1 {
			t.Errorf("%s rendered %d times, want <= %d: cycle is not being cut",
				b.ID, n, maxTreeDepth+1)
		}
		if len(out) >= limit {
			t.Errorf("output hit the %d byte cap: renderer is still unbounded", limit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("printPrettyTree did not terminate on a cyclic childrenMap")
	}
}

// TestDisplayPrettyList_CycleEndToEnd exercises the public entry point on the
// reported graph, proving `bd list` as a whole terminates.
func TestDisplayPrettyList_CycleEndToEnd(t *testing.T) {
	issues, deps := epicBlockedByItsOwnChild()

	finished := make(chan string, 1)
	go func() {
		finished <- captureBoundedStdout(t, 1<<20, func() {
			displayPrettyListWithDeps(issues, false, deps, false, false)
		})
	}()

	select {
	case out := <-finished:
		if !strings.Contains(out, "Total: 2 issues") {
			t.Errorf("summary missing; got:\n%s", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("displayPrettyListWithDeps did not terminate on the GH#5887 graph")
	}
}

// TestPrintPrettyTree_DiamondStillRendersBothPaths guards against over-cutting:
// the visited set is scoped to the ancestor path, not the whole walk, so a node
// legitimately reachable through two different parents must render under each.
func TestPrintPrettyTree_DiamondStillRendersBothPaths(t *testing.T) {
	now := time.Now()
	mk := func(id string) *types.Issue {
		return &types.Issue{
			ID: id, Title: id, IssueType: "task", Status: "open",
			Priority: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	root, left, right, leaf := mk("bd-d0"), mk("bd-d1"), mk("bd-d2"), mk("bd-d3")
	// Distinct title so counting occurrences of the ID is not doubled by the
	// title column, which formatPrettyIssue also prints.
	leaf.Title = "shared leaf"

	childrenMap := map[string][]*types.Issue{
		root.ID:  {left, right},
		left.ID:  {leaf},
		right.ID: {leaf},
	}

	out := captureBoundedStdout(t, 1<<20, func() {
		printPrettyTree(childrenMap, root.ID, "", nil)
	})

	if n := strings.Count(out, leaf.ID); n != 2 {
		t.Errorf("%s rendered %d times, want 2 (once under each parent):\n%s",
			leaf.ID, n, out)
	}
	if strings.Contains(out, "(cycle)") {
		t.Errorf("diamond wrongly reported as a cycle:\n%s", out)
	}
}
