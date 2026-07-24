package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// MergeMetadata merges a single key into an issue's metadata JSON atomically.
// value is a raw JSON value, so nested objects/arrays are preserved (not
// stringified). Unlike the old read-then-write SlotSet, the read and write share
// ONE transaction: on a Dolt optimistic-commit conflict (MySQL 1213/1205,
// guaranteed server-side rollback) withRetryTx re-runs the whole body and
// re-reads the now-committed metadata, so a concurrent merge of a DIFFERENT key
// is preserved rather than clobbered. Dolt has no real row locking — FOR UPDATE
// / SKIP LOCKED are parse-only no-ops — so retry is the only safety net. The
// write routes through UpdateIssueInTx, so the merge records an EventUpdated
// history event (attributed to actor) and runs the configured metadata-schema
// validation, exactly as the old SlotSet did — now atomic. SlotSet is built on
// this. Routes ephemeral IDs to the wisps table (no DOLT_COMMIT); permanent
// issues get a Dolt commit.
func (s *DoltStore) MergeMetadata(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, issueID) {
		return s.mergeMetadataWisp(ctx, issueID, key, value, actor)
	}

	// withRetryTx owns BeginTx and the final Commit. The read+merge+write inside
	// the fn is a single transaction; the retry is what fixes the cross-tx
	// clobber the old SlotSet suffered from.
	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.MergeMetadataInTx(ctx, tx, issueID, key, value, actor); err != nil {
			return err
		}

		// Dolt versioning for permanent issues. The merge routes through
		// UpdateIssueInTx, which also writes an EventUpdated row into events, so
		// stage both tables before committing (mirrors CloseIssue).
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: merge metadata %s.%s", issueID, key)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// mergeMetadataWisp merges a metadata key on a wisp. Mirrors closeWisp: no Dolt
// versioning since wisps live in dolt_ignored tables. The read and write still
// share the one transaction, so the atomic-merge property holds.
func (s *DoltStore) mergeMetadataWisp(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.MergeMetadataInTx(ctx, tx, issueID, key, value, actor); err != nil {
		return err
	}
	return wrapTransactionError("commit merge metadata wisp", tx.Commit())
}

// SlotSet sets a key-value pair in the issue's metadata JSON.
// If the issue has no metadata, a new JSON object is created.
// If the key already exists, its value is overwritten.
//
// Built on MergeMetadata so the read-modify-write is atomic: two concurrent
// SlotSet calls on different keys both survive. The string value is stored as a
// JSON string (json.Marshal(value) yields "value"), keeping the stored metadata
// byte-compatible with the historical whole-metadata rewrite.
func (s *DoltStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling slot value for %s.%s: %w", issueID, key, err)
	}
	return s.MergeMetadata(ctx, issueID, key, raw, actor)
}

// SlotGet retrieves the value of a metadata key from an issue.
// Returns an error if the issue has no metadata or the key is not found.
func (s *DoltStore) SlotGet(ctx context.Context, issueID, key string) (string, error) {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return "", fmt.Errorf("getting issue %s: %w", issueID, err)
	}

	if len(issue.Metadata) == 0 {
		return "", fmt.Errorf("no slot %q on %s: no metadata", key, issueID)
	}

	metadata := make(map[string]interface{})
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		return "", fmt.Errorf("parsing metadata for %s: %w", issueID, err)
	}

	val, ok := metadata[key]
	if !ok {
		return "", fmt.Errorf("no slot %q on %s: key not found", key, issueID)
	}

	switch v := val.(type) {
	case string:
		return v, nil
	default:
		// Non-string values are returned as JSON
		raw, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshaling slot value for %s.%s: %w", issueID, key, err)
		}
		return string(raw), nil
	}
}

// SlotClear removes a metadata key from an issue.
// It is not an error to clear a key that doesn't exist.
//
// Built on DeleteMetadataInTx so the read-modify-write is atomic, exactly like
// MergeMetadata: a concurrent SlotClear (or SlotSet of a different key) can no
// longer clobber this write between the read and the write. Clearing an absent
// key is a no-op that writes nothing.
func (s *DoltStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	// Route ephemeral IDs to wisps table (falls through for promoted wisps).
	// Wisps skip DOLT_COMMIT since they live in dolt_ignored tables.
	if s.isActiveWisp(ctx, issueID) {
		return s.clearMetadataWisp(ctx, issueID, key, actor)
	}

	return s.withRetryTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.DeleteMetadataInTx(ctx, tx, issueID, key, actor); err != nil {
			return err
		}

		// DeleteMetadataInTx routes through UpdateIssueInTx (issues + events),
		// so stage both before committing. A no-op clear writes nothing, which
		// DOLT_COMMIT reports as nothing-to-commit (handled below).
		for _, table := range []string{"issues", "events"} {
			_, _ = tx.ExecContext(ctx, "CALL DOLT_ADD(?)", table)
		}
		commitMsg := fmt.Sprintf("bd: clear metadata %s.%s", issueID, key)
		if _, err := tx.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
			commitMsg, s.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
		return nil
	})
}

// clearMetadataWisp clears a metadata key on a wisp. Mirrors mergeMetadataWisp /
// closeWisp: no Dolt versioning since wisps live in dolt_ignored tables.
func (s *DoltStore) clearMetadataWisp(ctx context.Context, issueID, key, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := issueops.DeleteMetadataInTx(ctx, tx, issueID, key, actor); err != nil {
		return err
	}
	return wrapTransactionError("commit clear metadata wisp", tx.Commit())
}
