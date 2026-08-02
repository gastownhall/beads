package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/fs"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// proxiedUpdateRetryMaxElapsed bounds the whole-attempt retry loop for one
// issue's update. A var so tests can shrink it when exercising conflict
// exhaustion; it tracks the shared default rather than restating it.
var proxiedUpdateRetryMaxElapsed = uow.DefaultTxRetryMaxElapsed

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

// proxiedUpdateAttempt is what one read-merge-write attempt hands back to the
// shared retry loop. Exactly one of issue and fail is set; before and
// notesOverwritten feed the reporting that must run once per landed write
// rather than once per attempt.
type proxiedUpdateAttempt struct {
	issue            *types.Issue
	before           *types.Issue
	fail             *updateIDFailure
	notesOverwritten bool
}

// uowStageProvider records the errors the two stages the shared retry loop
// owns — opening the unit of work and committing it — hand back on the most
// recent attempt, so applyUpdateProxiedOne can attribute the error it gets
// from uow.RunTxResultWithin to the stage that produced it. The bespoke loop
// this replaced attributed errors inline, at the site that produced them; the
// recorded errors put attribution back on identity rather than on guesswork
// about what an error looks like. Both fields are reset on every NewUOW, so
// they describe the final attempt only.
type uowStageProvider struct {
	uow.UnitOfWorkProvider
	newUOWErr error
	commitErr error
}

func (p *uowStageProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	p.newUOWErr, p.commitErr = err, nil
	if err != nil {
		return uw, err
	}
	return &uowStageRecorder{UnitOfWork: uw, provider: p}, nil
}

type uowStageRecorder struct {
	uow.UnitOfWork
	provider *uowStageProvider
}

func (u *uowStageRecorder) Commit(ctx context.Context, message string) error {
	err := u.UnitOfWork.Commit(ctx, message)
	u.provider.commitErr = err
	return err
}

// applyUpdateProxiedOne applies one issue's update through uow.RunTxResultWithin,
// which redoes the WHOLE read-merge-write in a fresh unit of work when Dolt
// reports a serialization failure. It is the retry/commit implementation every
// unit-of-work write path shares: uow.RunTx and uow.RunTxResult both delegate
// to it.
//
// The retry must wrap the whole attempt, never just the commit: a
// serialization failure means the server already rolled the transaction back,
// so re-committing the same session (the old uow.CommitWithRetries call) can
// only ever produce "nothing to commit" — which the old code swallowed,
// printing "✓ Updated" and exiting 0 for a write that was silently lost.
// Redoing the attempt re-reads the winner's committed row, so merge
// operations (metadata edits, note appends) resolve against authoritative
// state instead of erasing it.
func applyUpdateProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, *updateIDFailure, error) {
	if uowProvider == nil {
		return nil, nil, HandleError("proxied-server UOW provider not initialized")
	}

	provider := &uowStageProvider{UnitOfWorkProvider: uowProvider}
	attempt, err := uow.RunTxResultWithin(ctx, provider, proxiedUpdateRetryMaxElapsed,
		func(ctx context.Context, uw uow.UnitOfWork) (proxiedUpdateAttempt, string, error) {
			return applyUpdateProxiedAttempt(ctx, uw, id, in)
		})
	if err != nil {
		// The retry loop hands back the error a stage returned, unchanged
		// (backoff unwraps its Permanent envelope), so the errors recorded by
		// provider identify the stage by identity. The one error no stage
		// produced is the context's own: backoff substitutes ctx.Err() when
		// cancellation cuts the loop short between attempts.
		switch {
		case provider.newUOWErr != nil && errors.Is(err, provider.newUOWErr):
			fmt.Fprintf(os.Stderr, "Error opening unit of work for %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("opening unit of work: %v", err)}, nil
		case uow.IsSerializationError(err):
			// Retries exhausted while losing Dolt's commit-time merge. The
			// write did NOT land; fail loudly instead of exiting 0.
			fmt.Fprintf(os.Stderr, "Error updating %s: retries exhausted on write conflicts: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("retries exhausted on write conflicts: %v", err)}, nil
		case provider.commitErr != nil && errors.Is(err, provider.commitErr):
			fmt.Fprintf(os.Stderr, "Error committing %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("committing: %v", err)}, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Cancellation cut the retry loop short between attempts (SIGINT
			// cancels bd's root context). That is not a per-issue verdict:
			// abort the whole batch, as the loop this replaced did. A commit
			// that itself failed with a context error is NOT this case — it is
			// a per-ID commit failure, caught by the arm above.
			return nil, nil, err
		default:
			// Unreachable today: the attempt returns terminal per-issue
			// failures as an attempt result, never as an error, so the only
			// errors it can produce are serialization failures. Attribute
			// anything new it grows to the update, which is where it came from.
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}, nil
		}
	}
	if attempt.fail != nil {
		return nil, attempt.fail, nil
	}

	// Post-commit reporting: the write has landed (or was the legitimately
	// empty working set of a wisp-only update, which RunTxResultWithin
	// tolerates), so these run exactly once no matter how many attempts the
	// conflict retry burned.
	if attempt.notesOverwritten {
		warnNotesReplacement(id)
	}
	if err := fireProxiedUpdateHooks(ctx, attempt.before, attempt.issue); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
	}
	return attempt.issue, nil, nil
}

// applyUpdateProxiedAttempt runs one full read-merge-write attempt in the fresh
// unit of work handed to it. It returns a serialization failure verbatim so the
// shared retry loop redoes the whole attempt — the server-side rollback
// guarantees nothing landed. Terminal per-issue failures (not found, claim
// conflicts, guard mismatches) print to stderr and come back as a non-nil fail
// with an empty commit message, so nothing is committed, the multi-ID loop
// records the failed ID, keeps going, and still exits non-zero — matching the
// non-proxied path. A guard refusal sets fail.GuardMismatch so the exit code
// distinguishes it (ExitGuardMismatch vs 1).
func applyUpdateProxiedAttempt(ctx context.Context, uw uow.UnitOfWork, id string, in *updateInput) (proxiedUpdateAttempt, string, error) {
	issueUC := uw.IssueUseCase()
	current, err := issueUC.GetIssue(ctx, id)
	if err != nil || current == nil {
		wispCurrent, wispErr := issueUC.GetWisp(ctx, id)
		if wispErr == nil && wispCurrent != nil {
			current = wispCurrent
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)})
		} else {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: "issue not found"})
		}
	}
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
	}

	// bd-98s5c: an unguarded assignee update must not silently overwrite
	// another actor's live claim. Skipped under --if-assignee: that CAS names
	// the holder explicitly (park stays possible without --force). Also
	// skipped under --claim: the claim CAS is itself the anti-steal gate (a
	// foreign live claim fails it with the canonical "already claimed" copy),
	// and an assignee edit that rides a WON claim only ever touches the
	// actor's own fresh claim. The proxied-server path is where cross-actor
	// collisions actually happen — every shared-dolt-server clone writes
	// through here. A policy refusal: terminal per-issue failure, exit 1,
	// never GuardMismatch/13.
	if newAssignee, ok := in.fields["assignee"].(string); ok && in.ifAssignee == nil && !in.claim {
		if err := validateIssueReassignable(id, current, actor, newAssignee,
			uowClaimPoolAliases(ctx, uw), in.force); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
		}
	}

	spec := buildUpdateSpecForIssue(current, in)
	notesOverwritten := replacesExistingNotes(current.Notes, in.fields)

	updated, err := issueUC.ApplyUpdate(ctx, id, spec, actor)
	if err != nil {
		if uow.IsSerializationError(err) {
			return proxiedUpdateAttempt{}, "", err
		}
		if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
			fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("claiming issue: %v", err)})
		}
		// Close policy refused the status change. Same copy the proxied close
		// prints for the same two refusals, so the boundary reads identically
		// whichever verb a script reached it through. A policy refusal is a
		// terminal per-issue failure — exit 1, never GuardMismatch/13.
		if errors.Is(err, storage.ErrCloseOpenChildren) {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
		}
		if errors.Is(err, storage.ErrCloseBlocked) {
			fmt.Fprintf(os.Stderr, "%v (use --force to override)\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("%v (use --force to override)", err)})
		}
		if isGuardMismatch(err) {
			// bd-wsqvw guard verdict: the precondition no longer holds, nothing
			// was written. Loud and non-zero, never collapsed to success —
			// GuardMismatch routes the batch to ExitGuardMismatch.
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("precondition failed: %v", err), GuardMismatch: true})
		}
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)})
	}

	// The commit belongs to RunTxResultWithin: it retries the whole attempt on a
	// serialization failure (Dolt rolled the transaction back server-side, so
	// nothing landed) and tolerates "nothing to commit", the legitimately-empty
	// working set of a wisp-only update — those live in dolt_ignored tables, so a
	// successful ApplyUpdate can leave the Dolt commit layer with nothing to do.
	// The lost-write flavor of nothing-to-commit — re-committing a rolled-back
	// session — cannot arise, because each attempt commits its own fresh unit of
	// work exactly once.
	return proxiedUpdateAttempt{
		issue:            updated,
		before:           current,
		notesOverwritten: notesOverwritten,
	}, fmt.Sprintf("bd: update %s", id), nil
}

// failedUpdateAttempt returns a terminal per-issue failure with no commit
// message, so the shared retry loop skips the commit and stops retrying.
func failedUpdateAttempt(fail *updateIDFailure) (proxiedUpdateAttempt, string, error) {
	return proxiedUpdateAttempt{fail: fail}, "", nil
}

func fireProxiedUpdateHooks(ctx context.Context, before, after *types.Issue) error {
	if after == nil {
		return nil
	}
	runner, err := proxiedHookRunner(ctx)
	if err != nil {
		return fmt.Errorf("hook runner: %w", err)
	}
	if runner == nil {
		return nil
	}
	if err := runner.RunSync(hooks.EventUpdate, after); err != nil {
		return fmt.Errorf("on_update hook: %w", err)
	}
	if before != nil &&
		before.Status != types.StatusClosed &&
		after.Status == types.StatusClosed {
		if err := runner.RunSync(hooks.EventClose, after); err != nil {
			return fmt.Errorf("on_close hook: %w", err)
		}
	}
	return nil
}

func proxiedHookRunner(ctx context.Context) (*hooks.Runner, error) {
	if hookRunner != nil {
		return hookRunner, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	fsProvider := fs.NewFileSystemProvider(cwd, newBeadsDirTemplates(), newFileSystemAdapters())
	resolution := fsProvider.BeadsDirFSUseCase().ResolveBeadsDir(ctx)
	if resolution.BeadsDir == "" {
		return nil, nil
	}
	return hooks.NewRunner(filepath.Join(resolution.BeadsDir, "hooks")), nil
}

// buildUpdateSpecForIssue translates gathered CLI input into a domain
// UpdateSpec. It never pre-merges row state: merge-shaped edits are passed as
// operation keys and resolved by the repository inside the mutation
// transaction.
func buildUpdateSpecForIssue(current *types.Issue, in *updateInput) domain.UpdateSpec {
	fields := make(map[string]any, len(in.fields))
	for k, v := range in.fields {
		fields[k] = v
	}

	if in.clearDeferStatus && current.Status == types.StatusDeferred {
		fields["status"] = string(types.StatusOpen)
	}
	// Metadata edits and note appends pass through as merge OPERATIONS: the
	// repository resolves them against the row re-read inside the mutation
	// transaction (issueops.ResolveMergeOps via the domain/db Update path).
	// Merging here against `current` — a read from this unit of work's MVCC
	// snapshot — silently erased keys a concurrent writer committed after our
	// snapshot was taken: both processes exited 0, one write vanished.
	if in.hasAppendNotes {
		fields[issueops.OpAppendNotes] = in.appendNotes
	}
	if len(in.mergeMetadataIn) > 0 {
		fields[issueops.OpMergeMetadata] = in.mergeMetadataIn
	}
	if len(in.setMetadata) > 0 {
		fields[issueops.OpSetMetadata] = in.setMetadata
	}
	if len(in.unsetMetadata) > 0 {
		fields[issueops.OpUnsetMetadata] = in.unsetMetadata
	}
	// --force means both of its halves here too. The assignee half is applied
	// above by validateIssueReassignable; this is the close-policy half, which
	// the repository pops before it validates fields.
	if in.force {
		fields[issueops.OpForceClosePolicy] = true
	}

	return domain.UpdateSpec{
		Fields:           fields,
		Claim:            in.claim,
		AddLabels:        in.addLabels,
		RemoveLabels:     in.removeLabels,
		SetLabels:        in.setLabels,
		Reparent:         in.reparent,
		ExpectedAssignee: in.ifAssignee,
		ExpectedStatus:   in.ifStatus,
	}
}
