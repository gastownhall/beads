package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/worktreeremove"
)

type singleWorktreeStringFlag struct {
	name  string
	value string
	set   bool
}

func (flag *singleWorktreeStringFlag) Set(value string) error {
	if flag.set {
		return fmt.Errorf("--%s may be specified only once", flag.name)
	}
	flag.set = true
	if value == "" {
		return fmt.Errorf("--%s requires a non-empty value", flag.name)
	}
	flag.value = value
	return nil
}

func (flag *singleWorktreeStringFlag) String() string { return flag.value }

func (flag *singleWorktreeStringFlag) Type() string { return "string" }

type singleWorktreeBoolFlag struct {
	name  string
	value bool
	set   bool
}

func (flag *singleWorktreeBoolFlag) Set(value string) error {
	if flag.set {
		return fmt.Errorf("--%s may be specified only once", flag.name)
	}
	flag.set = true
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("invalid boolean value %q", value)
	}
	flag.value = parsed
	return nil
}

func (flag *singleWorktreeBoolFlag) String() string { return strconv.FormatBool(flag.value) }

func (flag *singleWorktreeBoolFlag) Type() string { return "bool" }

func (flag *singleWorktreeBoolFlag) IsBoolFlag() bool { return true }

type worktreeRemoveOptions struct {
	force      singleWorktreeBoolFlag
	mergedInto singleWorktreeStringFlag
}

func (options *worktreeRemoveOptions) validate() error {
	if options.force.set && options.mergedInto.set {
		return fmt.Errorf("--force and --merged-into cannot be used together")
	}
	return nil
}

func newWorktreeRemoveCommand() *cobra.Command {
	return newWorktreeRemoveCommandWithHooks(worktreeRemoveHooks{})
}

func newWorktreeRemoveCommandWithHook(beforeFinalCheck func() error) *cobra.Command {
	return newWorktreeRemoveCommandWithHooks(worktreeRemoveHooks{beforeFinalCheck: beforeFinalCheck})
}

type worktreeRemoveHooks struct {
	afterTargetResolution func() error
	beforeFinalCheck      func() error
	beforeRemove          func() error
	afterRemoval          func() error
}

func newWorktreeRemoveCommandWithHooks(hooks worktreeRemoveHooks) *cobra.Command {
	options := &worktreeRemoveOptions{
		force:      singleWorktreeBoolFlag{name: "force"},
		mergedInto: singleWorktreeStringFlag{name: "merged-into"},
	}
	command := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a worktree with safety checks",
		Long: `Remove a registered git worktree with fail-closed safety checks.

Without --force, the target must be clean and its pinned HEAD must be contained
in either the configured upstream or the single comparator selected by
--merged-into. Comparators may be full refs, unambiguous short ref names, or
full commit object IDs. Revision expressions and worktree-local pseudorefs such
as HEAD and ORIG_HEAD are rejected.

--force skips cleanliness and containment requirements, but it does not skip
registered-identity and concurrent-change checks. --force and --merged-into
are mutually exclusive, and each flag may be specified at most once.

Worktree removal and .gitignore cleanup are not atomic. If removal succeeds but
cleanup fails, this command returns an error that explicitly reports the
worktree as removed; it does not claim or attempt a rollback.

Examples:
  bd worktree remove feature-auth                    # Check the configured upstream
  bd worktree remove feature-auth --merged-into main # Check containment in main
  bd worktree remove feature-auth --force            # Skip clean/containment checks`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			return options.validate()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorktreeRemove(cmd, args, options, hooks)
		},
	}
	command.Flags().Var(&options.force, "force", "Skip cleanliness and containment checks")
	command.Flags().Lookup("force").NoOptDefVal = "true"
	command.Flags().Var(&options.mergedInto, "merged-into", "Require worktree HEAD to be contained in this ref")
	return command
}

var worktreeRemoveCmd = newWorktreeRemoveCommand()

// worktreeRemovalRequest contains the command-selected policy mode. The Git
// adapter resolves the registered target independently from this request.
type worktreeRemovalRequest struct{ mode worktreeremove.Mode }

// worktreeRemovalApproval contains adapter observations for the policy. The
// policy, not the observer, constructs the destructive mutation and cleanup.
type worktreeRemovalApproval struct {
	facts      worktreeremove.PrepareFacts
	prepareErr error
}

type worktreeRemovalRevalidation struct {
	facts worktreeremove.RevalidationFacts
	err   error
}

// worktreeRemovalObserver collects typed policy facts without performing the
// destructive Git operation.
type worktreeRemovalObserver interface {
	Prepare(context.Context, worktreeRemovalRequest) (worktreeRemovalApproval, error)
	Revalidate(context.Context, worktreeRemovalApproval) (worktreeRemovalRevalidation, error)
	Failure(context.Context, worktreeRemovalApproval, error) (worktreeRemovalFailure, error)
}

// worktreeRemovalFailure retains command-edge diagnostics while keeping its
// state classification in the typed policy facts.
type worktreeRemovalFailure struct {
	facts  worktreeremove.FailureFacts
	render func(worktreeremove.FailureKind, error) error
}

// worktreeRemovalPreMutationError marks an interruption before Git receives
// the destructive command, so it must not enter post-failure reinspection.
type worktreeRemovalPreMutationError struct{ err error }

func (err *worktreeRemovalPreMutationError) Error() string { return err.err.Error() }

func (err *worktreeRemovalPreMutationError) Unwrap() error { return err.err }

// worktreeRemovalMutator performs only operations approved by the policy.
type worktreeRemovalMutator interface {
	Remove(context.Context, worktreeremove.Mutation) error
	Cleanup(context.Context, worktreeremove.Cleanup) error
}

// worktreeRemovalPresenter renders command outcomes after mutation state is
// known. It intentionally does not observe the filesystem or Git.
type worktreeRemovalPresenter interface {
	Removed(worktreeremove.Mutation) error
	Failure(worktreeremove.FailureKind, error)
}

// runWorktreeRemovalOrchestration sequences the three policy phases around
// the destructive mutation. Adapter-private filesystem evidence never crosses
// this boundary; only typed facts and the approved mutation do.
func runWorktreeRemovalOrchestration(
	ctx context.Context,
	request worktreeRemovalRequest,
	observer worktreeRemovalObserver,
	mutator worktreeRemovalMutator,
	presenter worktreeRemovalPresenter,
) error {
	approval, err := observer.Prepare(ctx, request)
	if err != nil {
		return fmt.Errorf("cannot prepare worktree removal: %w", err)
	}
	plan, err := worktreeremove.Prepare(worktreeremove.Request{Mode: request.mode}, approval.facts)
	if err != nil {
		if approval.prepareErr != nil {
			return fmt.Errorf("cannot prepare worktree removal: %w", approval.prepareErr)
		}
		return fmt.Errorf("cannot prepare worktree removal: %w", err)
	}
	if approval.prepareErr != nil {
		return fmt.Errorf("cannot prepare worktree removal: %w", approval.prepareErr)
	}
	mutation := plan.Mutation()
	cleanup, requiresCleanup := plan.Cleanup()

	revalidation, err := observer.Revalidate(ctx, approval)
	if err != nil {
		return err
	}
	if err := worktreeremove.Revalidate(plan, revalidation.facts); err != nil {
		if revalidation.err != nil {
			return fmt.Errorf("worktree changed before removal: %w; nothing was removed", revalidation.err)
		}
		return err
	}
	if revalidation.err != nil {
		return fmt.Errorf("worktree changed before removal: %w; nothing was removed", revalidation.err)
	}

	if err := mutator.Remove(ctx, mutation); err != nil {
		var partial *worktreeRemovalPartialError
		if errors.As(err, &partial) {
			return err
		}
		var interrupted *worktreeRemovalPreMutationError
		if errors.As(err, &interrupted) {
			return err
		}
		failureObservation, failureErr := observer.Failure(ctx, approval, err)
		if failureErr != nil {
			return failureErr
		}
		failure, classifyErr := worktreeremove.ClassifyFailure(plan, failureObservation.facts, err)
		if classifyErr != nil {
			return fmt.Errorf("cannot classify worktree removal failure: %w", classifyErr)
		}
		presenter.Failure(failure.Kind, failure.RemoveErr)
		if failureObservation.render != nil {
			return failureObservation.render(failure.Kind, failure.RemoveErr)
		}
		return fmt.Errorf("git worktree remove failed: %w", failure.RemoveErr)
	}
	if requiresCleanup {
		if err := mutator.Cleanup(ctx, cleanup); err != nil {
			return err
		}
	}
	return presenter.Removed(mutation)
}
