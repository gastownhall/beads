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

func resolvedBeadsRepoRoot(repoPath string) string {
	return filepath.Dir(ResolveBeadsDirForRepo(repoPath))
}

func clearResolveBeadsDirCache() {
	resolveBeadsDirCache = sync.Map{}
}
