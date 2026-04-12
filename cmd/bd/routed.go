package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	doltstorage "github.com/steveyegge/beads/internal/storage/dolt"
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

func findTownRootFromBeadsDir(beadsDir string) string {
	current := filepath.Dir(beadsDir)
	for {
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Dir(beadsDir)
		}
		current = parent
	}
}

func loadRoutePathFromStore(ctx context.Context, localStore storage.DoltStorage, prefix string) (string, bool) {
	rawStore := storage.UnwrapStore(localStore)
	rawDB, ok := rawStore.(storage.RawDBAccessor)
	if !ok {
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] store %T does not expose RawDBAccessor after unwrap (%T)\n", localStore, rawStore)
		}
		return "", false
	}

	db := rawDB.UnderlyingDB()
	if db == nil {
		db = rawDB.DB()
	}
	if db == nil {
		return "", false
	}

	var routePath string
	if err := db.QueryRowContext(ctx, "SELECT path FROM routes WHERE prefix = ?", prefix).Scan(&routePath); err != nil {
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] routes table lookup for %q failed: %v\n", prefix, err)
		}
		return "", false
	}
	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] routes table matched %q -> %q\n", prefix, routePath)
	}
	return routePath, routePath != ""
}

func resolveRouteBeadsDirPath(townRoot, routePath string) string {
	if routePath == "" {
		return ""
	}
	if routePath == "." {
		if townRoot == "" {
			return ""
		}
		return filepath.Join(townRoot, ".beads")
	}

	resolved := routePath
	if !filepath.IsAbs(resolved) {
		if townRoot == "" {
			return ""
		}
		resolved = filepath.Join(townRoot, resolved)
	}
	if filepath.Base(resolved) == ".beads" {
		return resolved
	}
	return filepath.Join(resolved, ".beads")
}

func databaseCandidatesFromRoutePath(townRoot, routePath string) []string {
	addCandidate := func(seen map[string]struct{}, candidates []string, candidate string) []string {
		candidate = strings.TrimSpace(candidate)
		switch candidate {
		case "", ".", "..", ".beads", "mayor", "rig":
			return candidates
		}
		if _, exists := seen[candidate]; exists {
			return candidates
		}
		seen[candidate] = struct{}{}
		return append(candidates, candidate)
	}

	cleaned := filepath.Clean(routePath)
	seen := make(map[string]struct{})
	candidates := make([]string, 0, 2)

	if filepath.IsAbs(cleaned) && townRoot != "" {
		if rel, err := filepath.Rel(townRoot, cleaned); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) > 0 {
				candidates = addCandidate(seen, candidates, parts[0])
			}
		}
	}

	if !filepath.IsAbs(cleaned) {
		parts := strings.Split(filepath.ToSlash(cleaned), "/")
		if len(parts) > 0 {
			candidates = addCandidate(seen, candidates, parts[0])
		}
	}

	base := filepath.Base(cleaned)
	if base == ".beads" {
		base = filepath.Base(filepath.Dir(cleaned))
	}
	candidates = addCandidate(seen, candidates, base)

	return candidates
}

func openNamedDatabaseStore(ctx context.Context, currentBeadsDir, verificationBeadsDir, database string) (storage.DoltStorage, error) {
	cfg, err := configfile.Load(currentBeadsDir)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.IsDoltServerMode() {
		return nil, fmt.Errorf("current workspace is not in dolt server mode")
	}

	return doltstorage.NewFromConfigWithOptions(ctx, currentBeadsDir, &doltstorage.Config{
		Database: database,
		BeadsDir: verificationBeadsDir,
	})
}

func openRouteTableFallbackStore(ctx context.Context, localStore storage.DoltStorage, currentBeadsDir, id string) (storage.DoltStorage, bool) {
	prefix := types.ExtractPrefix(id)
	if prefix == "" {
		return nil, false
	}

	routePath, ok := loadRoutePathFromStore(ctx, localStore, prefix)
	if !ok {
		return nil, false
	}

	townRoot := findTownRootFromBeadsDir(currentBeadsDir)
	targetBeadsDir := resolveRouteBeadsDirPath(townRoot, routePath)
	if targetBeadsDir != "" && !sameBeadsDir(targetBeadsDir, currentBeadsDir) {
		if info, err := os.Stat(targetBeadsDir); err == nil && info.IsDir() {
			cfg, cfgErr := configfile.Load(targetBeadsDir)
			if cfgErr == nil && cfg != nil {
				routedStore, err := newDoltStoreFromConfig(ctx, targetBeadsDir)
				if err == nil {
					return routedStore, true
				}
				if os.Getenv("BD_DEBUG_ROUTING") != "" {
					fmt.Fprintf(os.Stderr, "[routing] routes table matched %q -> %q but open failed: %v\n", prefix, targetBeadsDir, err)
				}
			} else if os.Getenv("BD_DEBUG_ROUTING") != "" {
				fmt.Fprintf(os.Stderr, "[routing] routes table matched %q -> %q but metadata is unavailable (cfg=%v err=%v); trying database fallback\n", prefix, targetBeadsDir, cfg != nil, cfgErr)
			}
		}
	}

	for _, database := range databaseCandidatesFromRoutePath(townRoot, routePath) {
		routedStore, err := openNamedDatabaseStore(ctx, currentBeadsDir, targetBeadsDir, database)
		if err == nil {
			if os.Getenv("BD_DEBUG_ROUTING") != "" {
				fmt.Fprintf(os.Stderr, "[routing] routes table matched %q -> database %q via %q\n", prefix, database, routePath)
			}
			return routedStore, true
		}
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] routes table matched %q but database %q failed: %v\n", prefix, database, err)
		}
	}

	return nil, false
}

// openPrefixRoutedStore reopens the authoritative store for a routed issue ID when
// routes.jsonl says the bead lives in a different rig database.
func openPrefixRoutedStore(ctx context.Context, localStore storage.DoltStorage, id string) (storage.DoltStorage, bool, error) {
	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] openPrefixRoutedStore id=%q dbPath=%q beadsDirOverride=%v store=%T\n", id, dbPath, beadsDirOverride(), localStore)
	}
	if dbPath == "" || beadsDirOverride() {
		return nil, false, nil
	}

	currentBeadsDir := currentCommandBeadsDir()
	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] current command beads dir=%q\n", currentBeadsDir)
	}
	if currentBeadsDir == "" {
		return nil, false, nil
	}

	beforeDatabase := os.Getenv("BEADS_DOLT_SERVER_DATABASE")
	targetBeadsDir, routed, err := routing.ResolveBeadsDirForID(ctx, id, currentBeadsDir)
	redirectedDatabase := os.Getenv("BEADS_DOLT_SERVER_DATABASE") != "" &&
		os.Getenv("BEADS_DOLT_SERVER_DATABASE") != beforeDatabase
	if err == nil && routed && (redirectedDatabase || !sameBeadsDir(targetBeadsDir, currentBeadsDir)) {
		routedStore, err := newDoltStoreFromConfig(ctx, targetBeadsDir)
		if err != nil {
			return nil, false, fmt.Errorf("opening routed store at %s: %w", targetBeadsDir, err)
		}
		return routedStore, true, nil
	}

	if fallbackStore, fallbackRouted := openRouteTableFallbackStore(ctx, localStore, currentBeadsDir, id); fallbackRouted {
		return fallbackStore, true, nil
	}

	return nil, false, err
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue.
// Prefix-routed IDs reopen the owning rig store directly; otherwise this falls
// back to the local store and contributor auto-routing.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	if routedStore, routed, err := openPrefixRoutedStore(ctx, localStore, id); err != nil {
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
	if routedStore, routed, err := openPrefixRoutedStore(ctx, localStore, id); err != nil {
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
