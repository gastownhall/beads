package domain

import (
	"context"

	"github.com/steveyegge/beads/internal/configfile"
)

type ContextRepository interface {
	RepoContext(ctx context.Context) (RepoPaths, error)
	Role(ctx context.Context) (role string, hasRole bool, err error)
	BackendConfig(ctx context.Context) (BackendConfig, error)
	ServerPort(ctx context.Context) (int, error)
	ProxiedServerRoot(ctx context.Context) (string, error)
	SyncRemote(ctx context.Context) (string, error)
}

type RepoPaths struct {
	BeadsDir     string
	RepoRoot     string
	CWDRepoRoot  string
	IsRedirected bool
	IsWorktree   bool
}

type BackendConfig struct {
	DoltMode            string
	Database            string
	ProjectID           string
	ServerHost          string
	DataDir             string
	IsServerMode        bool
	IsProxiedServerMode bool
}

type ContextInfo struct {
	BeadsDir     string
	RepoRoot     string
	CWDRepoRoot  string
	IsRedirected bool
	IsWorktree   bool
	Backend      string
	DoltMode     string
	ServerHost   string
	ServerPort   int
	ProxiedDir   string
	Database     string
	DataDir      string
	ProjectID    string
	SyncRemote   string
	Role         string
	BdVersion    string
}

type ContextUseCase interface {
	GetContextInfo(ctx context.Context) (ContextInfo, error)
}

func NewContextUseCase(repo ContextRepository, version string) ContextUseCase {
	return &contextUseCaseImpl{repo: repo, version: version}
}

type contextUseCaseImpl struct {
	repo    ContextRepository
	version string
}

var _ ContextUseCase = (*contextUseCaseImpl)(nil)

func (u *contextUseCaseImpl) GetContextInfo(ctx context.Context) (ContextInfo, error) {
	paths, err := u.repo.RepoContext(ctx)
	if err != nil {
		return ContextInfo{}, err
	}

	role, hasRole, err := u.repo.Role(ctx)
	if err != nil {
		return ContextInfo{}, err
	}

	backend, err := u.repo.BackendConfig(ctx)
	if err != nil {
		return ContextInfo{}, err
	}

	info := ContextInfo{
		BeadsDir:     paths.BeadsDir,
		RepoRoot:     paths.RepoRoot,
		CWDRepoRoot:  paths.CWDRepoRoot,
		IsRedirected: paths.IsRedirected,
		IsWorktree:   paths.IsWorktree,
		Backend:      configfile.BackendDolt,
		DoltMode:     backend.DoltMode,
		Database:     backend.Database,
		ProjectID:    backend.ProjectID,
		DataDir:      backend.DataDir,
		BdVersion:    u.version,
	}

	if hasRole {
		info.Role = role
	}

	if backend.IsServerMode {
		info.ServerHost = backend.ServerHost
		port, err := u.repo.ServerPort(ctx)
		if err != nil {
			return ContextInfo{}, err
		}
		info.ServerPort = port
	}

	if backend.IsProxiedServerMode {
		proxiedDir, err := u.repo.ProxiedServerRoot(ctx)
		if err != nil {
			return ContextInfo{}, err
		}
		info.ProxiedDir = proxiedDir
	}

	remote, err := u.repo.SyncRemote(ctx)
	if err != nil {
		return ContextInfo{}, err
	}
	info.SyncRemote = remote

	return info, nil
}
