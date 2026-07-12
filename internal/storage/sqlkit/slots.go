package sqlkit

import (
	"context"
	"encoding/json"
	"fmt"
)

// Metadata slots are a gt per-issue key/value store layered over the issue's metadata
// JSON column. They are pure GetIssue/UpdateIssue manipulation with no version-control
// dependency, so the logic is identical to the embedded-Dolt reference (slots.go).

// SlotSet sets a key/value pair in the issue's metadata JSON.
func (s *Store) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
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
	return s.UpdateIssue(ctx, issueID, map[string]interface{}{"metadata": string(raw)}, actor)
}

// SlotGet retrieves the value of a metadata key from an issue.
func (s *Store) SlotGet(ctx context.Context, issueID, key string) (string, error) {
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
	if str, ok := val.(string); ok {
		return str, nil
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return "", fmt.Errorf("marshaling slot value for %s.%s: %w", issueID, key, err)
	}
	return string(raw), nil
}

// SlotClear removes a metadata key from an issue. Clearing an absent key (or an issue
// with no metadata) is a silent no-op.
func (s *Store) SlotClear(ctx context.Context, issueID, key, actor string) error {
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
	return s.UpdateIssue(ctx, issueID, map[string]interface{}{"metadata": string(raw)}, actor)
}
