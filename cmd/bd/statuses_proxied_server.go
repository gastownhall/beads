package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

func runStatusesProxiedServer(ctx context.Context) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	var customStatuses []types.CustomStatus
	if cs, err := uw.ConfigUseCase().GetCustomStatuses(ctx); err == nil {
		customStatuses = cs
	}

	return renderStatuses(customStatuses)
}
