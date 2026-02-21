package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
)

// SetConfig sets a configuration value
func (s *DoltStore) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.execContext(ctx, `
		INSERT INTO config (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set config %s: %w", key, err)
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
	}
	s.cacheMu.Unlock()

	return nil
}

// GetConfig retrieves a configuration value
func (s *DoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	var scanErr error

	err := s.withRetry(ctx, func() error {
		scanErr = s.db.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
		return scanErr
	})

	if err == sql.ErrNoRows || scanErr == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get config %s: %w", key, err)
	}
	return value, nil
}

// GetAllConfig retrieves all configuration values
func (s *DoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, "SELECT `key`, value FROM config")
	if err != nil {
		return nil, fmt.Errorf("failed to get all config: %w", err)
	}
	defer rows.Close()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("failed to scan config: %w", err)
		}
		config[key] = value
	}
	return config, rows.Err()
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
	_, err := s.execContext(ctx, `
		INSERT INTO metadata (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set metadata %s: %w", key, err)
	}
	return nil
}

// GetMetadata retrieves a metadata value
func (s *DoltStore) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get metadata %s: %w", key, err)
	}
	return value, nil
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
