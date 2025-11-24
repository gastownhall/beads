package sqlite

import (
	"context"
	"database/sql"
)

// SetConfig sets a configuration value
func (s *SQLiteStorage) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// GetConfig gets a configuration value
func (s *SQLiteStorage) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// GetAllConfig gets all configuration key-value pairs
func (s *SQLiteStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	config := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		config[key] = value
	}
	return config, rows.Err()
}

// DeleteConfig deletes a configuration value
func (s *SQLiteStorage) DeleteConfig(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM config WHERE key = ?`, key)
	return err
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
	return err
}

// GetMetadata gets a metadata value (for internal state like import hashes)
func (s *SQLiteStorage) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
