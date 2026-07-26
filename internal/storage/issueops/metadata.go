package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// MergeMetadataInTx merges a single key into an issue's metadata JSON within an
// existing transaction: it reads the current metadata, sets metadata[key]=value
// (a raw JSON value, so nested objects/arrays are preserved), and writes the
// whole object back — all on the SAME tx, so a concurrent writer cannot clobber
// a different key between the read and the write (the caller's withRetryTx/commit
// re-runs this on a serialization conflict, re-reading the latest metadata).
// Routes to the issues or wisps table automatically. value must be valid JSON.
//
// The write goes through UpdateIssueInTx, so the merge inherits everything a
// metadata update does — NormalizeMetadataValue, the configured metadata-schema
// validation, the EventUpdated history event with actor attribution, and
// updated_at — behavior-identical to the old cross-tx SlotSet, now atomic.
func MergeMetadataInTx(ctx context.Context, tx DBTX, issueID, key string, value json.RawMessage, actor string) error {
	if !json.Valid(value) {
		return fmt.Errorf("metadata value for key %q on %s is not valid JSON", key, issueID)
	}

	m, err := readMetadataMapInTx(ctx, tx, issueID)
	if err != nil {
		return err
	}
	m[key] = value
	return writeMergedMetadataInTx(ctx, tx, issueID, m, actor)
}

// DeleteMetadataInTx removes a single key from an issue's metadata JSON within an
// existing transaction, reading and writing on the SAME tx so a concurrent merge
// of a different key cannot be clobbered by the delete. Clearing a key that is
// absent (or an issue with no metadata) is a no-op that writes nothing and
// records no event, matching the historical SlotClear. Routes to the issues or
// wisps table automatically. The write goes through UpdateIssueInTx, inheriting
// the same event/validation/normalization the generic update path applies.
func DeleteMetadataInTx(ctx context.Context, tx DBTX, issueID, key, actor string) error {
	m, err := readMetadataMapInTx(ctx, tx, issueID)
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil // key absent (or no metadata) — nothing to clear
	}
	delete(m, key)
	return writeMergedMetadataInTx(ctx, tx, issueID, m, actor)
}

// readMetadataMapInTx reads an issue's metadata column (routed to issues/wisps)
// and unmarshals it into a raw-value map. Existing values are kept as raw JSON so
// they round-trip byte-for-byte. An empty or null metadata column yields a fresh
// map; a missing issue returns a wrapped storage.ErrNotFound (mirroring
// CloseIssueInTx).
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func readMetadataMapInTx(ctx context.Context, tx DBTX, issueID string) (map[string]json.RawMessage, error) {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	issueTable, _, _, _ := WispTableRouting(isWisp)

	var raw sql.NullString
	err := tx.QueryRowContext(ctx,
		fmt.Sprintf("SELECT metadata FROM %s WHERE id = ?", issueTable), issueID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, issueID)
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata for %s: %w", issueID, err)
	}

	m := make(map[string]json.RawMessage)
	if raw.Valid && raw.String != "" && raw.String != "null" {
		if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
			return nil, fmt.Errorf("parse metadata for %s: %w", issueID, err)
		}
	}
	return m, nil
}

// writeMergedMetadataInTx validates the fully-merged metadata blob against the
// configured schema (preserving the check the generic update path runs in the
// store wrapper) and then writes it via UpdateIssueInTx, which records the
// EventUpdated history event, normalizes the value, and bumps updated_at — all
// on the caller's transaction.
func writeMergedMetadataInTx(ctx context.Context, tx DBTX, issueID string, m map[string]json.RawMessage, actor string) error {
	merged, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal metadata for %s: %w", issueID, err)
	}
	if err := ValidateMetadataIfConfigured(json.RawMessage(merged)); err != nil {
		return err
	}
	if _, err := UpdateIssueInTx(ctx, tx, issueID, map[string]interface{}{"metadata": string(merged)}, actor); err != nil {
		return err
	}
	return nil
}
