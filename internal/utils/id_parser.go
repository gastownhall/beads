// Package utils provides utility functions for issue ID parsing and resolution.
package utils

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ParseIssueID ensures an issue ID has the configured prefix.
// If the input already has the prefix (e.g., "bd-a3f8e9"), returns it as-is.
// If the input lacks the prefix (e.g., "a3f8e9"), adds the configured prefix.
// Works with hierarchical IDs too: "a3f8e9.1.2" → "bd-a3f8e9.1.2"
func ParseIssueID(input string, prefix string) string {
	if prefix == "" {
		prefix = "bd-"
	}
	
	if strings.HasPrefix(input, prefix) {
		return input
	}
	
	return prefix + input
}

// ResolvePartialID resolves a potentially partial issue ID to a full ID.
// Supports:
// - Full IDs: "bd-a3f8e9" or "a3f8e9" → "bd-a3f8e9"
// - Partial IDs: "a3f8" → "bd-a3f8e9" (if unique match, requires hash IDs)
// - Hierarchical: "a3f8e9.1" → "bd-a3f8e9.1"
//
// Returns an error if:
// - No issue found matching the ID
// - Multiple issues match (ambiguous prefix)
//
// Note: Partial ID matching (shorter prefixes) requires hash-based IDs (bd-165).
// For now, this primarily handles prefix-optional input (bd-a3f8e9 vs a3f8e9).
func ResolvePartialID(ctx context.Context, store storage.Storage, input string) (string, error) {
	// Get the configured prefix
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd-"
	}
	
	// Ensure the input has the prefix
	parsedID := ParseIssueID(input, prefix)
	
	// First try exact match
	_, err = store.GetIssue(ctx, parsedID)
	if err == nil {
		return parsedID, nil
	}
	
	// If exact match failed, try prefix search
	filter := types.IssueFilter{}
	
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return "", fmt.Errorf("failed to search issues: %w", err)
	}
	
	var matches []string
	for _, issue := range issues {
		if strings.HasPrefix(issue.ID, parsedID) {
			matches = append(matches, issue.ID)
		}
	}
	
	if len(matches) == 0 {
		return "", fmt.Errorf("no issue found matching %q", input)
	}
	
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous ID %q matches %d issues: %v\nUse more characters to disambiguate", input, len(matches), matches)
	}
	
	return matches[0], nil
}

// ResolvePartialIDs resolves multiple potentially partial issue IDs.
// Returns the resolved IDs and any errors encountered.
func ResolvePartialIDs(ctx context.Context, store storage.Storage, inputs []string) ([]string, error) {
	var resolved []string
	for _, input := range inputs {
		fullID, err := ResolvePartialID(ctx, store, input)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, fullID)
	}
	return resolved, nil
}
