package importer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// IssueDataChanged checks if an issue's data has changed from the database version
func IssueDataChanged(existing *types.Issue, updates map[string]interface{}) bool {
	fc := newFieldComparator()
	for key, newVal := range updates {
		if fc.checkFieldChanged(key, existing, newVal) {
			return true
		}
	}
	return false
}

// fieldComparator handles comparison logic for different field types
type fieldComparator struct {
	strFrom func(v interface{}) (string, bool)
	intFrom func(v interface{}) (int64, bool)
}

func newFieldComparator() *fieldComparator {
	fc := &fieldComparator{}

	fc.strFrom = func(v interface{}) (string, bool) {
		switch t := v.(type) {
		case string:
			return t, true
		case *string:
			if t == nil {
				return "", true
			}
			return *t, true
		case nil:
			return "", true
		default:
			return "", false
		}
	}

	fc.intFrom = func(v interface{}) (int64, bool) {
		switch t := v.(type) {
		case int:
			return int64(t), true
		case int32:
			return int64(t), true
		case int64:
			return t, true
		case float64:
			if t == float64(int64(t)) {
				return int64(t), true
			}
			return 0, false
		default:
			return 0, false
		}
	}

	return fc
}

func (fc *fieldComparator) equalStr(existingVal string, newVal interface{}) bool {
	s, ok := fc.strFrom(newVal)
	if !ok {
		return false
	}
	return existingVal == s
}

func (fc *fieldComparator) equalPtrStr(existing *string, newVal interface{}) bool {
	s, ok := fc.strFrom(newVal)
	if !ok {
		return false
	}
	if existing == nil {
		return s == ""
	}
	return *existing == s
}

func (fc *fieldComparator) equalStatus(existing types.Status, newVal interface{}) bool {
	switch t := newVal.(type) {
	case types.Status:
		return existing == t
	case string:
		return string(existing) == t
	default:
		return false
	}
}

func (fc *fieldComparator) equalIssueType(existing types.IssueType, newVal interface{}) bool {
	switch t := newVal.(type) {
	case types.IssueType:
		return existing == t
	case string:
		return string(existing) == t
	default:
		return false
	}
}

func (fc *fieldComparator) equalPriority(existing int, newVal interface{}) bool {
	newPriority, ok := fc.intFrom(newVal)
	return ok && int64(existing) == newPriority
}

func (fc *fieldComparator) equalBool(existingVal bool, newVal interface{}) bool {
	switch t := newVal.(type) {
	case bool:
		return existingVal == t
	default:
		return false
	}
}

func (fc *fieldComparator) checkFieldChanged(key string, existing *types.Issue, newVal interface{}) bool {
	switch key {
	case "title":
		return !fc.equalStr(existing.Title, newVal)
	case "description":
		return !fc.equalStr(existing.Description, newVal)
	case "status":
		return !fc.equalStatus(existing.Status, newVal)
	case "priority":
		return !fc.equalPriority(existing.Priority, newVal)
	case "issue_type":
		return !fc.equalIssueType(existing.IssueType, newVal)
	case "design":
		return !fc.equalStr(existing.Design, newVal)
	case "acceptance_criteria":
		return !fc.equalStr(existing.AcceptanceCriteria, newVal)
	case "notes":
		return !fc.equalStr(existing.Notes, newVal)
	case "assignee":
		return !fc.equalStr(existing.Assignee, newVal)
	case "external_ref":
		return !fc.equalPtrStr(existing.ExternalRef, newVal)
	case "pinned":
		return !fc.equalBool(existing.Pinned, newVal)
	default:
		return false
	}
}

// RenameImportedIssuePrefixes renames all issues and their references to match the target prefix.
//
// This function handles three ID formats:
//   - Sequential numeric IDs: "old-123" → "new-123"
//   - Hash-based IDs: "old-abc1" → "new-abc1"
//   - Hierarchical IDs: "old-abc1.2.3" → "new-abc1.2.3"
//
// The suffix (everything after "prefix-") is preserved during rename, only the prefix changes.
// This preserves issue identity across prefix renames while maintaining parent-child relationships
// in hierarchical IDs (dots denote subtask nesting, e.g., bd-abc1.2 is child 2 of bd-abc1).
//
// All text references to old IDs in issue fields (title, description, notes, etc.) and
// dependency relationships are updated to use the new IDs.
func RenameImportedIssuePrefixes(issues []*types.Issue, targetPrefix string) error {
	// Build a mapping of old IDs to new IDs
	idMapping := make(map[string]string)

	for _, issue := range issues {
		oldPrefix := utils.ExtractIssuePrefix(issue.ID)
		if oldPrefix == "" {
			return fmt.Errorf("cannot rename issue %s: malformed ID (no hyphen found)", issue.ID)
		}

		if oldPrefix != targetPrefix {
			// Extract the suffix part (supports both numeric "123" and hash "abc1" and hierarchical "abc.1.2")
			suffix := strings.TrimPrefix(issue.ID, oldPrefix+"-")

			// Validate that the suffix is valid (alphanumeric + dots for hierarchy)
			if suffix == "" || !isValidIDSuffix(suffix) {
				return fmt.Errorf("cannot rename issue %s: invalid suffix '%s'", issue.ID, suffix)
			}

			newID := fmt.Sprintf("%s-%s", targetPrefix, suffix)
			idMapping[issue.ID] = newID
		}
	}

	// Now update all issues and their references
	for _, issue := range issues {
		// Update the issue ID itself if it needs renaming
		if newID, ok := idMapping[issue.ID]; ok {
			issue.ID = newID
		}

		// Update all text references in issue fields
		issue.Title = replaceIDReferences(issue.Title, idMapping)
		issue.Description = replaceIDReferences(issue.Description, idMapping)
		if issue.Design != "" {
			issue.Design = replaceIDReferences(issue.Design, idMapping)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = replaceIDReferences(issue.AcceptanceCriteria, idMapping)
		}
		if issue.Notes != "" {
			issue.Notes = replaceIDReferences(issue.Notes, idMapping)
		}

		// Update dependency references
		for i := range issue.Dependencies {
			if newID, ok := idMapping[issue.Dependencies[i].IssueID]; ok {
				issue.Dependencies[i].IssueID = newID
			}
			if newID, ok := idMapping[issue.Dependencies[i].DependsOnID]; ok {
				issue.Dependencies[i].DependsOnID = newID
			}
		}

		// Update comment references
		for i := range issue.Comments {
			issue.Comments[i].Text = replaceIDReferences(issue.Comments[i].Text, idMapping)
		}
	}

	return nil
}

// replaceIDReferences replaces all old issue ID references with new ones in text
func replaceIDReferences(text string, idMapping map[string]string) string {
	if len(idMapping) == 0 {
		return text
	}

	// Sort old IDs by length descending to handle longer IDs first
	oldIDs := make([]string, 0, len(idMapping))
	for oldID := range idMapping {
		oldIDs = append(oldIDs, oldID)
	}
	sort.Slice(oldIDs, func(i, j int) bool {
		return len(oldIDs[i]) > len(oldIDs[j])
	})

	result := text
	for _, oldID := range oldIDs {
		newID := idMapping[oldID]
		result = replaceBoundaryAware(result, oldID, newID)
	}
	return result
}

// replaceBoundaryAware replaces oldID with newID only when surrounded by boundaries
func replaceBoundaryAware(text, oldID, newID string) string {
	if !strings.Contains(text, oldID) {
		return text
	}

	var result strings.Builder
	i := 0
	for i < len(text) {
		// Find next occurrence
		idx := strings.Index(text[i:], oldID)
		if idx == -1 {
			result.WriteString(text[i:])
			break
		}

		actualIdx := i + idx
		// Check boundary before
		beforeOK := actualIdx == 0 || isBoundary(text[actualIdx-1])
		// Check boundary after
		afterIdx := actualIdx + len(oldID)
		afterOK := afterIdx >= len(text) || isBoundary(text[afterIdx])

		// Write up to this match
		result.WriteString(text[i:actualIdx])

		if beforeOK && afterOK {
			// Valid match - replace
			result.WriteString(newID)
		} else {
			// Invalid match - keep original
			result.WriteString(oldID)
		}

		i = afterIdx
	}

	return result.String()
}

func isBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '.' || c == '!' || c == '?' || c == ':' || c == ';' || c == '(' || c == ')' || c == '[' || c == ']' || c == '{' || c == '}'
}

// isValidIDSuffix validates the suffix portion of an issue ID (everything after "prefix-").
//
// Beads supports three ID formats, all of which this function must accept:
//   - Sequential numeric: "123", "999" (legacy format)
//   - Hash-based (base36): "abc1", "6we", "zzz" (current format, content-addressed)
//   - Hierarchical: "abc1.2", "6we.2.3" (subtasks, dot-separated child counters)
//
// The dot separator in hierarchical IDs represents parent-child relationships:
// "bd-abc1.2" means child #2 of parent "bd-abc1". Maximum depth is 3 levels.
//
// Rejected: uppercase letters, hyphens (would be confused with prefix separator),
// and special characters.
func isValidIDSuffix(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || c == '.') {
			return false
		}
	}
	return true
}
