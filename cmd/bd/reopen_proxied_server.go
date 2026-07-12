package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

type reopenProxiedOutcome struct {
	id       string
	before   *types.Issue
	after    *types.Issue
	reopened bool
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

	outcomes := make([]reopenProxiedOutcome, 0, len(args))
	reopenedIssues := []*types.Issue{}
	hasError := false

	for _, id := range args {
		// Per-ID fresh UOW: commit-only retries cannot recover a lost Dolt snapshot.
		outcome, ok, soft, err := reopenProxiedOneFresh(ctx, id, reason)
		if err != nil {
			return err
		}
		if soft {
			hasError = true
			continue
		}
		if !ok || !outcome.reopened {
			continue
		}
		outcomes = append(outcomes, outcome)
		if jsonOut {
			reopenedIssues = append(reopenedIssues, outcome.after)
		} else {
			suffix := ""
			if reason != "" {
				suffix = ": " + reason
			}
			fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), outcome.id, suffix)
		}
		if err := fireProxiedReopenHooks(ctx, outcome.after); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", outcome.id, err)
		}
	}

	if jsonOut && len(reopenedIssues) > 0 {
		_ = outputJSON(reopenedIssues)
	}
	if hasError {
		return SilentExit()
	}
	return nil
}

func reopenProxiedOneFresh(ctx context.Context, id, reason string) (reopenProxiedOutcome, bool, bool, error) {
	var outcome reopenProxiedOutcome
	err := uow.RunWithFreshUOWRetries(ctx, uowProvider, fmt.Sprintf("bd: reopen %s", id), func(ctx context.Context, uw uow.UnitOfWork) error {
		outcome = reopenProxiedOutcome{}
		o, ok, softFail := reopenProxiedOne(ctx, uw, id, reason)
		if softFail {
			return errProxiedSoftFailure
		}
		if !ok {
			// already open: durable no-op, skip commit
			outcome = o
			return errProxiedNoCommit
		}
		if !o.reopened {
			outcome = o
			return errProxiedNoCommit
		}
		outcome = o
		return nil
	})
	if errors.Is(err, errProxiedSoftFailure) {
		return reopenProxiedOutcome{}, false, true, nil
	}
	if errors.Is(err, errProxiedNoCommit) {
		return outcome, true, false, nil
	}
	if err != nil && !isDoltNothingToCommit(err) {
		return reopenProxiedOutcome{}, false, false, HandleErrorRespectJSON("reopen %s: %v", id, err)
	}
	return outcome, true, false, nil
}

// reopenProxiedOne returns (outcome, reopenedOK, softFailure).
// softFailure means not found / domain error already printed.
// reopenedOK false with softFailure false means already open (not an error).
func reopenProxiedOne(ctx context.Context, uw uow.UnitOfWork, id, reason string) (reopenProxiedOutcome, bool, bool) {
	current, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
	if current == nil {
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return reopenProxiedOutcome{}, false, true
	}
	if current.Status != types.StatusClosed {
		fmt.Fprintf(os.Stderr, "%s is already %s\n", id, current.Status)
		return reopenProxiedOutcome{id: id, before: current, after: current, reopened: false}, false, false
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
		fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", id, err)
		return reopenProxiedOutcome{}, false, true
	}

	oldStatus := string(current.Status)
	if oldStatus == "" {
		oldStatus = "closed"
	}
	audit.LogFieldChange(id, "status", oldStatus, string(types.StatusOpen), actor, reason)
	return reopenProxiedOutcome{id: id, before: current, after: res.Issue, reopened: res.Reopened}, true, false
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
