package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// runCloseDirectIfRevision closes exactly one issue under an active
// --if-revision guard (A8, beads#4682) on the non-proxied route, bypassing
// `bd close`'s batch architecture entirely.
//
// issueops.BatchCloseItem carries no per-item ExpectedVersion —
// batchcloser.go's own CloseBatchRequest.Force doc states that is
// unimplemented by design ("no batch item carries [a lifecycle precondition]
// today") — so a guarded close cannot ride BatchCloser at all. It goes
// through issueops.Lifecycle.Close instead, the single-id primitive
// CloseRequest.ExpectedVersion was built for. requireSingleIfRevisionID has
// already refused more than one id by the time this runs, so there is no
// batch to preserve here.
func runCloseDirectIfRevision(ctx context.Context, id, reason string, force bool, session string, expectedVersion int64) error {
	result, err := resolveAndGetIssueForMutation(ctx, store, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
		return &exitError{Code: 1}
	}
	defer result.Close()
	if result.Issue == nil {
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return &exitError{Code: 1}
	}

	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	ops, err := writeOps(result.Store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
		return &exitError{Code: 1}
	}

	preCloseStatus := string(result.Issue.Status)
	closeResult, closeErr := ops.Close(opsCtx, issueops.CloseRequest{
		Actor:           actor,
		IssueID:         result.ResolvedID,
		Reason:          reason,
		Session:         session,
		Force:           force,
		ExpectedVersion: &expectedVersion,
	})
	if closeErr != nil {
		if reported, ok := reportIfRevisionFailure("closing", id, closeErr, &expectedVersion); ok {
			return reported
		}
		fmt.Fprintln(os.Stderr, closeDirectRefusal(id, closeErr))
		return &exitError{Code: 1}
	}

	// Open children only survive to here when --force waived the engine's
	// refusal. Say so, so orphaned children are never silent — parity with
	// close.go's batch route (close.go:194-196).
	if closeResult.OpenChildren > 0 {
		fmt.Fprintf(os.Stderr, "warning: closing %s with %d open child issue(s) still active\n", result.ResolvedID, closeResult.OpenChildren)
	}

	// Molecule auto-close is a retry-safe, fully state-derived post-close
	// contract (close.go's autoCloseCompletedMolecule doc), so it must replay
	// here exactly as it does on the batch route — on a real close AND on an
	// idempotent re-close, since the guarded route bypasses BatchCloser
	// entirely (mc-zndi7.75) and would otherwise leave a molecule's root
	// stranded open forever.
	mutatedIDs := []string{result.ResolvedID}
	if molID := autoCloseCompletedMolecule(ctx, result.Store, result.ResolvedID, actor, session); molID != "" {
		mutatedIDs = append(mutatedIDs, molID)
	}

	if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
		Command:  "close",
		IssueIDs: mutatedIDs,
	}); err != nil {
		return HandleErrorRespectJSON("failed to commit: %v", err)
	}
	SetLastTouchedID(result.ResolvedID)

	reportClosedIfRevisionResult(result.ResolvedID, reason, preCloseStatus, closeResult)
	return nil
}

// runCloseProxiedIfRevision is runCloseDirectIfRevision's proxied-server twin:
// the same single-id bypass, reached through issueops.Lifecycle via
// proxiedIssueLifecycle rather than a routed store.
func runCloseProxiedIfRevision(ctx context.Context, id, reason string, force bool, session string, expectedVersion int64) error {
	ops, err := proxiedIssueLifecycle()
	if err != nil {
		return HandleError("%v", err)
	}

	// Pre-close snapshot for the audit entry's old status, mirroring
	// proxiedUpdateTarget's advisory pre-read. Best effort: a read failure
	// here does not block the close, which reports not-found uniformly on its
	// own if the id is bad.
	preCloseStatus := "open"
	if rd, rerr := proxiedIssueReader(); rerr == nil {
		if details, gerr := rd.Get(ctx, issueops.GetRequest{ID: id}); gerr == nil {
			preCloseStatus = string(details.Issue.Status)
		}
	}

	closeResult, closeErr := ops.Close(ctx, issueops.CloseRequest{
		Actor:           actor,
		IssueID:         id,
		Reason:          reason,
		Session:         session,
		Force:           force,
		ExpectedVersion: &expectedVersion,
	})
	if closeErr != nil {
		if errors.Is(closeErr, context.Canceled) || errors.Is(closeErr, context.DeadlineExceeded) {
			return closeErr
		}
		if reported, ok := reportIfRevisionFailure("closing", id, closeErr, &expectedVersion); ok {
			return reported
		}
		fmt.Fprintln(os.Stderr, closeProxiedRefusal(id, closeErr))
		return &exitError{Code: 1}
	}

	// Open children only survive to here when --force waived the engine's
	// refusal. Say so, so orphaned children are never silent — parity with
	// the direct route above and with close_proxied_server.go's batch route.
	if closeResult.OpenChildren > 0 {
		fmt.Fprintf(os.Stderr, "warning: closing %s with %d open child issue(s) still active\n", id, closeResult.OpenChildren)
	}

	// Molecule auto-close is a retry-safe, fully state-derived post-close
	// contract, so it must replay here too — on a real close AND on an
	// idempotent re-close — exactly as closeProxiedRunPostClose drives it for
	// the batch route (mc-zndi7.75).
	runCloseIfRevisionProxiedPostClose(ctx, id, session)

	SetLastTouchedID(id)
	reportClosedIfRevisionResult(id, reason, preCloseStatus, closeResult)
	return nil
}

// runCloseIfRevisionProxiedPostClose re-drives molecule auto-close after a
// guarded proxied close, in its own unit of work exactly as
// closeProxiedRunPostClose runs it for the batch route: outside the close's
// own transaction, because it is outside that transaction's contract. A
// failure here is reported and swallowed — best effort, matching
// autoCloseCompletedMolecule's direct-route twin — so a molecule-healing
// hiccup never turns an otherwise-successful guarded close into a failure.
func runCloseIfRevisionProxiedPostClose(ctx context.Context, id, session string) {
	if uowProvider == nil {
		return
	}
	autoClosedMol, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (*types.Issue, string, error) {
		var warnings []string
		mol := autoCloseProxiedCompletedMolecule(ctx, uw, id, actor, session, &warnings)
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
		if mol == nil {
			return nil, "", nil
		}
		return mol, "bd: auto-close " + mol.ID, nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-close work failed: %v\n", err)
		return
	}
	if autoClosedMol != nil && !jsonOutput {
		debug.PrintNormal("%s Auto-closed completed molecule %s\n", ui.RenderPass("✓"), formatFeedbackID(autoClosedMol.ID, autoClosedMol.Title))
	}
}

// reportClosedIfRevisionResult is the one-id success report shared by both
// --if-revision close routes: the audit entry (suppressed for an idempotent
// re-close, matching close.go's batch behavior) and the human/--json output.
//
// ON_CLOSE HOOK FIRING IS DELIBERATELY NOT GATED HERE (mc-zndi7.75 item 2).
// ops.Close above reaches hookIssueOperations.Close (embedded) or
// recordingIssueUC.CloseIssueChecked (proxied), and BOTH fire on_close on
// every success, idempotent re-close included — pinned, by name, as
// deliberate "legacy parity" in internal/storage/hook_issue_operations_test.go
// ("close no-op still fires close" — "Do not 'unify' this either") and in
// internal/storage/uow/notifying_test.go ("a re-close still reports the
// close" — "THE BATCH COMPOSITIONS DISAGREE WITH THIS ONE, deliberately").
// Both comments give the same reasoning: "a re-close answers 'it is closed',
// and a script reconciling on that answer must not be told only sometimes."
// Only the BATCH closers (hookBatchCloser / hookBatchCloser's proxied twin)
// gate on Changed, and only to stop a replayed TEARDOWN BATCH from re-running
// on_close once per item on every pass (ga-2yaqp.1) — a multi-item hazard
// this single-id guarded close does not have. Reusing that gating here would
// contradict two independently pinned, reasoned invariants rather than fix a
// bug; flagged for human review instead of changed unilaterally.
func reportClosedIfRevisionResult(id, reason, preCloseStatus string, closeResult issueops.CloseResult) {
	closedIssue := closeResult.Issue
	if closedIssue != nil {
		closedIssue.Dependencies = nil
	}
	if closeResult.Changed {
		audit.LogFieldChange(id, "status", preCloseStatus, "closed", actor, reason)
	}
	if jsonOutput {
		if closedIssue != nil {
			_ = outputJSON([]*types.Issue{closedIssue})
		}
		return
	}
	title := ""
	if closedIssue != nil {
		title = closedIssue.Title
	}
	debug.PrintNormal("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(id, title), reason)
}
