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
	id       string
	before   *types.Issue
	after    *types.Issue
	reopened bool
}

func runReopenProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) {
	if len(args) == 0 {
		FatalErrorRespectJSON("no issue ID provided")
	}
	reason, _ := cmd.Flags().GetString("reason")
	jsonOut, _ := cmd.Flags().GetBool("json")

	if uowProvider == nil {
		FatalError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		FatalErrorRespectJSON("open unit of work: %v", err)
	}
	defer uw.Close(ctx)

	outcomes := make([]reopenProxiedOutcome, 0, len(args))
	reopenedIssues := []*types.Issue{}
	hasError := false

	for _, id := range args {
		outcome, ok := reopenProxiedOne(ctx, uw, id, reason)
		if !ok {
			hasError = true
			continue
		}
		if !outcome.reopened {
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
	}

	if len(outcomes) > 0 {
		msg := reopenProxiedCommitMessage(outcomes)
		if err := uw.Commit(ctx, msg); err != nil && !isDoltNothingToCommit(err) {
			FatalErrorRespectJSON("commit reopen: %v", err)
		}
		for _, o := range outcomes {
			if err := fireProxiedReopenHooks(ctx, o.after); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: %v\n", o.id, err)
			}
		}
	}

	if jsonOut && len(reopenedIssues) > 0 {
		_ = outputJSON(reopenedIssues)
	}
	if hasError {
		os.Exit(1)
	}
}

func reopenProxiedOne(ctx context.Context, uw uow.UnitOfWork, id, reason string) (reopenProxiedOutcome, bool) {
	current, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
	if current == nil {
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return reopenProxiedOutcome{}, false
	}
	if current.Status != types.StatusClosed {
		fmt.Fprintf(os.Stderr, "%s is already %s\n", id, current.Status)
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
		fmt.Fprintf(os.Stderr, "Error reopening %s: %v\n", id, err)
		return reopenProxiedOutcome{}, false
	}

	oldStatus := string(current.Status)
	if oldStatus == "" {
		oldStatus = "closed"
	}
	audit.LogFieldChange(id, "status", oldStatus, string(types.StatusOpen), actor, reason)
	return reopenProxiedOutcome{id: id, before: current, after: res.Issue, reopened: res.Reopened}, true
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
