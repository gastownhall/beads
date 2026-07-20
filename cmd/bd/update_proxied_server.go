package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v4"
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
// issue's update (matches uow.CommitWithRetries' budget). A var so tests can
// shrink it when exercising conflict exhaustion.
var proxiedUpdateRetryMaxElapsed = 15 * time.Second

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
		issue, failReason, err := applyUpdateProxiedOne(ctx, id, in)
		if err != nil {
			return err
		}
		if failReason != "" {
			failures = append(failures, updateIDFailure{ID: id, Error: failReason})
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

// applyUpdateProxiedOne applies one issue's update, redoing the WHOLE
// read-merge-write in a fresh unit of work when Dolt reports a serialization
// failure (the withRetryTx idiom from internal/storage/dolt).
//
// The retry must wrap the whole attempt, never just the commit: a
// serialization failure means the server already rolled the transaction back,
// so re-committing the same session (the old uow.CommitWithRetries call) can
// only ever produce "nothing to commit" — which the old code swallowed,
// printing "✓ Updated" and exiting 0 for a write that was silently lost.
// Redoing the attempt re-reads the winner's committed row, so merge
// operations (metadata edits, note appends) resolve against authoritative
// state instead of erasing it.
func applyUpdateProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, string, error) {
	if uowProvider == nil {
		return nil, "", HandleError("proxied-server UOW provider not initialized")
	}

	var issue *types.Issue
	var failReason string
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 25 * time.Millisecond
	bo.MaxElapsedTime = proxiedUpdateRetryMaxElapsed
	err := backoff.Retry(func() error {
		var retryable bool
		var attemptErr error
		issue, failReason, retryable, attemptErr = applyUpdateProxiedAttempt(ctx, id, in)
		if attemptErr == nil {
			return nil
		}
		if retryable {
			return attemptErr
		}
		return backoff.Permanent(attemptErr)
	}, backoff.WithContext(bo, ctx))
	if err != nil {
		if uow.IsSerializationError(err) {
			// Retries exhausted while losing Dolt's commit-time merge. The
			// write did NOT land; fail loudly instead of exiting 0.
			fmt.Fprintf(os.Stderr, "Error updating %s: retries exhausted on write conflicts: %v\n", id, err)
			return nil, fmt.Sprintf("retries exhausted on write conflicts: %v", err), nil
		}
		return nil, "", err
	}
	return issue, failReason, nil
}

// applyUpdateProxiedAttempt runs one full read-merge-write attempt in a fresh
// unit of work. retryable is true only for serialization failures, where the
// server-side rollback guarantees nothing landed and the whole attempt is safe
// to redo. Terminal per-issue failures (not found, claim conflicts, commit
// errors) print to stderr and return a non-empty failReason with no error, so
// the multi-ID loop records the failed ID, keeps going, and still exits
// non-zero — matching the non-proxied path.
func applyUpdateProxiedAttempt(ctx context.Context, id string, in *updateInput) (issue *types.Issue, failReason string, retryable bool, err error) {
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening unit of work for %s: %v\n", id, err)
		return nil, fmt.Sprintf("opening unit of work: %v", err), false, nil
	}
	defer uw.Close(ctx)

	issueUC := uw.IssueUseCase()
	current, err := issueUC.GetIssue(ctx, id)
	if err != nil || current == nil {
		wispCurrent, wispErr := issueUC.GetWisp(ctx, id)
		if wispErr == nil && wispCurrent != nil {
			current = wispCurrent
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
			return nil, fmt.Sprintf("resolving issue: %v", err), false, nil
		} else {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return nil, "issue not found", false, nil
		}
	}
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return nil, err.Error(), false, nil
	}

	spec := buildUpdateSpecForIssue(current, in)

	updated, err := issueUC.ApplyUpdate(ctx, id, spec, actor)
	if err != nil {
		if uow.IsSerializationError(err) {
			return nil, "", true, err
		}
		if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
			fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
			return nil, fmt.Sprintf("claiming issue: %v", err), false, nil
		}
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return nil, fmt.Sprintf("updating: %v", err), false, nil
	}

	if err := uw.Commit(ctx, fmt.Sprintf("bd: update %s", id)); err != nil {
		if uow.IsSerializationError(err) {
			// Dolt rolled the whole transaction back server-side; nothing
			// landed. Signal the caller to redo the read-merge-write.
			return nil, "", true, err
		}
		if !isDoltNothingToCommit(err) {
			fmt.Fprintf(os.Stderr, "Error committing %s: %v\n", id, err)
			return nil, fmt.Sprintf("committing: %v", err), false, nil
		}
		// "Nothing to commit" here is the legitimately-empty working set:
		// wisp-only updates live in dolt_ignored tables, so a successful
		// ApplyUpdate can leave nothing for the Dolt commit layer. The
		// lost-write flavor — nothing-to-commit from re-committing a
		// rolled-back session — cannot reach this branch because each attempt
		// commits its own fresh unit of work exactly once.
	}

	if err := fireProxiedUpdateHooks(ctx, current, updated); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
	}
	return updated, "", false, nil
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

	return domain.UpdateSpec{
		Fields:       fields,
		Claim:        in.claim,
		AddLabels:    in.addLabels,
		RemoveLabels: in.removeLabels,
		SetLabels:    in.setLabels,
		Reparent:     in.reparent,
	}
}
