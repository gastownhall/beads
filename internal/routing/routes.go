package routing

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// RoutesFileName is the town-level prefix routing table used by orchestrator setups.
const RoutesFileName = "routes.jsonl"

// Route maps an issue ID prefix to a rig path relative to the town root.
type Route struct {
	Prefix string `json:"prefix"`
	Path   string `json:"path"`
}

// LoadRoutes loads prefix routes from a .beads/routes.jsonl file.
// Missing files are treated as "no routes configured".
func LoadRoutes(beadsDir string) ([]Route, error) {
	routesPath := filepath.Join(beadsDir, RoutesFileName)
	file, err := os.Open(routesPath) //nolint:gosec // Path is derived from a discovered .beads dir.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var routes []Route
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var route Route
		if err := json.Unmarshal([]byte(line), &route); err != nil {
			continue
		}
		if route.Prefix != "" && route.Path != "" {
			routes = append(routes, route)
		}
	}

	return routes, scanner.Err()
}

// ResolveBeadsDirForID resolves the authoritative .beads directory for an issue
// ID by consulting town-level prefix routes.
//
// Fork note: multi-database GT towns can recover into a state where the nearest
// local .beads directory is not the owning store for a prefix. Canonicalizing
// redirects and walking back to the town routing table keeps cross-rig lookups
// pointed at the authoritative rig database instead of the caller's local one.
// When no route matches, it returns the current beads dir with routed=false so
// callers can use their existing store.
func ResolveBeadsDirForID(ctx context.Context, id, currentBeadsDir string) (string, bool, error) {
	_ = ctx

	routes, townRoot := findTownRoutes(currentBeadsDir)
	if len(routes) == 0 {
		return currentBeadsDir, false, nil
	}

	prefix := types.ExtractPrefix(id)
	if prefix == "" {
		return currentBeadsDir, false, nil
	}

	for _, route := range routes {
		if route.Prefix != prefix {
			continue
		}

		var targetPath string
		if route.Path == "." {
			targetPath = filepath.Join(townRoot, ".beads")
		} else {
			targetPath = filepath.Join(townRoot, route.Path, ".beads")
		}

		redirectInfo := beads.ResolveRedirect(targetPath)
		targetPath = redirectInfo.TargetDir
		if redirectInfo.WasRedirected && redirectInfo.SourceDatabase != "" &&
			os.Getenv("BEADS_DOLT_SERVER_DATABASE") == "" {
			_ = os.Setenv("BEADS_DOLT_SERVER_DATABASE", redirectInfo.SourceDatabase)
		}

		if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
			return targetPath, true, nil
		}
	}

	return currentBeadsDir, false, nil
}

func findTownRoot(startDir string) string {
	current := startDir
	for {
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func canonicalizeRoutingBeadsDir(beadsDir string) string {
	if beadsDir == "" {
		return ""
	}
	return utils.NormalizePathForComparison(beads.ResolveRedirect(beadsDir).TargetDir)
}

func findTownRootForBeadsDirFromCWD(currentBeadsDir string) string {
	targetBeadsDir := canonicalizeRoutingBeadsDir(currentBeadsDir)
	if targetBeadsDir == "" {
		return ""
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	current := cwd
	for {
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			candidateBeadsDir := filepath.Join(current, ".beads")
			if info, statErr := os.Stat(candidateBeadsDir); statErr == nil && info.IsDir() {
				if canonicalizeRoutingBeadsDir(candidateBeadsDir) == targetBeadsDir {
					return current
				}
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func findNearestTownRoutesFromPath(startDir string) ([]Route, string) {
	if startDir == "" {
		return nil, ""
	}

	current := startDir
	for {
		if _, err := os.Stat(filepath.Join(current, "mayor", "town.json")); err == nil {
			townBeadsDir := filepath.Join(current, ".beads")
			routes, loadErr := LoadRoutes(townBeadsDir)
			if loadErr == nil && len(routes) > 0 {
				return routes, current
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil, ""
		}
		current = parent
	}
}

func findNearestTownRoutesFromCWD() ([]Route, string) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, ""
	}
	return findNearestTownRoutesFromPath(cwd)
}

func findTownRoutes(currentBeadsDir string) ([]Route, string) {
	if currentBeadsDir != "" {
		routes, err := LoadRoutes(currentBeadsDir)
		if err == nil && len(routes) > 0 {
			if townRoot := findTownRootForBeadsDirFromCWD(currentBeadsDir); townRoot != "" {
				return routes, townRoot
			}
			if townRoot := findTownRoot(filepath.Dir(currentBeadsDir)); townRoot != "" {
				return routes, townRoot
			}
			return routes, filepath.Dir(currentBeadsDir)
		}

		if routes, townRoot := findNearestTownRoutesFromPath(filepath.Dir(currentBeadsDir)); len(routes) > 0 && townRoot != "" {
			return routes, townRoot
		}
		return nil, ""
	}

	routes, townRoot := findNearestTownRoutesFromCWD()
	if len(routes) == 0 || townRoot == "" {
		return nil, ""
	}
	return routes, townRoot
}
