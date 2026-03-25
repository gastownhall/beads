package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// SetConfig sets a configuration value
func (s *DoltStore) SetConfig(ctx context.Context, key, value string) error {
	if err := s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetConfigInTx(ctx, tx, key, value)
	}); err != nil {
		return err
	}

	// Invalidate caches for keys that affect cached data
	s.cacheMu.Lock()
	switch key {
	case "status.custom":
		s.customStatusCached = false
		s.customStatusCache = nil
		s.customStatusDetailedCache = nil
	case "types.custom":
		s.customTypeCached = false
		s.customTypeCache = nil
	case "types.infra":
		s.infraTypeCached = false
		s.infraTypeCache = nil
	}
	s.cacheMu.Unlock()

	// Rebuild status views when custom statuses change
	if key == "status.custom" {
		if err := s.RebuildStatusViews(ctx); err != nil {
			return fmt.Errorf("failed to rebuild status views: %w", err)
		}
	}

	return nil
}

// GetConfig retrieves a configuration value
func (s *DoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		value, err = issueops.GetConfigInTx(ctx, tx, key)
		return err
	})
	return value, err
}

// GetAllConfig retrieves all configuration values
func (s *DoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	var result map[string]string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.GetAllConfigInTx(ctx, tx)
		return err
	})
	return result, err
}

// DeleteConfig removes a configuration value
func (s *DoltStore) DeleteConfig(ctx context.Context, key string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.DeleteConfigInTx(ctx, tx, key)
	})
}

// SetMetadata sets a metadata value
func (s *DoltStore) SetMetadata(ctx context.Context, key, value string) error {
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.SetMetadataInTx(ctx, tx, key, value)
	})
}

// GetMetadata retrieves a metadata value
func (s *DoltStore) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		value, err = issueops.GetMetadataInTx(ctx, tx, key)
		return err
	})
	return value, err
}

// GetCustomStatuses returns custom status name strings from config (backward-compatible API).
// Callers that need category information should use GetCustomStatusesDetailed instead.
func (s *DoltStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	s.cacheMu.Lock()
	if s.customStatusCached {
		result := s.customStatusCache
		s.cacheMu.Unlock()
		return result, nil
	}
	s.cacheMu.Unlock()

	// Populate via detailed method which handles parsing and fallback
	detailed, err := s.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	return types.CustomStatusNames(detailed), nil
}

// GetCustomStatusesDetailed returns typed custom statuses with category information.
// Falls back to config.yaml if DB config is unavailable.
// On parse errors (malformed config), logs a warning and returns nil (degraded mode).
// Results are cached per DoltStore lifetime and invalidated when SetConfig
// updates the "status.custom" key.
func (s *DoltStore) GetCustomStatusesDetailed(ctx context.Context) ([]types.CustomStatus, error) {
	s.cacheMu.Lock()
	if s.customStatusCached {
		result := s.customStatusDetailedCache
		s.cacheMu.Unlock()
		return result, nil
	}
	s.cacheMu.Unlock()

	var detailed []types.CustomStatus
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		detailed, txErr = issueops.ResolveCustomStatusesDetailedInTx(ctx, tx)
		return txErr
	})
	if err != nil {
		// DB unavailable — fall back to config.yaml before giving up.
		log.Printf("warning: failed to resolve custom statuses: %v. Custom statuses disabled. Fix with: bd config set status.custom \"valid,values\"", err)
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return issueops.ParseStatusFallback(yamlStatuses), nil
		}
		return nil, nil
	}

	s.cacheMu.Lock()
	if !s.customStatusCached {
		s.customStatusDetailedCache = detailed
		s.customStatusCache = types.CustomStatusNames(detailed)
		s.customStatusCached = true
	}
	s.cacheMu.Unlock()

	return detailed, nil
}

// GetCustomTypes returns custom issue type values from config.
// If the database doesn't have custom types configured, falls back to config.yaml.
// This fallback is essential during operations when the database connection is
// temporarily unavailable or when types.custom hasn't been configured yet.
// Returns an empty slice if no custom types are configured.
// Results are cached per DoltStore lifetime and invalidated when SetConfig
// updates the "types.custom" key.
func (s *DoltStore) GetCustomTypes(ctx context.Context) ([]string, error) {
	s.cacheMu.Lock()
	if s.customTypeCached {
		result := s.customTypeCache
		s.cacheMu.Unlock()
		return result, nil
	}
	s.cacheMu.Unlock()

	var result []string
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		result, txErr = issueops.ResolveCustomTypesInTx(ctx, tx)
		return txErr
	})
	if err != nil {
		// DB unavailable — fall back to config.yaml.
		if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			return yamlTypes, nil
		}
		return nil, nil
	}

	s.cacheMu.Lock()
	s.customTypeCache = result
	s.customTypeCached = true
	s.cacheMu.Unlock()

	return result, nil
}

// GetInfraTypes returns infrastructure type names from config.
// Infrastructure types are routed to the wisps table to keep the versioned
// issues table clean. Defaults to ["agent", "rig", "role", "message"] if
// no custom configuration exists.
// Falls back: DB config "types.infra" → config.yaml types.infra → defaults.
// Results are cached per DoltStore lifetime and invalidated when SetConfig
// updates the "types.infra" key.
func (s *DoltStore) GetInfraTypes(ctx context.Context) map[string]bool {
	s.cacheMu.Lock()
	if s.infraTypeCached {
		result := s.infraTypeCache
		s.cacheMu.Unlock()
		return result
	}
	s.cacheMu.Unlock()

	var result map[string]bool
	if err := s.withReadTx(ctx, func(tx *sql.Tx) error {
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

	s.cacheMu.Lock()
	s.infraTypeCache = result
	s.infraTypeCached = true
	s.cacheMu.Unlock()

	return result
}
