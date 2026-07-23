//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// MergeMetadata merges a single key into an issue's metadata JSON atomically.
// value is a raw JSON value, so nested objects/arrays are preserved. The read
// and write share ONE transaction (withConn commit=true), so a concurrent merge
// of a DIFFERENT key cannot clobber this one between the read and the write. The
// write routes through UpdateIssueInTx, so the merge records an EventUpdated
// history event (attributed to actor) and runs the configured metadata-schema
// validation, exactly as the old SlotSet did. Routes to the issues or wisps
// table automatically. SlotSet is built on this.
func (s *EmbeddedDoltStore) MergeMetadata(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.MergeMetadataInTx(ctx, tx, issueID, key, value, actor)
	})
}

// SlotSet sets a key-value pair in the issue's metadata JSON.
//
// Built on MergeMetadata so the read-modify-write is atomic: two concurrent
// SlotSet calls on different keys both survive. The string value is stored as a
// JSON string (json.Marshal(value) yields "value"), keeping the stored metadata
// byte-compatible with the historical whole-metadata rewrite.
func (s *EmbeddedDoltStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling slot value for %s.%s: %w", issueID, key, err)
	}
	return s.MergeMetadata(ctx, issueID, key, raw, actor)
}

// SlotGet retrieves the value of a metadata key from an issue.
func (s *EmbeddedDoltStore) SlotGet(ctx context.Context, issueID, key string) (string, error) {
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
		raw, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("marshaling slot value for %s.%s: %w", issueID, key, err)
		}
		return string(raw), nil
	}
}

// SlotClear removes a metadata key from an issue.
//
// Built on DeleteMetadataInTx so the read-modify-write is atomic (withConn
// commit=true), exactly like MergeMetadata: a concurrent SlotClear or SlotSet of
// a different key can no longer clobber this write. Clearing an absent key is a
// no-op that writes nothing.
func (s *EmbeddedDoltStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	return s.withConn(ctx, true, func(tx *sql.Tx) error {
		return issueops.DeleteMetadataInTx(ctx, tx, issueID, key, actor)
	})
}
