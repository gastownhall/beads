package domain

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

type fakeContextRepo struct {
	paths       RepoPaths
	pathsErr    error
	role        string
	roleOK      bool
	roleErr     error
	backend     BackendConfig
	backendErr  error
	port        int
	portErr     error
	portCalled  bool
	proxiedRoot string
	proxiedErr  error
	proxCalled  bool
	syncRemote  string
	syncErr     error
}

func (f *fakeContextRepo) RepoContext(ctx context.Context) (RepoPaths, error) {
	return f.paths, f.pathsErr
}

func (f *fakeContextRepo) Role(ctx context.Context) (string, bool, error) {
	return f.role, f.roleOK, f.roleErr
}

func (f *fakeContextRepo) BackendConfig(ctx context.Context) (BackendConfig, error) {
	return f.backend, f.backendErr
}

func (f *fakeContextRepo) ServerPort(ctx context.Context) (int, error) {
	f.portCalled = true
	return f.port, f.portErr
}

func (f *fakeContextRepo) ProxiedServerRoot(ctx context.Context) (string, error) {
	f.proxCalled = true
	return f.proxiedRoot, f.proxiedErr
}

func (f *fakeContextRepo) SyncRemote(ctx context.Context) (string, error) {
	return f.syncRemote, f.syncErr
}

func TestContextUseCase_Embedded(t *testing.T) {
	repo := &fakeContextRepo{
		paths: RepoPaths{
			BeadsDir:     "/repo/.beads",
			RepoRoot:     "/repo",
			CWDRepoRoot:  "/cwd",
			IsRedirected: true,
			IsWorktree:   true,
		},
		role:   "contributor",
		roleOK: true,
		backend: BackendConfig{
			Backend:   configfile.BackendDolt,
			DoltMode:  configfile.DoltModeEmbedded,
			Database:  "beads",
			ProjectID: "proj-1",
			DataDir:   "/data",
		},
		syncRemote: "origin-url",
	}

	info, err := NewContextUseCase(repo, "1.2.3").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}

	if info.BeadsDir != "/repo/.beads" || info.RepoRoot != "/repo" || info.CWDRepoRoot != "/cwd" {
		t.Errorf("paths not mapped: %+v", info)
	}
	if !info.IsRedirected || !info.IsWorktree {
		t.Errorf("flags not mapped: %+v", info)
	}
	if info.Role != "contributor" {
		t.Errorf("Role = %q, want contributor", info.Role)
	}
	if info.Backend != configfile.BackendDolt {
		t.Errorf("Backend = %q, want %q", info.Backend, configfile.BackendDolt)
	}
	if info.BdVersion != "1.2.3" {
		t.Errorf("BdVersion = %q, want 1.2.3", info.BdVersion)
	}
	if info.DoltMode != configfile.DoltModeEmbedded || info.Database != "beads" || info.ProjectID != "proj-1" || info.DataDir != "/data" {
		t.Errorf("backend config not mapped: %+v", info)
	}
	if info.SyncRemote != "origin-url" {
		t.Errorf("SyncRemote = %q, want origin-url", info.SyncRemote)
	}
	if info.ServerHost != "" || info.ServerPort != 0 || info.ProxiedDir != "" {
		t.Errorf("server/proxied fields should be empty in embedded mode: %+v", info)
	}
	if repo.portCalled {
		t.Error("ServerPort should not be queried outside server mode")
	}
	if repo.proxCalled {
		t.Error("ProxiedServerRoot should not be queried outside proxied-server mode")
	}
}

// TestSetBackendIdentityIsTotal. The method decides all three members every
// time it is called rather than only the ones it has a value for, so a snapshot
// that already carried a Dolt mode cannot come out of it describing two
// backends at once. Both routes to a snapshot call it, and only this makes the
// result independent of what either had already filled in.
func TestSetBackendIdentityIsTotal(t *testing.T) {
	info := ContextInfo{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeServer,
		Database: "left_over",
	}

	info.SetBackendIdentity("acme", configfile.DoltModeEmbedded, configfile.DefaultDoltDatabase)

	if info.Backend != "acme" || info.DoltMode != "" || info.Database != "" {
		t.Errorf("Backend/DoltMode/Database = %q/%q/%q, want acme//: a non-Dolt backend kept a Dolt identity",
			info.Backend, info.DoltMode, info.Database)
	}

	info.SetBackendIdentity(configfile.BackendDolt, configfile.DoltModeServer, "beads")
	if info.DoltMode != configfile.DoltModeServer || info.Database != "beads" {
		t.Errorf("DoltMode/Database = %q/%q, want server/beads", info.DoltMode, info.Database)
	}
}

// TestContextUseCase_RegisteredBackendGetsNoDoltIdentity.
//
// Every context surface answers from this projection — `bd context`, `bd
// context --json`, and GET /v0/beads/context — so a workspace on a registered
// backend that is described here as embedded Dolt is described that way on all
// three. It is not a cosmetic mislabel: the HTTP handshake is the one endpoint
// automation is told to trust for a server's identity, and it was reporting the
// exact topology `bd serve` refuses to serve.
//
// The three Dolt-derived values all default rather than fail — an absent
// dolt_mode reads "embedded", an absent dolt_database reads "beads" — so a
// registered workspace that configures none of them was described in full
// detail, entirely wrongly.
func TestContextUseCase_RegisteredBackendGetsNoDoltIdentity(t *testing.T) {
	repo := &fakeContextRepo{
		backend: BackendConfig{
			// What configfile.Load defaults to for a workspace that
			// configures no Dolt anything. The whole point is that these
			// values describe Dolt and this workspace is not on it.
			Backend:   "acme",
			DoltMode:  configfile.DoltModeEmbedded,
			Database:  configfile.DefaultDoltDatabase,
			ProjectID: "proj-1",
		},
	}

	info, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}
	if info.Backend != "acme" {
		t.Errorf("Backend = %q, want acme: the projection named a backend this workspace is not on", info.Backend)
	}
	if info.DoltMode != "" {
		t.Errorf("DoltMode = %q, want empty: a registered backend has no Dolt mode, and bd cannot invent one", info.DoltMode)
	}
	if info.Database != "" {
		t.Errorf("Database = %q, want empty: %q is the Dolt default, not a name this backend answers from",
			info.Database, configfile.DefaultDoltDatabase)
	}
	// Everything that is not Dolt-derived still describes the workspace.
	if info.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q, want proj-1", info.ProjectID)
	}
}

func TestContextUseCase_ServerMode(t *testing.T) {
	repo := &fakeContextRepo{
		backend: BackendConfig{
			Backend:      configfile.BackendDolt,
			DoltMode:     configfile.DoltModeServer,
			ServerHost:   "db.example.com",
			IsServerMode: true,
		},
		port: 3307,
	}

	info, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}
	if info.ServerHost != "db.example.com" {
		t.Errorf("ServerHost = %q, want db.example.com", info.ServerHost)
	}
	if info.ServerPort != 3307 {
		t.Errorf("ServerPort = %d, want 3307", info.ServerPort)
	}
	if info.ProxiedDir != "" {
		t.Errorf("ProxiedDir = %q, want empty", info.ProxiedDir)
	}
	if !repo.portCalled {
		t.Error("ServerPort should be queried in server mode")
	}
}

func TestContextUseCase_ProxiedServerMode(t *testing.T) {
	repo := &fakeContextRepo{
		backend: BackendConfig{
			Backend:             configfile.BackendDolt,
			DoltMode:            configfile.DoltModeProxiedServer,
			IsProxiedServerMode: true,
		},
		proxiedRoot: "/repo/.beads/proxieddb",
	}

	info, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}
	if info.ProxiedDir != "/repo/.beads/proxieddb" {
		t.Errorf("ProxiedDir = %q, want /repo/.beads/proxieddb", info.ProxiedDir)
	}
	if info.ServerHost != "" || info.ServerPort != 0 {
		t.Errorf("server fields should be empty in proxied mode: %+v", info)
	}
	if !repo.proxCalled {
		t.Error("ProxiedServerRoot should be queried in proxied-server mode")
	}
	if repo.portCalled {
		t.Error("ServerPort should not be queried in proxied-server mode")
	}
}

func TestContextUseCase_RoleUnset(t *testing.T) {
	repo := &fakeContextRepo{role: "", roleOK: false}

	info, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}
	if info.Role != "" {
		t.Errorf("Role = %q, want empty", info.Role)
	}
}

func TestContextUseCase_RoleNotEmittedWhenNotDetermined(t *testing.T) {
	repo := &fakeContextRepo{role: "contributor", roleOK: false}

	info, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
	if err != nil {
		t.Fatalf("GetContextInfo: %v", err)
	}
	if info.Role != "" {
		t.Errorf("Role = %q, want empty when hasRole is false", info.Role)
	}
}

func TestContextUseCase_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("boom")

	cases := map[string]*fakeContextRepo{
		"repo_context": {pathsErr: sentinel},
		"role":         {roleErr: sentinel},
		"backend":      {backendErr: sentinel},
		"server_port":  {backend: BackendConfig{IsServerMode: true}, portErr: sentinel},
		"proxied_root": {backend: BackendConfig{IsProxiedServerMode: true}, proxiedErr: sentinel},
		"sync_remote":  {syncErr: sentinel},
	}

	for name, repo := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewContextUseCase(repo, "v0").GetContextInfo(context.Background())
			if !errors.Is(err, sentinel) {
				t.Fatalf("expected sentinel error, got %v", err)
			}
		})
	}
}
