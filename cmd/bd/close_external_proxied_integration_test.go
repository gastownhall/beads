//go:build cgo

package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerCloseHonorsExternalBlockers drives `bd close` over a proxied
// server, which reaches the provider's BatchCloser accessor — the same policy
// batch closer the store arm uses. An unsatisfied `external:` blocker refuses
// the close without --force, --claim-next walks past the held issue, --force
// bypasses the guard, and re-closing the now-closed issue is the idempotent
// no-op (ga-ktn9pe.4.8).
func TestProxiedServerCloseHonorsExternalBlockers(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cxb")

	held := bdProxiedCreate(t, bd, p.dir, "Waits for payments", "--priority", "0")
	free := bdProxiedCreate(t, bd, p.dir, "Free work", "--priority", "2")
	done := bdProxiedCreate(t, bd, p.dir, "Finished work", "--priority", "3")
	if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", held.ID, "external:remote:payments"); err != nil {
		t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
	}

	bdProxiedClose(t, bd, p.dir, done.ID, "--claim-next")
	if got := bdProxiedShow(t, bd, p.dir, held.ID); got.Assignee != "" || got.Status != types.StatusOpen {
		t.Errorf("--claim-next claimed externally blocked %s (status=%s assignee=%q)", held.ID, got.Status, got.Assignee)
	}
	if got := bdProxiedShow(t, bd, p.dir, free.ID); got.Status != types.StatusInProgress {
		t.Errorf("--claim-next left %s %s, want it claimed", free.ID, got.Status)
	}

	if out := bdProxiedCloseFail(t, bd, p.dir, held.ID); !strings.Contains(out, "external:remote:payments") {
		t.Errorf("refusal does not name the blocker:\n%s", out)
	}
	if got := bdProxiedShow(t, bd, p.dir, held.ID); got.Status != types.StatusOpen {
		t.Errorf("%s status after a refused close = %s, want open", held.ID, got.Status)
	}
	bdProxiedClose(t, bd, p.dir, held.ID, "--force")
	bdProxiedClose(t, bd, p.dir, held.ID)
}

// TestProxiedServerUpdateClaimHonorsExternalBlockers pins `bd update --claim`
// over a proxied server: Lifecycle.Update with Claim set, which reaches
// ApplyUpdate and used to claim through the undecorated body's own ClaimIssue,
// past the policy. An externally blocked issue is refused (nonzero, the
// blocker named, nothing written) with or without --force; an unblocked one
// claims.
func TestProxiedServerUpdateClaimHonorsExternalBlockers(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cxu")

	held := bdProxiedCreate(t, bd, p.dir, "Waits for payments", "--priority", "0")
	free := bdProxiedCreate(t, bd, p.dir, "Free work", "--priority", "2")
	if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", held.ID, "external:remote:payments"); err != nil {
		t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
	}

	for _, args := range [][]string{{"update", held.ID, "--claim"}, {"update", held.ID, "--claim", "--force"}} {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
		if err == nil || !strings.Contains(stdout+stderr, "external:remote:payments") {
			t.Errorf("bd %s: err=%v, want a refusal naming the blocker\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if got := bdProxiedShow(t, bd, p.dir, held.ID); got.Assignee != "" || got.Status != types.StatusOpen {
			t.Errorf("bd %s claimed externally blocked %s (status=%s assignee=%q)", strings.Join(args, " "), held.ID, got.Status, got.Assignee)
		}
	}
	if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "update", free.ID, "--claim"); err != nil {
		t.Fatalf("bd update %s --claim: %v\n%s\n%s", free.ID, err, stdout, stderr)
	}
	if got := bdProxiedShow(t, bd, p.dir, free.ID); got.Status != types.StatusInProgress || got.Assignee == "" {
		t.Errorf("unblocked %s after --claim: status=%s assignee=%q, want claimed", free.ID, got.Status, got.Assignee)
	}
}

// TestProxiedServerContinueStepClaimHonorsExternalBlockers pins `bd close
// --continue`'s auto-claim over a proxied server (uowMolWriter.ClaimStepIfOpen
// -> ClaimIssueIfOpen inside the post-close transaction): an externally
// blocked next step is not claimed; an unblocked one still is. With --no-auto
// nothing is claimed and the hint names only a step the guard does not refuse
// (GuardClaimInUOW, on the pre-resolved post-close provider).
func TestProxiedServerContinueStepClaimHonorsExternalBlockers(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cxc")

	molecule := func(title string) (first, next *types.Issue) {
		root := bdProxiedCreate(t, bd, p.dir, title, "-t", "epic", "--labels", "template")
		first = bdProxiedCreate(t, bd, p.dir, title+" step 1", "--parent", root.ID)
		next = bdProxiedCreate(t, bd, p.dir, title+" step 2", "--parent", root.ID, "--deps", "depends-on:"+first.ID)
		return first, next
	}
	heldFirst, held := molecule("Held")
	freeFirst, free := molecule("Free")
	heldNoAutoFirst, heldNoAuto := molecule("HeldNoAuto")
	freeNoAutoFirst, freeNoAuto := molecule("FreeNoAuto")
	for _, id := range []string{held.ID, heldNoAuto.ID} {
		if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", id, "external:remote:payments"); err != nil {
			t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
		}
	}

	// --no-auto claims nothing, and suggests only a step the claim would not
	// refuse: none when the external guard holds every ready step.
	out := bdProxiedClose(t, bd, p.dir, heldNoAutoFirst.ID, "--continue", "--no-auto")
	if strings.Contains(out, "--claim") || !strings.Contains(out, "No claimable steps") || !strings.Contains(out, heldNoAuto.ID) {
		t.Errorf("--continue --no-auto output for an externally held next step:\n%s\nwant no claim hint, and the reason", out)
	}
	out = bdProxiedClose(t, bd, p.dir, freeNoAutoFirst.ID, "--continue", "--no-auto")
	if !strings.Contains(out, "bd update "+freeNoAuto.ID+" --claim") {
		t.Errorf("--continue --no-auto output for an unblocked next step:\n%s\nwant the claim hint for %s", out, freeNoAuto.ID)
	}

	out = bdProxiedClose(t, bd, p.dir, heldFirst.ID, "--continue")
	// Nor does it suggest claiming it: `bd update --claim` refuses it too.
	if strings.Contains(out, "bd update "+held.ID+" --claim") || !strings.Contains(out, "external dependency") {
		t.Errorf("--continue output for an externally held next step:\n%s\nwant no claim hint, and the reason", out)
	}
	bdProxiedClose(t, bd, p.dir, freeFirst.ID, "--continue")
	db := openProxiedDB(t, p)
	if got, who := readStatus(t, db, held.ID), readAssignee(t, db, held.ID); got != types.StatusOpen || who != "" {
		t.Errorf("--continue claimed externally blocked step %s (status=%s assignee=%q)", held.ID, got, who)
	}
	if got := readStatus(t, db, free.ID); got != types.StatusInProgress {
		t.Errorf("--continue did not advance to the unblocked step %s (status=%s)", free.ID, got)
	}
	for _, id := range []string{heldNoAuto.ID, freeNoAuto.ID} {
		if got, who := readStatus(t, db, id), readAssignee(t, db, id); got != types.StatusOpen || who != "" {
			t.Errorf("--no-auto claimed %s (status=%s assignee=%q)", id, got, who)
		}
	}
}

// TestProxiedServerForcedStatusCloseBypassesExternalBlockers pins `bd update
// --status closed --force` over a proxied server: the policy's ApplyUpdate
// override ignored ForceClosePolicy, so the forced close was refused here
// while the direct route (and `bd close --force` on both) let it through.
// Unforced, it is still refused.
func TestProxiedServerForcedStatusCloseBypassesExternalBlockers(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cxf")

	held := bdProxiedCreate(t, bd, p.dir, "Waits for payments", "--priority", "0")
	if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", held.ID, "external:remote:payments"); err != nil {
		t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
	}
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "update", held.ID, "--status", "closed")
	if err == nil || !strings.Contains(stdout+stderr, "external:remote:payments") {
		t.Errorf("unforced bd update --status closed: err=%v, want a refusal naming the blocker\n%s\n%s", err, stdout, stderr)
	}
	db := openProxiedDB(t, p)
	if got := readStatus(t, db, held.ID); got != types.StatusOpen {
		t.Fatalf("refused update left %s %s, want open", held.ID, got)
	}
	if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "update", held.ID, "--status", "closed", "--force"); err != nil {
		t.Fatalf("bd update --status closed --force: %v\n%s\n%s", err, stdout, stderr)
	}
	if got := readStatus(t, db, held.ID); got != types.StatusClosed {
		t.Errorf("forced update left %s %s, want closed", held.ID, got)
	}
}

// TestProxiedServerCloseIgnoresUnrelatedExternalEdges is the proxied route's
// half of TestEmbeddedCloseIgnoresUnrelatedExternalEdges: closing unrelated
// work resolves no other issue's `external:` edge, so it prints no warning
// about that issue's unavailable project; the closed issue's own blocker is
// still refused.
func TestProxiedServerCloseIgnoresUnrelatedExternalEdges(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cxn")

	other := bdProxiedCreate(t, bd, p.dir, "Waits for nowhere")
	free := bdProxiedCreate(t, bd, p.dir, "Free work")
	upd := bdProxiedCreate(t, bd, p.dir, "Free work closed by update")
	held := bdProxiedCreate(t, bd, p.dir, "Waits for nowhere too")
	for _, edge := range [][2]string{{other.ID, "external:nowhere:cap"}, {held.ID, "external:nowhere:other"}} {
		if stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dep", "add", edge[0], edge[1]); err != nil {
			t.Fatalf("bd dep add: %v\n%s\n%s", err, stdout, stderr)
		}
	}

	for _, args := range [][]string{{"close", free.ID}, {"update", upd.ID, "--status", "closed"}} {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
		if err != nil {
			t.Fatalf("bd %s: %v\n%s\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "external project") {
			t.Errorf("bd %s warned about a project only an unrelated issue references:\n%s%s", strings.Join(args, " "), stdout, stderr)
		}
	}

	if out := bdProxiedCloseFail(t, bd, p.dir, held.ID); !strings.Contains(out, "external:nowhere:other") {
		t.Errorf("refusal does not name the closed issue's own blocker:\n%s", out)
	}
	if got := bdProxiedShow(t, bd, p.dir, held.ID); got.Status != types.StatusOpen {
		t.Errorf("%s status after a refused close = %s, want open", held.ID, got.Status)
	}
}
