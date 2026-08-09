package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

func runUpdateProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return HandleErrorRespectJSON("no issue ID provided")
	}

	in, err := gatherUpdateInput(ctx, cmd)
	if err != nil {
		return err
	}
	if isUpdateInputNoop(in) {
		fmt.Println("No updates specified")
		return nil
	}

	// Derive success-output format from the global JSON decision (--json OR
	// --format json OR config), the same signal reportUpdateFailures uses, so
	// success output and the failure report never disagree on format within one
	// invocation. This matches the non-proxied path in update.go.
	jsonOut := jsonOutput
	var updated []*types.Issue
	// failures accumulates every requested ID that could not be updated —
	// generic per-ID errors as well as a lost --claim race. In a mixed batch a
	// later winner must NOT flip the exit code back to success and hide the
	// failed IDs from exit-code automation; report them all and exit non-zero,
	// mirroring the non-proxied path in update.go (beads audit finding #10).
	var failures []updateIDFailure

	for _, id := range args {
		issue, fail, err := applyUpdateProxiedOne(ctx, id, in)
		if err != nil {
			return err
		}
		if fail != nil {
			failures = append(failures, *fail)
			continue
		}
		if jsonOut {
			updated = append(updated, issue)
		} else {
			fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
		}
	}

	if jsonOut && len(updated) > 0 {
		_ = outputJSON(updated)
	}
	if len(failures) > 0 {
		return reportUpdateFailures(failures, len(args))
	}
	return nil
}

// proxiedIssueLifecycle asks the unit-of-work provider for the write role, the
// same way proxiedIssueReader asks it for the read one.
func proxiedIssueLifecycle() (issueops.Lifecycle, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.IssueLifecycleSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the issue-lifecycle surface", uowProvider)
	}
	return src.IssueLifecycle()
}

// applyUpdateProxiedOne applies one issue's update — plain or --claim —
// through issueops.Lifecycle. What stays here is this surface's own protocol:
// the template guard, the advisory reassign pre-read, the per-id failure
// taxonomy the multi-id batch needs and the notes-overwrite warning. Hooks are
// NOT among them: they fire from the write plumbing now (the notifying provider
// wired in main.go), which is what makes an update fire the same events here as
// it does on the embedded path.
//
// Provenance carries the commit message this path has always written, so `bd
// dolt log` reads the same after the move as before it. The plane is
// deliberately NOT restricted: `bd update` has always resolved a wisp id,
// unlike the HTTP claim endpoint, which serves durable issues only.
//
// The pre-read is a read of its own, one transaction earlier than the
// mutation, so the guards it applies are ADVISORY. The mutation's own guards
// are all inside the contract's transaction, including the fence and the close
// policy.
func applyUpdateProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, *updateIDFailure, error) {
	ops, err := proxiedIssueLifecycle()
	if err != nil {
		return nil, nil, HandleError("%v", err)
	}
	before, fail := proxiedUpdateTarget(ctx, id, in)
	if fail != nil {
		return nil, fail, nil
	}
	patch, err := proxiedUpdatePatch(in, before)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}, nil
	}
	notesOverwritten := replacesExistingNotes(before.Notes, in.fields)

	var expectedStatus *issueops.Status
	if in.ifStatus != nil {
		// gatherUpdateInput refuses --if-assignee/--if-status alongside
		// --claim, which is also what the request contract requires of a claim,
		// so a claim reaches the contract with both guards nil by construction.
		expected := issueops.Status(*in.ifStatus)
		expectedStatus = &expected
	}
	result, err := runCommandUpdateMutation(ctx, ops, commandUpdateMutation{
		actor:            actor,
		issueID:          id,
		patch:            patch,
		claim:            in.claim,
		force:            in.force,
		expectedAssignee: in.ifAssignee,
		expectedStatus:   expectedStatus,
		provenance:       fmt.Sprintf("bd: update %s", id),
	})
	if err != nil {
		// Cancellation is not a verdict on this issue — SIGINT cancels bd's
		// root context, and every remaining id in the batch would fail the same
		// way, so abort the batch. A commit that itself failed with a context
		// error is indistinguishable from a cancellation between attempts;
		// aborting is the right call for both.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, proxiedUpdateFailure(id, err), nil
	}
	updated := result.Issue
	if updated == nil {
		fmt.Fprintf(os.Stderr, "Error updating %s: the update reported no issue\n", id)
		return nil, &updateIDFailure{ID: id, Error: "updating: no issue returned"}, nil
	}
	// `bd update` has never printed dependency records; the direct route drops
	// them for the same reason.
	updated.Dependencies = nil
	if in.claim {
		// A CLAIM answers an already-published surface, and that surface is the
		// bare row: issueops.ClaimResult omits labels, dependency records and
		// comments, but routing this path through Lifecycle hydrates labels.
		// Stripping them keeps `bd update --claim --json` byte-identical to the
		// v0 claim response, which TestProxiedServerServeClaim asserts with an
		// EMPTY allowlist. A plain update carries labels, as the direct route
		// does.
		updated.Labels = nil
		updated.Comments = nil
	}

	// Post-commit reporting: the write has landed, so these run exactly once no
	// matter how many attempts the contract's conflict retry burned.
	if notesOverwritten {
		warnNotesReplacement(id)
	}
	return updated, nil, nil
}

// proxiedUpdateTarget reads the row an update is about through the query role,
// for the things the mutation's own result cannot answer: whether the target is
// a template, whether an unguarded assignee edit is about to take the issue
// from a live foreign holder, whether --notes is about to replace existing
// notes, and whether --defer="" should also clear a deferred status. It no
// longer reads the pre-state to decide a hook: a status-crossing update fires
// on_update and nothing else, from the plumbing, exactly as it does on the
// embedded path.
func proxiedUpdateTarget(ctx context.Context, id string, in *updateInput) (*types.Issue, *updateIDFailure) {
	rd, err := proxiedIssueReader()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)}
	}
	details, err := rd.Get(ctx, issueops.GetRequest{ID: id})
	if err != nil {
		if errors.Is(err, issueops.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return nil, &updateIDFailure{ID: id, Error: "issue not found"}
		}
		fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)}
	}
	current := &details.Issue
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return nil, &updateIDFailure{ID: id, Error: err.Error()}
	}
	// bd-98s5c: an unguarded assignee update must not silently overwrite
	// another actor's live claim. Skipped under --if-assignee, whose CAS names
	// the holder explicitly (park stays possible without --force), and under
	// --claim, whose CAS is itself the anti-steal gate. The contract fences the
	// same transfer inside the mutation with ErrAlreadyClaimed; this pre-read is
	// what keeps the advice a user reads identical on both routes. A policy
	// refusal: terminal per-issue failure, exit 1, never GuardMismatch/13.
	if newAssignee, ok := in.fields["assignee"].(string); ok && in.ifAssignee == nil && !in.claim {
		if err := validateIssueReassignable(id, current, actor, newAssignee,
			proxiedClaimPoolAliases(ctx), in.force); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return nil, &updateIDFailure{ID: id, Error: err.Error()}
		}
	}
	return current, nil
}

// proxiedClaimPoolAliases is uowClaimPoolAliases for a caller that owns no unit
// of work: the pre-read fence's claim.pools lookup opens a short-lived unit of
// work of its own, and only when the fence is otherwise about to refuse, which
// is what the thunk is for. A read that fails yields no aliases, the same
// fail-closed answer its siblings give.
func proxiedClaimPoolAliases(ctx context.Context) func() []string {
	return func() []string {
		if uowProvider == nil {
			return nil
		}
		uw, err := uowProvider.NewUOW(ctx)
		if err != nil {
			return nil
		}
		defer uw.Close(ctx)
		return uowClaimPoolAliases(ctx, uw)()
	}
}

// proxiedUpdateFailure sorts a refused update into the per-id verdicts. A guard
// refusal sets GuardMismatch so the batch exits 13 rather than 1.
//
// The one verdict it cannot reproduce is stage attribution: "opening unit of
// work" and "committing" were told apart by watching the provider this path no
// longer owns, so both now read as the generic update failure. The id still
// fails, loudly and non-zero.
func proxiedUpdateFailure(id string, err error) *updateIDFailure {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return &updateIDFailure{ID: id, Error: "issue not found"}
	case errors.Is(err, storage.ErrAlreadyClaimed), errors.Is(err, storage.ErrNotClaimable):
		fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("claiming issue: %v", err)}
	case errors.Is(err, storage.ErrCloseOpenChildren):
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return &updateIDFailure{ID: id, Error: err.Error()}
	case errors.Is(err, storage.ErrCloseBlocked):
		fmt.Fprintf(os.Stderr, "%v (use --force to override)\n", err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("%v (use --force to override)", err)}
	case uow.IsSerializationError(err):
		// The contract spent its retry budget losing Dolt's commit-time merge.
		// The write did NOT land; fail loudly instead of exiting 0.
		fmt.Fprintf(os.Stderr, "Error updating %s: retries exhausted on write conflicts: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("retries exhausted on write conflicts: %v", err)}
	case isGuardMismatch(err):
		// bd-wsqvw guard verdict: the precondition no longer holds, nothing was
		// written. Loud and non-zero, never collapsed to success —
		// GuardMismatch routes the batch to ExitGuardMismatch.
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("precondition failed: %v", err), GuardMismatch: true}
	default:
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}
	}
}

// proxiedUpdatePatch reshapes gathered CLI input into the contract's typed
// patch. The field map goes through the same builder the direct route uses; the
// edits this input keeps beside that map (labels, reparent, the merge-shaped
// note and metadata edits) are folded in here, so both routes end up describing
// the same mutation.
//
// The merge-shaped edits stay merge-shaped: the contract resolves them against
// the row re-read inside the mutation transaction. Pre-merging them against
// `before` is what silently erased keys a concurrent writer had committed.
func proxiedUpdatePatch(in *updateInput, before *types.Issue) (issueops.IssuePatch, error) {
	patch, err := buildUpdatePatch(in.fields)
	if err != nil {
		return issueops.IssuePatch{}, err
	}
	patch.Labels.Add = in.addLabels
	patch.Labels.Remove = in.removeLabels
	if in.setLabels != nil {
		patch.Labels.Replace = setField(*in.setLabels)
	}
	if in.reparent != nil {
		patch.ParentID = setField(*in.reparent)
	}
	if in.hasAppendNotes {
		patch.AppendNotes = setField(in.appendNotes)
	}
	if len(in.mergeMetadataIn) > 0 {
		patch.Metadata.Merge = setField(in.mergeMetadataIn)
	}
	if len(in.setMetadata) > 0 {
		set, err := parseSetMetadataFlags(in.setMetadata)
		if err != nil {
			return issueops.IssuePatch{}, err
		}
		patch.Metadata.Set = set
	}
	if len(in.unsetMetadata) > 0 {
		patch.Metadata.Unset = in.unsetMetadata
	}
	// GH#3233: --defer="" restores ready visibility only if the issue was
	// actually deferred, exactly as the direct route decides it.
	if in.clearDeferStatus && before.Status == types.StatusDeferred {
		patch.Status = setField(types.StatusOpen)
	}
	return patch, nil
}
