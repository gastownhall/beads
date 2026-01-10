package sqlite

import (
	"context"
	"database/sql"
	"strings"
)

// SetConfig sets a configuration value
func (s *SQLiteStorage) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return wrapDBError("set config", err)
}

// GetConfig gets a configuration value
func (s *SQLiteStorage) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapDBError("get config", err)
}

// GetAllConfig gets all configuration key-value pairs
func (s *SQLiteStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return wrapDBError("delete config", err)
}

// OrphanHandling defines how to handle orphan issues during import
type OrphanHandling string

const (
	OrphanStrict    OrphanHandling = "strict"     // Reject imports with orphans
	OrphanResurrect OrphanHandling = "resurrect"  // Auto-resurrect parents from JSONL
	OrphanSkip      OrphanHandling = "skip"       // Skip orphans silently
	OrphanAllow     OrphanHandling = "allow"      // Allow orphans (default)
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return wrapDBError("set metadata", err)
}

// GetMetadata gets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
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
// Returns an empty slice if no custom types are configured.
func (s *SQLiteStorage) GetCustomTypes(ctx context.Context) ([]string, error) {
	value, err := s.GetConfig(ctx, CustomTypeConfigKey)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return parseCommaSeparatedList(value), nil
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
