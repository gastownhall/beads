package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// ParentLink describes a desired parent-child relationship to wire up in Jira.
// ChildIdentifier and ParentIdentifier are Jira keys (e.g., "PROJ-123"), and may
// be empty in dry-run mode when the corresponding bead would be newly created.
type ParentLink struct {
	ChildBeadID      string
	ChildIdentifier  string
	ChildJiraType    string
	ParentBeadID     string
	ParentIdentifier string
	ParentJiraType   string
	CurrentParentKey string
	Action           string // "assign" or "reparent"
}

// ParentReconcileStats summarizes a ReconcileParents run.
type ParentReconcileStats struct {
	Updated     int
	WouldUpdate int
	Mutations   []ParentLink
	Skipped     int
	NotFound    []string
	Errors      []error
}

// ResolveJiraTypeForBead returns the Jira issue type name for a local bead,
// preferring any preserved jira_type metadata from a prior Jira pull before
// falling back to the FieldMapper's TypeToTracker mapping.
func ResolveJiraTypeForBead(issue *types.Issue, mapper tracker.FieldMapper) string {
	if issue == nil {
		return ""
	}
	if len(issue.Metadata) > 0 {
		var meta struct {
			JiraType string `json:"jira_type"`
		}
		if err := json.Unmarshal(issue.Metadata, &meta); err == nil {
			if jt := strings.TrimSpace(meta.JiraType); jt != "" {
				return jt
			}
		}
	}
	if mapper != nil {
		if name, ok := mapper.TypeToTracker(issue.IssueType).(string); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// ValidateJiraHierarchy checks whether assigning parentID (parentType) as the
// Jira parent of childID (childType) violates standard Jira hierarchy rules.
// Unknown/custom Jira issue types (e.g. Jira Premium "Initiative") are allowed
// so Jira itself remains authoritative for custom hierarchies.
func ValidateJiraHierarchy(childID, childType, parentID, parentType string) error {
	if childID != "" && parentID != "" && strings.EqualFold(childID, parentID) {
		return fmt.Errorf("unsupported Jira hierarchy: cannot set %s as its own parent", childID)
	}
	parentLvl, parentKnown := jiraHierarchyLevel(parentType)
	if parentKnown && parentLvl < 0 {
		return fmt.Errorf("unsupported Jira hierarchy: %s (%s) cannot have %s (%s) as parent because Jira sub-tasks cannot have child issues",
			childID, childType, parentID, parentType)
	}
	childLvl, childKnown := jiraHierarchyLevel(childType)
	if childKnown && parentKnown && parentLvl <= childLvl {
		return fmt.Errorf("unsupported Jira hierarchy: %s (%s) cannot have %s (%s) as parent (parent must be a higher hierarchy level such as Epic, or map the child type to Sub-task via jira.type_map)",
			childID, childType, parentID, parentType)
	}
	return nil
}

func jiraHierarchyLevel(issueType string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(issueType)) {
	case "epic":
		return 1, true
	case "story", "task", "bug", "feature":
		return 0, true
	case "sub-task", "subtask":
		return -1, true
	default:
		return 0, false
	}
}

// resolveParentForCreate resolves a local bead's parent Jira key during issue
// creation, if the parent already has a valid Jira external_ref. Returns ""
// without error when no parent exists or when the parent has not yet been
// assigned a Jira external_ref (the post-sync ReconcileParents pass handles it
// once the parent is created).
func (t *Tracker) resolveParentForCreate(ctx context.Context, issue *types.Issue, mapper tracker.FieldMapper) (string, error) {
	if t == nil || t.store == nil || issue == nil || strings.TrimSpace(issue.ID) == "" {
		return "", nil
	}

	deps, err := t.store.GetDependenciesWithMetadata(ctx, issue.ID)
	if err != nil {
		return "", nil
	}

	seen := make(map[string]bool)
	var parentBeads []*types.Issue
	var parentIDs []string
	for _, dep := range deps {
		if dep == nil || dep.DependencyType != types.DepParentChild {
			continue
		}
		pid := strings.TrimSpace(dep.Issue.ID)
		if pid == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		p := dep.Issue
		parentBeads = append(parentBeads, &p)
		parentIDs = append(parentIDs, pid)
	}

	if len(parentBeads) == 0 {
		return "", nil
	}
	if len(parentBeads) > 1 {
		return "", fmt.Errorf("unsupported Jira hierarchy: %s has multiple parent-child dependencies (%s); Jira supports only one parent per issue",
			issue.ID, strings.Join(parentIDs, ", "))
	}

	parentBead := parentBeads[0]
	if parentBead.ID == issue.ID {
		return "", fmt.Errorf("unsupported Jira hierarchy: cannot set %s as its own parent", issue.ID)
	}
	if parentBead.ExternalRef == nil {
		return "", nil
	}
	ref := strings.TrimSpace(*parentBead.ExternalRef)
	if ref == "" || !t.IsExternalRef(ref) {
		return "", nil
	}
	parentKey := strings.TrimSpace(t.ExtractIdentifier(ref))
	if parentKey == "" {
		return "", nil
	}

	childType := ResolveJiraTypeForBead(issue, mapper)
	parentType := ResolveJiraTypeForBead(parentBead, mapper)
	if err := ValidateJiraHierarchy(issue.ID, childType, parentKey, parentType); err != nil {
		return "", err
	}

	return parentKey, nil
}

// ReconcileParents wires parent-child relationships from Beads dependencies onto
// Jira's parent field. It handles children created before their parent in the
// same sync as well as already-linked children with missing or outdated parents.
//
// Idempotent: fetches each child's current parent and skips UpdateIssue when
// Jira's parent already matches ParentIdentifier.
//
// In dryRun mode, read-only fetches still execute for existing Jira keys so
// hierarchy validation, idempotency, and reparent previews are accurate.
func (t *Tracker) ReconcileParents(ctx context.Context, links []ParentLink, dryRun bool) (*ParentReconcileStats, error) {
	stats := &ParentReconcileStats{}
	if len(links) == 0 {
		return stats, nil
	}
	if t == nil || t.client == nil {
		return nil, errors.New("Jira client not initialized")
	}

	fetched := make(map[string]*Issue, len(links)*2)
	notFoundSet := make(map[string]bool)
	recordNotFound := func(key string) {
		if !notFoundSet[key] {
			notFoundSet[key] = true
			stats.NotFound = append(stats.NotFound, key)
		}
	}

	fetchIssue := func(key string) (*Issue, error) {
		if cached, ok := fetched[key]; ok {
			return cached, nil
		}
		iss, err := t.client.GetIssue(ctx, key)
		if err != nil {
			if isJiraNotFoundError(err) {
				fetched[key] = nil
				return nil, nil
			}
			return nil, err
		}
		fetched[key] = iss
		return iss, nil
	}

	for _, link := range links {
		childKey := strings.TrimSpace(link.ChildIdentifier)
		parentKey := strings.TrimSpace(link.ParentIdentifier)

		if dryRun && (childKey == "" || parentKey == "") {
			childType := link.ChildJiraType
			parentType := link.ParentJiraType
			childLabel := firstNonEmptyStr(childKey, link.ChildBeadID)
			parentLabel := firstNonEmptyStr(parentKey, link.ParentBeadID)

			if childKey != "" {
				childIssue, err := fetchIssue(childKey)
				if err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("fetch child %s: %w", childKey, err))
					continue
				}
				if childIssue == nil {
					recordNotFound(childKey)
					continue
				}
				childType = firstNonEmptyStr(typeName(childIssue), childType)
				if childIssue.Fields.Parent != nil {
					link.CurrentParentKey = strings.TrimSpace(childIssue.Fields.Parent.Key)
				}
			}

			if parentKey != "" {
				parentIssue, err := fetchIssue(parentKey)
				if err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("fetch parent %s: %w", parentKey, err))
					continue
				}
				if parentIssue == nil {
					recordNotFound(parentKey)
					continue
				}
				parentType = firstNonEmptyStr(typeName(parentIssue), parentType)
			}

			if err := ValidateJiraHierarchy(childLabel, childType, parentLabel, parentType); err != nil {
				stats.Errors = append(stats.Errors, err)
				continue
			}

			if link.CurrentParentKey != "" {
				link.Action = "reparent"
			} else {
				link.Action = "assign"
			}
			stats.Mutations = append(stats.Mutations, link)
			stats.WouldUpdate++
			continue
		}

		if childKey == "" || parentKey == "" {
			continue
		}

		childIssue, err := fetchIssue(childKey)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("fetch child %s: %w", childKey, err))
			continue
		}
		if childIssue == nil {
			recordNotFound(childKey)
			continue
		}

		parentIssue, err := fetchIssue(parentKey)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("fetch parent %s: %w", parentKey, err))
			continue
		}
		if parentIssue == nil {
			recordNotFound(parentKey)
			continue
		}

		childType := firstNonEmptyStr(typeName(childIssue), link.ChildJiraType)
		parentType := firstNonEmptyStr(typeName(parentIssue), link.ParentJiraType)
		if err := ValidateJiraHierarchy(childKey, childType, parentKey, parentType); err != nil {
			stats.Errors = append(stats.Errors, err)
			continue
		}

		currentParentKey := ""
		if childIssue.Fields.Parent != nil {
			currentParentKey = strings.TrimSpace(childIssue.Fields.Parent.Key)
		}
		if currentParentKey != "" && strings.EqualFold(currentParentKey, parentKey) {
			stats.Skipped++
			continue
		}

		link.CurrentParentKey = currentParentKey
		if currentParentKey != "" {
			link.Action = "reparent"
		} else {
			link.Action = "assign"
		}

		if dryRun {
			stats.Mutations = append(stats.Mutations, link)
			stats.WouldUpdate++
			continue
		}

		if err := t.client.UpdateIssue(ctx, childKey, map[string]interface{}{
			"parent": map[string]string{"key": parentKey},
		}); err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("set parent of %s (%s) → %s (%s): %w",
				childKey, childType, parentKey, parentType, err))
			continue
		}

		childIssue.Fields.Parent = &ParentField{
			ID:  parentIssue.ID,
			Key: parentKey,
		}
		stats.Mutations = append(stats.Mutations, link)
		stats.Updated++
	}

	return stats, nil
}

func isJiraNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "jira API returned 404:")
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
