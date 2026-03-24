package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

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

	value, err := s.GetConfig(ctx, "status.custom")
	if err != nil {
		// On database error, try fallback to config.yaml
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return parseStatusFallback(yamlStatuses), nil
		}
		return nil, err
	}

	var detailed []types.CustomStatus
	if value != "" {
		parsed, parseErr := types.ParseCustomStatusConfig(value)
		if parseErr != nil {
			// Degraded mode: log warning, return empty (CLI remains operable)
			log.Printf("warning: invalid status.custom config: %v. Custom statuses disabled. Fix with: bd config set status.custom \"valid,values\"", parseErr)
			detailed = nil
		} else {
			detailed = parsed
		}
	} else if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
		detailed = parseStatusFallback(yamlStatuses)
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

// parseStatusFallback converts legacy []string status names (from YAML) to []CustomStatus.
// All statuses get CategoryUnspecified since YAML fallback may use flat format.
func parseStatusFallback(names []string) []types.CustomStatus {
	// Try parsing as new format first (YAML might have "name:category" entries)
	joined := strings.Join(names, ",")
	if parsed, err := types.ParseCustomStatusConfig(joined); err == nil {
		return parsed
	}
	// Fall back to treating each as an untyped name
	result := make([]types.CustomStatus, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			result = append(result, types.CustomStatus{Name: name, Category: types.CategoryUnspecified})
		}
	}
	return result
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

	value, err := s.GetConfig(ctx, "types.custom")
	if err != nil {
		// On database error, try fallback to config.yaml
		if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
			return yamlTypes, nil
		}
		return nil, err
	}

	var result []string
	if value != "" {
		result = parseCommaSeparatedList(value)
	} else if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
		result = yamlTypes
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

	var types []string

	value, err := s.GetConfig(ctx, "types.infra")
	if err == nil && value != "" {
		types = parseCommaSeparatedList(value)
	}

	if len(types) == 0 {
		if yamlTypes := config.GetInfraTypesFromYAML(); len(yamlTypes) > 0 {
			types = yamlTypes
		}
	}

	if len(types) == 0 {
		types = storage.DefaultInfraTypes()
	}

	result := make(map[string]bool, len(types))
	for _, t := range types {
		result[t] = true
	}

	s.cacheMu.Lock()
	s.infraTypeCache = result
	s.infraTypeCached = true
	s.cacheMu.Unlock()

	return result
}

// parseCommaSeparatedList splits a comma-separated string into a slice of trimmed entries.
// Empty entries are filtered out.
func parseCommaSeparatedList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
