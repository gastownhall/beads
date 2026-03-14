// Package utils provides utility functions for issue ID parsing and resolution.
package utils

import (
	"context"
	"fmt"
	"strings"

	"github.com/gastownhall/beads/internal/storage"
	"github.com/gastownhall/beads/internal/types"
)

// parseIssueID ensures an issue ID has the configured prefix.
// If the input already has the prefix (e.g., "bd-a3f8e9"), returns it as-is.
// If the input lacks the prefix (e.g., "a3f8e9"), adds the configured prefix.
// Works with hierarchical IDs too: "a3f8e9.1.2" → "bd-a3f8e9.1.2"
func parseIssueID(input string, prefix string) string {
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
// - Without hyphen: "bda3f8e9" or "wya3f8e9" → "bd-a3f8e9"
// - Partial IDs: "a3f8" → "bd-a3f8e9" (if unique match)
// - Hierarchical: "a3f8e9.1" → "bd-a3f8e9.1"
//
// Returns an error if:
// - No issue found matching the ID
// - Multiple issues match (ambiguous prefix)
func ResolvePartialID(ctx context.Context, store storage.Storage, input string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("cannot resolve issue ID %q: storage is nil", input)
	}

	// Fast path: Use SearchIssues with exact ID filter (GH#942).
	// This uses the same query path as "bd list --id", ensuring consistency.
	// Previously we used GetIssue which could fail in cases where SearchIssues
	// with filter.IDs succeeded, likely due to subtle query differences.
	exactFilter := types.IssueFilter{IDs: []string{input}}
	if issues, err := store.SearchIssues(ctx, "", exactFilter); err == nil && len(issues) > 0 {
		return issues[0].ID, nil
	}

	// Get the configured prefix
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd"
	}

	// Ensure prefix has hyphen for ID format
	prefixWithHyphen := prefix
	if !strings.HasSuffix(prefix, "-") {
		prefixWithHyphen = prefix + "-"
	}

	// Build known prefixes from config for deterministic multi-hyphen prefix handling.
	// This avoids relying solely on looksLikePrefixedID heuristics when the repo
	// explicitly declares which prefixes are valid.
	knownPrefixes := []string{strings.TrimSuffix(prefix, "-")}
	if allowed, aErr := store.GetConfig(ctx, "allowed_prefixes"); aErr == nil && allowed != "" {
		for _, p := range strings.Split(allowed, ",") {
			p = strings.TrimSpace(p)
			p = strings.TrimSuffix(p, "-")
			if p != "" {
				knownPrefixes = append(knownPrefixes, p)
			}
		}
	}

	// Normalize input:
	// 1. If it has the full prefix with hyphen (bd-a3f8e9), use as-is
	// 2. If it starts with any known/allowed prefix, use as-is (config-aware cross-prefix)
	// 3. If it has ANY prefix (heuristic fallback), use as-is for cross-prefix lookup
	// 4. Otherwise, add prefix with hyphen (handles both bare hashes and prefix-without-hyphen cases)

	var normalizedID string

	if strings.HasPrefix(input, prefixWithHyphen) {
		// Already has configured prefix with hyphen: "bd-a3f8e9"
		normalizedID = input
	} else if hasKnownPrefix(input, knownPrefixes) {
		// Starts with a known/allowed prefix (e.g., "hacker-news-ko4" when allowed_prefixes includes "hacker-news")
		normalizedID = input
	} else if looksLikePrefixedID(input) {
		// Has a different prefix (e.g., "aap-4ar" when configured prefix is "hq-")
		// Don't prepend configured prefix - use as-is for cross-prefix lookup (GH#1513)
		normalizedID = input
	} else {
		// Bare hash or prefix without hyphen: "a3f8e9", "07b8c8", "bda3f8e9" → all get prefix with hyphen added
		normalizedID = prefixWithHyphen + input
	}

	// Try exact match on normalized ID using SearchIssues (GH#942)
	normalizedFilter := types.IssueFilter{IDs: []string{normalizedID}}
	if issues, err := store.SearchIssues(ctx, "", normalizedFilter); err == nil && len(issues) > 0 {
		return issues[0].ID, nil
	}

	// If exact match failed, try substring search.
	// Use the hash part as a search query to leverage SQL-level filtering
	// (id LIKE %hash%) instead of loading ALL issues into memory.
	// On large databases (23k+ issues over MySQL wire protocol), loading all
	// issues took 60+ seconds; with SQL filtering it's near-instant.
	hashPart := strings.TrimPrefix(normalizedID, prefixWithHyphen)

	filter := types.IssueFilter{}
	issues, err := store.SearchIssues(ctx, hashPart, filter)
	if err != nil {
		return "", fmt.Errorf("failed to search issues: %w", err)
	}

	var matches []string
	var exactMatch string

	for _, issue := range issues {
		// Check for exact full ID match first (case: user typed full ID with different prefix)
		if issue.ID == input {
			exactMatch = issue.ID
			break
		}

		// Extract hash from each issue using config-aware prefix extraction.
		// This correctly handles multi-hyphen prefixes (e.g., "hacker-news-ko4"
		// yields hash "ko4", not "news-ko4" from naive first-hyphen split).
		var issueHash string
		if p := ExtractIssuePrefixKnown(issue.ID, knownPrefixes); p != "" && strings.HasPrefix(issue.ID, p+"-") {
			issueHash = issue.ID[len(p)+1:]
		} else {
			issueHash = issue.ID
		}

		// Check for exact hash match (excluding hierarchical children)
		if issueHash == hashPart {
			exactMatch = issue.ID
			// Don't break - keep searching in case there's a full ID match
		}

		// Check if the issue hash contains the input hash as substring
		if strings.Contains(issueHash, hashPart) {
			matches = append(matches, issue.ID)
		}
	}

	// Prefer exact match over substring matches
	if exactMatch != "" {
		return exactMatch, nil
	}

	// Fallback: explicitly search wisps table for partial ID resolution.
	// DoltStore.SearchIssues merges wisps when Ephemeral is nil, but
	// transaction-level SearchIssues does not. This ensures wisps are
	// always resolvable by partial ID.
	if len(matches) == 0 {
		ephTrue := true
		wispFilter := types.IssueFilter{Ephemeral: &ephTrue}
		if wisps, wispErr := store.SearchIssues(ctx, hashPart, wispFilter); wispErr == nil {
			for _, w := range wisps {
				if w.ID == input {
					return w.ID, nil
				}
				var wHash string
				if p := ExtractIssuePrefixKnown(w.ID, knownPrefixes); p != "" && strings.HasPrefix(w.ID, p+"-") {
					wHash = w.ID[len(p)+1:]
				} else {
					wHash = w.ID
				}
				if wHash == hashPart {
					exactMatch = w.ID
				}
				if strings.Contains(wHash, hashPart) {
					matches = append(matches, w.ID)
				}
			}
			if exactMatch != "" {
				return exactMatch, nil
			}
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

// looksLikePrefixedID checks if input appears to already have a prefix.
// A prefixed ID has the format "prefix-hash" where prefix is 1-8 lowercase
// letters/numbers and hash is alphanumeric (potentially with dots for hierarchical IDs).
// Examples: "aap-4ar", "bd-a3f8e9", "myproject-abc.1"
func looksLikePrefixedID(input string) bool {
	idx := strings.Index(input, "-")
	if idx <= 0 || idx > 8 {
		// No hyphen, hyphen at start, or prefix too long
		return false
	}

	prefix := input[:idx]
	suffix := input[idx+1:]

	// Prefix must be non-empty lowercase alphanumeric
	for _, c := range prefix {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}

	// Suffix must be non-empty and start with alphanumeric
	if len(suffix) == 0 {
		return false
	}
	first := rune(suffix[0])
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}

	return true
}

// hasKnownPrefix checks if input starts with any of the known prefixes followed
// by a hyphen. Used to detect already-prefixed input before falling back to the
// looksLikePrefixedID heuristic.
func hasKnownPrefix(input string, knownPrefixes []string) bool {
	for _, p := range knownPrefixes {
		if p != "" && strings.HasPrefix(input, p+"-") {
			return true
		}
	}
	return false
}
