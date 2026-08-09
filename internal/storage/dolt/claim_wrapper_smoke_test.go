package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// The wrapper-routing residue of the issueops.Claimer contract, and the THIRD
// instance of the same shape as checked_wrapper_smoke_test.go's.
//
// DoltStore.ClaimIssue (issues.go) is a SEPARATE COMPOSITION of the bodies the
// Claimer contract exercises. It decides for itself, inside its own
// withCircuitWrite, whether an id belongs to the wisps plane, and routes there
// through claimWisp — a different transaction wrapper, with no Dolt version
// commit — or to withRetryTx over issueops.ClaimIssueInTx plus a
// doltAddAndCommitInTx it spells itself. The ROLE path never calls this method:
// issueClaimer.Claim goes through runIssueOperationTx to
// issueops.ExecuteClaim, and ExecuteClaim REFUSES every wisp id before the CAS
// (issueops/claimer.go's "A wisp id is ErrNotFound"). So the wisp branch of
// this wrapper is unreachable from every case in claimer_contract.go on every
// leg, by construction rather than by accident.
//
// It is unreachable from the audit suite too. backend/conformance/claim.go's
// five claim cases all seed durable issues, and the one place a wisp IS claimed
// anywhere in the tree — the ReadyClaimer contract's ephemeral case — reaches
// issueops.ClaimReadyIssueInTx, not claimWisp. claimWisp has exactly one caller
// in this package, the line below the branch, and before this file nothing
// called it: deleting its body outright left the whole suite green.
//
// These tests are therefore deliberately NARROW, for the reason the checked
// wrappers' residue gives. They do not re-test what the contract and the audit
// suite own — no refusal taxonomy beyond the one branch, no CAS ordering, no
// lease or event accounting. Each asks only whether this wrapper still routes
// to the shared body that implements one of the decisions it makes, and half of
// what follows is the POSITIVE half of a branch: a wrapper that refuses
// correctly and never writes passed everything before this.
func TestClaimIssueWrapperRoutesItsOwnBranches(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	planeRow := func(t *testing.T, table, id string) (string, string) {
		t.Helper()
		var status, assignee string
		//nolint:gosec // G201: table is one of this file's two hardcoded names.
		query := "SELECT status, COALESCE(assignee, '') FROM " + table + " WHERE id = ?"
		if err := store.db.QueryRowContext(ctx, query, id).Scan(&status, &assignee); err != nil {
			t.Fatalf("read %s row %s: %v", table, id, err)
		}
		return status, assignee
	}
	rowCount := func(t *testing.T, table, id string) int {
		t.Helper()
		var rows int
		//nolint:gosec // G201: table is one of this file's two hardcoded names.
		query := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
		if err := store.db.QueryRowContext(ctx, query, id).Scan(&rows); err != nil {
			t.Fatalf("count %s rows for %s: %v", table, id, err)
		}
		return rows
	}
	historyCount := func(t *testing.T) int {
		t.Helper()
		var entries int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&entries); err != nil {
			t.Fatalf("count dolt_log: %v", err)
		}
		return entries
	}

	// C-A. The whole wisp branch, on its positive side. The role answers a wisp
	// id with ErrNotFound; this wrapper CLAIMS it, in the wisps plane. The
	// second and third assertions are what make it about routing rather than
	// about the absence of an error: the ephemeral row is the one that moved,
	// and claiming it must not promote a copy into the durable plane.
	t.Run("ClaimsAWispInTheWispsPlane", func(t *testing.T) {
		createWisp(t, ctx, store, "clwrap-wisp")
		if err := store.ClaimIssue(ctx, "clwrap-wisp", "worker"); err != nil {
			t.Fatalf("ClaimIssue of a wisp: %v — the wrapper's wisp branch does not reach the CAS", err)
		}
		if status, assignee := planeRow(t, "wisps", "clwrap-wisp"); status != string(types.StatusInProgress) || assignee != "worker" {
			t.Errorf("wisps row = (status %q, assignee %q), want (%q, %q)",
				status, assignee, types.StatusInProgress, "worker")
		}
		if rows := rowCount(t, "issues", "clwrap-wisp"); rows != 0 {
			t.Errorf("issues holds %d row(s) for the claimed wisp, want 0 — claiming a wisp must not promote it", rows)
		}
	})

	// THERE IS NO CASE HERE FOR "a wisp claim takes no Dolt version commit",
	// and the absence is a measured result rather than an oversight. claimWisp
	// says in so many words that it skips versioning because wisps live in
	// dolt_ignored tables, which reads like a decision worth pinning. It is not
	// observable. Two mutations in opposite directions both left a dolt_log
	// delta assertion green: routing the wisp through the DURABLE branch
	// instead (the durable branch stages "issues" and "events", neither of
	// which a wisp claim dirties, so the empty-staged-set guard suppresses the
	// commit), and adding a doltAddAndCommitInTx over the wisp tables to
	// claimWisp itself (DOLT_ADD stages nothing for an ignored table, so the
	// same guard fires). The property is enforced by the dolt_ignore
	// configuration, one layer below this wrapper, and a case asserting it here
	// would be a test that cannot fail.
	//
	// C-C. And the wisp branch's refusal, which is a different question from
	// C-A: the branch runs its CAS over a bare BeginTx with a deferred
	// Rollback, so a refusal has to both surface the sentinel AND leave the
	// ephemeral row alone. The role cannot ask this at all.
	t.Run("RefusesAWispHeldByAnotherActorAndLeavesItAlone", func(t *testing.T) {
		createWisp(t, ctx, store, "clwrap-wisp-held")
		if err := store.ClaimIssue(ctx, "clwrap-wisp-held", "worker1"); err != nil {
			t.Fatalf("first ClaimIssue of a wisp: %v", err)
		}
		if err := store.ClaimIssue(ctx, "clwrap-wisp-held", "worker2"); !errors.Is(err, storage.ErrAlreadyClaimed) {
			t.Fatalf("second actor claiming a held wisp: err = %v, want ErrAlreadyClaimed", err)
		}
		if status, assignee := planeRow(t, "wisps", "clwrap-wisp-held"); status != string(types.StatusInProgress) || assignee != "worker1" {
			t.Errorf("wisps row = (status %q, assignee %q) after a refused claim, want the first worker's claim intact (%q, %q)",
				status, assignee, types.StatusInProgress, "worker1")
		}
	})

	// C-D. The durable branch's own commit. That HEAD advances on a real claim
	// is already pinned (empty_commit_test.go); what is not is the MESSAGE,
	// which this wrapper composes itself and which differs from the role's —
	// issueops.ClaimCommitMessage names the claimant, this one names only the
	// issue. `bd dolt log` is the audit trail for a claim, so a wrapper that
	// stopped naming the issue would leave a reader unable to tell which claim
	// a commit was, with every other test in the tree green.
	t.Run("VersionsADurableClaimUnderItsOwnCommitMessage", func(t *testing.T) {
		createPerm(t, ctx, store, "clwrap-durable")
		before := historyCount(t)
		if err := store.ClaimIssue(ctx, "clwrap-durable", "worker"); err != nil {
			t.Fatalf("ClaimIssue: %v", err)
		}
		if after := historyCount(t); after != before+1 {
			t.Fatalf("history entries went %d -> %d across a durable claim, want exactly one more", before, after)
		}
		var commits int
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_log WHERE message = ?", "bd: claim clwrap-durable").Scan(&commits); err != nil {
			t.Fatalf("count dolt_log by message: %v", err)
		}
		if commits != 1 {
			t.Errorf("dolt_log holds %d commit(s) named %q, want 1", commits, "bd: claim clwrap-durable")
		}
	})
}
