package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/routing"
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

// beadsDirOverride returns true if BEADS_DIR explicitly pins the active database.
// When set, prefix routing must stay disabled and callers should use the local store.
func beadsDirOverride() bool {
	return os.Getenv("BEADS_DIR") != ""
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

func currentCommandBeadsDir() string {
	if dbPath == "" {
		return ""
	}
	if beadsDir := resolveCommandBeadsDir(dbPath); beadsDir != "" {
		return beadsDir
	}
	return filepath.Dir(dbPath)
}

func sameBeadsDir(a, b string) bool {
	return utils.NormalizePathForComparison(a) == utils.NormalizePathForComparison(b)
}

// openPrefixRoutedStore reopens the authoritative store for a routed issue ID when
// routes.jsonl says the bead lives in a different rig database.
func openPrefixRoutedStore(ctx context.Context, id string) (storage.DoltStorage, bool, error) {
	if dbPath == "" || beadsDirOverride() {
		return nil, false, nil
	}

	currentBeadsDir := currentCommandBeadsDir()
	if currentBeadsDir == "" {
		return nil, false, nil
	}

	beforeDatabase := os.Getenv("BEADS_DOLT_SERVER_DATABASE")
	targetBeadsDir, routed, err := routing.ResolveBeadsDirForID(ctx, id, currentBeadsDir)
	redirectedDatabase := os.Getenv("BEADS_DOLT_SERVER_DATABASE") != "" &&
		os.Getenv("BEADS_DOLT_SERVER_DATABASE") != beforeDatabase
	if err != nil || !routed || (!redirectedDatabase && sameBeadsDir(targetBeadsDir, currentBeadsDir)) {
		return nil, false, err
	}

	routedStore, err := newDoltStoreFromConfig(ctx, targetBeadsDir)
	if err != nil {
		return nil, false, fmt.Errorf("opening routed store at %s: %w", targetBeadsDir, err)
	}
	return routedStore, true, nil
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue.
// Prefix-routed IDs reopen the owning rig store directly; otherwise this falls
// back to the local store and contributor auto-routing.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	if routedStore, routed, err := openPrefixRoutedStore(ctx, id); err != nil {
		return nil, err
	} else if routed {
		result, resolveErr := resolveAndGetFromStore(ctx, routedStore, id, true)
		if resolveErr != nil {
			_ = routedStore.Close()
			return nil, resolveErr
		}
		result.closeFn = func() { _ = routedStore.Close() }
		return result, nil
	}

	// Try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
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

// getIssueWithRouting gets an issue by exact ID.
// Prefix-routed IDs reopen the owning rig store directly; otherwise this falls
// back to the local store and contributor auto-routing.
//
// Returns a RoutedResult containing the issue and the store to use for related queries.
// The caller MUST call result.Close() when done to release any routed storage.
func getIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	if routedStore, routed, err := openPrefixRoutedStore(ctx, id); err != nil {
		return nil, err
	} else if routed {
		issue, getErr := routedStore.GetIssue(ctx, id)
		if getErr != nil {
			_ = routedStore.Close()
			return nil, getErr
		}
		return &RoutedResult{
			Issue:      issue,
			Store:      routedStore,
			Routed:     true,
			ResolvedID: id,
			closeFn:    func() { _ = routedStore.Close() },
		}, nil
	}

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

	// If not found locally, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}
