package domain

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

type ConfigSQLRepository interface {
	GetMetadata(ctx context.Context, key string) (string, error)
	SetMetadata(ctx context.Context, key, value string) error
	SetLocalMetadata(ctx context.Context, key, value string) error
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
	DeleteConfig(ctx context.Context, key string) error
	GetAllConfig(ctx context.Context) (map[string]string, error)

	GetCustomTypes(ctx context.Context) ([]string, error)
	GetAllowedPrefixes(ctx context.Context) (string, error)
	GetAdaptiveIDConfig(ctx context.Context) (AdaptiveIDConfig, error)

	GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error)
	ListAllStatusNames(ctx context.Context) ([]string, error)
	GetInfraTypes(ctx context.Context) (map[string]bool, error)
}

type ConfigUseCase interface {
	VerifyInit(ctx context.Context) (VerifyResult, error)
	GetCustomTypes(ctx context.Context) ([]string, error)
	LoadCreateContext(ctx context.Context) (CreateContext, error)

	GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error)
	ListAllStatusNames(ctx context.Context) ([]string, error)
	GetInfraTypes(ctx context.Context) (map[string]bool, error)
	IsInfraTypeCtx(ctx context.Context, t types.IssueType) (bool, error)

	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
	DeleteConfig(ctx context.Context, key string) error
	GetAllConfig(ctx context.Context) (map[string]string, error)
}

// CreateContext bundles the read-only config inputs that bd create needs
// before inserting an issue. Returned by ConfigUseCase.LoadCreateContext in
// a single round trip to keep the proxied-server path cheap.
type CreateContext struct {
	IssuePrefix     string
	AllowedPrefixes string
	CustomTypes     []string
}

type Issue struct{}

type BatchCreateOptions struct{}

type GlobalDatabaseParams struct{}

type ImportResult struct{}

type VerifyResult struct {
	ProjectID   string
	IssuePrefix string
	Missing     []string
}

func NewConfigUseCase(cfgRepo ConfigSQLRepository) ConfigUseCase {
	return &configUseCaseImpl{cfgRepo: cfgRepo}
}

type configUseCaseImpl struct {
	cfgRepo ConfigSQLRepository
}

var _ ConfigUseCase = (*configUseCaseImpl)(nil)

func (u *configUseCaseImpl) VerifyInit(ctx context.Context) (VerifyResult, error) {
	projectID, err := u.cfgRepo.GetMetadata(ctx, "_project_id")
	if err != nil {
		return VerifyResult{}, fmt.Errorf("VerifyInit: read _project_id: %w", err)
	}
	issuePrefix, err := u.cfgRepo.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return VerifyResult{}, fmt.Errorf("VerifyInit: read issue_prefix: %w", err)
	}

	var missing []string
	if projectID == "" {
		missing = append(missing, "metadata._project_id")
	}
	if issuePrefix == "" {
		missing = append(missing, "config.issue_prefix")
	}

	return VerifyResult{
		ProjectID:   projectID,
		IssuePrefix: issuePrefix,
		Missing:     missing,
	}, nil
}

func (u *configUseCaseImpl) GetCustomTypes(ctx context.Context) ([]string, error) {
	out, err := u.cfgRepo.GetCustomTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetCustomTypes: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error) {
	out, err := u.cfgRepo.GetCustomStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetCustomStatuses: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) ListAllStatusNames(ctx context.Context) ([]string, error) {
	out, err := u.cfgRepo.ListAllStatusNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListAllStatusNames: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) GetInfraTypes(ctx context.Context) (map[string]bool, error) {
	out, err := u.cfgRepo.GetInfraTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetInfraTypes: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) IsInfraTypeCtx(ctx context.Context, t types.IssueType) (bool, error) {
	infra, err := u.GetInfraTypes(ctx)
	if err != nil {
		return false, err
	}
	return infra[string(t)], nil
}

func (u *configUseCaseImpl) GetConfig(ctx context.Context, key string) (string, error) {
	out, err := u.cfgRepo.GetConfig(ctx, key)
	if err != nil {
		return "", fmt.Errorf("GetConfig: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) SetConfig(ctx context.Context, key, value string) error {
	if err := u.cfgRepo.SetConfig(ctx, key, value); err != nil {
		return fmt.Errorf("SetConfig: %w", err)
	}
	return nil
}

func (u *configUseCaseImpl) DeleteConfig(ctx context.Context, key string) error {
	if err := u.cfgRepo.DeleteConfig(ctx, key); err != nil {
		return fmt.Errorf("DeleteConfig: %w", err)
	}
	return nil
}

func (u *configUseCaseImpl) GetAllConfig(ctx context.Context) (map[string]string, error) {
	out, err := u.cfgRepo.GetAllConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetAllConfig: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) LoadCreateContext(ctx context.Context) (CreateContext, error) {
	prefix, err := u.cfgRepo.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return CreateContext{}, fmt.Errorf("LoadCreateContext: read issue_prefix: %w", err)
	}
	allowed, err := u.cfgRepo.GetAllowedPrefixes(ctx)
	if err != nil {
		return CreateContext{}, fmt.Errorf("LoadCreateContext: read allowed_prefixes: %w", err)
	}
	customTypes, err := u.cfgRepo.GetCustomTypes(ctx)
	if err != nil {
		return CreateContext{}, fmt.Errorf("LoadCreateContext: read custom types: %w", err)
	}
	return CreateContext{
		IssuePrefix:     prefix,
		AllowedPrefixes: allowed,
		CustomTypes:     customTypes,
	}, nil
}
