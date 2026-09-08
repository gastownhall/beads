package domain

import (
	"context"
	"fmt"
	"time"
)

type BootstrapUseCase interface {
	BootstrapProject(ctx context.Context, params BootstrapProjectParams) (BootstrapProjectResult, error)
}

type BootstrapProjectParams struct {
	Prefix         string
	ProjectID      string
	BdVersion      string
	LastImportTime time.Time

	RepoID  string
	CloneID string

	RemoteName string
	RemoteURL  string
}

type BootstrapProjectResult struct {
	RemoteConfigured bool
}

func NewBootstrapUseCase(cfgRepo ConfigSQLRepository, remoteUseCase DoltRemoteUseCase) BootstrapUseCase {
	return &bootstrapUseCaseImpl{
		cfgRepo:       cfgRepo,
		remoteUseCase: remoteUseCase,
	}
}

type bootstrapUseCaseImpl struct {
	cfgRepo       ConfigSQLRepository
	remoteUseCase DoltRemoteUseCase
}

var _ BootstrapUseCase = (*bootstrapUseCaseImpl)(nil)

func (u *bootstrapUseCaseImpl) BootstrapProject(ctx context.Context, params BootstrapProjectParams) (BootstrapProjectResult, error) {
	if params.Prefix == "" {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: Prefix must not be empty")
	}
	if params.ProjectID == "" {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: ProjectID must not be empty")
	}
	if params.BdVersion == "" {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: BdVersion must not be empty")
	}
	if params.LastImportTime.IsZero() {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: LastImportTime must not be zero")
	}

	if err := u.cfgRepo.SetConfig(ctx, "issue_prefix", params.Prefix); err != nil {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set issue_prefix: %w", err)
	}
	if err := u.cfgRepo.SetMetadata(ctx, "_project_id", params.ProjectID); err != nil {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set _project_id: %w", err)
	}
	if params.RepoID != "" {
		if err := u.cfgRepo.SetMetadata(ctx, "repo_id", params.RepoID); err != nil {
			return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set repo_id: %w", err)
		}
	}
	if params.CloneID != "" {
		if err := u.cfgRepo.SetMetadata(ctx, "clone_id", params.CloneID); err != nil {
			return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set clone_id: %w", err)
		}
	}
	if err := u.cfgRepo.SetMetadata(ctx, "last_import_time", params.LastImportTime.UTC().Format(time.RFC3339)); err != nil {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set last_import_time: %w", err)
	}
	if err := u.cfgRepo.SetLocalMetadata(ctx, "bd_version", params.BdVersion); err != nil {
		return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: set bd_version: %w", err)
	}

	var result BootstrapProjectResult
	if params.RemoteURL != "" {
		name := params.RemoteName
		if name == "" {
			name = "origin"
		}
		remotes, err := u.remoteUseCase.ListRemotes(ctx)
		if err != nil {
			return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: list remotes: %w", err)
		}
		exists := false
		for _, r := range remotes {
			if r.Name == name {
				exists = true
				break
			}
		}
		if !exists {
			if err := u.remoteUseCase.CreateRemote(ctx, name, params.RemoteURL); err != nil {
				return BootstrapProjectResult{}, fmt.Errorf("BootstrapProject: create remote %s: %w", name, err)
			}
			result.RemoteConfigured = true
		}
	}

	return result, nil
}
