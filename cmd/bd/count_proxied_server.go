package main

import (
	"context"

	"github.com/spf13/cobra"
)

func runCountProxiedServer(cmd *cobra.Command, ctx context.Context) error {
	filter, groupBy, issueType, includeInfra, err := parseCountFilter(cmd)
	if err != nil {
		return err
	}

	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	if includeInfra {
		cfg, err := loadProxiedListFilterConfig(ctx, uw)
		if err != nil {
			return HandleError("%v", err)
		}
		applyCountIncludeInfra(&filter, issueType, cfg)
	} else {
		filter.SkipWisps = true
	}

	return executeCount(ctx, uw.IssueUseCase(), filter, groupBy)
}
