package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/ui"
)

type deleteInput struct {
	ids        []string
	force      bool
	dryRun     bool
	jsonOutput bool
}

func gatherDeleteInput(cmd *cobra.Command, args []string) (*deleteInput, error) {
	if cmd.Flags().Changed("cascade") {
		return nil, fmt.Errorf("--cascade is not supported in proxied-server mode (delete always cascades)")
	}

	in := &deleteInput{}
	in.ids = append(in.ids, args...)

	if fromFile, _ := cmd.Flags().GetString("from-file"); fromFile != "" {
		ids, err := readIssueIDsFromFile(fromFile)
		if err != nil {
			return nil, fmt.Errorf("reading file: %w", err)
		}
		in.ids = append(in.ids, ids...)
	}
	in.ids = uniqueStrings(in.ids)

	in.force, _ = cmd.Flags().GetBool("force")
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")
	in.jsonOutput = jsonOutput
	return in, nil
}

func runDeleteProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	in, err := gatherDeleteInput(cmd, args)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if len(in.ids) == 0 {
		_ = cmd.Usage()
		return HandleError("no issue IDs provided")
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	if in.dryRun || !in.force {
		return runDeleteProxiedPreviewTx(ctx, in)
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (domain.DeleteIssuesResult, string, error) {
		issueUC := uw.IssueUseCase()

		preview, err := issueUC.PreviewDelete(ctx, in.ids)
		if err != nil {
			return domain.DeleteIssuesResult{}, "", fmt.Errorf("preview: %w", err)
		}
		if len(preview.NotFound) > 0 {
			return domain.DeleteIssuesResult{}, "", fmt.Errorf("issues not found: %s", strings.Join(preview.NotFound, ", "))
		}

		res, err := issueUC.DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs:                  in.ids,
			Cascade:              true,
			UpdateTextReferences: true,
		}, actor)
		if err != nil {
			return domain.DeleteIssuesResult{}, "", fmt.Errorf("delete: %w", err)
		}
		if res.DeletedCount == 0 {
			return domain.DeleteIssuesResult{}, "", fmt.Errorf("issues not found: %s", strings.Join(in.ids, ", "))
		}

		return res, fmt.Sprintf("bd: delete %d issue(s)", res.DeletedCount), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	renderDeleteProxiedResult(in, res)
	return nil
}

type deletePreviewResult struct {
	preview domain.DeletePreview
	res     domain.DeleteIssuesResult
}

func runDeleteProxiedPreviewTx(ctx context.Context, in *deleteInput) error {
	result, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (deletePreviewResult, error) {
		issueUC := uw.IssueUseCase()

		preview, err := issueUC.PreviewDelete(ctx, in.ids)
		if err != nil {
			return deletePreviewResult{}, fmt.Errorf("preview: %w", err)
		}
		if len(preview.NotFound) > 0 {
			return deletePreviewResult{}, fmt.Errorf("issues not found: %s", strings.Join(preview.NotFound, ", "))
		}

		res, err := issueUC.DeleteIssues(ctx, domain.DeleteIssuesParams{
			IDs:     in.ids,
			Cascade: true,
			DryRun:  true,
		}, actor)
		if err != nil {
			return deletePreviewResult{}, fmt.Errorf("preview counts: %w", err)
		}

		return deletePreviewResult{preview: preview, res: res}, nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if in.jsonOutput {
		_ = outputJSON(map[string]any{
			"would_delete":         result.res.DeletedCount,
			"dependencies_removed": result.res.DependenciesCount,
			"labels_removed":       result.res.LabelsCount,
			"events_removed":       result.res.EventsCount,
			"ids":                  in.ids,
			"not_found":            result.preview.NotFound,
			"connected":            sortedKeys(result.preview.ConnectedIssues),
			"dry_run":              in.dryRun,
		})
		return nil
	}
	renderDeletePreview(in, result.preview, result.res)
	return nil
}

func renderDeletePreview(in *deleteInput, preview domain.DeletePreview, res domain.DeleteIssuesResult) {
	fmt.Printf("\n%s\n", ui.RenderFail("⚠️  DELETE PREVIEW"))
	fmt.Printf("\nIssues to delete (%d):\n", len(in.ids))
	for _, id := range in.ids {
		title := ""
		if iss, ok := preview.Issues[id]; ok && iss != nil {
			title = iss.Title
		}
		fmt.Printf("  %s: %s\n", id, title)
	}
	fmt.Printf("\nCascade is always enabled — dependent issues will be removed.\n")
	fmt.Printf("\nWould remove:\n")
	fmt.Printf("  %d issue(s) total\n", res.DeletedCount)
	fmt.Printf("  %d dependency link(s)\n", res.DependenciesCount)
	fmt.Printf("  %d label(s)\n", res.LabelsCount)
	fmt.Printf("  %d event(s)\n", res.EventsCount)

	if len(preview.ConnectedIssues) > 0 {
		fmt.Printf("\nConnected issues (text references may be rewritten):\n")
		for _, id := range sortedKeys(preview.ConnectedIssues) {
			iss := preview.ConnectedIssues[id]
			title := ""
			if iss != nil {
				title = iss.Title
			}
			fmt.Printf("  %s: %s\n", id, title)
		}
	}

	if in.dryRun {
		fmt.Printf("\n(Dry-run mode - no changes made)\n")
		return
	}
	fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
	fmt.Printf("To proceed, run: %s\n",
		ui.RenderWarn("bd delete "+strings.Join(in.ids, " ")+" --force"))
}

func renderDeleteProxiedResult(in *deleteInput, res domain.DeleteIssuesResult) {
	if in.jsonOutput {
		_ = outputJSON(map[string]any{
			"deleted":              in.ids,
			"deleted_count":        res.DeletedCount,
			"dependencies_removed": res.DependenciesCount,
			"labels_removed":       res.LabelsCount,
			"events_removed":       res.EventsCount,
			"references_updated":   res.ReferencesUpdated,
		})
		return
	}
	fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), res.DeletedCount)
	fmt.Printf("  Removed %d dependency link(s)\n", res.DependenciesCount)
	fmt.Printf("  Removed %d label(s)\n", res.LabelsCount)
	fmt.Printf("  Removed %d event(s)\n", res.EventsCount)
	fmt.Printf("  Updated text references in %d issue(s)\n", res.ReferencesUpdated)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
