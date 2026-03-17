package beads

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/utils"
)

// RuntimeOwnershipMode captures who owns the lifecycle of a resolved runtime.
// This is intentionally coarse for the first consolidation slice; later runtime
// manager work can refine it without making callers rediscover ownership.
type RuntimeOwnershipMode string

const (
	RuntimeOwnershipUnknown     RuntimeOwnershipMode = "unknown"
	RuntimeOwnershipExternal    RuntimeOwnershipMode = "external"
	RuntimeOwnershipRepoManaged RuntimeOwnershipMode = "repo-managed"
	RuntimeOwnershipTestManaged RuntimeOwnershipMode = "test-managed"
)

// RepoRuntime is the canonical resolved view of a repo-local Beads runtime.
// It centralizes the repo/beads identity, effective Dolt DB identity, and the
// connection fields that callers previously rediscovered independently.
type RepoRuntime struct {
	RepoPath         string
	SourceBeadsDir   string
	BeadsDir         string
	Redirect         SourceDatabaseInfo
	Backend          string
	DatabasePath     string
	Database         string
	DoltDataDir      string
	DoltMode         string
	ServerMode       bool
	Host             string
	Port             int
	ExplicitPort     bool
	User             string
	TLS              bool
	SharedServerMode bool
	OwnershipMode    RuntimeOwnershipMode
}

// BuildFallbackRepoRuntime constructs a best-effort runtime from already
// resolved repo/beads/config inputs when redirect-aware resolution cannot be
// used directly. Callers still own the surrounding config-load policy.
func BuildFallbackRepoRuntime(repoPath, sourceBeadsDir, beadsDir string, cfg *configfile.Config) *RepoRuntime {
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if sourceBeadsDir == "" {
		sourceBeadsDir = beadsDir
	}

	return &RepoRuntime{
		RepoPath:         repoPath,
		SourceBeadsDir:   sourceBeadsDir,
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
		OwnershipMode:    resolveRuntimeOwnershipMode(),
	}
}

// ResolveRepoRuntimeFromRepoPath resolves a repo-local runtime from a repo root
// or worktree path. The returned BeadsDir is redirect-aware and points at the
// authoritative .beads directory for the repo.
func ResolveRepoRuntimeFromRepoPath(repoPath string) (*RepoRuntime, error) {
	if strings.TrimSpace(repoPath) == "" {
		return nil, fmt.Errorf("repo path required")
	}
	return ResolveRepoRuntimeFromBeadsDir(resolveBeadsDirForRepoPath(repoPath))
}

// ResolveRepoRuntimeFromBeadsDir resolves a canonical runtime from a .beads
// directory, following redirects and preserving the source dolt_database when a
// redirect points at a shared target.
func ResolveRepoRuntimeFromBeadsDir(beadsDir string) (*RepoRuntime, error) {
	return ResolveRepoRuntimeFromBeadsDirWithConfig(beadsDir, nil)
}

// ResolveRepoRuntimeFromBeadsDirWithConfig resolves a canonical runtime from a
// .beads directory while reusing a caller-provided metadata config when
// available. This lets storage/CLI paths converge on one runtime model without
// forcing an extra metadata read.
func ResolveRepoRuntimeFromBeadsDirWithConfig(beadsDir string, fileCfg *configfile.Config) (*RepoRuntime, error) {
	if strings.TrimSpace(beadsDir) == "" {
		return nil, fmt.Errorf("beads directory required")
	}

	sourceBeadsDir := canonicalizeBeadsDirPath(beadsDir)
	redirectInfo := ResolveRedirect(sourceBeadsDir)
	targetBeadsDir := redirectInfo.TargetDir

	var err error
	if fileCfg == nil {
		fileCfg, err = configfile.Load(targetBeadsDir)
		if err != nil {
			return nil, err
		}
	}
	if fileCfg == nil {
		fileCfg = configfile.DefaultConfig()
	}

	databaseName := fileCfg.GetDoltDatabase()
	if redirectInfo.WasRedirected && redirectInfo.SourceDatabase != "" && os.Getenv("BEADS_DOLT_SERVER_DATABASE") == "" {
		databaseName = redirectInfo.SourceDatabase
	}

	return &RepoRuntime{
		RepoPath:         filepath.Dir(sourceBeadsDir),
		SourceBeadsDir:   sourceBeadsDir,
		BeadsDir:         targetBeadsDir,
		Redirect:         redirectInfo,
		Backend:          fileCfg.GetBackend(),
		DatabasePath:     fileCfg.DatabasePath(targetBeadsDir),
		Database:         databaseName,
		DoltDataDir:      fileCfg.GetDoltDataDir(),
		DoltMode:         fileCfg.GetDoltMode(),
		ServerMode:       fileCfg.IsDoltServerMode(),
		Host:             fileCfg.GetDoltServerHost(),
		Port:             doltserver.DefaultConfig(targetBeadsDir).Port,
		ExplicitPort:     fileCfg.DoltServerPort > 0,
		User:             fileCfg.GetDoltServerUser(),
		TLS:              fileCfg.GetDoltServerTLS(),
		SharedServerMode: doltserver.IsSharedServerMode(),
		OwnershipMode:    resolveRuntimeOwnershipMode(),
	}, nil
}

// ResolveRepoRuntimeFromDBPath resolves a canonical runtime from a Dolt data
// directory path. This is used by path-only reopen helpers that need to recover
// repo-local metadata such as dolt_database and the tracked server port.
func ResolveRepoRuntimeFromDBPath(dbPath string) (*RepoRuntime, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, fmt.Errorf("database path required")
	}

	actualDBPath := utils.CanonicalizePath(dbPath)
	seen := map[string]struct{}{}
	candidates := make([]string, 0, 4)

	addCandidate := func(path string) {
		if path == "" {
			return
		}
		key := utils.NormalizePathForComparison(path)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, path)
	}

	addCandidate(filepath.Dir(dbPath))
	addCandidate(filepath.Dir(actualDBPath))

	if found := FindBeadsDir(); found != "" {
		addCandidate(found)
		addCandidate(utils.CanonicalizePath(found))
	}

	for _, candidate := range candidates {
		runtime, err := ResolveRepoRuntimeFromBeadsDir(candidate)
		if err != nil {
			continue
		}
		if utils.PathsEqual(runtime.DatabasePath, dbPath) || utils.PathsEqual(runtime.DatabasePath, actualDBPath) {
			return runtime, nil
		}
	}

	return nil, fmt.Errorf("no runtime found for database path %q", dbPath)
}

func resolveRuntimeOwnershipMode() RuntimeOwnershipMode {
	switch {
	case os.Getenv("BEADS_TEST_MODE") == "1":
		return RuntimeOwnershipTestManaged
	case doltserver.IsSharedServerMode():
		return RuntimeOwnershipExternal
	default:
		return RuntimeOwnershipRepoManaged
	}
}

func resolveBeadsDirForRepoPath(repoPath string) string {
	repoPath = utils.CanonicalizePath(repoPath)

	localBeadsDir := filepath.Join(repoPath, ".beads")
	if info, err := os.Stat(localBeadsDir); err == nil && info.IsDir() {
		return localBeadsDir
	}

	if fallback := worktreeFallbackBeadsDirForRepo(repoPath); fallback != "" {
		return fallback
	}

	return localBeadsDir
}

func worktreeFallbackBeadsDirForRepo(repoPath string) string {
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
