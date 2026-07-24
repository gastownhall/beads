package main

import (
	"context"
	"fmt"
	"os"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

func runEpicStatusProxiedServer(ctx context.Context, eligibleOnly bool) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	epics, err := uw.IssueUseCase().GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return HandleErrorRespectJSON("getting epic status: %v", err)
	}
	return renderEpicStatus(epics, eligibleOnly)
}

func runCloseEligibleEpicsProxiedServer(ctx context.Context, dryRun bool) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	epics, err := uw.IssueUseCase().GetEpicsEligibleForClosure(ctx)
	if err != nil {
		uw.Close(ctx)
		return HandleErrorRespectJSON("getting eligible epics: %v", err)
	}
	eligibleEpics := filterEligibleEpics(epics)
	uw.Close(ctx)

	if len(eligibleEpics) == 0 {
		return outputNoEligibleEpics()
	}
	if dryRun {
		return outputCloseEligibleDryRun(eligibleEpics)
	}

	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}

	closedIDs, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) ([]string, string, error) {
		epics, err := uw.IssueUseCase().GetEpicsEligibleForClosure(ctx)
		if err != nil {
			return nil, "", err
		}
		var closed []string
		for _, epicStatus := range filterEligibleEpics(epics) {
			if _, err := uw.IssueUseCase().CloseIssue(ctx, epicStatus.Epic.ID, domain.CloseIssueParams{Reason: "All children completed"}, "system"); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", epicStatus.Epic.ID, err)
				continue
			}
			closed = append(closed, epicStatus.Epic.ID)
		}
		return closed, "epic: close eligible", nil
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if len(closedIDs) > 0 {
		commandDidWrite.Store(true)
	}
	return outputCloseEligibleResult(closedIDs)
}
