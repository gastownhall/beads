//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedExternalCapabilityGuardsDirectClose drives the direct `bd close`
// route, which reaches the BatchCloser role through the store chain. An issue
// whose `external:` blocker is unsatisfied must refuse to close without
// --force, exactly as `bd update --status closed` and the proxied route
// refuse, and --claim-next must not hand it out.
func TestEmbeddedExternalCapabilityGuardsDirectClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xc")
	// No external_projects entry: the capability is unresolvable, which the
	// policy treats as unsatisfied (fail closed).
	held := bdCreate(t, bd, dir, "Waits for payments", "--type", "task", "--priority", "0")
	free := bdCreate(t, bd, dir, "Free work", "--type", "task", "--priority", "2")
	done := bdCreate(t, bd, dir, "Finished work", "--type", "task", "--priority", "3")
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("dep", "add", held.ID, "external:remote:payments"); err != nil {
		t.Fatalf("bd dep add: %v\n%s", err, out)
	}

	if out, err := run("close", done.ID, "--claim-next"); err != nil {
		t.Fatalf("bd close --claim-next: %v\n%s", err, out)
	}
	if got := bdShow(t, bd, dir, held.ID); got.Assignee != "" || got.Status != types.StatusOpen {
		t.Errorf("--claim-next claimed externally blocked %s (status=%s assignee=%q)", held.ID, got.Status, got.Assignee)
	}
	if got := bdShow(t, bd, dir, free.ID); got.Status != types.StatusInProgress {
		t.Errorf("--claim-next left %s %s, want it claimed (the only ready issue)", free.ID, got.Status)
	}

	out, err := run("close", held.ID)
	if err == nil || !strings.Contains(out, "external:remote:payments") {
		t.Errorf("bd close of an externally blocked issue: err=%v, want a refusal naming the blocker\n%s", err, out)
	}
	if got := bdShow(t, bd, dir, held.ID); got.Status != types.StatusOpen {
		t.Errorf("%s status after a refused close = %s, want open", held.ID, got.Status)
	}

	if out, err := run("close", held.ID, "--force"); err != nil {
		t.Fatalf("bd close --force must still bypass the external guard: %v\n%s", err, out)
	}

	// The idempotent re-close (ga-ktn9pe.4.8): the issue is closed now, and
	// closing it again without --force is the no-op every close path promises,
	// external blocker or not.
	if out, err := run("close", held.ID); err != nil {
		t.Errorf("re-closing already-closed %s without --force: %v, want the idempotent no-op\n%s", held.ID, err, out)
	}
	if out, err := run("update", held.ID, "--status", "closed"); err != nil {
		t.Errorf("bd update --status closed on already-closed %s: %v, want no external refusal\n%s", held.ID, err, out)
	}
}

// TestEmbeddedExternalCapabilityGuardsUpdateClaim pins `bd update --claim` on
// the direct route: the store chain's policy lifecycle guarded only a close, so
// the claim went straight to the backend's compare-and-set and took externally
// blocked work that `bd ready --claim` would never hand out. It is refused now
// (nonzero, the blocker named, nothing written) with or without --force, and
// an unblocked issue still claims.
func TestEmbeddedExternalCapabilityGuardsUpdateClaim(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xu")
	held := bdCreate(t, bd, dir, "Waits for payments", "--type", "task", "--priority", "0")
	free := bdCreate(t, bd, dir, "Free work", "--type", "task", "--priority", "2")
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("dep", "add", held.ID, "external:remote:payments"); err != nil {
		t.Fatalf("bd dep add: %v\n%s", err, out)
	}

	for _, args := range [][]string{{"update", held.ID, "--claim"}, {"update", held.ID, "--claim", "--force"}} {
		out, err := run(args...)
		if err == nil || !strings.Contains(out, "external:remote:payments") {
			t.Errorf("bd %s: err=%v, want a refusal naming the blocker\n%s", strings.Join(args, " "), err, out)
		}
		if got := bdShow(t, bd, dir, held.ID); got.Assignee != "" || got.Status != types.StatusOpen {
			t.Errorf("bd %s claimed externally blocked %s (status=%s assignee=%q)", strings.Join(args, " "), held.ID, got.Status, got.Assignee)
		}
	}
	if out, err := run("update", free.ID, "--claim"); err != nil {
		t.Fatalf("bd update %s --claim: %v\n%s", free.ID, err, out)
	}
	if got := bdShow(t, bd, dir, free.ID); got.Status != types.StatusInProgress || got.Assignee == "" {
		t.Errorf("unblocked %s after --claim: status=%s assignee=%q, want claimed", free.ID, got.Status, got.Assignee)
	}
}

// TestEmbeddedExternalCapabilityGuardsContinueStepClaim pins `bd close
// --continue`'s auto-claim on the direct route (storeMolWriter.ClaimStepIfOpen,
// a claim inside a transaction the store chain's policy cannot see). Molecule
// step readiness is computed from within-molecule edges only, so the next step
// an unsatisfied `external:` blocker holds looked ready and was claimed. It is
// not claimed now; an unblocked next step still is. With --no-auto nothing is
// claimed and the hint names only a step the guard does not refuse.
func TestEmbeddedExternalCapabilityGuardsContinueStepClaim(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xc")
	molecule := func(title string) (first, next *types.Issue) {
		root := bdCreate(t, bd, dir, title, "--type", "epic", "--labels", "template")
		first = bdCreate(t, bd, dir, title+" step 1", "--type", "task", "--parent", root.ID)
		next = bdCreate(t, bd, dir, title+" step 2", "--type", "task", "--parent", root.ID, "--deps", "depends-on:"+first.ID)
		return first, next
	}
	heldFirst, held := molecule("Held")
	freeFirst, free := molecule("Free")
	heldNoAutoFirst, heldNoAuto := molecule("HeldNoAuto")
	freeNoAutoFirst, freeNoAuto := molecule("FreeNoAuto")
	for _, id := range []string{held.ID, heldNoAuto.ID} {
		cmd := exec.Command(bd, "dep", "add", id, "external:remote:payments")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd dep add: %v\n%s", err, out)
		}
	}

	// --no-auto claims nothing, and suggests only a step the claim would not
	// refuse: none when the external guard holds every ready step.
	out := bdClose(t, bd, dir, heldNoAutoFirst.ID, "--continue", "--no-auto")
	if strings.Contains(out, "--claim") || !strings.Contains(out, "No claimable steps") || !strings.Contains(out, heldNoAuto.ID) {
		t.Errorf("--continue --no-auto output for an externally held next step:\n%s\nwant no claim hint, and the reason", out)
	}
	if got := bdShow(t, bd, dir, heldNoAuto.ID); got.Status != types.StatusOpen || got.Assignee != "" {
		t.Errorf("--no-auto claimed %s (status=%s assignee=%q)", heldNoAuto.ID, got.Status, got.Assignee)
	}
	out = bdClose(t, bd, dir, freeNoAutoFirst.ID, "--continue", "--no-auto")
	if !strings.Contains(out, "bd update "+freeNoAuto.ID+" --claim") {
		t.Errorf("--continue --no-auto output for an unblocked next step:\n%s\nwant the claim hint for %s", out, freeNoAuto.ID)
	}
	if got := bdShow(t, bd, dir, freeNoAuto.ID); got.Status != types.StatusOpen {
		t.Errorf("--no-auto claimed %s (status=%s)", freeNoAuto.ID, got.Status)
	}

	out = bdClose(t, bd, dir, heldFirst.ID, "--continue")
	if got := bdShow(t, bd, dir, held.ID); got.Status != types.StatusOpen || got.Assignee != "" {
		t.Errorf("--continue claimed externally blocked step %s (status=%s assignee=%q)", held.ID, got.Status, got.Assignee)
	}
	// Nor does it suggest claiming it: `bd update --claim` refuses it too.
	if strings.Contains(out, "bd update "+held.ID+" --claim") || !strings.Contains(out, "external dependency") {
		t.Errorf("--continue output for an externally held next step:\n%s\nwant no claim hint, and the reason", out)
	}
	_ = bdClose(t, bd, dir, freeFirst.ID, "--continue")
	if got := bdShow(t, bd, dir, free.ID); got.Status != types.StatusInProgress {
		t.Errorf("--continue did not advance to the unblocked step %s (status=%s)", free.ID, got.Status)
	}
}

// TestEmbeddedCloseIgnoresUnrelatedExternalEdges pins the direct close route
// to the closed issue's OWN `external:` edges: another issue's edge to a
// project that is not configured used to be resolved on every `bd close`, so
// closing unrelated work printed `Warning: external project "nowhere" is
// unavailable…`. The issue's own blocker is still refused.
func TestEmbeddedCloseIgnoresUnrelatedExternalEdges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xu")
	other := bdCreate(t, bd, dir, "Waits for nowhere", "--type", "task")
	free := bdCreate(t, bd, dir, "Free work", "--type", "task")
	upd := bdCreate(t, bd, dir, "Free work closed by update", "--type", "task")
	held := bdCreate(t, bd, dir, "Waits for nowhere too", "--type", "task")
	run := func(args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	for _, edge := range [][2]string{{other.ID, "external:nowhere:cap"}, {held.ID, "external:nowhere:other"}} {
		if stdout, stderr, err := run("dep", "add", edge[0], edge[1]); err != nil {
			t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
		}
	}

	for _, args := range [][]string{{"close", free.ID}, {"update", upd.ID, "--status", "closed"}} {
		stdout, stderr, err := run(args...)
		if err != nil {
			t.Fatalf("bd %s: %v\n%s\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "external project") {
			t.Errorf("bd %s warned about a project only an unrelated issue references:\n%s%s", strings.Join(args, " "), stdout, stderr)
		}
	}

	stdout, stderr, err := run("close", held.ID)
	if err == nil || !strings.Contains(stdout+stderr, "external:nowhere:other") {
		t.Errorf("bd close of an externally blocked issue: err=%v, want a refusal naming its own blocker\n%s\n%s", err, stdout, stderr)
	}
	if got := bdShow(t, bd, dir, held.ID); got.Status != types.StatusOpen {
		t.Errorf("%s status after a refused close = %s, want open", held.ID, got.Status)
	}
}
