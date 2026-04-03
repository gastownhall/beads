//go:build cgo

package embeddeddolt

import (
	"context"
	"encoding/json"
	"fmt"
)

// SlotSet sets a key-value pair in the issue's metadata JSON.
func (s *EmbeddedDoltStore) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("getting issue %s: %w", issueID, err)
	}

	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
			return fmt.Errorf("parsing metadata for %s: %w", issueID, err)
		}
	}
	metadata[key] = value

	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata for %s: %w", issueID, err)
	}

	updates := map[string]interface{}{"metadata": string(raw)}
	return s.UpdateIssue(ctx, issueID, updates, actor)
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
func (s *EmbeddedDoltStore) SlotClear(ctx context.Context, issueID, key, actor string) error {
	issue, err := s.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("getting issue %s: %w", issueID, err)
	}

	if len(issue.Metadata) == 0 {
		return nil
	}

	metadata := make(map[string]interface{})
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		return fmt.Errorf("parsing metadata for %s: %w", issueID, err)
	}

	if _, ok := metadata[key]; !ok {
		return nil
	}
	delete(metadata, key)

	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata for %s: %w", issueID, err)
	}

	updates := map[string]interface{}{"metadata": string(raw)}
	return s.UpdateIssue(ctx, issueID, updates, actor)
}
