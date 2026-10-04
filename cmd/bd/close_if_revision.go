package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/debug"
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
		if reported, ok := reportIfRevisionFailure("closing", id, closeErr); ok {
			return reported
		}
		fmt.Fprintln(os.Stderr, closeDirectRefusal(id, closeErr))
		return &exitError{Code: 1}
	}

	if err := commitPendingIfEmbedded(ctx, result.Store, actor, doltAutoCommitParams{
		Command:  "close",
		IssueIDs: []string{result.ResolvedID},
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
		if reported, ok := reportIfRevisionFailure("closing", id, closeErr); ok {
			return reported
		}
		fmt.Fprintln(os.Stderr, closeProxiedRefusal(id, closeErr))
		return &exitError{Code: 1}
	}
	SetLastTouchedID(id)
	reportClosedIfRevisionResult(id, reason, preCloseStatus, closeResult)
	return nil
}

// reportClosedIfRevisionResult is the one-id success report shared by both
// --if-revision close routes: the audit entry (suppressed for an idempotent
// re-close, matching close.go's batch behavior) and the human/--json output.
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
