package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

func runFindDuplicatesProxiedServer(ctx context.Context, filter types.IssueFilter, status, method string, threshold float64, limit int, model string) error {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	defer uw.Close(ctx)

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
	if err != nil {
		// The cap is enforced by the search itself, before the default
		// closed-row removal below: closed rows the search matched count
		// against it, exactly as they do on the direct route.
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return HandleErrorRespectJSON("fetching issues: %v", err)
	}
	issues := filterClosedIfNoStatus(page.Items, status)

	return reportFindDuplicates(ctx, issues, method, threshold, limit, model)
}
