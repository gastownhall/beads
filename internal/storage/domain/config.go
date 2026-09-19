package domain

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

type ConfigSQLRepository interface {
	GetMetadata(ctx context.Context, key string) (string, error)
	SetMetadata(ctx context.Context, key, value string) error
	GetLocalMetadata(ctx context.Context, key string) (string, error)
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
	GetMetadata(ctx context.Context, key string) (string, error)
	// SetMetadata writes one durable metadata value inside the caller's unit of
	// work. It is the write half of the GetMetadata above it:
	// issueops.Bootstrapper's unit-of-work body reads the identity, refuses over
	// an identified substrate and writes the new one in ONE transaction, and a
	// refusal decided outside the transaction that writes is a refusal two
	// racing inits both pass. The identity's keys are named once, in
	// internal/workapi; this seam moves bytes.
	SetMetadata(ctx context.Context, key, value string) error
	GetLocalMetadata(ctx context.Context, key string) (string, error)
	// SetLocalMetadata writes one clone-local, dolt-ignored value inside the
	// caller's unit of work. It is the write half of GetLocalMetadata, and it
	// exists because issueops.VersionReconciler's unit-of-work body reads its
	// two markers, decides through the shared planner in internal/workapi and
	// writes them back — all in ONE transaction, which is what makes the read
	// it qualifies and the write it plans see the same snapshot.
	//
	// The DECISION is deliberately not here: both routes plan through
	// workapi.PlanVersionReconcile, so the rule for which version may overwrite
	// which is stated once.
	SetLocalMetadata(ctx context.Context, key, value string) error
}

// CreateContext bundles the read-only config inputs that bd create needs
// before inserting an issue. Returned by ConfigUseCase.LoadCreateContext in
// a single round trip to keep the proxied-server path cheap.
type CreateContext struct {
	IssuePrefix     string
	AllowedPrefixes string
	CustomTypes     []string
	CustomStatuses  []types.CustomStatus
	// InfraTypes is the resolved infrastructure-type set. A create whose type
	// is in this set is routed to the wisp tables, the same routing the
	// embedded and direct stores apply from IsInfraTypeCtx.
	InfraTypes map[string]bool
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
	// The repo returns only the DB `types.infra` config; an unset/empty value
	// yields an empty map. Embedded resolves the same way but then falls back to
	// config.yaml and finally the hardcoded defaults (["agent","role","message"])
	// via issueops.ResolveInfraTypesInTx / DoltStore.GetInfraTypes. Reproduce that
	// fallback here so `-t message` auto-routes to ephemeral on this seam exactly
	// as on the embedded store, instead of being treated as a plain type (#4547 F-3).
	if len(out) == 0 {
		typeList := config.GetInfraTypesFromYAML()
		if len(typeList) == 0 {
			typeList = DefaultInfraTypes()
		}
		out = make(map[string]bool, len(typeList))
		for _, t := range typeList {
			out[t] = true
		}
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

func (u *configUseCaseImpl) GetMetadata(ctx context.Context, key string) (string, error) {
	out, err := u.cfgRepo.GetMetadata(ctx, key)
	if err != nil {
		return "", fmt.Errorf("GetMetadata: %w", err)
	}
	return out, nil
}

func (u *configUseCaseImpl) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	out, err := u.cfgRepo.GetLocalMetadata(ctx, key)
	if err != nil {
		return "", fmt.Errorf("GetLocalMetadata: %w", err)
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

func (u *configUseCaseImpl) SetMetadata(ctx context.Context, key, value string) error {
	if err := u.cfgRepo.SetMetadata(ctx, key, value); err != nil {
		return fmt.Errorf("SetMetadata: %w", err)
	}
	return nil
}

func (u *configUseCaseImpl) SetLocalMetadata(ctx context.Context, key, value string) error {
	if err := u.cfgRepo.SetLocalMetadata(ctx, key, value); err != nil {
		return fmt.Errorf("SetLocalMetadata: %w", err)
	}
	return nil
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
	customStatuses, err := u.cfgRepo.GetCustomStatuses(ctx)
	if err != nil {
		return CreateContext{}, fmt.Errorf("LoadCreateContext: read custom statuses: %w", err)
	}
	infraTypes, err := u.GetInfraTypes(ctx)
	if err != nil {
		return CreateContext{}, fmt.Errorf("LoadCreateContext: read infra types: %w", err)
	}
	return CreateContext{
		IssuePrefix:     prefix,
		AllowedPrefixes: allowed,
		CustomTypes:     customTypes,
		CustomStatuses:  customStatuses,
		InfraTypes:      infraTypes,
	}, nil
}
