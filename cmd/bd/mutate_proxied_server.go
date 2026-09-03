package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

func proxiedMutateIssue(ctx context.Context, id, commitMsg string, mutate func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error) (*types.Issue, error) {
	if uowProvider == nil {
		return nil, fmt.Errorf("proxied-server UOW provider not initialized")
	}
	var updated *types.Issue
	err := uow.RunTx(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		issue, isWisp, rerr := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
		if errors.Is(rerr, storage.ErrNotFound) {
			return "", fmt.Errorf("issue %s not found", id)
		}
		if rerr != nil {
			return "", fmt.Errorf("resolving %s: %w", id, rerr)
		}
		if err := validateIssueUpdatable(id, issue); err != nil {
			return "", err
		}
		if err := mutate(ctx, uw, issue, isWisp); err != nil {
			return "", err
		}
		if isWisp {
			updated, _ = uw.IssueUseCase().GetWisp(ctx, issue.ID)
		} else {
			updated, _ = uw.IssueUseCase().GetIssue(ctx, issue.ID)
		}
		return commitMsg, nil
	})
	if err != nil {
		return nil, err
	}
	commandDidWrite.Store(true)
	return updated, nil
}

// force applies only to assignee updates: it bypasses the live-claim reassign
// fence (bd-98s5c). Callers whose updates never carry "assignee" pass false.
func proxiedUpdateIssueFields(ctx context.Context, id, commitMsg string, updates map[string]any, force bool) (*types.Issue, error) {
	return proxiedMutateIssue(ctx, id, commitMsg, func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error {
		// bd-98s5c: an unguarded assignee update (bd assign via the proxied
		// server) must not silently overwrite another actor's live claim.
		if newAssignee, ok := updates["assignee"].(string); ok {
			if err := validateIssueReassignable(id, issue, actor, newAssignee,
				uowClaimPoolAliases(ctx, uw), force); err != nil {
				return err
			}
		}
		if isWisp {
			return uw.IssueUseCase().UpdateWisp(ctx, issue.ID, updates, actor)
		}
		return uw.IssueUseCase().UpdateIssue(ctx, issue.ID, updates, actor)
	})
}

func runAssignProxiedServer(ctx context.Context, args []string, force bool) error {
	id := args[0]
	assignee := args[1]
	updated, err := proxiedUpdateIssueFields(ctx, id, "bd: assign "+id, map[string]any{"assignee": assignee}, force)
	if err != nil {
		return HandleErrorRespectJSON("assign %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	title := issueTitleOrEmpty(updated)
	if assignee == "" {
		fmt.Printf("%s Unassigned %s\n", ui.RenderPass("✓"), formatFeedbackID(id, title))
	} else {
		fmt.Printf("%s Assigned %s to %s\n", ui.RenderPass("✓"), formatFeedbackID(id, title), assignee)
	}
	return nil
}

func runPriorityProxiedServer(ctx context.Context, args []string) error {
	id := args[0]
	priority, err := validation.ValidatePriority(args[1])
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	updated, err := proxiedUpdateIssueFields(ctx, id, "bd: priority "+id, map[string]any{"priority": priority}, false)
	if err != nil {
		return HandleErrorRespectJSON("priority %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Set priority of %s to P%d\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(updated)), priority)
	return nil
}

func runNoteProxiedServer(ctx context.Context, id, noteText string) error {
	updated, err := proxiedMutateIssue(ctx, id, "bd: note "+id, func(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool) error {
		combined := issue.Notes
		if combined != "" {
			combined += "\n"
		}
		combined += noteText
		updates := map[string]any{"notes": combined}
		if isWisp {
			return uw.IssueUseCase().UpdateWisp(ctx, issue.ID, updates, actor)
		}
		return uw.IssueUseCase().UpdateIssue(ctx, issue.ID, updates, actor)
	})
	if err != nil {
		return HandleErrorRespectJSON("note %s: %v", id, err)
	}
	if jsonOutput {
		if updated != nil {
			return outputJSON(updated)
		}
		return nil
	}
	fmt.Printf("%s Note added to %s\n", ui.RenderPass("✓"), formatFeedbackID(id, issueTitleOrEmpty(updated)))
	return nil
}

// runTagProxiedServer is `bd tag` on the proxied-server route, and it is the
// one verb in this file that does NOT go through proxiedMutateIssue.
//
// It cannot, and that is the point. proxiedMutateIssue hands its callback a
// unit of work and an isWisp boolean, which is an invitation to pick a plane —
// and the tag callback accepted it, choosing between AddWispLabel and AddLabel
// exactly the way cmd/bd/label_proxied_server.go used to before ga-26w10.
// A label edit is a patch, and issueops.Lifecycle is what applies one:
// UpdateRequest.IssuePlaneOnly stays false, so the role resolves the plane
// inside its own transaction and there is no boolean here to get backwards.
//
// The two reads it still makes are front-door work rather than plumbing.
// issueops.Reader.Get supplies the issue the template guard needs — the roles
// have no opinion about templates, and the direct route refuses one — and the
// role takes an exact id by contract, which Get's issue-then-wisp lookup is.
// The label arrives already normalized: tag.go does that before choosing a
// route, so this path and the direct one cannot disagree about what was stored.
func runTagProxiedServer(ctx context.Context, id, label string) error {
	reader, err := openIssueReader()
	if err != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, err)
	}
	details, err := reader.Get(ctx, issueops.GetRequest{ID: id})
	if errors.Is(err, storage.ErrNotFound) {
		return HandleErrorRespectJSON("tag %s: issue %s not found", id, id)
	}
	if err != nil {
		return HandleErrorRespectJSON("tag %s: resolving %s: %v", id, id, err)
	}
	if verr := validateIssueUpdatable(id, &details.Issue); verr != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, verr)
	}

	lifecycle, err := openIssueLifecycle()
	if err != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, err)
	}
	// The role commits its own Dolt version inside the storage layer, so
	// `--dolt-auto-commit batch` is deferred on the context rather than by
	// blanking a commit message — see applyLabelEdit in cmd/bd/label.go.
	ctx, err = issueOpsContext(ctx)
	if err != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, err)
	}
	result, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor:   actor,
		IssueID: details.ID,
		Patch:   issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{label}}},
	})
	if err != nil {
		return HandleErrorRespectJSON("tag %s: %v", id, err)
	}
	commandDidWrite.Store(true)

	if jsonOutput {
		if result.Issue != nil {
			return outputJSON(result.Issue)
		}
		return nil
	}
	fmt.Printf("%s Added label %q to %s\n", ui.RenderPass("✓"), label, formatFeedbackID(id, issueTitleOrEmpty(result.Issue)))
	return nil
}
