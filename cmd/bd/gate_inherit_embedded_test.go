//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
	wantReady := func(out string, title string, want bool, when string) {
		t.Helper()
		if got := strings.Contains(out, title); got != want {
			t.Errorf("%s: %q in ready = %v, want %v\n%s", when, title, got, want, out)
		}
	}

	epic := bdCreate(t, bd, dir, "Inherit epic", "--type", "epic")
	bdCreate(t, bd, dir, "Inherit child one", "--type", "task", "--parent", epic.ID)
	bdCreate(t, bd, dir, "Inherit child two", "--type", "task", "--parent", epic.ID)

	out := ready()
	wantReady(out, "Inherit epic", true, "before gate")
	wantReady(out, "Inherit child one", true, "before gate")
	wantReady(out, "Inherit child two", true, "before gate")

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
	wantReady(out, "Inherit epic", false, "gate open, existing children")
	wantReady(out, "Inherit child one", false, "gate open, existing children")
	wantReady(out, "Inherit child two", false, "gate open, existing children")

	// A child filed AFTER the gate must be born hidden too: the fence is a
	// property of the tree at read time, not a snapshot at gate time.
	bdCreate(t, bd, dir, "Inherit child late", "--type", "task", "--parent", epic.ID)
	out = ready()
	wantReady(out, "Inherit child late", false, "gate open, later-born child")

	bdGate(t, bd, dir, "resolve", gateID, "--reason", "released")
	out = ready()
	wantReady(out, "Inherit epic", true, "after resolve")
	wantReady(out, "Inherit child one", true, "after resolve")
	wantReady(out, "Inherit child two", true, "after resolve")
	wantReady(out, "Inherit child late", true, "after resolve")
}
