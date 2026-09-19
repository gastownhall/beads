//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The embedded half of the Claimer contract's wrapper-routing residue; the
// server-backed half is internal/storage/dolt/claim_wrapper_smoke_test.go, and
// the reasoning is the same one checked_wrapper_smoke_test.go wrote down.
//
// EmbeddedDoltStore.ClaimIssue (issues.go) reaches issueops.ClaimIssueInTx
// DIRECTLY, under withConn. The ROLE path does not: issueClaimer.Claim goes
// through runIssueOperationTx to issueops.ExecuteClaim, and ExecuteClaim
// refuses every wisp id BEFORE the CAS (issueops/claimer.go's "A wisp id is
// ErrNotFound"). ClaimIssueInTx has no such refusal — it routes an ephemeral id
// to the wisps tables and claims it there — so this wrapper answers a wisp id
// with a CLAIM where the role answers ErrNotFound, and no case in
// claimer_contract.go can see that on any leg.
//
// Nor can the audit suite: backend/conformance/claim.go's five claim cases all
// seed durable issues, and the only wisp claim anywhere else in the tree goes
// through ClaimReadyIssueInTx.
//
// These two cases are deliberately NARROW. The durable branch is what the audit
// suite already drives on this backend, and the option this wrapper forwards —
// withConn's commit flag — is pinned by that suite reading the row back. What
// is left is the plane decision, and its positive half is the half nothing had.
func TestEmbeddedClaimIssueWrapperClaimsTheWispPlane(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "clwrap")
	ctx := t.Context()

	planeRow := func(t *testing.T, table, id string) (string, string) {
		t.Helper()
		var status, assignee string
		//nolint:gosec // G201: table is one of this file's two hardcoded names.
		te.queryScalar(t, ctx, "SELECT status, COALESCE(assignee, '') FROM "+table+" WHERE id = ?", []any{id}, &status, &assignee)
		return status, assignee
	}
	createWisp := func(t *testing.T, id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("create wisp %s: %v", id, err)
		}
	}

	// E-A. The positive half of the plane decision: this wrapper CLAIMS a wisp
	// where the role refuses one, and the claim lands on the EPHEMERAL row. The
	// durable-plane check is what makes it about routing rather than about the
	// absence of an error — claiming a wisp must not promote it.
	t.Run("ClaimsAWispInTheWispsPlane", func(t *testing.T) {
		createWisp(t, "clwrap-wisp")
		if err := te.store.ClaimIssue(ctx, "clwrap-wisp", "worker"); err != nil {
			t.Fatalf("ClaimIssue of a wisp: %v — the wrapper does not reach the wisp-routing CAS", err)
		}
		if status, assignee := planeRow(t, "wisps", "clwrap-wisp"); status != string(types.StatusInProgress) || assignee != "worker" {
			t.Errorf("wisps row = (status %q, assignee %q), want (%q, %q)",
				status, assignee, types.StatusInProgress, "worker")
		}
		te.assertRowNotExists(t, ctx, "issues", "clwrap-wisp")
	})

	// E-B. Its refusal, which fails independently of the win above: the
	// anti-steal guard has to hold on the ephemeral plane too, and the
	// transaction has to roll back rather than leave a half-applied claim.
	t.Run("RefusesAWispHeldByAnotherActorAndLeavesItAlone", func(t *testing.T) {
		createWisp(t, "clwrap-wisp-held")
		if err := te.store.ClaimIssue(ctx, "clwrap-wisp-held", "worker1"); err != nil {
			t.Fatalf("first ClaimIssue of a wisp: %v", err)
		}
		if err := te.store.ClaimIssue(ctx, "clwrap-wisp-held", "worker2"); !errors.Is(err, storage.ErrAlreadyClaimed) {
			t.Fatalf("second actor claiming a held wisp: err = %v, want ErrAlreadyClaimed", err)
		}
		if status, assignee := planeRow(t, "wisps", "clwrap-wisp-held"); status != string(types.StatusInProgress) || assignee != "worker1" {
			t.Errorf("wisps row = (status %q, assignee %q) after a refused claim, want the first worker's claim intact (%q, %q)",
				status, assignee, types.StatusInProgress, "worker1")
		}
	})
}
