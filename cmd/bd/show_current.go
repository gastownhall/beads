package main

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/issueops"
)

// resolveCurrentIssueID determines the current active issue for the agent.
// Priority: in-progress assigned to actor > hooked > last touched.
//
// The lookup is the library's (issueops.FindCurrentIssue, over the Querier
// role every backend serves: queryIssues on a remote one), and both routes ask
// it: the direct route with store.Querier(), the proxied route with the
// provider's. What stays here is CLI policy: who the actor is, and the
// last-touched fallback, which is a file in this workspace.
//
// A failed read is an error. The fallback answers "nothing is in progress",
// and only reads that answered may say so: taking the http backend's refusal
// for a miss is what printed the wrong issue with exit 0.
func resolveCurrentIssueID(ctx context.Context) (string, error) {
	var querier issueops.Querier
	if usesProxiedServer() {
		q, err := proxiedQuerier()
		if err != nil {
			return "", fmt.Errorf("finding the current issue: %w", err)
		}
		querier = q
	} else if store != nil {
		q, err := store.Querier()
		if err != nil {
			return "", fmt.Errorf("finding the current issue: %w", err)
		}
		querier = q
	}
	return resolveCurrentIssueIDFrom(ctx, querier, getActorWithGit, GetLastTouchedID)
}

func resolveCurrentIssueIDFrom(ctx context.Context, querier issueops.Querier, currentActor func() string, fallback func() string) (string, error) {
	if querier == nil {
		return fallback(), nil
	}
	id, err := issueops.FindCurrentIssue(ctx, querier, currentActor())
	if err != nil {
		return "", err
	}
	if id == "" {
		return fallback(), nil
	}
	return id, nil
}
