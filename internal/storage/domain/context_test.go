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

func TestContextUseCase_ServerMode(t *testing.T) {
	repo := &fakeContextRepo{
		backend: BackendConfig{
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
