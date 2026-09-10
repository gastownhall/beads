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
	// Backend is the resolved storage backend name, spelled the way the store
	// open resolves it (configfile.GetBackend): "dolt" for the Dolt family
	// including the legacy empty value, and a registered backend's own name
	// otherwise. It is what tells GetContextInfo whether the Dolt-derived
	// members beside it describe this workspace at all.
	Backend             string
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

// SetBackendIdentity records which backend a workspace is on, and the
// Dolt-derived identity ONLY when that backend is Dolt.
//
// It is a method rather than three assignments because there are two routes to
// a snapshot — this package's use case, and `bd context`'s direct route, which
// reads the config files itself so it can answer in degraded states where no
// database opens — and one policy they both call is what keeps them from naming
// one workspace two ways.
//
// The gate is load-bearing, not defensive, because both Dolt values DEFAULT
// rather than fail: configfile reads an absent dolt_mode as "embedded" and an
// absent dolt_database as "beads". So a workspace on a registered backend,
// which configures neither, was described in confident detail as embedded Dolt
// on database "beads" — on `bd context`, on `bd context --json`, and on GET
// /v0/beads/context, which is the one endpoint automation is told to trust for
// a server's identity.
//
// A non-Dolt backend reports the EMPTY string for both, and that is the only
// value bd can assert. A registered backend's Open reads whatever it wants out
// of the workspace; bd does not implement it and cannot know which logical
// database it settled on, so any non-empty guess would be the same lie made
// quieter. Both remain required strings on the wire — the shape is unchanged,
// only the claim is dropped.
//
// The rest of the Dolt projection is gated already, one layer down:
// IsDoltServerMode and IsDoltProxiedServerMode are false for any backend that
// is not Dolt, so the bind endpoint and the proxied root were never published
// for one. These two were the members with no such guard.
func (info *ContextInfo) SetBackendIdentity(backend, doltMode, database string) {
	info.Backend = backend
	info.DoltMode, info.Database = "", ""
	if backend == configfile.BackendDolt {
		info.DoltMode, info.Database = doltMode, database
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
		ProjectID:    backend.ProjectID,
		DataDir:      backend.DataDir,
		BdVersion:    u.version,
	}
	info.SetBackendIdentity(backend.Backend, backend.DoltMode, backend.Database)

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
