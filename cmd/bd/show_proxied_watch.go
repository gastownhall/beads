package main

import (
	"context"
	"errors"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// proxiedIssueWatchSource feeds `bd show --watch` from the proxied-server
// provider. Every render and every poll opens its own short unit of work, so a
// long watch never pins a transaction and each read sees the latest commit.
// After the first render the resolved id is used, so a partial id is resolved
// once, as the direct route's first render does.
func proxiedIssueWatchSource(in *showProxiedInput) issueWatchSource {
	id := in.ids[0]
	formatTime := func(t time.Time) string {
		if in.localTime {
			t = t.Local()
		}
		return t.Format("2006-01-02 15:04")
	}
	return issueWatchSource{
		render: func(ctx context.Context) *types.Issue {
			uw, err := proxiedOpenReadUOW(ctx)
			if err != nil {
				return nil
			}
			defer uw.Close(ctx)
			issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
			if err != nil {
				reportIssueLookupFailure("fetching", id, err)
				return nil
			}
			id = issue.ID
			proxiedRenderIssue(ctx, uw, issue, isWisp, in, 0, formatTime)
			return issue
		},
		// Opens its unit of work directly rather than through
		// proxiedOpenReadUOW, which reports its own failure: a poll must stay
		// silent and leave the verdict to watchIssueLoop.
		fetch: func(ctx context.Context) (*types.Issue, error) {
			if uowProvider == nil {
				return nil, errors.New("proxied-server UOW provider not initialized")
			}
			uw, err := uowProvider.NewUOW(ctx)
			if err != nil {
				return nil, err
			}
			defer uw.Close(ctx)
			issue, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
			return issue, err
		},
	}
}
