package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

var errRoutingUnavailable = errors.New("routing unavailable")

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

// isRoutingFailure reports whether a routed tier's error must reach the caller
// instead of falling through to the next tier: anything but "this tier has no
// route" (errRoutingUnavailable) or a plain not-found. An unreadable
// routes.jsonl or a routed store that fails to open is why the lookup missed,
// and returning the local not-found in its place would misstate the cause.
func isRoutingFailure(err error) bool {
	return err != nil && !errors.Is(err, errRoutingUnavailable) && !isNotFoundErr(err)
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
		r.closeFn = nil
	}
}

// commitRoutedWrite commits what command wrote through result when result is
// routed. The post-run auto-commit covers only the local store, so a command
// that writes to a routed target commits that write itself, as bd update and
// bd close do. An unrouted result is left to the post-run auto-commit.
func commitRoutedWrite(ctx context.Context, result *RoutedResult, command string) error {
	if !result.Routed {
		return nil
	}
	return commitPendingIfEmbedded(ctx, result.Store, currentActor(), doltAutoCommitParams{
		Command:  command,
		IssueIDs: []string{result.ResolvedID},
	})
}

// resolveAndGetIssueWithRouting resolves a partial ID and gets the issue.
// Tries the local store first, then prefix-based routing via routes.jsonl,
// then falls back to auto-routing (routing.mode=auto). A routing failure, such
// as an unreadable routes.jsonl or a routed store that fails to open, is
// returned as itself rather than as the local not-found.
//
// Returns a RoutedResult containing the issue, resolved ID, and the store to use.
// The caller MUST call result.Close() when done to release any routed storage.
//
// Routed target stores, prefix and auto alike, are opened read-only; mutating
// commands must use resolveAndGetIssueForMutation instead so a routed read can
// never write migrations or other open-time mutations into a foreign project
// (GH#3231, #4141).
func resolveAndGetIssueWithRouting(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	return resolveAndGetIssueWithRoutingAccess(ctx, localStore, id, false)
}

// resolveAndGetIssueForMutation resolves an issue like
// resolveAndGetIssueWithRouting, but opens the routed target store writable so
// mutation commands commit to the repository that holds the issue: a
// prefix-routed rig, or the target of any auto-route, a contributor's planning
// repository included (openAutoRoutedStore).
func resolveAndGetIssueForMutation(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	return resolveAndGetIssueWithRoutingAccess(ctx, localStore, id, true)
}

func resolveAndGetIssueWithRoutingAccess(ctx context.Context, localStore storage.DoltStorage, id string, writable bool) (*RoutedResult, error) {
	// Try local store first.
	result, err := resolveAndGetFromStore(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}

	// The first routed tier that refuses the id answers instead of the local
	// not-found (isRoutedResolutionRefusal). The refusal is checked before
	// isRoutingFailure, which it would also satisfy, so the auto tier still
	// gets its chance.
	var refusal error

	// If not found locally, try prefix-based routing via routes.jsonl.
	// This handles cross-rig lookups where the ID's prefix maps to a different
	// database (e.g., hr-8wn.1 routes to the herald rig's database).
	if isNotFoundErr(err) {
		if prefixResult, prefixErr := resolveViaPrefixRoutingWithAccess(ctx, id, writable, false); prefixErr == nil {
			return prefixResult, nil
		} else if isRoutedResolutionRefusal(prefixErr) {
			refusal = prefixErr
		} else if isRoutingFailure(prefixErr) {
			return nil, prefixErr
		}
	}

	// If not found via prefix routing, try auto-routing as fallback (GH#2345).
	// The target reopens writable when the caller has write intent.
	if isNotFoundErr(err) {
		if autoResult, autoErr := resolveViaAutoRoutingWithAccess(ctx, localStore, id, writable, false); autoErr == nil {
			return autoResult, nil
		} else if refusal != nil {
			return nil, refusal
		} else if isRoutingFailure(autoErr) {
			return nil, autoErr
		}
	}

	return nil, err
}

// isRoutedResolutionRefusal reports whether a routed tier's resolution error
// answers in place of the local store's not-found. Both sentinels mean the
// routed store holds what the id names but will not pick it: the id names
// several issues there (utils.ErrAmbiguousID), or it only abbreviates an
// issue there and exact resolution was required
// (utils.ErrAbbreviatedIDNotAllowed). Reporting the local "no issue found"
// instead would be false.
func isRoutedResolutionRefusal(err error) bool {
	return errors.Is(err, utils.ErrAmbiguousID) || errors.Is(err, utils.ErrAbbreviatedIDNotAllowed)
}

// idResolverFor is the store id resolution should ask. A backend that can only
// look ids up exactly (utils.ExactIDLookupStore, the http backend) sits under
// the decorator chain, which does not forward that capability, so the
// resolver is handed the backend itself: resolution is a pure read, and the
// decorators (hooks, the external-dependency policy, telemetry) decide
// nothing about it. Every other store is resolved through s unchanged.
func idResolverFor(s storage.DoltStorage) utils.PartialIDResolverStore {
	if s == nil {
		return s
	}
	if raw := storage.UnwrapStore(s); raw != nil {
		if exact, ok := raw.(utils.ExactIDLookupStore); ok && exact.ExactIDLookupOnly() {
			return raw
		}
	}
	return s
}

// resolveAndGetFromStore resolves a partial ID and gets the issue from a specific store.
func resolveAndGetFromStore(ctx context.Context, s storage.DoltStorage, id string, routed bool) (*RoutedResult, error) {
	// First, resolve the partial ID
	resolvedID, err := utils.ResolvePartialID(ctx, idResolverFor(s), id)
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

// resolveAndGetFromStoreExact is resolveAndGetFromStore's exact-match sibling:
// it requires utils.ResolvePartialIDExact instead of the default abbreviation-
// tolerant resolver, so a non-exact candidate (e.g. a reserved word that
// happens to be a leading-prefix abbreviation of some issue's hash) is
// reported as "not found" rather than silently resolved.
func resolveAndGetFromStoreExact(ctx context.Context, s storage.DoltStorage, id string, routed bool) (*RoutedResult, error) {
	resolvedID, err := utils.ResolvePartialIDExact(ctx, idResolverFor(s), id)
	if err != nil {
		return nil, err
	}

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

// resolveAndGetIssueForMutationExact resolves an issue like
// resolveAndGetIssueForMutation, but requires an EXACT id match against each
// store it tries (no leading-prefix abbreviation matching) — see
// utils.ResolvePartialIDExact. Cross-rig prefix routing and auto-routing are
// still attempted as fallbacks exactly as before if the local store doesn't
// have the issue at all; only the per-store resolution within each tier is
// tightened. Used by write paths where an id argument
// that doesn't exactly name a real issue must error out instead of silently
// mutating whatever it fuzzy-matches (`bd comment list <id>`, a typo for
// `bd comments list`, was silently resolving "list" to a wisp whose hash
// happened to start with "list"). A routing failure is returned as itself, as
// resolveAndGetIssueForMutation returns it.
func resolveAndGetIssueForMutationExact(ctx context.Context, localStore storage.DoltStorage, id string) (*RoutedResult, error) {
	result, err := resolveAndGetFromStoreExact(ctx, localStore, id, false)
	if err == nil {
		return result, nil
	}
	if !isNotFoundErr(err) {
		return nil, err
	}

	// The routed tiers resolve with utils.ResolvePartialIDExact too, so each
	// can refuse the id on its own — it abbreviates a real issue in the routed
	// rig, or is a hash several issues share there — and both wrappers hand
	// that error back unwrapped. Keep the first such refusal: the local error
	// is only a plain not-found, and returning it would tell the caller no
	// issue matches an id a routed store demonstrably holds, which is exactly
	// the falsehood the sentinels exist to prevent (isRoutedResolutionRefusal).
	// The refusal is checked before isRoutingFailure, which it would also
	// satisfy, so the auto tier still gets its chance at an exact match.
	var refusal error
	prefixResult, prefixErr := resolveViaPrefixRoutingWithAccess(ctx, id, true, true)
	switch {
	case prefixErr == nil:
		return prefixResult, nil
	case isRoutedResolutionRefusal(prefixErr):
		refusal = prefixErr
	case isRoutingFailure(prefixErr):
		return nil, prefixErr
	}

	// Write intent, as in resolveAndGetIssueForMutation: the auto-route opens
	// writable so the write commits where the issue lives. The auto tier's own
	// refusal is a routing failure here, and is returned as one.
	autoResult, autoErr := resolveViaAutoRoutingWithAccess(ctx, localStore, id, true, true)
	switch {
	case autoErr == nil:
		return autoResult, nil
	case refusal != nil:
		return nil, refusal
	case isRoutingFailure(autoErr):
		return nil, autoErr
	}

	return nil, err
}

// resolveViaAutoRoutingWithAccess attempts to find an issue using auto-routing.
// This is the fallback when the local store doesn't have the issue (GH#2345).
// Returns a RoutedResult if the issue is found in the auto-routed store.
//
// writable is the caller's write intent: the target opens writable for it,
// whichever rule selected the target (openAutoRoutedStore).
//
// exact selects utils.ResolvePartialIDExact over the default abbreviation-
// tolerant resolver for the auto-routed store lookup, mirroring the local-store
// distinction in resolveAndGetFromStore vs resolveAndGetFromStoreExact.
func resolveViaAutoRoutingWithAccess(ctx context.Context, localStore storage.DoltStorage, id string, writable bool, exact bool) (*RoutedResult, error) {
	routedStore, routed, _, err := openAutoRoutedStore(ctx, localStore, writable)
	if err != nil {
		return nil, err
	}
	if !routed {
		return nil, fmt.Errorf("%w: no auto-routed store available", errRoutingUnavailable)
	}

	var result *RoutedResult
	if exact {
		result, err = resolveAndGetFromStoreExact(ctx, routedStore, id, true)
	} else {
		result, err = resolveAndGetFromStore(ctx, routedStore, id, true)
	}
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
// in routes.jsonl and opening the target rig's database read-only.
//
// This enables cross-rig lookups: when running from a redirected .beads directory
// (e.g., crew/beercan → town/.beads with database "hq"), a bead ID like "hr-8wn.1"
// can be resolved by following the "hr-" route to the herald rig's .beads directory,
// which declares dolt_database="herald".
//
// The read-only open guarantees a routed read cannot mutate the target; mutation
// commands must route through resolveViaPrefixRoutingWithAccess with writable=true.
func resolveViaPrefixRouting(ctx context.Context, id string) (*RoutedResult, error) {
	return resolveViaPrefixRoutingWithAccess(ctx, id, false, false)
}

// resolveViaPrefixRoutingWithAccess is the shared implementation that selects the
// store-open mode. writable opens the routed target writable, behaving like running
// the command inside that rig; false keeps the read-only open that guarantees a
// routed read cannot mutate the target (bd-6dnrw.32). exact selects
// utils.ResolvePartialIDExact over the default abbreviation-tolerant resolver for
// the routed store lookup, mirroring resolveAndGetFromStore vs
// resolveAndGetFromStoreExact.
func resolveViaPrefixRoutingWithAccess(ctx context.Context, id string, writable bool, exact bool) (*RoutedResult, error) {
	matchedRoute, targetBeadsDir, err := lookupPrefixRoute(id)
	if err != nil {
		return nil, err
	}

	// Refuse what admission refuses from the target's own contents first, as
	// an auto-route does (openAutoRoutedStore): the dolt_database lookup
	// below would otherwise report a historical SQLite or legacy Dolt rig as
	// a missing dolt_database field, or a missing metadata.json.
	if _, err := admitForeignWorkspace(targetBeadsDir); err != nil {
		return nil, fmt.Errorf("prefix route %q to %s: %w", matchedRoute.Prefix, matchedRoute.Path, err)
	}

	// Check that the target has a different dolt_database
	targetDB, err := loadDoltDatabase(targetBeadsDir)
	if err == nil && targetDB == "" {
		err = fmt.Errorf("%s has no dolt_database field", filepath.Join(targetBeadsDir, "metadata.json"))
	}
	if err != nil {
		return nil, fmt.Errorf("prefix route %q to %s: target rig has no dolt_database configured: %w", matchedRoute.Prefix, matchedRoute.Path, err)
	}

	debug.Logf("[routing] Prefix %q matched route to %s (database: %s)\n", matchedRoute.Prefix, matchedRoute.Path, targetDB)

	targetStore, err := openRoutedTargetStore(ctx, targetBeadsDir, targetDB, writable)
	if err != nil {
		return nil, fmt.Errorf("opening routed store for %s: %w", matchedRoute.Path, err)
	}

	var result *RoutedResult
	if exact {
		result, err = resolveAndGetFromStoreExact(ctx, targetStore, id, true)
	} else {
		result, err = resolveAndGetFromStore(ctx, targetStore, id, true)
	}
	if err != nil {
		_ = targetStore.Close()
		return nil, err
	}
	result.closeFn = func() { _ = targetStore.Close() }

	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Resolved %s via prefix route to %s (database: %s)\n", id, remedy.PrintedValue(matchedRoute.Path), remedy.PrintedValue(targetDB))
	}

	return result, nil
}

// lookupPrefixRoute finds the routes.jsonl route for id and returns it with
// the target rig's .beads directory. Having no route to follow (no prefix in
// the ID, no beads directory, no routes, no matching route, or a route back to
// the current database) wraps errRoutingUnavailable; an unreadable routes.jsonl
// does not, so callers report it instead of falling through.
func lookupPrefixRoute(id string) (*prefixRoute, string, error) {
	if strings.Index(id, "-") <= 0 {
		return nil, "", fmt.Errorf("%w: no prefix in ID %q", errRoutingUnavailable, id)
	}

	// Find the resolved beads directory (where routes.jsonl lives)
	currentBeadsDir := resolveCommandBeadsDir(dbPath)
	if currentBeadsDir == "" {
		return nil, "", fmt.Errorf("%w: no beads directory available", errRoutingUnavailable)
	}

	// Load routes from routes.jsonl
	routes, err := loadPrefixRoutes(currentBeadsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: no routes available", errRoutingUnavailable)
		}
		return nil, "", fmt.Errorf("loading routes: %w", err)
	}
	if len(routes) == 0 {
		return nil, "", fmt.Errorf("%w: no routes available", errRoutingUnavailable)
	}

	// Find the most specific route for this ID
	matchedRoute := matchPrefixRoute(routes, id)
	if matchedRoute == nil {
		return nil, "", fmt.Errorf("%w: no route for ID %q", errRoutingUnavailable, id)
	}

	// Skip if the route points to current directory (town-level, already checked)
	if matchedRoute.Path == "." {
		return nil, "", fmt.Errorf("%w: route points to current database", errRoutingUnavailable)
	}

	// Derive the town root from the current beads dir.
	// currentBeadsDir is typically <town_root>/.beads
	townRoot := filepath.Dir(currentBeadsDir)

	// Resolve the target rig's .beads directory
	rigDir := filepath.Join(townRoot, matchedRoute.Path)
	return matchedRoute, beads.FollowRedirect(filepath.Join(rigDir, ".beads")), nil
}

// openRoutedTargetStore opens the store of a routed target, prefix or auto,
// writable or read-only. BEADS_DOLT_SERVER_DATABASE outranks the target's
// metadata.json when a store resolves its database name, and it names the
// launching workspace's database (set by the user, or by
// preserveRedirectSourceDatabase after a redirect), so while the target opens
// it is pointed at database, the target's own dolt_database. Otherwise a
// server-mode open would connect to the launcher's database on the shared
// server, and an embedded writable open would create or migrate a wrongly
// named database under the target. An empty database leaves the environment
// as it is: the target names no database of its own to prefer.
//
// It opens through openForeignStore, so a target admitForeignWorkspace
// refuses (a legacy Dolt workspace among them) is refused before the open can
// start a server there or run migrations into its database. That is not every
// target bd run inside it refuses: see guardForeignLegacyDoltWorkspace and
// targetSelectsServerMode for the legacy Dolt layouts it admits.
//
// A writable open never creates the target's database
// (newExistingDoltStoreFromConfig): the open runs before the lookup it serves,
// so an id that misses must leave a repository that has not been bootstrapped
// yet as it found it. A target that has been bootstrapped does run its
// open-time schema migrations on such a miss, as any bd command run in that
// repository would; they bring it to the schema this bd writes, not to
// anything the missed id asked for.
func openRoutedTargetStore(ctx context.Context, beadsDir, database string, writable bool) (storage.DoltStorage, error) {
	if database != "" {
		origDB := os.Getenv("BEADS_DOLT_SERVER_DATABASE")
		_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", database)
		defer func() {
			// Restore the original env var
			if origDB != "" {
				_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", origDB)
			} else {
				_ = os.Unsetenv("BEADS_DOLT_SERVER_DATABASE")
			}
		}()
	}
	if writable {
		return openForeignStore(ctx, beadsDir, foreignWrite)
	}
	return openForeignStore(ctx, beadsDir, foreignRead)
}

// matchPrefixRoute returns the route whose prefix is the longest leading match
// for beadID, or nil if none matches. Route prefixes end in "-", so a
// multi-hyphen prefix such as "claude-os-" matches "claude-os-76l", and with
// both "hq-" and "hq-cv-" configured, "hq-cv-abc" takes the "hq-cv-" route
// (GH#5048).
func matchPrefixRoute(routes []prefixRoute, beadID string) *prefixRoute {
	var best *prefixRoute
	for i, r := range routes {
		if !strings.HasSuffix(r.Prefix, "-") || !strings.HasPrefix(beadID, r.Prefix) {
			continue
		}
		if best == nil || len(r.Prefix) > len(best.Prefix) {
			best = &routes[i]
		}
	}
	return best
}

// loadPrefixRoutes loads prefix-to-path routes from routes.jsonl in the beads directory.
func loadPrefixRoutes(beadsDir string) ([]prefixRoute, error) {
	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	file, err := os.Open(routesPath) //nolint:gosec // G304: path is constructed from trusted beads directory
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

// loadDoltDatabase reads the dolt_database field from a .beads/metadata.json
// file, and reports a metadata.json it cannot read or parse; a file without the
// field returns "" and no error.
func loadDoltDatabase(beadsDir string) (string, error) {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath) //nolint:gosec // G304: path is constructed from trusted beads directory
	if err != nil {
		return "", err
	}
	var meta struct {
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", fmt.Errorf("parsing %s: %w", remedy.PrintedValue(metadataPath), remedy.PrintedError(err))
	}
	return meta.DoltDatabase, nil
}

// autoRouteTargetDatabase is the dolt_database an auto-routed open points its
// target at (openRoutedTargetStore). A read stays tolerant: a metadata.json
// that is unreadable or names no database yields "", and the target's own open
// decides. A write refuses both, as a prefix route does,
// because the launcher's BEADS_DOLT_SERVER_DATABASE would then pick the
// database, and an embedded writable open creates the database it is given
// when the target holds any other (newExistingDoltStoreFromConfig refuses only
// a target that holds none).
//
// A target with no metadata.json at all is still opened, read or written. That
// is the planning repository contributor setup creates (runContributorWizard,
// autoConfigureForkContributor) until the first routed bd create writes one
// (ensureBeadsDirForPath). Setup names its database as any workspace without
// metadata.json does, after the variable when it is set and the default
// database otherwise, so the open is pointed at the only embedded database the
// target holds, or at the default database when it holds none or several.
// Resolving the name again at use would follow the launcher's variable, which
// need not be the one setup ran with: a read would then miss the database
// setup created, and a write would create another beside it.
func autoRouteTargetDatabase(beadsDir string, writable bool) (string, error) {
	database, err := loadDoltDatabase(beadsDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if databases := embeddeddolt.Databases(beadsDir); len(databases) == 1 {
			return databases[0], nil
		}
		return configfile.DefaultDoltDatabase, nil
	case !writable:
		return database, nil
	case err != nil:
		// A read or parse error repeats the routed path as the OS spelled it.
		return "", remedy.PrintedError(err)
	case database == "":
		return "", fmt.Errorf("%s has no dolt_database field", remedy.PrintedValue(filepath.Join(beadsDir, "metadata.json")))
	}
	return database, nil
}
