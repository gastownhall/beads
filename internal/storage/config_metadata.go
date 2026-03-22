package storage

import (
	"context"

	"github.com/steveyegge/beads/internal/types"
)

// ConfigMetadataStore provides extended config, metadata, and type introspection.
type ConfigMetadataStore interface {
	GetMetadata(ctx context.Context, key string) (string, error)
	SetMetadata(ctx context.Context, key, value string) error
	DeleteConfig(ctx context.Context, key string) error
	GetCustomStatuses(ctx context.Context) ([]string, error)
	GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error)
	GetCustomTypes(ctx context.Context) ([]string, error)
	GetInfraTypes(ctx context.Context) map[string]bool
	IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool
}
