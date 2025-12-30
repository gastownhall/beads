package routing

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// RoutesFileName is the name of the routes configuration file
const RoutesFileName = "routes.jsonl"

// Route represents a prefix-to-path routing rule
type Route struct {
	Prefix string `json:"prefix"` // Issue ID prefix (e.g., "gt-")
	Path   string `json:"path"`   // Relative path to .beads directory
}

// LoadRoutes loads routes from routes.jsonl in the given beads directory.
// Returns an empty slice if the file doesn't exist.
func LoadRoutes(beadsDir string) ([]Route, error) {
	routesPath := filepath.Join(beadsDir, RoutesFileName)
	file, err := os.Open(routesPath) //nolint:gosec // routesPath is constructed from known beadsDir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No routes file is not an error
		}
		return nil, err
	}
	defer file.Close()

	var routes []Route
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}

		var route Route
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			continue // Skip malformed lines
		}
		if route.Prefix != "" && route.Path != "" {
			routes = append(routes, route)
		}
	}

	return routes, scanner.Err()
}

// ExtractPrefix extracts the prefix from an issue ID.
// For "gt-abc123", returns "gt-".
// For "bd-abc123", returns "bd-".
// Returns empty string if no prefix found.
func ExtractPrefix(id string) string {
	idx := strings.Index(id, "-")
	if idx < 0 {
		return ""
	}
	return id[:idx+1] // Include the hyphen
}

// ExtractProjectFromPath extracts the project name from a route path.
// For "beads/mayor/rig", returns "beads".
// For "gastown/crew/max", returns "gastown".
func ExtractProjectFromPath(path string) string {
	// Get the first component of the path
	parts := strings.Split(path, "/")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

// LookupRigByName finds a route by rig name (first path component).
// For example, LookupRigByName("beads", beadsDir) would find the route
// with path "beads/mayor/rig" and return it.
//
// Returns the matching route and true if found, or zero Route and false if not.
func LookupRigByName(rigName, beadsDir string) (Route, bool) {
	routes, err := LoadRoutes(beadsDir)
	if err != nil || len(routes) == 0 {
		return Route{}, false
	}

	for _, route := range routes {
		project := ExtractProjectFromPath(route.Path)
		if project == rigName {
			return route, true
		}
	}

	return Route{}, false
}

// LookupRigForgiving finds a route using flexible matching.
// Accepts any of these formats and normalizes them:
//   - "bd-" (exact prefix)
//   - "bd"  (prefix without hyphen, will try "bd-")
//   - "beads" (rig name)
//
// This provides good agent UX - meet them where they are.
// It searches for routes.jsonl in the current beads dir first, then at the town level.
func LookupRigForgiving(input, beadsDir string) (Route, bool) {
	route, _, found := lookupRigForgivingWithTown(input, beadsDir)
	return route, found
}

// lookupRigForgivingWithTown finds a route with flexible matching and returns the town root.
// Returns (route, townRoot, found).
func lookupRigForgivingWithTown(input, beadsDir string) (Route, string, bool) {
	routes, townRoot := findTownRoutes(beadsDir)
	if len(routes) == 0 {
		return Route{}, "", false
	}

	// Normalize: remove trailing hyphen for comparison
	normalized := strings.TrimSuffix(input, "-")

	for _, route := range routes {
		// Try exact prefix match (with or without hyphen)
		prefixBase := strings.TrimSuffix(route.Prefix, "-")
		if normalized == prefixBase || input == route.Prefix {
			return route, townRoot, true
		}

		// Try rig name match
		project := ExtractProjectFromPath(route.Path)
		if input == project {
			return route, townRoot, true
		}
	}

	return Route{}, "", false
}

// ResolveBeadsDirForRig returns the beads directory for a given rig identifier.
// This is used by --rig and --prefix flags to create issues in a different rig.
//
// The input is forgiving - accepts any of:
//   - "beads", "gastown" (rig names)
//   - "bd-", "gt-" (exact prefixes)
//   - "bd", "gt" (prefixes without hyphen)
//
// Parameters:
//   - rigOrPrefix: rig name or prefix in any format
//   - currentBeadsDir: the current .beads directory (used to find routes.jsonl)
//
// Returns:
//   - beadsDir: the target .beads directory path
//   - prefix: the issue prefix for that rig (e.g., "bd-")
//   - err: error if rig not found or path doesn't exist
func ResolveBeadsDirForRig(rigOrPrefix, currentBeadsDir string) (beadsDir string, prefix string, err error) {
	route, townRoot, found := lookupRigForgivingWithTown(rigOrPrefix, currentBeadsDir)
	if !found {
		return "", "", fmt.Errorf("rig or prefix %q not found in routes.jsonl", rigOrPrefix)
	}

	// Resolve the target beads directory
	var targetPath string
	if route.Path == "." {
		// Special case: "." means the town beads directory
		targetPath = filepath.Join(townRoot, ".beads")
	} else {
		// Normal path resolution relative to town root
		targetPath = filepath.Join(townRoot, route.Path, ".beads")
	}

	// Follow redirect if present
	targetPath = resolveRedirect(targetPath)

	// Verify the target exists
	if info, statErr := os.Stat(targetPath); statErr != nil || !info.IsDir() {
		return "", "", fmt.Errorf("rig %q beads directory not found: %s", rigOrPrefix, targetPath)
	}

	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Rig %q -> prefix %s, path %s (townRoot=%s)\n", rigOrPrefix, route.Prefix, targetPath, townRoot)
	}

	return targetPath, route.Prefix, nil
}

// ResolveToExternalRef attempts to convert a foreign issue ID to an external reference
// using routes.jsonl for prefix-based routing.
//
// If the ID's prefix matches a route, returns "external:<project>:<id>".
// Otherwise, returns empty string (no route found).
//
// Example: If routes.jsonl has {"prefix": "bd-", "path": "beads/mayor/rig"}
// then ResolveToExternalRef("bd-abc", beadsDir) returns "external:beads:bd-abc"
func ResolveToExternalRef(id, beadsDir string) string {
	routes, err := LoadRoutes(beadsDir)
	if err != nil || len(routes) == 0 {
		return ""
	}

	prefix := ExtractPrefix(id)
	if prefix == "" {
		return ""
	}

	for _, route := range routes {
		if route.Prefix == prefix {
			project := ExtractProjectFromPath(route.Path)
			if project != "" {
				return fmt.Sprintf("external:%s:%s", project, id)
			}
		}
	}

	return ""
}

// ResolveBeadsDirForID determines which beads directory contains the given issue ID.
// It first checks the local beads directory, then consults routes.jsonl for prefix-based routing.
// If routes.jsonl is not found locally, it searches up to the town root.
//
// Parameters:
//   - ctx: context for database operations
//   - id: the issue ID to look up
//   - currentBeadsDir: the current/local .beads directory path
//
// Returns:
//   - beadsDir: the resolved .beads directory path
//   - routed: true if the ID was routed to a different directory
//   - err: any error encountered
func ResolveBeadsDirForID(ctx context.Context, id, currentBeadsDir string) (string, bool, error) {
	// Step 1: Check for routes.jsonl based on ID prefix
	// First try local, then walk up to find town-level routes
	routes, townRoot := findTownRoutes(currentBeadsDir)
	if len(routes) > 0 {
		prefix := ExtractPrefix(id)
		if prefix != "" {
			for _, route := range routes {
				if route.Prefix == prefix {
					// Found a matching route - resolve the path
					var targetPath string
					if route.Path == "." {
						// Special case: "." means the town beads directory
						targetPath = filepath.Join(townRoot, ".beads")
					} else {
						// Normal path resolution relative to town root
						targetPath = filepath.Join(townRoot, route.Path, ".beads")
					}

					// Follow redirect if present
					targetPath = resolveRedirect(targetPath)

					// Verify the target exists
					if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
						// Debug logging
						if os.Getenv("BD_DEBUG_ROUTING") != "" {
							fmt.Fprintf(os.Stderr, "[routing] ID %s matched prefix %s -> %s (townRoot=%s)\n", id, prefix, targetPath, townRoot)
						}
						return targetPath, true, nil
					} else if os.Getenv("BD_DEBUG_ROUTING") != "" {
						fmt.Fprintf(os.Stderr, "[routing] ID %s matched prefix %s but target %s not found: %v\n", id, prefix, targetPath, err)
					}
				}
			}
		}
	}

	// Step 2: No route matched or no routes file - use local store
	return currentBeadsDir, false, nil
}

// findTownRoot walks up from startDir looking for a town root.
// Returns the town root path, or empty string if not found.
// A town root is identified by the presence of mayor/town.json.
func findTownRoot(startDir string) string {
	current := startDir
	for {
		// Check for primary marker (mayor/town.json)
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "" // Reached filesystem root
		}
		current = parent
	}
}

// findTownRoutes searches for routes.jsonl at the town level.
// It walks up from currentBeadsDir to find the town root, then loads routes
// from <townRoot>/.beads/routes.jsonl.
// Returns (routes, townRoot). Returns nil routes if not in an orchestrator town or no routes found.
func findTownRoutes(currentBeadsDir string) ([]Route, string) {
	// First try the current beads dir (works if we're already at town level)
	routes, err := LoadRoutes(currentBeadsDir)
	if err == nil && len(routes) > 0 {
		// Return the parent of the beads dir as "town root" for path resolution
		return routes, filepath.Dir(currentBeadsDir)
	}

	// Walk up to find town root
	townRoot := findTownRoot(currentBeadsDir)
	if townRoot == "" {
		return nil, "" // Not in a town
	}

	// Load routes from town beads
	townBeadsDir := filepath.Join(townRoot, ".beads")
	routes, err = LoadRoutes(townBeadsDir)
	if err != nil || len(routes) == 0 {
		return nil, "" // No town routes
	}

	return routes, townRoot
}

// resolveRedirect checks for a redirect file in the beads directory
// and resolves the redirect path if present.
func resolveRedirect(beadsDir string) string {
	redirectFile := filepath.Join(beadsDir, "redirect")
	data, err := os.ReadFile(redirectFile) //nolint:gosec // redirectFile is constructed from known beadsDir
	if err != nil {
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] No redirect file at %s: %v\n", redirectFile, err)
		}
		return beadsDir // No redirect
	}

	redirectPath := strings.TrimSpace(string(data))
	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Read redirect: %q from %s\n", redirectPath, redirectFile)
	}
	if redirectPath == "" {
		return beadsDir
	}

	// Handle relative paths
	if !filepath.IsAbs(redirectPath) {
		redirectPath = filepath.Join(beadsDir, redirectPath)
	}

	// Clean and resolve the path
	redirectPath = filepath.Clean(redirectPath)
	if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Resolved redirect path: %s\n", redirectPath)
	}

	// Verify the redirect target exists
	if info, err := os.Stat(redirectPath); err == nil && info.IsDir() {
		if os.Getenv("BD_DEBUG_ROUTING") != "" {
			fmt.Fprintf(os.Stderr, "[routing] Followed redirect from %s -> %s\n", beadsDir, redirectPath)
		}
		return redirectPath
	} else if os.Getenv("BD_DEBUG_ROUTING") != "" {
		fmt.Fprintf(os.Stderr, "[routing] Redirect target check failed: %v\n", err)
	}

	return beadsDir
}

// RoutedStorage represents a storage connection that may have been routed
// to a different beads directory than the local one.
type RoutedStorage struct {
	Storage  storage.Storage
	BeadsDir string
	Routed   bool // true if this is a routed (non-local) storage
}

// Close closes the storage connection
func (rs *RoutedStorage) Close() error {
	if rs.Storage != nil {
		return rs.Storage.Close()
	}
	return nil
}

// GetRoutedStorageForID returns a storage connection for the given issue ID.
// If the ID matches a route, it opens a connection to the routed database.
// Otherwise, it returns nil (caller should use their existing storage).
//
// The caller is responsible for closing the returned RoutedStorage.
func GetRoutedStorageForID(ctx context.Context, id, currentBeadsDir string) (*RoutedStorage, error) {
	beadsDir, routed, err := ResolveBeadsDirForID(ctx, id, currentBeadsDir)
	if err != nil {
		return nil, err
	}

	if !routed {
		return nil, nil // No routing needed, caller should use existing storage
	}

	// Open storage for the routed directory
	dbPath := filepath.Join(beadsDir, "beads.db")
	store, err := sqlite.New(ctx, dbPath)
	if err != nil {
		return nil, err
	}

	return &RoutedStorage{
		Storage:  store,
		BeadsDir: beadsDir,
		Routed:   true,
	}, nil
}
