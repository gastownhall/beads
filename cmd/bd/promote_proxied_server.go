package main

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// runPromoteProxiedServer is the proxied-server route for `bd promote`. It
// runs issueops.Promote, the entry the direct route runs, over the provider's
// own role accessors, so the two routes promote the same way: one
// Lifecycle.Update whose patch is the persistence move (the move
// `bd update --persistent` makes on this route: same id, wisp_type retained,
// labels/deps/events/comments and the child counter carried to the permanent
// tables, inbound edges retargeted), then the promotion comment through the
// Commenter. The hooks fire from the notifying provider (main.go): on_update
// for the move and again for the comment, as on the direct route.
//
// Each role runs its own transaction, so a promotion is two Dolt commits, the
// move's ("bd: promote <id>", the classic message) and the comment's
// ("bd: comment <id>"), and a comment that fails leaves the promotion standing
// behind a warning, as on every other route.
func runPromoteProxiedServer(ctx context.Context, id, reason string) error {
	if uowProvider == nil {
		return HandleErrorRespectJSON("proxied-server UOW provider not initialized")
	}

	ruw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	fullID, err := utils.ResolvePartialID(ctx, uowMolReader{uw: ruw}, id)
	ruw.Close(ctx)
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", id, err)
	}

	roles, err := proxiedPromoteRoles()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	promoted, err := issueops.Promote(ctx, roles, issueops.PromoteRequest{
		Actor: currentActor(), IssueID: fullID, Reason: reason, Provenance: "bd: promote " + fullID,
	})
	if err != nil {
		return promoteFailure(fullID, err)
	}
	return reportPromotion(fullID, promoted, func() *types.Issue {
		// Labels included, like the direct route's store.GetIssue.
		uwr, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return nil
		}
		defer uwr.Close(ctx)
		updated, _ := uowMolReader{uw: uwr}.GetIssue(ctx, fullID)
		return updated
	})
}

// proxiedPromoteRoles asks the provider for the three roles issueops.Promote
// composes, each through its own accessor, so each is the notifying one.
func proxiedPromoteRoles() (issueops.PromoteRoles, error) {
	reader, err := proxiedIssueReader()
	if err != nil {
		return issueops.PromoteRoles{}, err
	}
	lifecycle, err := proxiedIssueLifecycle()
	if err != nil {
		return issueops.PromoteRoles{}, err
	}
	commenter, err := proxiedCommenter()
	if err != nil {
		return issueops.PromoteRoles{}, err
	}
	return issueops.PromoteRoles{Reader: reader, Lifecycle: lifecycle, Commenter: commenter}, nil
}
