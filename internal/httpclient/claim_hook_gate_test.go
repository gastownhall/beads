// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/claim_hook_gate_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The harm the batch close's claim check exists to prevent, driven through the
// REAL decorator the client process builds.
//
// internal/storage/hook_batch_closer.go fires the workspace's on_update hook on
// ClaimedNext's PRESENCE alone: it re-derives nothing, and it cannot — it is
// below the wire and has no view of what the server was asked or what landed.
// Its only two preconditions are a nil error and a non-nil claim, and both are
// answers this store hands it. So a server that serves a claim the request
// never earned does not merely mislead a printout; it runs a user-controlled
// subprocess against a row that may not be this agent's work at all, and L11
// says that hook fires client-side even against a server that runs none itself.
//
// The gate is the decode's, so the proof belongs here rather than in the
// decorator: hookBatchCloser is correct as written and there is nothing to fix
// in it.
//
// UNTAGGED, DELIBERATELY, and it is why this file plants its own hook rather
// than reusing served_hooks_test.go's plantHook. That file is //go:build cgo
// because its subject is the SERVED env; this one needs no server and no
// database — a stub wire is a better lying server than a real one — and a gate
// that carried the cgo tag would go blind in exactly the non-cgo build where
// the guard could rot unobserved.

// hookedBatchCloser builds the chain cmd/bd's wireStorageDecorators composes,
// with this store where the opened store goes, and hands back the closer taken
// off the DECORATED store — the accessor being where the decorator adds its
// layer.
func hookedBatchCloser(t *testing.T, w *stubWire, hooksDir string) (issueops.BatchCloser, *hooks.Runner) {
	t.Helper()
	runner := hooks.NewRunner(hooksDir)
	var decorated storage.DoltStorage = storage.NewHookFiringStore(batchCloseStore(t, w), runner)
	closer, err := decorated.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser() off the decorated store: %v", err)
	}
	// The same precondition its siblings in served_hooks_test.go carry: without
	// it, a decorator that stopped wrapping the batch closer would let the two
	// negatives below pass vacuously — they assert an ABSENCE, and an absence is
	// free once nothing fires. It is assertable here because RoleFiresHooks now
	// knows *hookBatchCloser (upstream #5594 closed the four-way gap and pinned
	// the set with an AST census, so a future role cannot go missing the way this
	// one did).
	if !storage.RoleFiresHooks(closer) {
		t.Fatal("the decorated store handed back a batch closer that fires no hooks; this test proves nothing")
	}
	// The CONTROL case below carries the same weight behaviorally — it fires a
	// real on_update through this exact wiring — so the two guards fail
	// independently rather than one standing in for the other.
	return closer, runner
}

// plantOnUpdate writes an on_update hook that touches a marker, and returns the
// marker path. Its ABSENCE is what the two refusal cases assert, which is why
// the control case below is not optional.
func plantOnUpdate(t *testing.T, hooksDir string) string {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "on_update.fired")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "on_update"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: a hook must be executable to run at all
		t.Fatalf("plant the on_update hook: %v", err)
	}
	return marker
}

func hookFired(t *testing.T, marker string) bool {
	t.Helper()
	_, err := os.Stat(marker)
	return err == nil
}

// TestAnUnearnedClaimNeverReachesTheUpdateHook runs the control first, on
// purpose: a negative about a hook is worth nothing until the same wiring has
// been shown to fire one.
func TestAnUnearnedClaimNeverReachesTheUpdateHook(t *testing.T) {
	claim := issueops.ReadyRequest{}
	landed := apigen.CloseOutcome{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}}
	reclosed := apigen.CloseOutcome{IssueId: "bd-1", Issue: &types.Issue{ID: "bd-1"}, AlreadyClosed: ptr(true)}

	for _, tc := range []struct {
		name    string
		request issueops.CloseBatchRequest
		outcome apigen.CloseOutcome
		wantRun bool
	}{
		{
			name: "an earned claim fires it",
			request: issueops.CloseBatchRequest{
				Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, ClaimNext: &claim,
			},
			outcome: landed,
			wantRun: true,
		},
		{
			name:    "a claim the request never asked for does not",
			request: issueops.CloseBatchRequest{Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}},
			outcome: landed,
		},
		{
			name: "a claim for a batch that landed nothing does not",
			request: issueops.CloseBatchRequest{
				Actor: "w", Items: []issueops.BatchCloseItem{{IssueID: "bd-1"}}, ClaimNext: &claim,
			},
			outcome: reclosed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooksDir := t.TempDir()
			marker := plantOnUpdate(t, hooksDir)
			w := &stubWire{batchClose: &apigen.BatchCloseResponse{
				Outcomes:    []apigen.CloseOutcome{tc.outcome},
				ClaimedNext: &types.IssueWithCounts{Issue: &types.Issue{ID: "bd-next"}},
			}}
			closer, runner := hookedBatchCloser(t, w, hooksDir)

			_, err := closer.CloseBatch(t.Context(), tc.request)
			if tc.wantRun && err != nil {
				t.Fatalf("CloseBatch refused a claim the batch earned: %v", err)
			}
			// t.Error rather than t.Fatal, so a build that accepts the claim
			// still reaches the hook assertion below and reports the HARM
			// beside the acceptance. Stopping here would hide the fact this
			// test exists to observe.
			if !tc.wantRun && err == nil {
				t.Error("CloseBatch accepted an unearned claim; the hook below is the harm that follows")
			}
			// The runner fires asynchronously, so the negative has to outlast a
			// hook that was going to run rather than race it.
			if !runner.Wait(10 * time.Second) {
				t.Fatal("the hook runner did not quiesce")
			}
			if tc.wantRun {
				if !hookFired(t, marker) {
					t.Fatal("an earned claim ran no on_update hook; the wiring under the negatives below proves nothing")
				}
				return
			}
			if hookFired(t, marker) {
				t.Error("an unearned claim ran this workspace's on_update hook against a row the request never earned")
			}
		})
	}
}
