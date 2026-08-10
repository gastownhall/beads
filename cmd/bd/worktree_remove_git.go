package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/worktreeremove"
)

// gitWorktreeRemovalAdapter confines worktree-removal Git and filesystem
// observations to the command edge. The policy package receives only tags;
// FileInfo values, paths, bytes, and fingerprints remain in its plan.
type gitWorktreeRemovalAdapter struct {
	name    string
	options *worktreeRemoveOptions
	hooks   worktreeRemoveHooks
	plan    *worktreeRemovalPlan
	output  []byte
}

func (adapter *gitWorktreeRemovalAdapter) Prepare(
	ctx context.Context,
	_ worktreeRemovalRequest,
) (worktreeRemovalApproval, error) {
	plan, err := prepareWorktreeRemoval(ctx, adapter.name, adapter.options, adapter.hooks.afterTargetResolution)
	if err != nil {
		return worktreeRemovalApproval{}, err
	}
	adapter.plan = plan

	return worktreeRemovalApproval{
		facts:      plan.prepareFacts,
		prepareErr: plan.prepareErr,
	}, nil
}

func (adapter *gitWorktreeRemovalAdapter) Revalidate(
	ctx context.Context,
	_ worktreeRemovalApproval,
) (worktreeRemovalRevalidation, error) {
	if adapter.hooks.beforeFinalCheck != nil {
		if err := adapter.hooks.beforeFinalCheck(); err != nil {
			return worktreeRemovalRevalidation{}, fmt.Errorf("worktree removal interrupted before final safety check: %w", err)
		}
	}
	observation := adapter.plan.observeRevalidation(ctx)
	return worktreeRemovalRevalidation{facts: observation.facts, err: observation.err}, nil
}

func (adapter *gitWorktreeRemovalAdapter) Remove(ctx context.Context, mutation worktreeremove.Mutation) error {
	if adapter.plan == nil || mutation.TargetPath != adapter.plan.target.path || mutation.Force != adapter.plan.force {
		return fmt.Errorf("cannot remove worktree with an unapproved mutation")
	}
	if adapter.hooks.beforeRemove != nil {
		if err := adapter.hooks.beforeRemove(); err != nil {
			return &worktreeRemovalPreMutationError{
				err: fmt.Errorf("worktree removal interrupted before the destructive operation: %w", err),
			}
		}
	}

	args := []string{"-c", "core.ignorecase=false", "worktree", "remove"}
	if mutation.Force {
		args = append(args, "--force")
	}
	args = append(args, "--", mutation.TargetPath)
	output, err := adapter.plan.git.combinedOutput(ctx, adapter.plan.executionRoot, args...)
	adapter.output = output
	if err != nil {
		return err
	}
	if adapter.hooks.afterRemoval != nil {
		if err := adapter.hooks.afterRemoval(); err != nil {
			return &worktreeRemovalPartialError{
				path:  mutation.TargetPath,
				stage: "post-removal processing",
				err:   err,
			}
		}
	}
	return nil
}

func (adapter *gitWorktreeRemovalAdapter) Failure(
	ctx context.Context,
	_ worktreeRemovalApproval,
	removeErr error,
) (worktreeRemovalFailure, error) {
	observation := adapter.plan.observeRemovalFailure(ctx)
	return worktreeRemovalFailure{
		facts: observation.facts,
		render: func(kind worktreeremove.FailureKind, removeErr error) error {
			return formatWorktreeRemovalFailure(kind, observation, removeErr, adapter.output)
		},
	}, nil
}

func (adapter *gitWorktreeRemovalAdapter) Cleanup(_ context.Context, cleanup worktreeremove.Cleanup) error {
	if adapter.plan == nil || adapter.plan.gitignoreCleanup == nil || cleanup.Entry != adapter.plan.gitignoreCleanup.entry {
		return fmt.Errorf("cannot clean managed ignore entry without its approved identity")
	}
	if cleanup.Entry == "" {
		return nil
	}
	if err := adapter.plan.gitignoreCleanup.apply(); err != nil {
		return &worktreeRemovalPartialError{
			path:  adapter.plan.target.path,
			stage: ".gitignore cleanup",
			err:   err,
		}
	}
	return nil
}

type cliWorktreeRemovalPresenter struct{}

func (cliWorktreeRemovalPresenter) Removed(mutation worktreeremove.Mutation) error {
	if jsonOutput {
		result := map[string]interface{}{"removed": mutation.TargetPath}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Printf("%s Removed worktree: %s\n", ui.RenderPass("✓"), mutation.TargetPath)
	return nil
}

func (cliWorktreeRemovalPresenter) Failure(worktreeremove.FailureKind, error) {}
