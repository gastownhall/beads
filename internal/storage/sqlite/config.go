package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// SetConfig sets a configuration value
func (s *SQLiteStorage) SetConfig(ctx context.Context, key, value string) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return wrapDBError("set config", err)
}

// GetConfig gets a configuration value
func (s *SQLiteStorage) GetConfig(ctx context.Context, key string) (string, error) {
	s.checkFreshness()
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, wrapDBError("get config", err)
}

// GetAllConfig gets all configuration key-value pairs
func (s *SQLiteStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	s.checkFreshness()
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, wrapDBError("query all config", err)
	}
	defer func() { _ = rows.Close() }()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, wrapDBError("scan config row", err)
		}
		config[key] = value
	}
	return config, wrapDBError("iterate config rows", rows.Err())
}

// DeleteConfig deletes a configuration value
func (s *SQLiteStorage) DeleteConfig(ctx context.Context, key string) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return wrapDBError("delete config", err)
}

// OrphanHandling is an alias for storage.OrphanHandling for backward compatibility.
// Deprecated: Use storage.OrphanHandling directly.
type OrphanHandling = storage.OrphanHandling

// Orphan handling constants - aliases for storage package constants.
// Deprecated: Use storage.OrphanStrict, etc. directly.
const (
	OrphanStrict    = storage.OrphanStrict    // Reject imports with orphans
	OrphanResurrect = storage.OrphanResurrect // Auto-resurrect parents from JSONL
	OrphanSkip      = storage.OrphanSkip      // Skip orphans silently
	OrphanAllow     = storage.OrphanAllow     // Allow orphans (default)
)

// GetOrphanHandling gets the import.orphan_handling config value
// Returns OrphanAllow (the default) if not set or if value is invalid
func (s *SQLiteStorage) GetOrphanHandling(ctx context.Context) OrphanHandling {
	value, err := s.GetConfig(ctx, "import.orphan_handling")
	if err != nil || value == "" {
		return OrphanAllow // Default
	}

	switch OrphanHandling(value) {
	case OrphanStrict, OrphanResurrect, OrphanSkip, OrphanAllow:
		return OrphanHandling(value)
	default:
		return OrphanAllow // Invalid value, use default
	}
}

// SetMetadata sets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) SetMetadata(ctx context.Context, key, value string) error {
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return wrapDBError("set metadata", err)
}

// GetMetadata gets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	s.checkFreshness()
	// Hold read lock during database operations to prevent reconnect() from
	// closing the connection mid-query (GH#607 race condition fix)
	s.reconnectMu.RLock()
	defer s.reconnectMu.RUnlock()

	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, wrapDBError("get metadata", err)
}

// CustomStatusConfigKey is the config key for custom status states
const CustomStatusConfigKey = "status.custom"

// CustomTypeConfigKey is the config key for custom issue types
const CustomTypeConfigKey = "types.custom"

// GetCustomStatuses retrieves the list of custom status states from config.
// Custom statuses are stored as comma-separated values in the "status.custom" config key.
// Returns an empty slice if no custom statuses are configured.
func (s *SQLiteStorage) GetCustomStatuses(ctx context.Context) ([]string, error) {
	value, err := s.GetConfig(ctx, CustomStatusConfigKey)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCustomStatuses(value), nil
}

// GetCustomTypes retrieves the list of custom issue types from config.
// Custom types are stored as comma-separated values in the "types.custom" config key.
// If the database doesn't have custom types configured, falls back to config.yaml.
// This fallback is essential during bd init when the database is being created
// but auto-import needs to validate issues with custom types (GH#1225).
// Returns an empty slice if no custom types are configured.
func (s *SQLiteStorage) GetCustomTypes(ctx context.Context) ([]string, error) {
	value, err := s.GetConfig(ctx, CustomTypeConfigKey)
	if err != nil {
		return nil, err
	}
	if value != "" {
		return parseCommaSeparatedList(value), nil
	}

	// Fallback to config.yaml when database doesn't have types.custom set.
	// This allows auto-import during bd init to work with custom types
	// defined in config.yaml before they're persisted to the database.
	if yamlTypes := config.GetCustomTypesFromYAML(); len(yamlTypes) > 0 {
		return yamlTypes, nil
	}

	return nil, nil
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

// parseCustomStatuses is an alias for parseCommaSeparatedList for backward compatibility.
func parseCustomStatuses(value string) []string {
	return parseCommaSeparatedList(value)
}
