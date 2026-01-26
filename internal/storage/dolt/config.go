package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetConfig sets a configuration value
func (s *DoltStore) SetConfig(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO config (` + "`key`" + `, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	if err != nil {
		return fmt.Errorf("failed to set config %s: %w", key, err)
	}
	return nil
}

// GetConfig retrieves a configuration value
func (s *DoltStore) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get config %s: %w", key, err)
	}
	return value, nil
}

// GetAllConfig retrieves all configuration values
func (s *DoltStore) GetAllConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT `key`, value FROM config")
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
	_, err := s.db.ExecContext(ctx, "DELETE FROM config WHERE `key` = ?", key)
	if err != nil {
		return fmt.Errorf("failed to delete config %s: %w", key, err)
	}
	return nil
}

// SetMetadata sets a metadata value
func (s *DoltStore) SetMetadata(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metadata (` + "`key`" + `, value) VALUES (?, ?)
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

// GetCustomStatuses returns custom status values from config
func (s *DoltStore) GetCustomStatuses(ctx context.Context) ([]string, error) {
	value, err := s.GetConfig(ctx, "status.custom")
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return strings.Split(value, ","), nil
}

// GetCustomTypes returns custom issue type values from config
func (s *DoltStore) GetCustomTypes(ctx context.Context) ([]string, error) {
	value, err := s.GetConfig(ctx, "types.custom")
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	return strings.Split(value, ","), nil
}
