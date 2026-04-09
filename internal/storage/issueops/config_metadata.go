package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetConfigInTx sets a configuration value within an existing transaction.
// Normalizes issue_prefix and checkout_suffix.* by stripping trailing hyphens.
func SetConfigInTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if key == "issue_prefix" || strings.HasPrefix(key, "checkout_suffix") {
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

// CheckoutSuffixKey returns the config key for a checkout suffix.
func CheckoutSuffixKey(checkoutID string) string {
	if checkoutID != "" {
		return "checkout_suffix." + checkoutID
	}
	return "checkout_suffix"
}

// SetCheckoutSuffixInTx sets the checkout_suffix.<checkoutID> config key
// within an existing transaction. No-op when suffix is empty.
func SetCheckoutSuffixInTx(ctx context.Context, tx *sql.Tx, checkoutID, suffix string) error {
	if suffix == "" {
		return nil
	}
	return SetConfigInTx(ctx, tx, CheckoutSuffixKey(checkoutID), suffix)
}

// SetCheckoutSuffixAndCommit sets the checkout suffix and creates a dolt
// commit in a single transaction. For use when only a raw *sql.DB is
// available (e.g., embedded bootstrap sync path where no store is open).
func SetCheckoutSuffixAndCommit(ctx context.Context, db *sql.DB, checkoutID, suffix, commitMsg string) error {
	if suffix == "" {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := SetCheckoutSuffixInTx(ctx, tx, checkoutID, suffix); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		return fmt.Errorf("dolt add: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?)", commitMsg); err != nil {
		return fmt.Errorf("dolt commit: %w", err)
	}
	return tx.Commit()
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
