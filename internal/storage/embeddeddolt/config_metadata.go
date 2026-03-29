package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func (s *EmbeddedDoltStore) SetConfig(ctx context.Context, key, value string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.SetConfigInTx(ctx, tx, key, value)
	})
}

func (s *EmbeddedDoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		value, err = issueops.GetConfigInTx(ctx, tx, key)
		return err
	})
	return value, err
}

func (s *EmbeddedDoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	var result map[string]string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllConfigInTx(ctx, tx)
		return err
	})
	return result, err
}

func (s *EmbeddedDoltStore) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		value, err = issueops.GetMetadataInTx(ctx, tx, key)
		return err
	})
	return value, err
}

func (s *EmbeddedDoltStore) SetMetadata(ctx context.Context, key, value string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.SetMetadataInTx(ctx, tx, key, value)
	})
}

// GetInfraTypes returns the set of infrastructure types that should be routed
// to the wisps table. Reads from DB config "types.infra", falls back to YAML,
// then to hardcoded defaults (agent, rig, role, message).
func (s *EmbeddedDoltStore) GetInfraTypes(ctx context.Context) map[string]bool {
	var result map[string]bool
	if err := s.withConn(ctx, false, func(tx *sql.Tx) error {
		result = issueops.ResolveInfraTypesInTx(ctx, tx)
		return nil
	}); err != nil || result == nil {
		// DB unavailable — fall back to YAML then defaults.
		var typeList []string
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			typeList = yamlTypes
		} else {
			typeList = storage.DefaultInfraTypes()
		}
		result = make(map[string]bool, len(typeList))
		for _, t := range typeList {
			result[t] = true
		}
	}
	return result
}

// IsInfraTypeCtx returns true if the issue type is an infrastructure type.
func (s *EmbeddedDoltStore) IsInfraTypeCtx(ctx context.Context, t types.IssueType) bool {
	return s.GetInfraTypes(ctx)[string(t)]
}
