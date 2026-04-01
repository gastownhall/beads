package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
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
// Tries the local store first, then prefix-based routing via routes.jsonl,
// then falls back to contributor auto-routing.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	// Try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}

	// If not found locally, try prefix-based routing via routes.jsonl.
	// This handles cross-rig lookups where the ID's prefix maps to a different
	// database (e.g., hr-8wn.1 routes to the herald rig's database).
	if isNotFoundErr(err) {
		if prefixResult, prefixErr := resolveViaPrefixRouting(ctx, id); prefixErr == nil {
			return prefixResult, nil
		}
	}

	// If not found via prefix routing, try contributor auto-routing as fallback (GH#2345).
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

// prefixRoute represents a prefix-to-path routing rule from routes.jsonl.
type prefixRoute struct {
	Prefix string `json:"prefix"` // Issue ID prefix (e.g., "hr-")
	Path   string `json:"path"`   // Relative path to rig directory from town root
}

// resolveViaPrefixRouting attempts to find an issue by looking up its prefix
// in routes.jsonl and opening the target rig's database.
//
// This enables cross-rig lookups: when running from a redirected .beads directory
// (e.g., crew/beercan → town/.beads with database "hq"), a bead ID like "hr-8wn.1"
// can be resolved by following the "hr-" route to the herald rig's .beads directory,
// which declares dolt_database="herald".
func resolveViaPrefixRouting(ctx context.Context, id string) (*RoutedResult, error) {
	// Extract prefix from the bead ID (e.g., "hr-" from "hr-8wn.1")
	prefix := extractBeadPrefix(id)
	if prefix == "" {
		return nil, fmt.Errorf("no prefix in ID %q", id)
	}

	// Find the resolved beads directory (where routes.jsonl lives)
	currentBeadsDir := resolveCommandBeadsDir(dbPath)
	if currentBeadsDir == "" {
		return nil, fmt.Errorf("no beads directory available")
	}

	// Load routes from routes.jsonl
	routes, err := loadPrefixRoutes(currentBeadsDir)
	if err != nil || len(routes) == 0 {
		return nil, fmt.Errorf("no routes available")
	}

	// Find matching route for this prefix
	var matchedRoute *prefixRoute
	for i, r := range routes {
		if r.Prefix == prefix {
			matchedRoute = &routes[i]
			break
		}
	}
	if matchedRoute == nil {
		return nil, fmt.Errorf("no route for prefix %q", prefix)
	}

	// Skip if the route points to current directory (town-level, already checked)
	if matchedRoute.Path == "." {
		return nil, fmt.Errorf("route points to current database")
	}

	// Derive the town root from the current beads dir.
	// currentBeadsDir is typically <town_root>/.beads
	townRoot := filepath.Dir(currentBeadsDir)

	// Resolve the target rig's .beads directory
	rigDir := filepath.Join(townRoot, matchedRoute.Path)
	targetBeadsDir := beads.FollowRedirect(filepath.Join(rigDir, ".beads"))

	// Check that the target has a different dolt_database
	targetDB := readDoltDatabase(targetBeadsDir)
	if targetDB == "" {
		return nil, fmt.Errorf("target rig has no dolt_database configured")
	}

	debug.Logf("[routing] Prefix %q matched route to %s (database: %s)\n", prefix, matchedRoute.Path, targetDB)

	// Open a read-only store for the target database.
	// We need to temporarily override BEADS_DOLT_SERVER_DATABASE so the store
	// connects to the correct database on the shared Dolt server.
	origDB := os.Getenv("BEADS_DOLT_SERVER_DATABASE")
	_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", targetDB)
	targetStore, err := newReadOnlyStoreFromConfig(ctx, targetBeadsDir)
	// Restore the original env var
	if origDB != "" {
		_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", origDB)
	} else {
		_ = os.Unsetenv("BEADS_DOLT_SERVER_DATABASE")
	}
	if err != nil {
		return nil, fmt.Errorf("opening routed store for %s: %w", matchedRoute.Path, err)
	}

	result, err := resolveAndGetFromStore(ctx, targetStore, id, true)
	if err != nil {
		_ = targetStore.Close()
		return nil, err
	}
	result.closeFn = func() { _ = targetStore.Close() }

	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Resolved %s via prefix route to %s (database: %s)\n", id, matchedRoute.Path, targetDB)
	}

	return result, nil
}

// extractBeadPrefix extracts the prefix from a bead ID.
// For example, "hr-8wn.1" returns "hr-", "hq-cv-abc" returns "hq-".
func extractBeadPrefix(beadID string) string {
	if beadID == "" {
		return ""
	}
	idx := strings.Index(beadID, "-")
	if idx <= 0 {
		return ""
	}
	return beadID[:idx+1]
}

// loadPrefixRoutes loads prefix-to-path routes from routes.jsonl in the beads directory.
func loadPrefixRoutes(beadsDir string) ([]prefixRoute, error) {
	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	file, err := os.Open(routesPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var routes []prefixRoute
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var route prefixRoute
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			continue
		}
		if route.Prefix != "" && route.Path != "" {
			routes = append(routes, route)
		}
	}
	return routes, scanner.Err()
}

// readDoltDatabase reads the dolt_database field from a .beads/metadata.json file.
func readDoltDatabase(beadsDir string) string {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return ""
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.DoltDatabase
}

// getIssueWithRouting gets an issue by exact ID.
// Tries the local store first, then prefix-based routing, then contributor auto-routing.
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

	// If not found locally, try prefix-based routing via routes.jsonl.
	if isNotFoundErr(err) {
		if prefixResult, prefixErr := resolveViaPrefixRouting(ctx, id); prefixErr == nil {
			return prefixResult, nil
		}
	}

	// If not found via prefix routing, try contributor auto-routing as fallback (GH#2345).
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRouting(ctx, localStore, id); autoErr == nil {
			return autoResult, nil
		}
	}

	return nil, err
}
