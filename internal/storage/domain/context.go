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

// PublishedContextFields is the workspace identity every context surface
// answers with, projected off the snapshot exactly once. `bd context` prints
// it, `bd context --json` marshals it, and GET /v0/beads/context serves it —
// and because the projection below is the only way any of them gets these
// values, the three cannot drift into naming the same workspace differently.
//
// WHAT IS ABSENT IS THE POINT, and its absence is structural rather than
// remembered.
//
// SyncRemote above all. It is populated unconditionally from the workspace's
// sync.remote config and remote URLs routinely embed credentials
// (https://x-access-token:TOKEN@host/...), so it has no member here: a surface
// that reaches identity through this type cannot publish it by forgetting to
// exclude it, and adding one is an edit to this file with the review that
// implies.
//
// The database bind endpoint (ServerHost, ServerPort) and the absolute host
// paths (CWDRepoRoot, ProxiedDir, DataDir) are absent for the same reason:
// advertising the endpoint invites a client to bypass the API and dial a
// server whose trust model is "root with an empty password on loopback", and
// the paths identify nothing a consumer needs. A surface that is entitled to
// them — the local CLI, printing a diagnostic to the operator's own terminal
// — reads them off the snapshot itself, beside this projection and visibly
// so.
type PublishedContextFields struct {
	BdVersion string
	Backend   string
	DoltMode  string
	Database  string
	BeadsDir  string
	RepoRoot  string
	ProjectID string
}

// PublishedContext projects a workspace snapshot onto the fields every context
// surface publishes.
func PublishedContext(info ContextInfo) PublishedContextFields {
	return PublishedContextFields{
		BdVersion: info.BdVersion,
		Backend:   info.Backend,
		DoltMode:  info.DoltMode,
		Database:  info.Database,
		BeadsDir:  info.BeadsDir,
		RepoRoot:  info.RepoRoot,
		ProjectID: info.ProjectID,
	}
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
