package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type closeProxiedInput struct {
	force       bool
	continueOn  bool
	noAuto      bool
	suggestNext bool
	claimNext   bool
	session     string
	jsonOut     bool
}

type closeProxiedOutcome struct {
	id     string
	before *types.Issue
	after  *types.Issue
	closed bool
}

func runCloseProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) {
	if len(args) == 0 {
		FatalErrorRespectJSON("no issue ID provided")
	}

	reasons, updatedArgs, err := resolveCloseReasons(cmd, args)
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}
	args = updatedArgs
	if err := validateCloseReasons(reasons); err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	in := gatherCloseProxiedInput(cmd)

	if in.continueOn && len(args) > 1 {
		FatalErrorRespectJSON("--continue only works when closing a single issue")
	}
	if in.suggestNext && len(args) > 1 {
		FatalErrorRespectJSON("--suggest-next only works when closing a single issue")
	}

	if uowProvider == nil {
		FatalError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		FatalErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	outcomes := make([]closeProxiedOutcome, 0, len(args))
	closedIssues := []*types.Issue{}
	for i, id := range args {
		reason := reasonForCloseIndex(reasons, i)
		outcome, ok := closeProxiedOne(ctx, uw, id, reason, in)
		if !ok {
			continue
		}
		outcomes = append(outcomes, outcome)
		if in.jsonOut {
			closedIssues = append(closedIssues, outcome.after)
		} else {
			fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(outcome.after.ID, outcome.after.Title), reason)
		}
	}

	var unblocked []*types.Issue
	if in.suggestNext && len(args) == 1 && len(outcomes) > 0 {
		unblocked = closeProxiedSuggestNext(ctx, uw, args[0])
	}

	var continueResult *ContinueResult
	if in.continueOn && len(args) == 1 && len(outcomes) > 0 {
		continueResult = closeProxiedContinue(ctx, uw, args[0], !in.noAuto)
	}

	var claimedNextIssue *types.Issue
	if in.claimNext && len(outcomes) > 0 && !in.continueOn {
		claimedNextIssue = closeProxiedClaimNext(ctx, uw, in.jsonOut)
	}

	if len(outcomes) > 0 {
		msg := closeProxiedCommitMessage(outcomes, claimedNextIssue, continueResult)
		if err := uw.Commit(ctx, msg); err != nil && !isDoltNothingToCommit(err) {
			FatalErrorRespectJSON("commit close: %v", err)
		}
		for _, o := range outcomes {
			if !o.closed {
				continue
			}
			if err := fireProxiedCloseHooks(ctx, o.before, o.after); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", o.id, err)
			}
		}
	}

	if !in.jsonOut {
		if len(unblocked) > 0 {
			fmt.Printf("\nNewly unblocked:\n")
			for _, issue := range unblocked {
				fmt.Printf("  • %s (P%d)\n", formatFeedbackID(issue.ID, issue.Title), issue.Priority)
			}
		}
		if continueResult != nil {
			PrintContinueResult(continueResult)
		}
		if claimedNextIssue != nil {
			fmt.Printf("%s Auto-claimed next ready issue: %s (P%d)\n", ui.RenderPass("✓"), formatFeedbackID(claimedNextIssue.ID, claimedNextIssue.Title), claimedNextIssue.Priority)
		}
	}

	if in.jsonOut && len(closedIssues) > 0 {
		switch {
		case len(unblocked) > 0:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "unblocked": unblocked})
		case continueResult != nil:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "continue": continueResult})
		case claimedNextIssue != nil:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "claimed": claimedNextIssue})
		default:
			_ = outputJSON(closedIssues)
		}
	}

	if len(args) > 0 && len(outcomes) == 0 {
		os.Exit(1)
	}
}

func gatherCloseProxiedInput(cmd *cobra.Command) closeProxiedInput {
	in := closeProxiedInput{}
	in.force, _ = cmd.Flags().GetBool("force")
	in.continueOn, _ = cmd.Flags().GetBool("continue")
	in.noAuto, _ = cmd.Flags().GetBool("no-auto")
	in.suggestNext, _ = cmd.Flags().GetBool("suggest-next")
	in.claimNext, _ = cmd.Flags().GetBool("claim-next")
	in.session, _ = cmd.Flags().GetString("session")
	if in.session == "" {
		in.session = os.Getenv("CLAUDE_SESSION_ID")
	}
	in.jsonOut, _ = cmd.Flags().GetBool("json")
	return in
}

func closeProxiedOne(ctx context.Context, uw uow.UnitOfWork, id, reason string, in closeProxiedInput) (closeProxiedOutcome, bool) {
	current, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
	if current == nil {
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return closeProxiedOutcome{}, false
	}

	if err := validateIssueClosable(id, current, in.force); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return closeProxiedOutcome{}, false
	}

	if !in.force && current.IssueType == types.TypeEpic {
		var openChildren int
		var err error
		if isWisp {
			openChildren, err = uw.IssueUseCase().CountOpenWispChildren(ctx, id)
		} else {
			openChildren, err = uw.IssueUseCase().CountOpenChildren(ctx, id)
		}
		if err == nil && openChildren > 0 {
			fmt.Fprintf(os.Stderr, "cannot close epic %s: %d open child issue(s); close children first or use --force to override\n", id, openChildren)
			return closeProxiedOutcome{}, false
		}
	}

	if !in.force {
		if err := checkGateSatisfaction(current); err != nil {
			fmt.Fprintf(os.Stderr, "cannot close %s: %s\n", id, err)
			return closeProxiedOutcome{}, false
		}
	}

	if !in.force {
		var blocked bool
		var blockers []string
		var err error
		if isWisp {
			blocked, blockers, err = uw.DependencyUseCase().IsWispBlocked(ctx, id)
		} else {
			blocked, blockers, err = uw.DependencyUseCase().IsBlocked(ctx, id)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error checking blockers for %s: %v\n", id, err)
			return closeProxiedOutcome{}, false
		}
		if blocked && len(blockers) > 0 {
			fmt.Fprintf(os.Stderr, "cannot close %s: blocked by open issues %v (use --force to override)\n", id, blockers)
			return closeProxiedOutcome{}, false
		}
	}

	params := domain.CloseIssueParams{Reason: reason, Session: in.session}
	var (
		res domain.CloseIssueResult
		err error
	)
	if isWisp {
		res, err = uw.IssueUseCase().CloseWisp(ctx, id, params, actor)
	} else {
		res, err = uw.IssueUseCase().CloseIssue(ctx, id, params, actor)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
		return closeProxiedOutcome{}, false
	}

	oldStatus := string(current.Status)
	if oldStatus == "" {
		oldStatus = "open"
	}
	audit.LogFieldChange(id, "status", oldStatus, "closed", actor, reason)

	autoCloseProxiedCompletedMolecule(ctx, uw, id, actor, in.session, in.jsonOut)

	return closeProxiedOutcome{id: id, before: current, after: res.Issue, closed: res.Closed}, true
}

func closeProxiedCommitMessage(outcomes []closeProxiedOutcome, claimed *types.Issue, cont *ContinueResult) string {
	ids := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		ids = append(ids, o.id)
	}
	msg := "bd: close " + strings.Join(ids, ", ")
	if cont != nil && cont.AutoAdvanced && cont.NextStep != nil {
		msg += "; advance to " + cont.NextStep.ID
	}
	if claimed != nil {
		msg += "; claim " + claimed.ID
	}
	return msg
}

func proxiedResolveIssueOrWisp(ctx context.Context, uw uow.UnitOfWork, id string) (*types.Issue, bool) {
	issue, err := uw.IssueUseCase().GetIssue(ctx, id)
	if err == nil && issue != nil {
		return issue, false
	}
	wisp, err := uw.IssueUseCase().GetWisp(ctx, id)
	if err == nil && wisp != nil {
		return wisp, true
	}
	return nil, false
}

func fireProxiedCloseHooks(ctx context.Context, before, after *types.Issue) error {
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
	if before != nil && before.Status != types.StatusClosed && after.Status == types.StatusClosed {
		if err := runner.RunSync(hooks.EventClose, after); err != nil {
			return fmt.Errorf("on_close hook: %w", err)
		}
	}
	return nil
}

func closeProxiedSuggestNext(ctx context.Context, uw uow.UnitOfWork, closedID string) []*types.Issue {
	unblocked, err := uw.IssueUseCase().GetNewlyUnblockedByClose(ctx, closedID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not compute newly unblocked: %v\n", err)
		return nil
	}
	return unblocked
}

func closeProxiedClaimNext(ctx context.Context, uw uow.UnitOfWork, jsonOut bool) *types.Issue {
	page, err := uw.IssueUseCase().GetReadyWork(ctx, types.WorkFilter{
		Status:     "open",
		Limit:      1,
		SortPolicy: types.SortPolicy("priority"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not get ready issues: %v\n", err)
		return nil
	}
	if len(page.Items) == 0 {
		if !jsonOut {
			fmt.Printf("\n%s No ready issues available to claim.\n", ui.RenderWarn("✨"))
		}
		return nil
	}

	nextIssue := page.Items[0]
	if _, err := uw.IssueUseCase().ClaimIssue(ctx, nextIssue.ID, actor); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim next issue %s: %v\n", nextIssue.ID, err)
		return nil
	}
	return nextIssue
}

func closeProxiedContinue(ctx context.Context, uw uow.UnitOfWork, closedID string, autoClaim bool) *ContinueResult {
	result, err := proxiedAdvanceToNextStep(ctx, uw, closedID, autoClaim, actor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not advance to next step: %v\n", err)
		return nil
	}
	return result
}

func autoCloseProxiedCompletedMolecule(ctx context.Context, uw uow.UnitOfWork, closedStepID string, actorName, session string, jsonOut bool) {
	moleculeID := proxiedFindParentMolecule(ctx, uw, closedStepID)
	if moleculeID == "" {
		return
	}

	root, err := uw.IssueUseCase().GetIssue(ctx, moleculeID)
	if err != nil || root == nil || root.Status == types.StatusClosed {
		return
	}
	if labels, err := uw.LabelUseCase().GetLabels(ctx, moleculeID); err == nil {
		root.Labels = labels
	}
	if !shouldAutoCloseCompletedRoot(root) {
		return
	}

	progress, err := proxiedGetMoleculeProgress(ctx, uw, moleculeID)
	if err != nil {
		return
	}
	if progress.Completed < progress.Total {
		return
	}

	params := domain.CloseIssueParams{Reason: "all steps complete", Session: session}
	if _, err := uw.IssueUseCase().CloseIssue(ctx, moleculeID, params, actorName); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not auto-close completed molecule %s: %v\n", moleculeID, err)
		return
	}
	if !jsonOut {
		fmt.Printf("%s Auto-closed completed molecule %s\n", ui.RenderPass("✓"), formatFeedbackID(moleculeID, root.Title))
	}
}

func proxiedFindParentMolecule(ctx context.Context, uw uow.UnitOfWork, issueID string) string {
	current := issueID
	for depth := 0; depth < 50; depth++ {
		deps, err := uw.DependencyUseCase().GetForIssueIDs(ctx, []string{current})
		if err != nil {
			return ""
		}
		var parent string
		for _, dep := range deps[current] {
			if dep.Type == types.DepParentChild {
				parent = dep.DependsOnID
				break
			}
		}
		if parent == "" {
			if current == issueID {
				return ""
			}
			return current
		}
		current = parent
	}
	return current
}

func proxiedLoadTemplateSubgraph(ctx context.Context, uw uow.UnitOfWork, templateID string) (*TemplateSubgraph, error) {
	root, err := uw.IssueUseCase().GetIssue(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("template %s not found", templateID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	visited := map[string]bool{root.ID: true}
	if err := proxiedLoadDescendants(ctx, uw, subgraph, root.ID, visited); err != nil {
		return nil, err
	}

	for _, issue := range subgraph.Issues {
		deps, err := uw.DependencyUseCase().GetForIssueIDs(ctx, []string{issue.ID})
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}
		for _, dep := range deps[issue.ID] {
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

func proxiedLoadDescendants(ctx context.Context, uw uow.UnitOfWork, subgraph *TemplateSubgraph, parentID string, visited map[string]bool) error {
	dependents, err := uw.DependencyUseCase().ListWithIssueMetadata(ctx, parentID, domain.DepListFilter{
		Direction: domain.DepDirectionIn,
	})
	if err != nil {
		return fmt.Errorf("failed to get dependents of %s: %w", parentID, err)
	}

	for _, dependent := range dependents {
		if dependent.DependencyType != types.DepParentChild {
			continue
		}
		if _, exists := subgraph.IssueMap[dependent.ID]; exists {
			continue
		}
		if visited[dependent.ID] {
			continue
		}
		child := dependent.Issue
		subgraph.Issues = append(subgraph.Issues, &child)
		subgraph.IssueMap[child.ID] = &child
		visited[child.ID] = true
		if err := proxiedLoadDescendants(ctx, uw, subgraph, child.ID, visited); err != nil {
			return err
		}
	}
	return nil
}

func proxiedGetMoleculeProgress(ctx context.Context, uw uow.UnitOfWork, moleculeID string) (*MoleculeProgress, error) {
	subgraph, err := proxiedLoadTemplateSubgraph(ctx, uw, moleculeID)
	if err != nil {
		return nil, err
	}

	progress := &MoleculeProgress{
		MoleculeID:    subgraph.Root.ID,
		MoleculeTitle: subgraph.Root.Title,
		Assignee:      subgraph.Root.Assignee,
		Total:         len(subgraph.Issues) - 1,
	}

	analysis := analyzeMoleculeParallel(subgraph)
	readyIDs := make(map[string]bool)
	for id, info := range analysis.Steps {
		if info.IsReady {
			readyIDs[id] = true
		}
	}

	var steps []*StepStatus
	for _, issue := range subgraph.Issues {
		if issue.ID == subgraph.Root.ID {
			continue
		}
		step := &StepStatus{Issue: issue}
		switch issue.Status {
		case types.StatusClosed:
			step.Status = "done"
			progress.Completed++
		case types.StatusInProgress:
			step.Status = "current"
			step.IsCurrent = true
			progress.CurrentStep = issue
		case types.StatusBlocked:
			step.Status = "blocked"
		default:
			if readyIDs[issue.ID] {
				step.Status = "ready"
				if progress.NextStep == nil {
					progress.NextStep = issue
				}
			} else {
				step.Status = "pending"
			}
		}
		steps = append(steps, step)
	}

	sortStepsByDependencyOrder(steps, subgraph)
	progress.Steps = steps

	if progress.CurrentStep == nil && progress.NextStep == nil {
		for _, step := range steps {
			if step.Status == "ready" {
				progress.NextStep = step.Issue
				break
			}
		}
	}

	return progress, nil
}

func proxiedAdvanceToNextStep(ctx context.Context, uw uow.UnitOfWork, closedStepID string, autoClaim bool, actorName string) (*ContinueResult, error) {
	closedStep, err := uw.IssueUseCase().GetIssue(ctx, closedStepID)
	if err != nil || closedStep == nil {
		wisp, wErr := uw.IssueUseCase().GetWisp(ctx, closedStepID)
		if wErr != nil || wisp == nil {
			return nil, fmt.Errorf("could not get closed step: %w", err)
		}
		closedStep = wisp
	}

	result := &ContinueResult{ClosedStep: closedStep}

	moleculeID := proxiedFindParentMolecule(ctx, uw, closedStepID)
	if moleculeID == "" {
		return nil, nil
	}
	result.MoleculeID = moleculeID

	progress, err := proxiedGetMoleculeProgress(ctx, uw, moleculeID)
	if err != nil {
		return nil, fmt.Errorf("could not load molecule: %w", err)
	}

	if progress.Completed >= progress.Total {
		result.MolComplete = true
		return result, nil
	}

	var readySteps []*types.Issue
	for _, step := range progress.Steps {
		if step.Status == "ready" {
			readySteps = append(readySteps, step.Issue)
		}
	}
	if len(readySteps) == 0 {
		return result, nil
	}
	result.NextStep = readySteps[0]

	if !autoClaim {
		return result, nil
	}

	for _, candidate := range readySteps {
		_, claimErr := uw.IssueUseCase().ClaimIssueIfOpen(ctx, candidate.ID, actorName)
		if claimErr == nil {
			result.NextStep = candidate
			result.AutoAdvanced = true
			return result, nil
		}
		if errors.Is(claimErr, storage.ErrAlreadyClaimed) || errors.Is(claimErr, storage.ErrNotClaimable) {
			continue
		}
		return result, nil
	}
	return result, nil
}
