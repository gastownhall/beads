package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
)

// SlotSetInTx sets key=value in the issue's Metadata JSON object within a transaction.
// Creates the metadata object if it doesn't exist. Routes to wisps or issues table.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func SlotSetInTx(ctx context.Context, tx *sql.Tx, id, key, value string) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	path := storage.JSONMetadataPath(key)
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET metadata = JSON_SET(COALESCE(metadata, '{}'), ?, ?), updated_at = ? WHERE id = ?", issueTable),
		path, value, time.Now().UTC(), id)
	return err
}

// SlotGetInTx reads a key from the issue's Metadata JSON object within a transaction.
// Returns (value, true, nil) when the key exists, ("", false, nil) when absent.
// Returns ("", false, storage.ErrNotFound) when the issue itself does not exist.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func SlotGetInTx(ctx context.Context, tx *sql.Tx, id, key string) (string, bool, error) {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	path := storage.JSONMetadataPath(key)
	var val sql.NullString
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) FROM %s WHERE id = ?", issueTable),
		path, id).Scan(&val)
	if err == sql.ErrNoRows {
		return "", false, fmt.Errorf("issue %s: %w", id, storage.ErrNotFound)
	}
	if err != nil {
		return "", false, err
	}
	if !val.Valid {
		return "", false, nil
	}
	return val.String, true, nil
}

// SlotClearInTx removes a key from the issue's Metadata JSON object within a transaction.
// No-op if the key is absent. Routes to wisps or issues table.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func SlotClearInTx(ctx context.Context, tx *sql.Tx, id, key string) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, _, _ := WispTableRouting(isWisp)
	path := storage.JSONMetadataPath(key)
	_, err := tx.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET metadata = JSON_REMOVE(metadata, ?), updated_at = ? WHERE id = ?", issueTable),
		path, time.Now().UTC(), id)
	return err
}
