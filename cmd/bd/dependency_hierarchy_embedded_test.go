//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func bdLinkForHierarchyTest(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, append([]string{"link"}, args...)...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd link %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestEmbeddedExplicitHierarchicalParentEdges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hg")
	grandparent := bdCreate(t, bd, dir, "Hierarchy grandparent", "--type", "epic")
	parent := bdCreate(t, bd, dir, "Hierarchy parent", "--id", grandparent.ID+".1", "--type", "task")

	t.Run("dep add immediate parent-child succeeds", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Dep child", "--id", parent.ID+".1", "--type", "task")
		out := bdDep(t, bd, dir, "add", child.ID, parent.ID, "--type", "parent-child")
		if !strings.Contains(out, "Added dependency") {
			t.Fatalf("unexpected dep add output: %s", out)
		}
	})

	t.Run("link immediate parent-child succeeds", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Link child", "--id", parent.ID+".2", "--type", "task")
		out := bdLinkForHierarchyTest(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")
		if !strings.Contains(out, "Linked") {
			t.Fatalf("unexpected link output: %s", out)
		}
	})

	t.Run("blocking immediate parent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Blocked child", "--id", parent.ID+".3", "--type", "task")
		out := bdDepFail(t, bd, dir, "add", child.ID, parent.ID, "--type", "blocks")
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected hierarchy rejection, got: %s", out)
		}
	})

	t.Run("blocks flag immediate parent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Blocks flag child", "--id", parent.ID+".4", "--type", "task")
		out := bdDepFail(t, bd, dir, parent.ID, "--blocks", child.ID)
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected hierarchy rejection, got: %s", out)
		}
	})

	t.Run("parent-child to grandparent rejected", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Grandparent child", "--id", parent.ID+".5", "--type", "task")
		out := bdDepFail(t, bd, dir, "add", child.ID, grandparent.ID, "--type", "parent-child")
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected ancestor rejection, got: %s", out)
		}
	})

	t.Run("bulk immediate parent-child succeeds and grandparent rejects", func(t *testing.T) {
		allowed := bdCreate(t, bd, dir, "Bulk child", "--id", parent.ID+".6", "--type", "task")
		allowedPath := filepath.Join(t.TempDir(), "allowed.jsonl")
		allowedBody := fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"parent-child\"}\n", allowed.ID, parent.ID)
		if err := os.WriteFile(allowedPath, []byte(allowedBody), 0o600); err != nil {
			t.Fatalf("write allowed bulk file: %v", err)
		}
		bdDep(t, bd, dir, "add", "--file", allowedPath)

		rejected := bdCreate(t, bd, dir, "Bulk grandparent child", "--id", parent.ID+".7", "--type", "task")
		rejectedPath := filepath.Join(t.TempDir(), "rejected.jsonl")
		rejectedBody := fmt.Sprintf("{\"from\":%q,\"to\":%q,\"type\":\"parent-child\"}\n", rejected.ID, grandparent.ID)
		if err := os.WriteFile(rejectedPath, []byte(rejectedBody), 0o600); err != nil {
			t.Fatalf("write rejected bulk file: %v", err)
		}
		out := bdDepFail(t, bd, dir, "add", "--file", rejectedPath)
		if !strings.Contains(out, "already a child") {
			t.Fatalf("expected bulk ancestor rejection, got: %s", out)
		}
	})

	// GH#3415: `bd list --parent <epic> --pretty` renders the tree the
	// storage.DescendantSearcher capability now fetches in one read. These
	// subtests pin the one-shot rendering contract around that walk.
	t.Run("list parent pretty all includes closed child subtree", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "GH3415 tree epic", "--type", "epic")
		open := bdCreate(t, bd, dir, "GH3415 open child", "--type", "task", "--parent", epic.ID)
		closed := bdCreate(t, bd, dir, "GH3415 closed child", "--type", "task", "--parent", epic.ID)
		grand := bdCreate(t, bd, dir, "GH3415 closed child's child", "--type", "task", "--parent", closed.ID)
		// --force: closing a parent with an open child is refused otherwise.
		if out, code := bdRunRaw(t, bd, dir, nil, "update", closed.ID, "--status", "closed", "--force"); code != 0 {
			t.Fatalf("bd update %s --status closed exited %d:\n%s", closed.ID, code, out)
		}

		all, code := bdRunRaw(t, bd, dir, nil, "list", "--parent", epic.ID, "--pretty", "--all")
		if code != 0 {
			t.Fatalf("bd list --parent --pretty --all exited %d:\n%s", code, all)
		}
		for _, id := range []string{epic.ID, open.ID, closed.ID, grand.ID} {
			if !strings.Contains(all, id) {
				t.Errorf("--pretty --all is missing %s:\n%s", id, all)
			}
		}

		pretty, code := bdRunRaw(t, bd, dir, nil, "list", "--parent", epic.ID, "--pretty")
		if code != 0 {
			t.Fatalf("bd list --parent --pretty exited %d:\n%s", code, pretty)
		}
		if !strings.Contains(pretty, open.ID) {
			t.Errorf("--pretty without --all is missing open child %s:\n%s", open.ID, pretty)
		}
		// The closed child is omitted, and so is the subtree only reachable
		// through it.
		if strings.Contains(pretty, closed.ID) {
			t.Errorf("--pretty without --all showed closed child %s:\n%s", closed.ID, pretty)
		}
		if strings.Contains(pretty, grand.ID) {
			t.Errorf("--pretty without --all showed the closed child's subtree %s:\n%s", grand.ID, pretty)
		}
	})

	// The embedded half of the capability parity contract: the same fixture
	// TestSearchDescendantsMatchesPerNodeWalk runs against the server store
	// (internal/storage/dolt), here through the embedded store the CLI
	// actually opens.
	t.Run("SearchDescendants matches the per-node walk", func(t *testing.T) {
		store := openStore(t, filepath.Join(dir, ".beads"), "hg")
		ctx := t.Context()

		const (
			rootID   = "hg-desc-root"
			aID      = "hg-desc-a"
			a1ID     = "hg-desc-a1"
			cycID    = "hg-desc-cyc"
			dottedID = "hg-desc-root.dotted"
			closedID = "hg-desc-closed"
			ccID     = "hg-desc-cc"
		)

		newIssue := func(id string, status types.Status) *types.Issue {
			return &types.Issue{ID: id, Title: "descendant fixture " + id, Status: status, IssueType: types.TypeTask}
		}
		create := func(issue *types.Issue) {
			t.Helper()
			if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", issue.ID, err)
			}
		}
		parentEdge := func(child, parent string) {
			t.Helper()
			dep := &types.Dependency{IssueID: child, DependsOnID: parent, Type: types.DepParentChild}
			if err := store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("AddDependency %s -> %s: %v", child, parent, err)
			}
		}

		create(newIssue(rootID, types.StatusOpen))
		create(newIssue(aID, types.StatusOpen))
		parentEdge(aID, rootID)
		create(newIssue(a1ID, types.StatusOpen))
		parentEdge(a1ID, aID)
		create(newIssue(cycID, types.StatusOpen))
		parentEdge(cycID, a1ID)
		// Cycle edge: the root becomes a child of its own grandchild, and the
		// walk's contract is convergence on already-broken data. The one-shot
		// AddDependencyWithOptions refuses it on the embedded store (only the
		// transaction API forwards SkipCycleCheck), so seed it inside a
		// transaction, where the per-edge cycle probe is the caller's to skip.
		err := store.RunInTransaction(ctx, "test: seed descendant-walk cycle edge", func(tx storage.Transaction) error {
			return tx.AddDependencyWithOptions(ctx,
				&types.Dependency{IssueID: rootID, DependsOnID: cycID, Type: types.DepParentChild},
				"tester", storage.DependencyAddOptions{SkipCycleCheck: true})
		})
		if err != nil {
			t.Fatalf("seed cycle edge %s -> %s: %v", rootID, cycID, err)
		}

		// A wisp child: its parent edge routes to wisp_dependencies.
		wisp := &types.Issue{Title: "descendant fixture wisp", Status: types.StatusOpen, IssueType: types.TypeTask, Ephemeral: true}
		if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
			t.Fatalf("CreateIssue (wisp): %v", err)
		}
		if wisp.ID == "" {
			ephemeral := true
			wisps, err := store.SearchIssues(ctx, "descendant fixture wisp", types.IssueFilter{Ephemeral: &ephemeral})
			if err != nil || len(wisps) == 0 {
				t.Fatalf("wisp ID unassigned and lookup failed: %v (results %d)", err, len(wisps))
			}
			wisp.ID = wisps[0].ID
		}
		parentEdge(wisp.ID, rootID)

		// An edge-less dotted-id child, matched by the id LIKE branch alone.
		create(newIssue(dottedID, types.StatusOpen))

		// A closed mid-level child with its own child.
		create(newIssue(closedID, types.StatusOpen))
		parentEdge(closedID, rootID)
		create(newIssue(ccID, types.StatusOpen))
		parentEdge(ccID, closedID)
		if err := store.CloseIssue(ctx, closedID, "done", "tester", "s1"); err != nil {
			t.Fatalf("CloseIssue %s: %v", closedID, err)
		}

		perNodeWalk := func(filter types.IssueFilter) []string {
			t.Helper()
			var out []string
			seen := make(map[string]bool)
			var visit func(node string)
			visit = func(node string) {
				nodeFilter := filter
				nodeFilter.ParentID = &node
				nodeFilter.Limit = 0
				children, err := store.SearchIssues(ctx, "", nodeFilter)
				if err != nil {
					t.Fatalf("per-node walk under %s: %v", node, err)
				}
				for _, child := range children {
					if seen[child.ID] {
						continue
					}
					seen[child.ID] = true
					out = append(out, child.ID)
					visit(child.ID)
				}
			}
			visit(rootID)
			return out
		}
		ids := func(issues []*types.Issue) []string {
			out := make([]string, len(issues))
			for i, issue := range issues {
				out[i] = issue.ID
			}
			slices.Sort(out)
			return out
		}

		// Unfiltered: every child shape is present and the cycle converges —
		// the root itself comes back (it is now a child of its own
		// grandchild), exactly as the per-node walk always answered it.
		got, err := store.SearchDescendants(ctx, rootID, types.IssueFilter{})
		if err != nil {
			t.Fatalf("SearchDescendants: %v", err)
		}
		gotIDs := ids(got)
		want := []string{rootID, aID, a1ID, cycID, wisp.ID, dottedID, closedID, ccID}
		slices.Sort(want)
		if !slices.Equal(gotIDs, want) {
			t.Fatalf("SearchDescendants IDs = %v, want %v", gotIDs, want)
		}
		walk := perNodeWalk(types.IssueFilter{})
		slices.Sort(walk)
		if !slices.Equal(gotIDs, walk) {
			t.Fatalf("SearchDescendants IDs = %v, per-node walk = %v", gotIDs, walk)
		}

		// Status: open prunes the closed child and its subtree, on both the
		// capability and the walk.
		openStatus := types.StatusOpen
		openGot, err := store.SearchDescendants(ctx, rootID, types.IssueFilter{Status: &openStatus})
		if err != nil {
			t.Fatalf("SearchDescendants (open): %v", err)
		}
		openWant := []string{rootID, aID, a1ID, cycID, wisp.ID, dottedID}
		slices.Sort(openWant)
		openWalk := perNodeWalk(types.IssueFilter{Status: &openStatus})
		slices.Sort(openWalk)
		if !slices.Equal(ids(openGot), openWant) || !slices.Equal(ids(openGot), openWalk) {
			t.Fatalf("SearchDescendants (open) IDs = %v, want %v, per-node walk (open) = %v", ids(openGot), openWant, openWalk)
		}
	})
}
