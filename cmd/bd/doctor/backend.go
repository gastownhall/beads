package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/utils"
)

var resolveBeadsDirCache sync.Map

type repoRuntimeInfo struct {
	Runtime *beads.RepoRuntime
	Config  *configfile.Config
}

func resolveRuntimeInfoForRepo(repoPath string) *repoRuntimeInfo {
	runtime, err := beads.ResolveRepoRuntimeFromRepoPath(repoPath)
	if err == nil && runtime != nil {
		cfg, cfgErr := configfile.Load(runtime.BeadsDir)
		if cfgErr != nil || cfg == nil {
			cfg = configfile.DefaultConfig()
		}
		return &repoRuntimeInfo{Runtime: runtime, Config: cfg}
	}

	beadsDir := resolveBeadsDir(filepath.Join(repoPath, ".beads"))
	cfg, cfgErr := configfile.Load(beadsDir)
	if cfgErr != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}

	return &repoRuntimeInfo{
		Runtime: &beads.RepoRuntime{
			RepoPath:         repoPath,
			SourceBeadsDir:   filepath.Join(repoPath, ".beads"),
			BeadsDir:         beadsDir,
			Backend:          cfg.GetBackend(),
			DatabasePath:     cfg.DatabasePath(beadsDir),
			Database:         cfg.GetDoltDatabase(),
			DoltDataDir:      cfg.GetDoltDataDir(),
			DoltMode:         cfg.GetDoltMode(),
			ServerMode:       cfg.IsDoltServerMode(),
			Host:             cfg.GetDoltServerHost(),
			Port:             doltserver.DefaultConfig(beadsDir).Port,
			ExplicitPort:     cfg.DoltServerPort > 0,
			User:             cfg.GetDoltServerUser(),
			TLS:              cfg.GetDoltServerTLS(),
			SharedServerMode: doltserver.IsSharedServerMode(),
		},
		Config: cfg,
	}
}

// getBackendAndBeadsDir resolves the effective .beads directory (following redirects)
// and returns the configured storage backend ("dolt" by default).
func getBackendAndBeadsDir(repoPath string) (backend string, beadsDir string) {
	runtimeInfo := resolveRuntimeInfoForRepo(repoPath)
	if runtimeInfo != nil && runtimeInfo.Runtime != nil {
		return runtimeInfo.Runtime.Backend, runtimeInfo.Runtime.BeadsDir
	}

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

	runtimeInfo := resolveRuntimeInfoForRepo(repoPath)
	if runtimeInfo != nil && runtimeInfo.Runtime != nil {
		resolveBeadsDirCache.Store(cacheKey, runtimeInfo.Runtime.BeadsDir)
		return runtimeInfo.Runtime.BeadsDir
	}

	resolved := resolveBeadsDirForRepoUncached(repoPath)
	resolveBeadsDirCache.Store(cacheKey, resolved)
	return resolved
}

func resolveBeadsDirForRepoUncached(repoPath string) string {
	localBeadsDir := filepath.Join(repoPath, ".beads")
	if info, err := os.Stat(localBeadsDir); err == nil && info.IsDir() {
		return resolveBeadsDir(localBeadsDir)
	}

	if fallback := worktreeFallbackBeadsDir(repoPath); fallback != "" {
		return resolveBeadsDir(fallback)
	}

	return resolveBeadsDir(localBeadsDir)
}

func clearResolveBeadsDirCache() {
	resolveBeadsDirCache = sync.Map{}
}

func worktreeFallbackBeadsDir(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}

	gitDir := gitPathForRepo(repoPath, strings.TrimSpace(lines[0]))
	commonDir := gitPathForRepo(repoPath, strings.TrimSpace(lines[1]))
	if gitDir == "" || commonDir == "" || utils.PathsEqual(gitDir, commonDir) {
		return ""
	}

	if filepath.Base(commonDir) == ".git" {
		return filepath.Join(filepath.Dir(commonDir), ".beads")
	}

	return filepath.Join(commonDir, ".beads")
}

func gitPathForRepo(repoPath, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoPath, path)
	}
	return utils.CanonicalizePath(path)
}
