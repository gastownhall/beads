package fs

import (
	"context"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/domain"
)

const proxiedServerRootDirName = "proxieddb"

func NewContextRepository(fsRepo domain.BeadsDirFSRepository) domain.ContextRepository {
	return &contextRepositoryImpl{fsRepo: fsRepo}
}

type contextRepositoryImpl struct {
	fsRepo domain.BeadsDirFSRepository
}

var _ domain.ContextRepository = (*contextRepositoryImpl)(nil)

func (r *contextRepositoryImpl) beadsDir(ctx context.Context) string {
	return r.fsRepo.ResolveBeadsDirPath(ctx).BeadsDir
}

func (r *contextRepositoryImpl) RepoContext(ctx context.Context) (domain.RepoPaths, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return domain.RepoPaths{}, err
	}
	return domain.RepoPaths{
		BeadsDir:     rc.BeadsDir,
		RepoRoot:     rc.RepoRoot,
		CWDRepoRoot:  rc.CWDRepoRoot,
		IsRedirected: rc.IsRedirected,
		IsWorktree:   rc.IsWorktree,
	}, nil
}

func (r *contextRepositoryImpl) Role(ctx context.Context) (string, bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return "", false, err
	}
	role, ok := rc.Role()
	return string(role), ok, nil
}

func (r *contextRepositoryImpl) BackendConfig(ctx context.Context) (domain.BackendConfig, error) {
	cfg, err := r.fsRepo.ReadBeadsConfig(ctx)
	if err != nil || cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	return domain.BackendConfig{
		DoltMode:            cfg.GetDoltMode(),
		Database:            cfg.GetDoltDatabase(),
		ProjectID:           cfg.ProjectID,
		ServerHost:          cfg.GetDoltServerHost(),
		DataDir:             cfg.GetDoltDataDir(),
		IsServerMode:        cfg.IsDoltServerMode(),
		IsProxiedServerMode: cfg.IsDoltProxiedServerMode(),
	}, nil
}

func (r *contextRepositoryImpl) ServerPort(ctx context.Context) (int, error) {
	return doltserver.DefaultConfig(r.beadsDir(ctx)).Port, nil
}

func (r *contextRepositoryImpl) ProxiedServerRoot(ctx context.Context) (string, error) {
	beadsDir := r.beadsDir(ctx)
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_ROOT_PATH", beadsDir); p != "" {
		return p, nil
	}
	info, err := r.fsRepo.ReadProxiedServerClientInfo(ctx)
	if err != nil {
		return "", err
	}
	if p := info.ResolvedRootPath(beadsDir); p != "" {
		return p, nil
	}
	return filepath.Join(beadsDir, proxiedServerRootDirName), nil
}

func (r *contextRepositoryImpl) SyncRemote(ctx context.Context) (string, error) {
	beadsDir := r.beadsDir(ctx)
	if v := config.GetStringFromDir(beadsDir, "sync.remote"); v != "" {
		return v, nil
	}
	return config.GetStringFromDir(beadsDir, "sync.git-remote"), nil
}

func envOrAbsJoin(envName, beadsDir string) string {
	p := os.Getenv(envName)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(beadsDir, p)
}
