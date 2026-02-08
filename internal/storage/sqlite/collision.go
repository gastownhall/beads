package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// collisionResult categorizes incoming issues by their relationship to existing DB state
type collisionResult struct {
	ExactMatches []string           // IDs that match exactly (idempotent import)
	Collisions   []*collisionDetail // Issues with same ID but different content
	NewIssues    []string           // IDs that don't exist in DB yet
	Renames      []*renameDetail    // Issues with same content but different ID (renames)
}

// renameDetail captures a rename/remap detected during collision detection
type renameDetail struct {
	OldID string       // ID in database (to be deleted)
	NewID string       // ID in incoming (to be created)
	Issue *types.Issue // The issue with new ID
}

// collisionDetail provides detailed information about a collision
type collisionDetail struct {
	ID                string       // The issue ID that collided
	IncomingIssue     *types.Issue // The issue from the import file
	ExistingIssue     *types.Issue // The issue currently in the database
	ConflictingFields []string     // List of field names that differ
	RemapIncoming     bool         // If true, remap incoming; if false, remap existing
}

// DetectCollisions compares incoming JSONL issues against DB state
// It distinguishes between:
//  1. Exact match (idempotent) - ID and content are identical
//  2. ID match but different content (collision/update) - same ID, different fields
//  3. New issue - ID doesn't exist in DB
//  4. External ref match - Different ID but same external_ref (update from external system)
//
// When an incoming issue has an external_ref, we match by external_ref first,
// then by ID. This enables re-syncing from external systems (Jira, GitHub, Linear).
//
// Returns a collisionResult categorizing all incoming issues.
func DetectCollisions(ctx context.Context, s *SQLiteStorage, incomingIssues []*types.Issue) (*collisionResult, error) {
	result := &collisionResult{
		ExactMatches: make([]string, 0),
		Collisions:   make([]*collisionDetail, 0),
		NewIssues:    make([]string, 0),
	}

	// Build content hash map for rename detection
	contentToID := make(map[string]string)
	for _, incoming := range incomingIssues {
		hash := hashIssueContent(incoming)
		contentToID[hash] = incoming.ID
	}

	// Check each incoming issue
	for _, incoming := range incomingIssues {
		var existing *types.Issue
		var err error

		// If incoming issue has external_ref, try matching by external_ref first
		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			existing, err = s.GetIssueByExternalRef(ctx, *incoming.ExternalRef)
			if err != nil {
				return nil, fmt.Errorf("failed to lookup by external_ref: %w", err)
			}
		}

		// If no external_ref match, try matching by ID
		if existing == nil {
			existing, err = s.GetIssue(ctx, incoming.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to lookup by ID: %w", err)
			}
		}

		// No match found - it's a new issue
		if existing == nil {
			result.NewIssues = append(result.NewIssues, incoming.ID)
			continue
		}

		// Found a match - check if content matches
		conflictingFields := compareIssues(existing, incoming)
		if len(conflictingFields) == 0 {
			// Exact match - idempotent import
			result.ExactMatches = append(result.ExactMatches, incoming.ID)
		} else {
			// Same ID/external_ref, different content - collision (needs update)
			result.Collisions = append(result.Collisions, &collisionDetail{
				ID:                incoming.ID,
				IncomingIssue:     incoming,
				ExistingIssue:     existing,
				ConflictingFields: conflictingFields,
			})
		}
	}

	return result, nil
}

// compareIssues returns list of field names that differ between two issues
func compareIssues(existing, incoming *types.Issue) []string {
	conflicts := []string{}

	if existing.Title != incoming.Title {
		conflicts = append(conflicts, "title")
	}
	if existing.Description != incoming.Description {
		conflicts = append(conflicts, "description")
	}
	if existing.Status != incoming.Status {
		conflicts = append(conflicts, "status")
	}
	if existing.Priority != incoming.Priority {
		conflicts = append(conflicts, "priority")
	}
	if existing.IssueType != incoming.IssueType {
		conflicts = append(conflicts, "issue_type")
	}
	if existing.Assignee != incoming.Assignee {
		conflicts = append(conflicts, "assignee")
	}
	if existing.Design != incoming.Design {
		conflicts = append(conflicts, "design")
	}
	if existing.AcceptanceCriteria != incoming.AcceptanceCriteria {
		conflicts = append(conflicts, "acceptance_criteria")
	}
	if existing.Notes != incoming.Notes {
		conflicts = append(conflicts, "notes")
	}
	if (existing.ExternalRef == nil && incoming.ExternalRef != nil) ||
		(existing.ExternalRef != nil && incoming.ExternalRef == nil) ||
		(existing.ExternalRef != nil && incoming.ExternalRef != nil && *existing.ExternalRef != *incoming.ExternalRef) {
		conflicts = append(conflicts, "external_ref")
	}

	return conflicts
}

// hashIssueContent creates a deterministic hash of issue content (excluding ID and timestamps)
//
// IMPORTANT: external_ref is included in the content hash. This means:
//   - Adding/removing/changing external_ref changes the content hash
//   - A local issue that gains an external_ref will have different content hash
//   - This is intentional: external_ref is semantically meaningful content
//
// Implications:
//  1. Rename detection won't match issues before/after adding external_ref
//  2. Content-based collision detection treats external_ref changes as updates
//  3. Idempotent import only when external_ref is identical
//
// This design choice ensures external system linkage is tracked as substantive content,
// not just metadata. See docs/HASH_ID_DESIGN.md for more on content hash philosophy.
func hashIssueContent(issue *types.Issue) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "title:%s\n", issue.Title)
	_, _ = fmt.Fprintf(h, "description:%s\n", issue.Description)
	_, _ = fmt.Fprintf(h, "status:%s\n", issue.Status)
	_, _ = fmt.Fprintf(h, "priority:%d\n", issue.Priority)
	_, _ = fmt.Fprintf(h, "type:%s\n", issue.IssueType)
	_, _ = fmt.Fprintf(h, "assignee:%s\n", issue.Assignee)
	_, _ = fmt.Fprintf(h, "design:%s\n", issue.Design)
	_, _ = fmt.Fprintf(h, "acceptance:%s\n", issue.AcceptanceCriteria)
	_, _ = fmt.Fprintf(h, "notes:%s\n", issue.Notes)
	// external_ref is included in content hash (see comment above)
	if issue.ExternalRef != nil {
		_, _ = fmt.Fprintf(h, "external_ref:%s\n", *issue.ExternalRef)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
