package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// isNotFoundErr returns true if the error indicates the issue was not found.
// This covers both storage.ErrNotFound (from GetIssue) and the plain error
// from ResolvePartialID which doesn't wrap the sentinel.
func isNotFoundErr(err error) bool {
	if errors.Is(err, storage.ErrNotFound) {
		return true
	}
	if err != nil && strings.Contains(err.Error(), "no issue found matching") {
		return true
	}
	return false
}

// RoutedResult contains the result of a routed issue lookup
type RoutedResult struct {
	Issue      *types.Issue
	Store      storage.DoltStorage // The store that contains this issue (may be routed)
	Routed     bool                // true if the issue was found via routing
	ResolvedID string              // The resolved (full) issue ID
	closeFn    func()              // Function to close routed storage (if any)
}

// Close closes any routed storage. Safe to call if Routed is false.
func (r *RoutedResult) Close() {
	if r.closeFn != nil {
		r.closeFn()
	}
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue.
// Tries the local store first, then falls back to contributor auto-routing.
// For ephemeral/wisp IDs (containing "-wisp-"), also checks the town root
// database as a fallback, since wisps are stored in the database where they
// were created (typically HQ) but their prefix may match a different rig's
// route (e.g., sh-wisp-xxx has "sh-" prefix but lives in hq.wisps).
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	// Try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}

	// For ephemeral/wisp IDs, try the town root database before prefix-based
	// routing. Wisps are created in the town context (HQ database) regardless
	// of their ID prefix, so prefix-based routing would misroute them to the
	// wrong database (e.g., sh-wisp-xxx routes to shipyard but lives in hq).
	if isNotFoundErr(err) && strings.Contains(id, "-wisp-") {
		if townResult, townErr := resolveViaWispTownFallback(ctx, localStore, id); townErr == nil {
			return townResult, nil
		}
	}

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}

// resolveAndGetFromStore resolves a partial ID and gets the issue from a specific store.
func resolveAndGetFromStore(ctx context.Context, s storage.DoltStorage, id string, routed bool) (*RoutedResult, error) {
	// First, resolve the partial ID
	resolvedID, err := utils.ResolvePartialID(ctx, s, id)
	if err != nil {
		return nil, err
	}

	// Then get the issue
	issue, err := s.GetIssue(ctx, resolvedID)
	if err != nil {
		return nil, err
	}

	return &RoutedResult{
		Issue:      issue,
		Store:      s,
		Routed:     routed,
		ResolvedID: resolvedID,
	}, nil
}

// resolveViaAutoRouting attempts to find an issue using contributor auto-routing.
// This is the fallback when the local store doesn't have the issue (GH#2345).
// Returns a RoutedResult if the issue is found in the auto-routed store.
func resolveViaAutoRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	routedStore, routed, err := openRoutedReadStore(ctx, localStore)
	if err != nil || !routed {
		return nil, fmt.Errorf("no auto-routed store available")
	}

	result, err := resolveAndGetFromStore(ctx, routedStore, id, true)
	if err != nil {
		_ = routedStore.Close()
		return nil, err
	}
	result.closeFn = func() { _ = routedStore.Close() }
	return result, nil
}

// resolveViaWispTownFallback attempts to find a wisp in the town root database.
// Wisps are always stored in the database where they were created (typically HQ/town),
// but their ID prefix may match a different rig's route in routes.jsonl. For example,
// sh-wisp-xxx has "sh-" prefix (routes to shipyard) but was created and stored in hq.wisps.
//
// This fallback walks up from the current beads directory to find the town root's
// .beads directory and opens a read-only store to check for the wisp there.
func resolveViaWispTownFallback(ctx context.Context, _ storage.DoltStorage, id string) (*RoutedResult, error) {
	townStore, err := openTownRootReadStore(ctx)
	if err != nil || townStore == nil {
		return nil, fmt.Errorf("no town root store available for wisp fallback")
	}

	result, err := resolveAndGetFromStore(ctx, townStore, id, true)
	if err != nil {
		_ = townStore.Close()
		return nil, err
	}
	result.closeFn = func() { _ = townStore.Close() }
	return result, nil
}

// getIssueWithRouting gets an issue by exact ID.
// Tries the local store first, then falls back to town root for wisps,
// then to contributor auto-routing.
//
// Returns a RoutedResult containing the issue and the store to use for related queries.
// The caller MUST call result.Close() when done to release any routed storage.
func getIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	// Try local store first.
	issue, err := localStore.GetIssue(ctx, id)
	if err == nil {
		return &RoutedResult{
			Issue:      issue,
			Store:      localStore,
			Routed:     false,
			ResolvedID: id,
		}, nil
	}

	// For ephemeral/wisp IDs, try the town root database before prefix-based
	// routing. Wisps live in the creating database (typically HQ), not where
	// their prefix routes to.
	if isNotFoundErr(err) && strings.Contains(id, "-wisp-") {
		if townResult, townErr := resolveViaWispTownFallback(ctx, localStore, id); townErr == nil {
			return townResult, nil
		}
	}

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}
