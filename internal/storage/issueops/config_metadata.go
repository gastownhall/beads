package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetConfigInTx sets a configuration value within an existing transaction.
// Normalizes issue_prefix by stripping trailing hyphens.
func SetConfigInTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if key == "issue_prefix" {
		value = strings.TrimSuffix(value, "-")
	}
	_, err := tx.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES (?, ?)", key, value)
	if err != nil {
		return fmt.Errorf("set config %s: %w", key, err)
	}
	return nil
}

// GetConfigInTx retrieves a configuration value within an existing transaction.
// Returns ("", nil) if the key does not exist.
func GetConfigInTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get config %s: %w", key, err)
	}
	return value, nil
}

// GetAllConfigInTx retrieves all configuration key-value pairs within an existing transaction.
func GetAllConfigInTx(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT `key`, value FROM config")
	if err != nil {
		return nil, fmt.Errorf("get all config: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("get all config: scan: %w", err)
		}
		result[k] = v
	}
	return result, rows.Err()
}

// SetMetadataInTx sets a metadata value within an existing transaction.
func SetMetadataInTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value)
	if err != nil {
		return fmt.Errorf("set metadata %s: %w", key, err)
	}
	return nil
}

// GetMetadataInTx retrieves a metadata value within an existing transaction.
// Returns ("", nil) if the key does not exist.
func GetMetadataInTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get metadata %s: %w", key, err)
	}
	return value, nil
}

// SetLocalMetadataInTx sets a value in the dolt-ignored local_metadata table
// within an existing transaction. Used for clone-local state that should not
// generate merge conflicts (tip timestamps, version stamps, sync cursors).
func SetLocalMetadataInTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", key, value)
	if err != nil {
		return fmt.Errorf("set local metadata %s: %w", key, err)
	}
	return nil
}

// GetLocalMetadataInTx retrieves a value from the dolt-ignored local_metadata
// table within an existing transaction. Returns ("", nil) if the key does not exist.
func GetLocalMetadataInTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM local_metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get local metadata %s: %w", key, err)
	}
	return value, nil
}
