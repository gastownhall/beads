//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_wrong_server_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The wrong-server security case, run through client → in-process bd serve →
// reference store, with the client's ExpectProjectID armed.
//
// This is the test that would have caught ga-b8ddd.11: before the fix, claimIssue
// sat in the client's baseline set, so a claim dispatched WITHOUT the handshake
// and its project-identity gate. A workspace whose server identity had drifted
// after connect would then land a claim on ANOTHER project's row — a silent
// cross-project write. Removing claimIssue from baselineOps forces the handshake
// first, so the claim now refuses before it writes.

// TestServedClaimRefusesAWrongServerBeforeWriting is the headline. The client
// expects one project; the server owns another; the claim must refuse with the
// typed mismatch and leave the target row byte-for-byte unchanged.
func TestServedClaimRefusesAWrongServerBeforeWriting(t *testing.T) {
	const expected = "proj-workspace-not-this-server"
	env := newServedEnvExpecting(t, "hws1", expected)
	ctx := t.Context()

	claimer, err := env.subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}

	const target = "hws1-1"
	seedServedIssue(t, ctx, env, target, types.StatusOpen)

	before, err := env.getIssue(ctx, target)
	if err != nil {
		t.Fatalf("read the seeded row: %v", err)
	}
	historyBefore, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history before: %v", err)
	}

	_, claimErr := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "alice", IssueID: target})
	if claimErr == nil {
		t.Fatal("claim against a wrong-identity server SUCCEEDED; this is the silent cross-project write ga-b8ddd.11 fixes")
	}
	if !errors.Is(claimErr, wire.ErrProjectMismatch) {
		t.Fatalf("claim refused with %v, want ErrProjectMismatch", claimErr)
	}
	var mismatch *wire.ProjectMismatchError
	if !errors.As(claimErr, &mismatch) {
		t.Fatalf("claim error is %T, want *wire.ProjectMismatchError", claimErr)
	}
	if mismatch.Expected != expected {
		t.Errorf("mismatch.Expected = %q, want the workspace's %q", mismatch.Expected, expected)
	}
	if mismatch.Got != servedProjectID {
		t.Errorf("mismatch.Got = %q, want the server's %q", mismatch.Got, servedProjectID)
	}

	// The whole point: nothing was written. The row is exactly as seeded and the
	// history did not grow, so the refusal happened before the claim dispatched.
	after, err := env.getIssue(ctx, target)
	if err != nil {
		t.Fatalf("re-read the target row: %v", err)
	}
	if after.Status != types.StatusOpen {
		t.Errorf("target status = %q, want it still %q — the refused claim wrote", after.Status, types.StatusOpen)
	}
	if after.Assignee != before.Assignee {
		t.Errorf("target assignee = %q, want the seeded %q — the refused claim wrote", after.Assignee, before.Assignee)
	}
	historyAfter, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history after: %v", err)
	}
	if historyAfter != historyBefore {
		t.Errorf("history grew from %d to %d; the refused claim wrote a server-side entry", historyBefore, historyAfter)
	}
}

// TestServedClaimSucceedsAgainstTheMatchingServer is the other direction: the
// forced handshake must NOT over-refuse. With the client pinned to the identity
// the server publishes, the claim lands and the server records it.
func TestServedClaimSucceedsAgainstTheMatchingServer(t *testing.T) {
	env := newServedEnvExpecting(t, "hws2", servedProjectID)
	ctx := t.Context()

	claimer, err := env.subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}

	const target = "hws2-1"
	seedServedIssue(t, ctx, env, target, types.StatusOpen)
	historyBefore, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history before: %v", err)
	}

	res, err := claimer.Claim(ctx, issueops.ClaimRequest{Actor: "alice", IssueID: target})
	if err != nil {
		t.Fatalf("claim against the matching server: %v", err)
	}
	if !res.Changed {
		t.Errorf("claim reported Changed=false, want the row to have been taken")
	}
	if res.Issue == nil || res.Issue.Assignee != "alice" || res.Issue.Status != types.StatusInProgress {
		t.Fatalf("claim result = %+v, want alice holding an in-progress row", res.Issue)
	}

	after, err := env.getIssue(ctx, target)
	if err != nil {
		t.Fatalf("re-read the claimed row: %v", err)
	}
	if after.Assignee != "alice" || after.Status != types.StatusInProgress {
		t.Errorf("row after claim = {assignee %q, status %q}, want alice / in_progress", after.Assignee, after.Status)
	}
	historyAfter, err := env.countHistory(ctx)
	if err != nil {
		t.Fatalf("count history after: %v", err)
	}
	if historyAfter <= historyBefore {
		t.Errorf("history did not grow (%d -> %d); the claim's server-side write did not land", historyBefore, historyAfter)
	}
}
