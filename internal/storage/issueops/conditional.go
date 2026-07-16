package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// metadataJSONPath renders a MySQL/Dolt JSON path that references a single
// TOP-LEVEL metadata member by name. The member name is double-quoted so keys
// containing '.' (e.g. "gc.control_epoch") reference a flat key rather than a
// nested path, and embedded quotes/backslashes are escaped.
func metadataJSONPath(key string) string {
	esc := strings.ReplaceAll(key, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `$."` + esc + `"`
}

// validateMetadataKey rejects keys that cannot be safely embedded in a JSON path
// or that would be ambiguous. Control characters are rejected outright.
func validateMetadataKey(key string) error {
	if key == "" {
		return fmt.Errorf("metadata key must not be empty")
	}
	for _, r := range key {
		if r < 0x20 {
			return fmt.Errorf("metadata key %q contains a control character", key)
		}
	}
	return nil
}

// CompareAndSetMetadataKeyInTx atomically sets a single metadata key iff its
// current value matches expected, in a single guarded UPDATE. A nil expected
// means the key must currently be absent (claim-once); a non-nil expected means
// the key's current string value must equal *expected. The value is stored as a
// JSON string (matching SlotSet semantics).
//
// It is the sound compare-and-swap primitive: two concurrent writers necessarily
// write different values to the one metadata blob cell, so Dolt's cell-wise
// transaction merge forces a serialization conflict on all but one — no per-write
// nonce is required (verified against real Dolt). On a guard miss it re-reads the
// current value in the SAME transaction and returns a *storage.PreconditionFailedError.
//
// Routes to the correct table (issues/wisps) automatically. The caller owns the
// transaction and any Dolt versioning (DOLT_ADD/COMMIT).
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func CompareAndSetMetadataKeyInTx(ctx context.Context, tx DBTX, id, key string, expected *string, newValue, actor string) error {
	if err := validateMetadataKey(key); err != nil {
		return err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Confirm the row exists (so an absent bead is ErrNotFound, not a silent
	// no-op guard miss).
	if _, err := GetIssueInTx(ctx, tx, id); err != nil {
		return err
	}

	path := metadataJSONPath(key)
	// A metadata CAS also stamps a fresh whole-row revision nonce (B1.2) so it
	// invalidates any outstanding whole-row CAS token held against this bead.
	rev := NewRevision()
	setExpr := fmt.Sprintf(
		"metadata = JSON_SET(COALESCE(metadata, JSON_OBJECT()), %s, ?), updated_at = CURRENT_TIMESTAMP, revision = ?",
		"?")

	var (
		res sql.Result
		err error
	)
	if expected == nil {
		//nolint:gosec // G201: table name from WispTableRouting constants
		res, err = tx.ExecContext(ctx, fmt.Sprintf(
			"UPDATE %s SET %s WHERE id = ? AND JSON_EXTRACT(metadata, %s) IS NULL",
			issueTable, setExpr, "?"),
			path, newValue, rev, id, path)
	} else {
		//nolint:gosec // G201: table name from WispTableRouting constants
		res, err = tx.ExecContext(ctx, fmt.Sprintf(
			"UPDATE %s SET %s WHERE id = ? AND JSON_UNQUOTE(JSON_EXTRACT(metadata, %s)) = ?",
			issueTable, setExpr, "?"),
			path, newValue, rev, id, path, *expected)
	}
	if err != nil {
		return fmt.Errorf("compare-and-set metadata key %q on %s: %w", key, id, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for CAS on %s: %w", id, err)
	}
	if n == 0 {
		return newMetadataPreconditionError(ctx, tx, issueTable, id, key, path, expected)
	}

	// Record the update event (SlotSet parity).
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventUpdated, actor, "", newValue); err != nil {
		return fmt.Errorf("recording CAS event for %s: %w", id, err)
	}
	return nil
}

// CompareAndClearMetadataKeyInTx atomically REMOVES a single metadata key iff
// its current value equals expected, in a single guarded UPDATE. It is the
// symmetric release for CompareAndSetMetadataKeyInTx's claim (e.g. releasing a
// reservation or lease you hold).
//
// It is idempotent when the key is already absent: a guard miss that re-reads to
// "absent" returns success (you wanted it gone and it is), while a key held by a
// different value returns a *storage.PreconditionFailedError.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func CompareAndClearMetadataKeyInTx(ctx context.Context, tx DBTX, id, key, expected, actor string) error {
	if err := validateMetadataKey(key); err != nil {
		return err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)
	if _, err := GetIssueInTx(ctx, tx, id); err != nil {
		return err
	}
	path := metadataJSONPath(key)
	rev := NewRevision()
	//nolint:gosec // G201: table name from WispTableRouting constants
	res, err := tx.ExecContext(ctx, fmt.Sprintf(
		"UPDATE %s SET metadata = JSON_REMOVE(COALESCE(metadata, JSON_OBJECT()), %s), updated_at = CURRENT_TIMESTAMP, revision = ? WHERE id = ? AND JSON_UNQUOTE(JSON_EXTRACT(metadata, %s)) = ?",
		issueTable, "?", "?"),
		path, rev, id, path, expected)
	if err != nil {
		return fmt.Errorf("compare-and-clear metadata key %q on %s: %w", key, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for compare-and-clear on %s: %w", id, err)
	}
	if n == 0 {
		// Guard missed. If the key is already absent, treat the release as an
		// idempotent success; otherwise it is held by a different value.
		expPtr := &expected
		errOut := newMetadataPreconditionError(ctx, tx, issueTable, id, key, path, expPtr)
		var pfe *storage.PreconditionFailedError
		if errors.As(errOut, &pfe) && pfe.CurrentValue == nil {
			return nil // already released
		}
		return errOut
	}
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventUpdated, actor, expected, ""); err != nil {
		return fmt.Errorf("recording CAS-clear event for %s: %w", id, err)
	}
	return nil
}

// SetMetadataKeyInTx sets a single metadata key unconditionally, in one guarded
// UPDATE (atomic single-statement — never a read-merge-write). This is the
// replacement for the old cross-transaction SlotSet blob rewrite: it cannot clobber
// a concurrently CAS-written sibling key beyond the one it targets.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func SetMetadataKeyInTx(ctx context.Context, tx DBTX, id, key, value, actor string) error {
	if err := validateMetadataKey(key); err != nil {
		return err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)
	if _, err := GetIssueInTx(ctx, tx, id); err != nil {
		return err
	}
	path := metadataJSONPath(key)
	rev := NewRevision()
	//nolint:gosec // G201: table name from WispTableRouting constants
	res, err := tx.ExecContext(ctx, fmt.Sprintf(
		"UPDATE %s SET metadata = JSON_SET(COALESCE(metadata, JSON_OBJECT()), %s, ?), updated_at = CURRENT_TIMESTAMP, revision = ? WHERE id = ?",
		issueTable, "?"),
		path, value, rev, id)
	if err != nil {
		return fmt.Errorf("set metadata key %q on %s: %w", key, id, err)
	}
	if _, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected for set metadata on %s: %w", id, err)
	}
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventUpdated, actor, "", value); err != nil {
		return fmt.Errorf("recording metadata event for %s: %w", id, err)
	}
	return nil
}

// ClearMetadataKeyInTx removes a single metadata key unconditionally, in one
// guarded UPDATE (atomic single-statement). Clearing an absent key is a no-op.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func ClearMetadataKeyInTx(ctx context.Context, tx DBTX, id, key, actor string) error {
	if err := validateMetadataKey(key); err != nil {
		return err
	}
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)
	if _, err := GetIssueInTx(ctx, tx, id); err != nil {
		return err
	}
	path := metadataJSONPath(key)
	rev := NewRevision()
	//nolint:gosec // G201: table name from WispTableRouting constants
	res, err := tx.ExecContext(ctx, fmt.Sprintf(
		"UPDATE %s SET metadata = JSON_REMOVE(COALESCE(metadata, JSON_OBJECT()), %s), updated_at = CURRENT_TIMESTAMP, revision = ? WHERE id = ? AND JSON_EXTRACT(metadata, %s) IS NOT NULL",
		issueTable, "?", "?"),
		path, rev, id, path)
	if err != nil {
		return fmt.Errorf("clear metadata key %q on %s: %w", key, id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for clear metadata on %s: %w", id, err)
	}
	if n == 0 {
		return nil // key was absent — nothing to clear
	}
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, types.EventUpdated, actor, "", ""); err != nil {
		return fmt.Errorf("recording metadata clear event for %s: %w", id, err)
	}
	return nil
}

// newMetadataPreconditionError re-reads the current metadata value (and the full
// row) inside the same transaction so the caller sees the state it lost to.
//
//nolint:gosec // G201: table name comes from WispTableRouting (hardcoded constants)
func newMetadataPreconditionError(ctx context.Context, tx DBTX, issueTable, id, key, path string, expected *string) error {
	var cur sql.NullString
	//nolint:gosec // G201: table name from WispTableRouting constants
	row := tx.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT JSON_UNQUOTE(JSON_EXTRACT(metadata, %s)) FROM %s WHERE id = ?", "?", issueTable),
		path, id)
	if err := row.Scan(&cur); err != nil {
		return fmt.Errorf("reading current metadata for %s after CAS miss: %w", id, err)
	}
	var current *string
	if cur.Valid {
		v := cur.String
		current = &v
	}
	currentIssue, _ := GetIssueInTx(ctx, tx, id)
	return &storage.PreconditionFailedError{
		ID:            id,
		Key:           key,
		ExpectedValue: expected,
		CurrentValue:  current,
		CurrentIssue:  currentIssue,
	}
}
