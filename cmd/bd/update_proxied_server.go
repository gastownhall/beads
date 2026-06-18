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
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

func runUpdateProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) {
	if len(args) == 0 {
		FatalErrorRespectJSON("no issue ID provided")
	}

	in := gatherUpdateInput(ctx, cmd)
	if isUpdateInputNoop(in) {
		fmt.Println("No updates specified")
		return
	}

	jsonOut, _ := cmd.Flags().GetBool("json")
	var updated []*types.Issue
	var anyUpdated bool

	for _, id := range args {
		issue, ok := applyUpdateProxiedOne(ctx, id, in)
		if !ok {
			continue
		}
		anyUpdated = true
		if jsonOut {
			updated = append(updated, issue)
		} else {
			fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
		}
	}

	if jsonOut && len(updated) > 0 {
		outputJSON(updated)
	}
	if !anyUpdated {
		os.Exit(1)
	}
}

func applyUpdateProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, bool) {
	if uowProvider == nil {
		FatalError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening unit of work for %s: %v\n", id, err)
		return nil, false
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
			return nil, false
		} else {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return nil, false
		}
	}
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return nil, false
	}

	spec, err := buildUpdateSpecForIssue(current, in)
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	updated, err := issueUC.ApplyUpdate(ctx, id, spec, actor)
	if err != nil {
		if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
			fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
		} else {
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		}
		return nil, false
	}

	if err := uw.Commit(ctx, fmt.Sprintf("bd: update %s", id)); err != nil && !isDoltNothingToCommit(err) {
		fmt.Fprintf(os.Stderr, "Error committing %s: %v\n", id, err)
		return nil, false
	}

	if err := fireProxiedUpdateHooks(ctx, current, updated); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
	}
	return updated, true
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

func buildUpdateSpecForIssue(current *types.Issue, in *updateInput) (domain.UpdateSpec, error) {
	fields := make(map[string]any, len(in.fields))
	for k, v := range in.fields {
		fields[k] = v
	}

	if in.clearDeferStatus && current.Status == types.StatusDeferred {
		fields["status"] = string(types.StatusOpen)
	}
	if in.hasAppendNotes {
		combined := current.Notes
		if combined != "" {
			combined += "\n"
		}
		combined += in.appendNotes
		fields["notes"] = combined
	}
	if len(in.mergeMetadataIn) > 0 {
		merged, err := mergeMetadata(current.Metadata, in.mergeMetadataIn)
		if err != nil {
			return domain.UpdateSpec{}, fmt.Errorf("metadata merge failed for %s: %w", current.ID, err)
		}
		fields["metadata"] = merged
	}
	if len(in.setMetadata) > 0 || len(in.unsetMetadata) > 0 {
		merged, err := applyMetadataEdits(current.Metadata, in.setMetadata, in.unsetMetadata)
		if err != nil {
			return domain.UpdateSpec{}, fmt.Errorf("metadata edit failed for %s: %w", current.ID, err)
		}
		fields["metadata"] = merged
	}

	spec := domain.UpdateSpec{
		Fields:       fields,
		Claim:        in.claim,
		AddLabels:    in.addLabels,
		RemoveLabels: in.removeLabels,
		SetLabels:    in.setLabels,
		Reparent:     in.reparent,
	}
	return spec, nil
}
