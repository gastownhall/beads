package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type reopenProxiedOutcome struct {
	id          string
	before      *types.Issue
	after       *types.Issue
	reopened    bool
	auditOld    string
	auditReason string
}

type reopenProxiedTxResult struct {
	outcomes []reopenProxiedOutcome
	hasError bool
	errors   []string
}

func runReopenProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return HandleErrorRespectJSON("no issue ID provided")
	}
	reason, _ := cmd.Flags().GetString("reason")
	jsonOut, _ := cmd.Flags().GetBool("json")

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (reopenProxiedTxResult, string, error) {
		var result reopenProxiedTxResult

		for _, id := range args {
			outcome, ok := reopenProxiedOne(ctx, uw, id, reason, &result.errors)
			if !ok {
				result.hasError = true
				continue
			}
			if outcome.reopened {
				result.outcomes = append(result.outcomes, outcome)
			}
		}

		if len(result.outcomes) == 0 {
			return result, "", nil
		}

		return result, reopenProxiedCommitMessage(result.outcomes), nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	for _, e := range res.errors {
		fmt.Fprintln(os.Stderr, e)
	}

	for _, o := range res.outcomes {
		if o.reopened {
			audit.LogFieldChange(o.id, "status", o.auditOld, string(types.StatusOpen), actor, o.auditReason)
		}
		if err := fireProxiedReopenHooks(ctx, o.after); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", o.id, err)
		}
		if !jsonOut {
			suffix := ""
			if reason != "" {
				suffix = ": " + reason
			}
			fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), o.id, suffix)
		}
	}

	if jsonOut && len(res.outcomes) > 0 {
		reopenedIssues := make([]*types.Issue, len(res.outcomes))
		for i, o := range res.outcomes {
			reopenedIssues[i] = o.after
		}
		_ = outputJSON(reopenedIssues)
	}

	if res.hasError {
		return SilentExit()
	}
	return nil
}

func reopenProxiedOne(ctx context.Context, uw uow.UnitOfWork, id, reason string, errors *[]string) (reopenProxiedOutcome, bool) {
	current, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
	if current == nil {
		*errors = append(*errors, fmt.Sprintf("Issue %s not found", id))
		return reopenProxiedOutcome{}, false
	}
	if current.Status != types.StatusClosed {
		*errors = append(*errors, fmt.Sprintf("%s is already %s", id, current.Status))
		return reopenProxiedOutcome{id: id, before: current, after: current, reopened: false}, true
	}

	params := domain.ReopenIssueParams{Reason: reason}
	var (
		res domain.ReopenIssueResult
		err error
	)
	if isWisp {
		res, err = uw.IssueUseCase().ReopenWisp(ctx, id, params, actor)
	} else {
		res, err = uw.IssueUseCase().ReopenIssue(ctx, id, params, actor)
	}
	if err != nil {
		*errors = append(*errors, fmt.Sprintf("Error reopening %s: %v", id, err))
		return reopenProxiedOutcome{}, false
	}

	oldStatus := string(current.Status)
	if oldStatus == "" {
		oldStatus = "closed"
	}
	return reopenProxiedOutcome{
		id:          id,
		before:      current,
		after:       res.Issue,
		reopened:    res.Reopened,
		auditOld:    oldStatus,
		auditReason: reason,
	}, true
}

func reopenProxiedCommitMessage(outcomes []reopenProxiedOutcome) string {
	ids := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		ids = append(ids, o.id)
	}
	return "bd: reopen " + strings.Join(ids, ", ")
}

func fireProxiedReopenHooks(ctx context.Context, after *types.Issue) error {
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
	return nil
}
