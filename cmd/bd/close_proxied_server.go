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
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
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

// closeProxiedPreflight is the CLI's own close policy, decided before the
// batch runs: the template/pin/assignee fence, the epic open-children refusal
// and machine-checkable gates. None of the three is library policy — they are
// `bd close`'s, they read the issue and nothing else, and the role has no
// vocabulary for them — so they stay here and the batch is handed only the
// items that survived them.
//
// errors is indexed by ARGUMENT position, not by item, so a refusal reported
// here and one reported by the batch still print in the order the caller typed
// the ids.
type closeProxiedPreflight struct {
	items    []issueops.BatchCloseItem
	itemArgs []int
	before   map[string]*types.Issue
	errors   []string
}

type closeProxiedOutcome struct {
	id          string
	before      *types.Issue
	after       *types.Issue
	closed      bool
	auditOld    string
	auditReason string
}

// closeProxiedPostClose is the work `bd close` does AFTER the closes have
// landed: molecule auto-close, --suggest-next and --continue.
type closeProxiedPostClose struct {
	unblocked      []*types.Issue
	continueResult *ContinueResult
	autoClosedMol  *types.Issue
	warnings       []string
}

func runCloseProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return HandleErrorRespectJSON("no issue ID provided")
	}

	reasons, updatedArgs, err := resolveCloseReasons(cmd, args)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	args = updatedArgs
	if err := validateCloseReasons(reasons); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	in := gatherCloseProxiedInput(cmd)

	if in.continueOn && len(args) > 1 {
		return HandleErrorRespectJSON("--continue only works when closing a single issue")
	}
	if in.suggestNext && len(args) > 1 {
		return HandleErrorRespectJSON("--suggest-next only works when closing a single issue")
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	pre, err := closeProxiedRunPreflight(ctx, args, reasons, in)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	// THE BATCH. One request, one transaction, one Dolt commit over N ids —
	// and the request is the only thing that says where the transaction ends,
	// because the role hands out no handle to hold open. The commit message is
	// the role's, and it names what LANDED, which is what this route's own
	// message did: a skipped id stays out of the log.
	var result issueops.CloseBatchResult
	if len(pre.items) > 0 {
		closer, cerr := proxiedBatchCloser()
		if cerr != nil {
			return HandleErrorRespectJSON("%v", cerr)
		}
		result, err = closer.CloseBatch(ctx, issueops.CloseBatchRequest{
			Actor:     actor,
			Items:     pre.items,
			Session:   in.session,
			Force:     in.force,
			ClaimNext: closeClaimNextRequest(in.claimNext, in.continueOn),
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
	}

	outcomes, closeReasons := closeProxiedOutcomes(&pre, result)
	post := closeProxiedRunPostClose(ctx, args, in, outcomes)

	for _, e := range pre.errors {
		if e != "" {
			fmt.Fprintln(os.Stderr, e)
		}
	}
	for _, w := range post.warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	for i, o := range outcomes {
		if o.closed {
			audit.LogFieldChange(o.id, "status", o.auditOld, "closed", actor, o.auditReason)
			if err := fireProxiedCloseHooks(ctx, o.before, o.after); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", o.id, err)
			}
		}
		if !in.jsonOut {
			fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), formatFeedbackID(o.after.ID, o.after.Title), closeReasons[i])
		}
	}

	var claimedNextIssue *types.Issue
	if result.ClaimedNext != nil {
		claimedNextIssue = result.ClaimedNext.Issue
	}

	if !in.jsonOut {
		if post.autoClosedMol != nil {
			fmt.Printf("%s Auto-closed completed molecule %s\n", ui.RenderPass("✓"), formatFeedbackID(post.autoClosedMol.ID, post.autoClosedMol.Title))
		}
		if len(post.unblocked) > 0 {
			fmt.Printf("\nNewly unblocked:\n")
			for _, issue := range post.unblocked {
				fmt.Printf("  • %s (P%d)\n", formatFeedbackID(issue.ID, issue.Title), issue.Priority)
			}
		}
		if post.continueResult != nil {
			PrintContinueResult(post.continueResult)
		}
		if claimedNextIssue != nil {
			fmt.Printf("%s Auto-claimed next ready issue: %s (P%d)\n", ui.RenderPass("✓"), formatFeedbackID(claimedNextIssue.ID, claimedNextIssue.Title), claimedNextIssue.Priority)
		} else if in.claimNext && len(outcomes) > 0 && !in.continueOn {
			fmt.Printf("\n%s No ready issues available to claim.\n", ui.RenderWarn("✨"))
		}
	}

	if in.jsonOut && len(outcomes) > 0 {
		closedIssues := make([]*types.Issue, len(outcomes))
		for i, o := range outcomes {
			closedIssues[i] = o.after
		}
		switch {
		case len(post.unblocked) > 0:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "unblocked": post.unblocked})
		case post.continueResult != nil:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "continue": post.continueResult})
		case claimedNextIssue != nil:
			_ = outputJSON(map[string]interface{}{"closed": closedIssues, "claimed": claimedNextIssue})
		default:
			_ = outputJSON(closedIssues)
		}
	}

	if len(args) > 0 && len(outcomes) == 0 {
		return SilentExit()
	}
	return nil
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

// proxiedBatchCloser hands back the guarded close-many surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedIssueReader performs, and for the same reason: the
// accessor is where each layer is added, so a command that reached for the
// constructor would get an unlayered closer.
func proxiedBatchCloser() (issueops.BatchCloser, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.BatchCloserSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the batch-close surface", uowProvider)
	}
	return src.BatchCloser()
}

// closeProxiedRunPreflight resolves every argument and applies the CLI's own
// close policy to it, in one read-only unit of work.
func closeProxiedRunPreflight(ctx context.Context, args, reasons []string, in closeProxiedInput) (closeProxiedPreflight, error) {
	pre := closeProxiedPreflight{
		errors: make([]string, len(args)),
		before: make(map[string]*types.Issue, len(args)),
	}
	_, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
		for i, id := range args {
			refusal, current := closeProxiedCheckOne(ctx, uw, id, in)
			if refusal != "" {
				pre.errors[i] = refusal
				continue
			}
			pre.before[id] = current
			pre.items = append(pre.items, issueops.BatchCloseItem{IssueID: id, Reason: reasonForCloseIndex(reasons, i)})
			pre.itemArgs = append(pre.itemArgs, i)
		}
		return struct{}{}, nil
	})
	return pre, err
}

// closeProxiedCheckOne returns one id's refusal, or "" and the resolved
// pre-close issue.
func closeProxiedCheckOne(ctx context.Context, uw uow.UnitOfWork, id string, in closeProxiedInput) (string, *types.Issue) {
	current, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Sprintf("Issue %s not found", id), nil
	}
	if err != nil {
		return fmt.Sprintf("Error resolving %s: %v", id, err), nil
	}

	// Mirrors the ordering in closeDirectCheckOne (ga-ktn9pe.4.8): a row already at
	// literal StatusClosed has no state change for close validation to guard, so
	// the re-close skips it and reaches the engine as the idempotent no-op it has
	// always been. Both close paths must agree here — diverging is the defect
	// class #5217 closed.
	if current.Status != types.StatusClosed {
		if err := validateIssueClosable(id, current, actor, in.force); err != nil {
			return err.Error(), nil
		}
	}

	// The open-children guard is deliberately NOT pre-checked here. It lives in
	// the close transaction, which is the only place it can be race-free, and
	// it answers for every parent rather than only epics — this pre-check was
	// epic-only, so a non-epic parent got a different refusal on this route
	// than on the direct one. closeDirectCheckOne dropped its copy for the same
	// reason; closeProxiedRefusal surfaces the engine's CloseOpenChildrenError
	// unprefixed, so both routes now spell one refusal one way.

	if !in.force {
		if err := checkGateSatisfaction(current); err != nil {
			return fmt.Sprintf("cannot close %s: %s", id, err), nil
		}
	}

	return "", current
}

// closeProxiedOutcomes folds the batch's per-item outcomes back onto the
// argument list: a refusal lands in its argument's own error slot so the
// stderr report stays in typed order, and the survivors keep the shape the
// display block has always consumed.
func closeProxiedOutcomes(pre *closeProxiedPreflight, result issueops.CloseBatchResult) ([]closeProxiedOutcome, []string) {
	var outcomes []closeProxiedOutcome
	var reasons []string
	for j, outcome := range result.Outcomes {
		item := pre.items[j]
		if outcome.Err != nil {
			pre.errors[pre.itemArgs[j]] = closeProxiedRefusal(item.IssueID, outcome.Err)
			continue
		}
		before := pre.before[item.IssueID]
		oldStatus := "open"
		if before != nil && before.Status != "" {
			oldStatus = string(before.Status)
		}
		after := outcome.Issue
		if after != nil {
			// `bd close` has never printed dependency records, on either
			// route: the direct route drops them from the operation's own
			// snapshot for exactly this reason.
			after.Dependencies = nil
		}
		outcomes = append(outcomes, closeProxiedOutcome{
			id:          item.IssueID,
			before:      before,
			after:       after,
			closed:      outcome.Changed,
			auditOld:    oldStatus,
			auditReason: item.Reason,
		})
		reasons = append(reasons, item.Reason)
	}
	return outcomes, reasons
}

// closeProxiedRefusal spells one item's typed refusal the way this route has
// always spelled it. The vocabulary is matched with errors.Is rather than by
// reading the message, which is the point of the outcome carrying a typed
// error at all.
func closeProxiedRefusal(id string, err error) string {
	switch {
	case errors.Is(err, storage.ErrCloseBlocked):
		return fmt.Sprintf("%v (use --force to override)", err)
	// The open-children refusal is already a complete sentence naming the
	// issue and its count, so it passes through unprefixed — exactly as
	// closeDirectRefusal spells it. Without this arm the two routes answer the
	// same refusal differently, which is the drift this branch exists to
	// remove; it became newly reachable here when the guard moved into the
	// transaction.
	case errors.Is(err, storage.ErrCloseOpenChildren):
		return err.Error()
	case errors.Is(err, storage.ErrNotFound):
		return fmt.Sprintf("Issue %s not found", id)
	default:
		return fmt.Sprintf("Error closing %s: %v", id, err)
	}
}

// closeProxiedRunPostClose runs molecule auto-close, --suggest-next and
// --continue once the closes have committed.
//
// They are outside the batch's transaction because they are outside its
// contract, and outside is where the direct route has always run them: it
// calls autoCloseCompletedMolecule and AdvanceToNextStep after ops.Close
// returns, each in its own write. The visible consequence is a SECOND Dolt
// commit when a molecule actually auto-closes or --continue actually advances.
// A plain close writes nothing in this pass, names no commit message, and
// therefore still produces exactly one commit for the command.
func closeProxiedRunPostClose(ctx context.Context, args []string, in closeProxiedInput, outcomes []closeProxiedOutcome) closeProxiedPostClose {
	if len(outcomes) == 0 {
		return closeProxiedPostClose{}
	}

	post, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (closeProxiedPostClose, string, error) {
		var out closeProxiedPostClose
		var wrote []string

		for _, o := range outcomes {
			mol := autoCloseProxiedCompletedMolecule(ctx, uw, o.id, actor, in.session, &out.warnings)
			if mol != nil {
				out.autoClosedMol = mol
				wrote = append(wrote, "auto-close "+mol.ID)
			}
		}

		if in.suggestNext && len(args) == 1 {
			unblocked, warn := closeProxiedSuggestNext(ctx, uw, args[0])
			out.unblocked = unblocked
			if warn != "" {
				out.warnings = append(out.warnings, warn)
			}
		}

		if in.continueOn && len(args) == 1 {
			cont, warn := closeProxiedContinue(ctx, uw, args[0], !in.noAuto)
			out.continueResult = cont
			if warn != "" {
				out.warnings = append(out.warnings, warn)
			}
			if cont != nil && cont.AutoAdvanced && cont.NextStep != nil {
				wrote = append(wrote, "advance to "+cont.NextStep.ID)
			}
		}

		if len(wrote) == 0 {
			return out, "", nil
		}
		return out, "bd: " + strings.Join(wrote, "; "), nil
	})
	if err != nil {
		post.warnings = append(post.warnings, fmt.Sprintf("post-close work failed: %v", err))
	}
	return post
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

func closeProxiedSuggestNext(ctx context.Context, uw uow.UnitOfWork, closedID string) ([]*types.Issue, string) {
	unblocked, err := uw.IssueUseCase().GetNewlyUnblockedByClose(ctx, closedID)
	if err != nil {
		return nil, fmt.Sprintf("could not compute newly unblocked: %v", err)
	}
	return unblocked, ""
}

func closeProxiedContinue(ctx context.Context, uw uow.UnitOfWork, closedID string, autoClaim bool) (*ContinueResult, string) {
	result, err := AdvanceToNextStep(ctx, newUOWMolWriter(uw), closedID, autoClaim, actor)
	if err != nil {
		return nil, fmt.Sprintf("could not advance to next step: %v", err)
	}
	return result, ""
}

func autoCloseProxiedCompletedMolecule(ctx context.Context, uw uow.UnitOfWork, closedStepID string, actorName, session string, warnings *[]string) *types.Issue {
	moleculeID := proxiedFindParentMolecule(ctx, uw, closedStepID)
	if moleculeID == "" {
		return nil
	}

	root, err := uw.IssueUseCase().GetIssue(ctx, moleculeID)
	if err != nil || root == nil || root.Status == types.StatusClosed {
		return nil
	}
	if labels, err := uw.LabelUseCase().GetLabels(ctx, moleculeID); err == nil {
		root.Labels = labels
	}
	if !shouldAutoCloseCompletedRoot(root) {
		return nil
	}

	progress, err := getMoleculeProgress(ctx, uowMolReader{uw: uw}, moleculeID)
	if err != nil {
		return nil
	}
	if progress.Completed < progress.Total {
		return nil
	}

	params := domain.CloseIssueParams{Reason: "all steps complete", Session: session}
	if _, err := uw.IssueUseCase().CloseIssue(ctx, moleculeID, params, actorName); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("could not auto-close completed molecule %s: %v", moleculeID, err))
		return nil
	}
	return root
}

func proxiedFindParentMolecule(ctx context.Context, uw uow.UnitOfWork, issueID string) string {
	return findParentMolecule(ctx, uowMolReader{uw: uw}, issueID)
}
