//go:build embeddeddolt

package embeddeddolt

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// SlotGet retrieves the value of a named slot from an issue's metadata JSON field.
// Returns storage.ErrNotFound (wrapped) if the key is absent.
func (s *EmbeddedDoltStore) SlotGet(ctx context.Context, issueID, key string) (string, error) {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return "", err
	}

	if len(issue.Metadata) == 0 {
		return "", fmt.Errorf("%w: slot %q not set on %s", storage.ErrNotFound, key, issueID)
	}

	var slots map[string]interface{}
	if err := json.Unmarshal(issue.Metadata, &slots); err != nil {
		return "", fmt.Errorf("failed to parse metadata for %s: %w", issueID, err)
	}

	val, ok := slots[key]
	if !ok {
		return "", fmt.Errorf("%w: slot %q not set on %s", storage.ErrNotFound, key, issueID)
	}
	switch v := val.(type) {
	case string:
		return v, nil
	default:
		b, _ := json.Marshal(v)
		return string(b), nil
	}
}

// SlotSet stores value at key in an issue's metadata JSON field.
// Creates the metadata object if it does not exist. Overwrites any prior value.
func (s *EmbeddedDoltStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	slots := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &slots); err != nil {
			return fmt.Errorf("failed to parse metadata for %s: %w", issueID, err)
		}
	}

	slots[key] = value

	updated, err := json.Marshal(slots)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	return s.UpdateIssue(ctx, issueID, map[string]interface{}{"metadata": string(updated)}, actor)
}

// SlotClear removes key from an issue's metadata JSON field.
// Returns nil (no error) if the key was already absent.
func (s *EmbeddedDoltStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	if len(issue.Metadata) == 0 {
		return nil // nothing to clear
	}

	slots := make(map[string]interface{})
	if err := json.Unmarshal(issue.Metadata, &slots); err != nil {
		return fmt.Errorf("failed to parse metadata for %s: %w", issueID, err)
	}

	if _, ok := slots[key]; !ok {
		return nil // key not present — no-op
	}

	delete(slots, key)

	updated, err := json.Marshal(slots)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	return s.UpdateIssue(ctx, issueID, map[string]interface{}{"metadata": string(updated)}, actor)
}
