//go:build cgo

package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedGateInheritsDownParentChain pins the property that a gate on a
// parent hides the parent AND every child from `bd ready` — children that
// existed when the gate was created and children created afterwards alike —
// and that resolving the gate frees all of them. The wyvern rig's operator
// tooling relies on this (its own gate labels are a snapshot; the bd ready
// fence is not), so a regression here would silently put shelved work back
// on agent fronts. Mechanism under test: a gate is a blocks-dependency onto
// the gate issue, and the parent-child leg of the blocked-consistency
// recompute cascades a parent's blockedness to its children.
func TestEmbeddedGateInheritsDownParentChain(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gi")
	store := openStore(t, beadsDir, "gi")
	if err := store.SetConfig(t.Context(), "types.custom", `["gate"]`); err != nil {
		t.Fatalf("SetConfig types.custom: %v", err)
	}
	store.Close()

	ready := func() string {
		t.Helper()
		cmd := exec.Command(bd, "ready", "--limit", "0")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd ready failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	// An issue counts as listed only when its ID is a whole token of the
	// output. Neither its title nor a substring of its ID will do: every
	// ready child of an epic carries a "← <epic title>" annotation, and a
	// child's hierarchical ID (<epic>.N) contains the epic's ID, so either
	// looser match would let a child row stand in for the epic.
	wantReady := func(out string, issue *types.Issue, want bool, when string) {
		t.Helper()
		if got := slices.Contains(strings.Fields(out), issue.ID); got != want {
			t.Errorf("%s: %s (%q) in ready = %v, want %v\n%s", when, issue.ID, issue.Title, got, want, out)
		}
	}

	epic := bdCreate(t, bd, dir, "Inherit epic", "--type", "epic")
	one := bdCreate(t, bd, dir, "Inherit child one", "--type", "task", "--parent", epic.ID)
	two := bdCreate(t, bd, dir, "Inherit child two", "--type", "task", "--parent", epic.ID)

	out := ready()
	wantReady(out, epic, true, "before gate")
	wantReady(out, one, true, "before gate")
	wantReady(out, two, true, "before gate")

	gateOut := bdGate(t, bd, dir, "create", "--blocks", epic.ID, "--reason", "hold the whole tree")
	var gateID string
	for _, word := range strings.Fields(gateOut) {
		if strings.HasPrefix(word, "gi-") {
			gateID = word
			break
		}
	}
	if gateID == "" {
		t.Fatalf("could not extract gate ID from output: %s", gateOut)
	}

	out = ready()
	wantReady(out, epic, false, "gate open, existing children")
	wantReady(out, one, false, "gate open, existing children")
	wantReady(out, two, false, "gate open, existing children")

	// A child filed AFTER the gate must be born hidden too: the fence is a
	// property of the tree at read time, not a snapshot at gate time.
	late := bdCreate(t, bd, dir, "Inherit child late", "--type", "task", "--parent", epic.ID)
	out = ready()
	wantReady(out, late, false, "gate open, later-born child")

	bdGate(t, bd, dir, "resolve", gateID, "--reason", "released")
	out = ready()
	wantReady(out, epic, true, "after resolve")
	wantReady(out, one, true, "after resolve")
	wantReady(out, two, true, "after resolve")
	wantReady(out, late, true, "after resolve")
}
