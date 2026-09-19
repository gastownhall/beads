package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// reopenProxiedTarget is one id that resolved, carrying the status it sat at
// before the reopens ran.
type reopenProxiedTarget struct {
	id string
	// status is the prior status, and it feeds the audit sidecar's old_value and
	// nothing else. The role's result is a post-state snapshot with no prior
	// status, and a constant "closed" is now wrong: a configured done status
	// reopens here too.
	status types.Status
}

// runReopenProxiedServer reopens each id through issueops.Lifecycle — the same
// role, reached through the same kind of accessor, that the direct route calls.
// Nothing here decides what a reopen means.
//
// A CONFIGURED DONE STATUS NOW REOPENS. This route used to compare the current
// status against literal StatusClosed and report "already <status>" for
// anything else, so an issue parked on a custom done status could not be
// reopened on a team server while the same command worked locally. The role
// speaks in terms of the configured done CATEGORY
// (issueops/issueops.go:417-420).
//
// ONE CALL PER ID, so one transaction and one history entry per id, where this
// route used to run every id in one unit of work under a hand-composed
// "bd: reopen a, b" message.
func runReopenProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return HandleErrorRespectJSON("no issue ID provided")
	}
	reason, _ := cmd.Flags().GetString("reason")
	jsonOut, _ := cmd.Flags().GetBool("json")

	targets, hasError, err := reopenProxiedResolve(ctx, args)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if len(targets) == 0 {
		if hasError {
			return SilentExit()
		}
		return nil
	}

	lifecycle, err := proxiedIssueLifecycle()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	reopenedIssues := []*types.Issue{}
	for _, target := range targets {
		result, err := lifecycle.Reopen(ctx, issueops.ReopenRequest{
			Actor:   actor,
			IssueID: target.id,
			Reason:  reason,
			// The label the direct route spells, so one reopen reads the same
			// in `bd dolt log` whichever route served it.
			Provenance: "bd: reopen " + target.id,
		})
		if err != nil {
			reportIssueLookupFailure("reopening", target.id, err)
			hasError = true
			continue
		}
		if !result.Changed {
			// Read off the result rather than off the pre-read: the status the
			// reopen left in place is the one the operation saw inside its own
			// transaction.
			fmt.Fprintln(os.Stderr, reopenNoOpMessage(target.id, reopenStatusOf(result.Issue, nil)))
			continue
		}

		audit.LogFieldChange(target.id, "status", string(target.status), string(types.StatusOpen), actor, reason)
		if jsonOut {
			if issue := result.Issue; issue != nil {
				// `bd reopen` has never printed dependency records, on either
				// route.
				issue.Dependencies = nil
				reopenedIssues = append(reopenedIssues, issue)
			}
			continue
		}
		suffix := ""
		if reason != "" {
			suffix = ": " + reason
		}
		fmt.Printf("%s Reopened %s%s\n", ui.RenderAccent("↻"), target.id, suffix)
	}

	if jsonOut && len(reopenedIssues) > 0 {
		_ = outputJSON(reopenedIssues)
	}

	if hasError {
		return SilentExit()
	}
	return nil
}

// reopenProxiedResolve resolves every id in ONE read-only unit of work and
// reports the ones that did not resolve, returning the survivors with the
// status each sat at.
//
// It exists for the two things the role's result cannot supply: the audit
// sidecar's old_value, and the difference between an absent id and a backend
// that failed to answer.
//
// It decides NOTHING about the reopen. A resolved id goes to the role whatever
// status it is at, including a status this route used to refuse; every guard
// that matters is inside the role's own transaction.
func reopenProxiedResolve(ctx context.Context, ids []string) ([]reopenProxiedTarget, bool, error) {
	var targets []reopenProxiedTarget
	failed := false
	_, err := uow.RunTxRead(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (struct{}, error) {
		source := workapi.NewUOWDetailSource(uw)
		for _, id := range ids {
			issue, _, err := workapi.GetIssueOrWisp(ctx, source, id)
			if err != nil {
				reportIssueLookupFailure("resolving", id, err)
				failed = true
				continue
			}
			targets = append(targets, reopenProxiedTarget{id: id, status: issue.Status})
		}
		return struct{}{}, nil
	})
	return targets, failed, err
}
