package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/issueops"
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
	case "types.custom":
		s.customTypeCached = false
		s.customTypeCache = nil
	case "types.infra":
		s.infraTypeCached = false
		s.infraTypeCache = nil
	}
	s.cacheMu.Unlock()

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
	_, err := s.execContext(ctx, "DELETE FROM config WHERE `key` = ?", key)
	if err != nil {
		return fmt.Errorf("failed to delete config %s: %w", key, err)
	}
	return nil
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

// GetCustomStatuses returns custom status values from config.
// If the database doesn't have custom statuses configured, falls back to config.yaml.
// Returns an empty slice if no custom statuses are configured.
// Results are cached per DoltStore lifetime and invalidated when SetConfig
// updates the "status.custom" key.
func (s *DoltStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	s.cacheMu.Lock()
	if s.customStatusCached {
		result := s.customStatusCache
		s.cacheMu.Unlock()
		return result, nil
	}
	s.cacheMu.Unlock()

	value, err := s.GetConfig(ctx, "status.custom")
	if err != nil {
		// On database error, try fallback to config.yaml
		if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
			return yamlStatuses, nil
		}
		return nil, err
	}

	var result []string
	if value != "" {
		result = parseCommaSeparatedList(value)
	} else if yamlStatuses := config.GetCustomStatusesFromYAML(); len(yamlStatuses) > 0 {
		result = yamlStatuses
	}

	s.cacheMu.Lock()
	s.customStatusCache = result
	s.customStatusCached = true
	s.cacheMu.Unlock()

	return result, nil
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
		types = defaultInfraTypes
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
