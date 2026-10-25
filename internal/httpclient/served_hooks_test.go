//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_hooks_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Client-side hook parity, design D9 L11.
//
// THE POSTURE. `bd serve` runs no hooks — it says so, and its own registered-
// backend test asserts an HTTP claim does NOT run the served workspace's
// on_update. That is the SERVER's side. This file is the client's: the seam sits
// below HookFiringStore in the client process (cmd/bd's wireStorageDecorators
// composes caller → HookFiringStore → telemetry → the opened store, and a
// registered backend's store goes through that call exactly as an embedded one
// does), so a workspace connected to a server still runs its own on_update when
// its own `bd` writes.
//
// WHY IT NEEDS A TEST AT ALL. HookFiringStore decorates six role accessors, and
// each one recurses into the inner store's accessor rather than delegating
// blindly — a blind delegation would hand back the inner claimer unchanged and
// silently drop the hook every landed claim owes. Nothing forces that for a
// backend that arrived through the registry, and nothing in the tree asserted
// it: the hook-firing tests are embedded-Dolt or proxied-Dolt, and the
// registered-backend test only pins the negative. A store whose Changed flag
// came out of an HTTP response rather than a SQL row is exactly where the
// no-op suppression could quietly invert.
//
// The residue L11 records stands: the hook fires AFTER the server's commit with
// no shared transaction, so a hook that re-reads sees the server's state.

// TestHooksFireOnHTTPWritesThroughTheDecoratorChain is the parity proof: a claim,
// an update and a close, all over the wire, each running this workspace's hook.
func TestHooksFireOnHTTPWritesThroughTheDecoratorChain(t *testing.T) {
	env := newServedEnv(t, "hhk")
	ctx := t.Context()

	hooksDir := t.TempDir()
	updateMarker := plantHook(t, hooksDir, "on_update")
	closeMarker := plantHook(t, hooksDir, "on_close")

	// The chain cmd/bd builds, with the http store where the opened store goes.
	// Taking the roles off the DECORATED store rather than off the client is the
	// whole subject: a command holds this interface, and the accessor is where
	// each decorator adds its layer.
	var decorated storage.DoltStorage = storage.NewHookFiringStore(env.subject, hooks.NewRunner(hooksDir))

	const id = "hhk-hooked"
	seed := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := env.createIssue(ctx, seed, "seed"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}

	claimer, err := decorated.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer() off the decorated store: %v", err)
	}
	if !storage.RoleFiresHooks(claimer) {
		t.Fatal("the decorated store handed back a claimer that fires no hooks; the decorator delegated instead of recursing")
	}
	if _, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "worker", IssueID: id}); err != nil {
		t.Fatalf("claim over http: %v", err)
	}
	waitForHook(t, updateMarker, "an HTTP claim")

	lifecycle, err := decorated.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() off the decorated store: %v", err)
	}
	if !storage.RoleFiresHooks(lifecycle) {
		t.Fatal("the decorated store handed back a lifecycle that fires no hooks")
	}
	if err := os.Remove(updateMarker); err != nil {
		t.Fatalf("clear the update marker: %v", err)
	}
	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "worker", IssueID: id,
		Patch: issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "edited over http"}},
	}); err != nil {
		t.Fatalf("update over http: %v", err)
	}
	waitForHook(t, updateMarker, "an HTTP update")

	if _, err := lifecycle.Close(ctx, issueops.CloseRequest{Actor: "worker", IssueID: id, Reason: "done"}); err != nil {
		t.Fatalf("close over http: %v", err)
	}
	waitForHook(t, closeMarker, "an HTTP close")

	// The writes are the server's, read back from the store the server serves —
	// never from the client, which is the thing under test.
	after, err := env.getIssue(ctx, id)
	if err != nil {
		t.Fatalf("read %s back from the reference store: %v", id, err)
	}
	if after.Title != "edited over http" {
		t.Errorf("title after the update = %q, want the edit to have landed server-side", after.Title)
	}
	if after.Status != types.StatusClosed {
		t.Errorf("status after the close = %q, want closed", after.Status)
	}
}

// TestTheIdempotentReclaimFiresNoHook is the suppression half, and it is the
// half an HTTP-sourced Changed flag could invert: already_claimed on a 200 means
// the SAME actor re-claimed, and reading it as a change would run the user's
// hook script once per poll for a write that never happened.
//
// PROVING AN ABSENCE. hooks.Runner.Run is fire-and-forget, so "the marker is not
// there yet" is not the same as "no hook ran", and a fixed sleep only decides
// how long the test is wrong for. The barrier here is a POSITIVE CONTROL on a
// SECOND issue: the log records one line per invocation with the issue id it
// fired for, the re-claim is followed by a genuinely-changed write on another
// row, and the assertion waits for THAT row's line. The re-claim's hook, had it
// fired, would have been launched strictly earlier — before the second write was
// even issued — so a log that reaches the barrier without it is evidence rather
// than a guess. The count is asserted as well as the contents, which is what
// catches a hook that fired for the right row the wrong number of times.
func TestTheIdempotentReclaimFiresNoHook(t *testing.T) {
	env := newServedEnv(t, "hhq")
	ctx := t.Context()

	hooksDir := t.TempDir()
	log := plantLoggingHook(t, hooksDir, "on_update")
	decorated := storage.NewHookFiringStore(env.subject, hooks.NewRunner(hooksDir))

	const claimed = "hhq-reclaim"
	const barrier = "hhq-barrier"
	for _, id := range []string{claimed, barrier} {
		seed := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := env.createIssue(ctx, seed, "seed"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	claimer, err := decorated.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	if _, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "worker", IssueID: claimed}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	waitForHookLine(t, log, claimed, "the first HTTP claim")

	res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "worker", IssueID: claimed})
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if res.Changed {
		t.Fatal("the re-claim reported Changed; already_claimed on a 200 is the idempotent re-claim")
	}

	// The barrier: a write that genuinely changes a DIFFERENT row, whose hook is
	// launched after the re-claim's would have been.
	lifecycle, err := decorated.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "worker", IssueID: barrier,
		Patch: issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: "barrier edit"}},
	}); err != nil {
		t.Fatalf("barrier update: %v", err)
	}
	waitForHookLine(t, log, barrier, "the barrier update")

	if got := countHookLines(t, log, claimed); got != 1 {
		t.Errorf("on_update fired %d times for %s, want 1: the idempotent re-claim ran the user's script for a write that never happened, "+
			"and a polling agent would run it once per poll", got, claimed)
	}
}

// plantHook writes an executable hook that touches a marker, and returns the
// marker path.
func plantHook(t *testing.T, hooksDir, name string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), name+".fired")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant the %s hook: %v", name, err)
	}
	return marker
}

// plantLoggingHook writes a hook that APPENDS the issue id it fired for, so the
// test can count invocations rather than observe a latch. `$1` is the issue id
// the runner passes as the hook's first argument.
func plantLoggingHook(t *testing.T, hooksDir, name string) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), name+".log")
	script := "#!/bin/sh\necho \"$1\" >> " + log + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant the %s hook: %v", name, err)
	}
	return log
}

// waitForHookLine polls until the log records an invocation for id.
func waitForHookLine(t *testing.T, log, id, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if countHookLines(t, log, id) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s ran no hook: %s recorded no line for %s", what, log, id)
}

func countHookLines(t *testing.T, log, id string) int {
	t.Helper()
	data, err := os.ReadFile(log) // #nosec G304 - the path is this test's own temp file
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read the hook log: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == id {
			n++
		}
	}
	return n
}

// waitForHook polls for the marker. hooks.Runner.Run is fire-and-forget by
// design — a slow hook must not block the write that triggered it — so the only
// honest assertion is a bounded wait.
func waitForHook(t *testing.T, marker, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s ran no hook: %s was never created", what, marker)
}
