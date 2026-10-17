//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestOrderCloseCascadeDepths is the pure ordering core of --cascade: every
// descendant closes before the ancestors that count it against the engine's
// open-children guard, typed items keep their argument slots, and discovered
// items take the slots after them. A typed id that is also another typed id's
// descendant must order by its discovered depth, not its argument position.
func TestOrderCloseCascadeDepths(t *testing.T) {
	root := closeDirectItem{arg: 0, id: "root", reason: "r"}
	leaf := closeDirectItem{arg: 1, id: "leaf", reason: "r"}
	exp := &cascadeExpansion{
		// The walk discovered nothing new: "leaf" was deduped into the typed
		// list, but the re-depth still recorded it at depth 2 under root.
		items: []cascadeItem{},
		depths: map[string]int{
			"root": 0,
			"leaf": 2,
		},
	}
	got := orderCloseCascade([]closeDirectItem{root, leaf}, 2, exp)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].id != "leaf" || got[1].id != "root" {
		t.Fatalf("order: got [%s, %s], want [leaf, root]", got[0].id, got[1].id)
	}
	if got[0].arg != 1 || got[1].arg != 0 {
		t.Fatalf("argument slots: got [%d, %d], want [1, 0]", got[0].arg, got[1].arg)
	}

	// Discovered items slot after the typed ones, deepest first.
	exp2 := &cascadeExpansion{
		items: []cascadeItem{
			{id: "mid", reason: "r", depth: 1},
			{id: "grand", reason: "r", depth: 2},
		},
		depths: map[string]int{"root": 0, "mid": 1, "grand": 2},
	}
	got2 := orderCloseCascade([]closeDirectItem{root}, 1, exp2)
	if len(got2) != 3 {
		t.Fatalf("got %d items, want 3", len(got2))
	}
	wantOrder := []string{"grand", "mid", "root"}
	for i, want := range wantOrder {
		if got2[i].id != want {
			t.Fatalf("order[%d]: got %s, want %s", i, got2[i].id, want)
		}
	}
	// Slots follow discovery order from argBase (mid=1, grand=2) and
	// survive the sort attached to their item — that pairing is what maps
	// each cascade item to its outcome slot in the report loop.
	if got2[0].arg != 2 || got2[1].arg != 1 {
		t.Fatalf("discovered slots: got [%d, %d], want [2, 1]", got2[0].arg, got2[1].arg)
	}

	// Nil expansion is the no-cascade pass-through.
	if passthrough := orderCloseCascade([]closeDirectItem{root}, 1, nil); len(passthrough) != 1 || passthrough[0].id != "root" {
		t.Fatal("nil expansion must return the typed items untouched")
	}
}

// TestCascadeSettleDepthsLongestPath pins the depth settlement the batch
// order reads: longest root-to-node path over the recorded edges, not any
// single walk's level counter. The first shape is the review's r2 defect 1 —
// P → C → G with both P and C typed: C must land at 1 (under P) and G at 2
// (under C), so G closes before C and C before P regardless of which root's
// walk found G first. The second is the diamond: X under two parents takes
// the deeper path.
func TestCascadeSettleDepthsLongestPath(t *testing.T) {
	exp := &cascadeExpansion{
		depths:  map[string]int{"P": 0, "C": 0},
		parents: map[string][]string{"C": {"P"}, "G": {"C"}},
	}
	exp.settleDepths()
	if exp.depths["P"] != 0 || exp.depths["C"] != 1 || exp.depths["G"] != 2 {
		t.Fatalf("typed mid depths: got P=%d C=%d G=%d, want 0/1/2", exp.depths["P"], exp.depths["C"], exp.depths["G"])
	}

	exp2 := &cascadeExpansion{
		depths:  map[string]int{"D": 0},
		parents: map[string][]string{"A": {"D"}, "B": {"D"}, "X": {"A", "B"}},
	}
	exp2.settleDepths()
	if exp2.depths["A"] != 1 || exp2.depths["B"] != 1 || exp2.depths["X"] != 2 {
		t.Fatalf("diamond depths: got A=%d B=%d X=%d, want 1/1/2", exp2.depths["A"], exp2.depths["B"], exp2.depths["X"])
	}
}

// countCloseLine counts the report lines an id earned, so a duplicated item
// (a typed id the cascade also discovered) cannot hide.
func countCloseLine(out, id string) int {
	return strings.Count(out, "Closed "+id+" ")
}

// TestEmbeddedCloseCascade covers `bd close --cascade` against a real
// embedded store: descendants close before their ancestors in the batch's
// one transaction (the epic lands WITHOUT --force, which only the guard
// passing in-transaction can prove), closed descendants are untouched, a
// blocked descendant prunes its subtree without --force, a closed parent is
// recovered without being re-closed, and a typed id that is also a
// descendant of another typed id closes exactly once.
func TestEmbeddedCloseCascade(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cc")

	t.Run("closes_descendants_deepest_first", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Cascade epic", "--type", "epic")
		childA := bdCreate(t, bd, dir, "Child A")
		childB := bdCreate(t, bd, dir, "Child B")
		grandchild := bdCreate(t, bd, dir, "Grandchild")
		bdDepAdd(t, bd, dir, childA.ID, epic.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, childB.ID, epic.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, grandchild.ID, childA.ID, "--type", "parent-child")

		// No --force: the epic's close may only land if its children closed
		// earlier in the SAME transaction, which is the ordering guarantee.
		out := bdClose(t, bd, dir, epic.ID, "--cascade", "--reason", "epic done")
		for _, id := range []string{epic.ID, childA.ID, childB.ID, grandchild.ID} {
			if !strings.Contains(out, "Closed "+id) {
				t.Errorf("output missing close of %s:\n%s", id, out)
			}
		}
		for _, id := range []string{epic.ID, childA.ID, childB.ID, grandchild.ID} {
			if got := bdShow(t, bd, dir, id); got.Status != types.StatusClosed {
				t.Errorf("%s status: got %q, want closed", id, got.Status)
			}
		}
		if got := bdShow(t, bd, dir, grandchild.ID); got.CloseReason != "epic done" {
			t.Errorf("grandchild close_reason: got %q, want %q", got.CloseReason, "epic done")
		}
	})

	t.Run("leaves_closed_descendants_alone", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Mixed epic", "--type", "epic")
		openChild := bdCreate(t, bd, dir, "Open child")
		closedChild := bdCreate(t, bd, dir, "Closed child")
		bdDepAdd(t, bd, dir, openChild.ID, epic.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, closedChild.ID, epic.ID, "--type", "parent-child")
		bdClose(t, bd, dir, closedChild.ID, "--reason", "done earlier")

		bdClose(t, bd, dir, epic.ID, "--cascade", "--reason", "mixed")
		if got := bdShow(t, bd, dir, closedChild.ID); got.CloseReason != "done earlier" {
			t.Errorf("closed descendant was touched: close_reason %q, want %q", got.CloseReason, "done earlier")
		}
		if got := bdShow(t, bd, dir, openChild.ID); got.Status != types.StatusClosed {
			t.Errorf("open child status: got %q, want closed", got.Status)
		}
	})

	t.Run("blocked_descendant_refuses_then_force", func(t *testing.T) {
		root := bdCreate(t, bd, dir, "Blocked root", "--type", "epic")
		mid := bdCreate(t, bd, dir, "Blocked mid")
		leaf := bdCreate(t, bd, dir, "Blocked leaf")
		bdDepAdd(t, bd, dir, mid.ID, root.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, leaf.ID, mid.ID, "--type", "parent-child")
		blocker := bdCreate(t, bd, dir, "External blocker")
		bdDepAdd(t, bd, dir, leaf.ID, blocker.ID, "--type", "blocked-by")

		// Without --force the leaf's blocker refusal propagates up: the leaf
		// stays open and the open-children guard refuses every ancestor of
		// it, so nothing in this subtree closes.
		out := bdCloseFail(t, bd, dir, root.ID, "--cascade")
		if !strings.Contains(out, "blocked by") {
			t.Errorf("expected the leaf's blocked refusal, got:\n%s", out)
		}
		for _, id := range []string{root.ID, mid.ID, leaf.ID} {
			if got := bdShow(t, bd, dir, id); got.Status == types.StatusClosed {
				t.Errorf("%s closed without --force", id)
			}
		}

		// --force sweeps the whole subtree in one batch.
		bdClose(t, bd, dir, root.ID, "--cascade", "--force", "--reason", "forced")
		for _, id := range []string{root.ID, mid.ID, leaf.ID} {
			if got := bdShow(t, bd, dir, id); got.Status != types.StatusClosed {
				t.Errorf("%s status after --force: got %q, want closed", id, got.Status)
			}
		}
	})

	t.Run("recovers_closed_parent", func(t *testing.T) {
		// The #3681 recovery shape: a parent closed while its child stayed
		// open. The cascade re-close is a no-op on the parent itself and
		// closes the stranded child.
		root := bdCreate(t, bd, dir, "Recovered root", "--type", "epic")
		stranded := bdCreate(t, bd, dir, "Stranded child")
		bdDepAdd(t, bd, dir, stranded.ID, root.ID, "--type", "parent-child")
		bdClose(t, bd, dir, root.ID, "--force", "--reason", "closed too early")

		bdClose(t, bd, dir, root.ID, "--cascade", "--reason", "recovery")
		if got := bdShow(t, bd, dir, root.ID); got.CloseReason != "closed too early" {
			t.Errorf("closed parent was re-closed: close_reason %q, want %q", got.CloseReason, "closed too early")
		}
		if got := bdShow(t, bd, dir, stranded.ID); got.Status != types.StatusClosed || got.CloseReason != "recovery" {
			t.Errorf("stranded child: got status %q reason %q, want closed/recovery", got.Status, got.CloseReason)
		}
	})

	t.Run("typed_descendant_closes_once", func(t *testing.T) {
		root := bdCreate(t, bd, dir, "Dedupe root", "--type", "epic")
		child := bdCreate(t, bd, dir, "Dedupe child")
		bdDepAdd(t, bd, dir, child.ID, root.ID, "--type", "parent-child")

		// Both ids typed: the cascade discovers child under root but must
		// not turn it into a second item — one close, one report line.
		out := bdClose(t, bd, dir, root.ID, child.ID, "--cascade", "--reason", "both")
		if n := countCloseLine(out, child.ID); n != 1 {
			t.Errorf("child reported %d times, want 1:\n%s", n, out)
		}
		if got := bdShow(t, bd, dir, root.ID); got.Status != types.StatusClosed {
			t.Errorf("root status: got %q, want closed", got.Status)
		}
	})

	t.Run("typed_mid_subtree_both_argument_orders", func(t *testing.T) {
		// The r2 review's defect 1: a typed id inside another typed id's
		// subtree must order behind its own children even though its subtree
		// is discovered from its own root turn. P → C → G with C typed, in
		// both argument orders: all three close WITHOUT --force, which only
		// the deepest-first order allows (the guard refuses a parent whose
		// child is still open in the same transaction).
		for _, order := range []struct{ first, second string }{{"C", "P"}, {"P", "C"}} {
			root := bdCreate(t, bd, dir, "Typed mid root "+order.first+order.second, "--type", "epic")
			mid := bdCreate(t, bd, dir, "Typed mid child")
			leaf := bdCreate(t, bd, dir, "Typed mid grandchild")
			bdDepAdd(t, bd, dir, mid.ID, root.ID, "--type", "parent-child")
			bdDepAdd(t, bd, dir, leaf.ID, mid.ID, "--type", "parent-child")

			args := []string{"--cascade", "--reason", "typed mid"}
			for _, which := range []string{order.first, order.second} {
				if which == "C" {
					args = append(args, mid.ID)
				} else {
					args = append(args, root.ID)
				}
			}
			out := bdClose(t, bd, dir, args...)
			for _, id := range []string{root.ID, mid.ID, leaf.ID} {
				if got := bdShow(t, bd, dir, id); got.Status != types.StatusClosed {
					t.Errorf("order %v: %s status: got %q, want closed (without --force the guard passing proves the order)\nstdout:\n%s", order, id, got.Status, out)
				}
			}
		}
	})

	t.Run("refused_typed_arg_keeps_cascade_slots", func(t *testing.T) {
		// The r2 review's defect 2: a typed argument refused in preflight
		// keeps its nil-outcome slot, so discovered items must number after
		// EVERY typed slot — numbering them from the survivor count collides
		// a discovered item with the surviving typed one that follows it,
		// and the batch's last writer wins the shared slot. X pinned; P
		// force-closed earlier with C stranded open.
		pinned := bdCreate(t, bd, dir, "Slot pinned", "--type", "epic")
		bdUpdate(t, bd, dir, pinned.ID, "--status", "pinned")
		parent := bdCreate(t, bd, dir, "Slot parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "Slot child")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")
		bdClose(t, bd, dir, parent.ID, "--force", "--reason", "early")

		out := bdCloseFail(t, bd, dir, pinned.ID, parent.ID, "--cascade", "--json", "--reason", "recover")
		arr := parseIssuesJSON(t, out)
		counts := make(map[string]int, len(arr))
		for _, issue := range arr {
			counts[issue.ID]++
		}
		if counts[parent.ID] != 1 {
			t.Errorf("parent listed %d times in closed array, want exactly 1:\n%s", counts[parent.ID], out)
		}
		if counts[child.ID] != 1 {
			t.Errorf("child listed %d times in closed array, want exactly 1 (slot collision hides it):\n%s", counts[child.ID], out)
		}
		if counts[pinned.ID] != 0 {
			t.Errorf("pinned refusal must not appear in the closed array:\n%s", out)
		}
		if !strings.Contains(out, "cannot modify pinned issue "+pinned.ID) {
			t.Errorf("output missing the pinned refusal:\n%s", out)
		}
		if got := bdShow(t, bd, dir, child.ID); got.Status != types.StatusClosed || got.CloseReason != "recover" {
			t.Errorf("child: got status %q reason %q, want closed/recover", got.Status, got.CloseReason)
		}
	})

	t.Run("json_includes_descendants", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "JSON epic", "--type", "epic")
		child := bdCreate(t, bd, dir, "JSON child")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		out := bdClose(t, bd, dir, epic.ID, "--cascade", "--json")
		closed := parseIssuesJSON(t, out)
		ids := make(map[string]bool, len(closed))
		for _, issue := range closed {
			ids[issue.ID] = true
		}
		if !ids[epic.ID] || !ids[child.ID] {
			t.Errorf("json output missing cascade descendants: got %v", ids)
		}
	})
}

// parseIssuesJSON extracts the first JSON array of issues from output that
// may carry non-JSON lines around it — stderr refusals before it, the
// partial-failure summary after it. A Decoder reads exactly one top-level
// value and stops, so trailing objects on the same stream are ignored.
func parseIssuesJSON(t *testing.T, out string) []*types.Issue {
	t.Helper()
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("no JSON array found in output:\n%s", out)
	}
	dec := json.NewDecoder(strings.NewReader(out[start:]))
	var issues []*types.Issue
	if err := dec.Decode(&issues); err != nil {
		t.Fatalf("parsing issues array: %v\n%s", err, out)
	}
	return issues
}

// TestProxiedServerCloseCascade is the proxied-route parity check: the same
// flag over the unit-of-work batch closes the same subtree, and the same
// blocked-descendant refusal propagates. Lives in the proxied integration
// lane because it needs a real Dolt sql-server behind the proxy.
func TestProxiedServerCloseCascade(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("closes_descendants", func(t *testing.T) {
		p := newSharedProxiedProject(t, bd, "ccs")
		epic := bdProxiedCreate(t, bd, p.dir, "Proxied cascade epic")
		childA := bdProxiedCreate(t, bd, p.dir, "Proxied child A")
		childB := bdProxiedCreate(t, bd, p.dir, "Proxied child B")
		bdProxiedDep(t, bd, p.dir, "add", childA.ID, epic.ID, "--type", "parent-child")
		bdProxiedDep(t, bd, p.dir, "add", childB.ID, epic.ID, "--type", "parent-child")

		out, err := bdProxiedRun(t, bd, p.dir, "close", epic.ID, "--cascade", "--reason", "proxied")
		if err != nil {
			t.Fatalf("proxied cascade close failed: %v\n%s", err, out)
		}
		for _, id := range []string{epic.ID, childA.ID, childB.ID} {
			if got := bdProxiedShow(t, bd, p.dir, id); got.Status != types.StatusClosed {
				t.Errorf("%s status: got %q, want closed", id, got.Status)
			}
		}
	})

	t.Run("blocked_descendant_refuses", func(t *testing.T) {
		p := newSharedProxiedProject(t, bd, "ccb")
		root := bdProxiedCreate(t, bd, p.dir, "Proxied blocked root")
		leaf := bdProxiedCreate(t, bd, p.dir, "Proxied blocked leaf")
		bdProxiedDep(t, bd, p.dir, "add", leaf.ID, root.ID, "--type", "parent-child")
		blocker := bdProxiedCreate(t, bd, p.dir, "Proxied blocker")
		bdProxiedDep(t, bd, p.dir, "add", leaf.ID, blocker.ID, "--type", "blocked-by")

		out, err := bdProxiedRun(t, bd, p.dir, "close", root.ID, "--cascade")
		if err == nil {
			t.Fatalf("expected blocked-refusal exit, got success:\n%s", out)
		}
		if !strings.Contains(string(out), "blocked by") {
			t.Errorf("expected blocked refusal, got:\n%s", out)
		}
		if got := bdProxiedShow(t, bd, p.dir, root.ID); got.Status == types.StatusClosed {
			t.Error("root closed despite blocked descendant")
		}
	})
}
