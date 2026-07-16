package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// SlotSet sets a key-value pair in the issue's metadata JSON.
// If the issue has no metadata, a new JSON object is created.
// If the key already exists, its value is overwritten.
//
// It is a single-statement guarded UPDATE (JSON_SET), NOT a read-merge-write, so
// it touches only the one key and cannot clobber a concurrently CAS-written
// sibling key.
func (s *DoltStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	return s.runIssueWrite(ctx, issueID, fmt.Sprintf("bd: set slot %s %s", issueID, key), func(tx *sql.Tx) error {
		return issueops.SetMetadataKeyInTx(ctx, tx, issueID, key, value, actor)
	})
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
// Single-statement guarded UPDATE (JSON_REMOVE), not a read-merge-write.
func (s *DoltStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	return s.runIssueWrite(ctx, issueID, fmt.Sprintf("bd: clear slot %s %s", issueID, key), func(tx *sql.Tx) error {
		return issueops.ClearMetadataKeyInTx(ctx, tx, issueID, key, actor)
	})
}
