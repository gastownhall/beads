package doctor

import (
	"path/filepath"
	"sync"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/utils"
)

var resolveBeadsDirCache sync.Map

// getBackendAndBeadsDir resolves the effective .beads directory (following redirects)
// and returns the configured storage backend ("dolt" by default).
func getBackendAndBeadsDir(repoPath string) (backend string, beadsDir string) {
	beadsDir = ResolveBeadsDirForRepo(repoPath)

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return configfile.BackendDolt, beadsDir
	}
	return cfg.GetBackend(), beadsDir
}

func ResolveBeadsDirForRepo(repoPath string) string {
	cacheKey := utils.CanonicalizePath(repoPath)
	if resolved, ok := resolveBeadsDirCache.Load(cacheKey); ok {
		return resolved.(string)
	}

	resolved := resolveBeadsDirForRepoUncached(repoPath)
	resolveBeadsDirCache.Store(cacheKey, resolved)
	return resolved
}

func resolveBeadsDirForRepoUncached(repoPath string) string {
	return beads.ResolveBeadsDirForRepo(repoPath)
}

// BeadsManagedStorageHooksDir returns the hooks directory that
// `bd hooks install --beads` writes to core.hooksPath — <effective .beads>/hooks
// — or "" when no beads storage resolves.
//
// install resolves that directory with beads.FindBeadsDir, which honors
// BEADS_DIR and .beads/redirect and can therefore land outside the repository.
// Uninstall and `bd doctor --fix` must resolve it exactly the same way: a value
// they cannot recognize is a value they cannot clear, which leaves
// core.hooksPath pointing at a hooks directory whose files uninstall just
// deleted (every hook silently disabled, beads-managed config still installed —
// the GH#4440 contract). Deliberately not ResolveBeadsDirForRepo, which is
// repo-anchored and blind to BEADS_DIR.
func BeadsManagedStorageHooksDir() string {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return ""
	}
	return filepath.Join(beadsDir, "hooks")
}

func resolvedBeadsRepoRoot(repoPath string) string {
	return filepath.Dir(ResolveBeadsDirForRepo(repoPath))
}

func clearResolveBeadsDirCache() {
	resolveBeadsDirCache = sync.Map{}
}
